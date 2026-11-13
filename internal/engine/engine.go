// Package engine runs a set of checks and produces a report. It decides
// whether each check can run, and runs it by the row of its kind in a
// dispatch table: the protocol package decides what a program check and a
// generated check return, and the rule package what a rule returns.
package engine

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"runtime/debug"
	"sync"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/protocol"
	"github.com/f-klement/errata/internal/provider"
	"github.com/f-klement/errata/internal/rule"
)

// Runner executes one check. The engine takes it as a field so tests can
// replace the check protocol and the rule runner with a fake.
type Runner func(ctx context.Context, root string, c check.Check) check.Result

// Engine runs checks against a repository root with a set of providers.
type Engine struct {
	Root      string
	Providers []provider.Provider
	Run       Runner
	// SkipsFail makes a skipped check fail the run. Execute copies it onto
	// the report under the same name, which the outputs and the exit code
	// read.
	SkipsFail bool
	// workers bounds the pool for a test; 0 is GOMAXPROCS (width).
	workers int
}

// New returns an engine that runs each check by the runner of its kind.
func New(root string, providers []provider.Provider) *Engine {
	return &Engine{Root: root, Providers: providers, Run: Dispatch}
}

// Table is a dispatch table: the runner of each kind of check, by the type
// of the kind, as check.Check.Kind holds it.
type Table map[reflect.Type]Runner

// kinds is the dispatch table errata runs with, a row for each kind.
var kinds = Table{
	reflect.TypeFor[*check.Program]():   protocol.Run,
	reflect.TypeFor[*check.Rule]():      rule.Run,
	reflect.TypeFor[*check.Generated](): protocol.RunGenerated,
}

// Dispatch is the production Runner: it runs c by the row of its kind.
func Dispatch(ctx context.Context, root string, c check.Check) check.Result {
	return kinds.Dispatch(ctx, root, c)
}

// Dispatch runs c by the runner of its kind. A kind with no row is unknown:
// errata did not run the check, so it cannot say the claim holds.
func (t Table) Dispatch(ctx context.Context, root string, c check.Check) check.Result {
	run, ok := t[reflect.TypeOf(c.Kind)]
	if !ok {
		return check.Result{Check: c, Verdict: check.VerdictUnknown, Message: fmt.Sprintf("errata has no runner for a check of kind %T", c.Kind)}
	}
	return run(ctx, root, c)
}

// Execute runs every check that the available providers can reach and skips
// the rest with the reason. The report holds a result for each check, in
// the order of checks, each followed by its witnesses' results, whatever
// order they finish in. Rules and their witnesses run at once on a pool of
// GOMAXPROCS workers (pool); a check of any other kind runs alone, after
// every check before it and before any after it, since a program may
// write the files a rule reads. The checks share one run (check.WithRun),
// and each job that runs alone ends what the run kept of its files
// (check.Wrote) before the next job starts.
func (e *Engine) Execute(ctx context.Context, checks []check.Check) check.Report {
	jobs := plan(provider.Union(e.Providers), checks)
	rep := check.Report{SkipsFail: e.SkipsFail, Results: make([]check.Result, len(jobs))}
	// The checks of the run share what a reader computes once for all of
	// them, such as the files a marker walk reads (check.Shared).
	e.runAll(check.WithRun(ctx), jobs, rep.Results)
	return rep
}

// job is one result of a report to compute: a check skipped with its gap,
// a run of the check, or a run of it on its witness.
type job struct {
	c       check.Check
	gap     string
	witness string
}

// plan is the jobs of checks, in the order of their results in the report.
func plan(caps provider.Capabilities, checks []check.Check) []job {
	var jobs []job
	for _, c := range checks {
		if ok, gap := provider.Reaches(caps, c.Scope); !ok {
			jobs = append(jobs, job{c: c, gap: gap})
			continue
		}
		jobs = append(jobs, job{c: c})
		for _, w := range c.Witness {
			jobs = append(jobs, job{c: c, witness: w})
		}
	}
	return jobs
}

// alone is whether j runs with no other job: a check that may write to
// the repository (mayWrite), on the repository or on a witness. A rule
// only reads, and a skip runs nothing.
func (j job) alone() bool {
	return j.gap == "" && mayWrite(j.c)
}

// mayWrite reports whether c, once run, may have written to the
// repository: every kind but a rule runs a program there (ADR 0011), so a
// rule after it reads the files again rather than what the run kept.
func mayWrite(c check.Check) bool {
	_, rule := c.Kind.(*check.Rule)
	return !rule
}

// run is j's result.
func (e *Engine) run(ctx context.Context, j job) check.Result {
	switch {
	case j.gap != "":
		return check.Result{Check: j.c, Gap: j.gap}
	case j.witness != "":
		return e.witness(ctx, j.c, j.witness)
	}
	return e.Run(ctx, e.Root, j.c)
}

// runAll puts each job's result into out at the job's index. Each stretch
// of jobs between two that run alone runs on the pool, and finishes before
// the next job alone starts. A job alone may have written to the
// repository, so the run drops what it kept (check.Wrote) before any job
// after it starts.
func (e *Engine) runAll(ctx context.Context, jobs []job, out []check.Result) {
	from := 0
	for i, j := range jobs {
		if j.alone() {
			e.pool(ctx, jobs[from:i], out[from:i])
			out[i] = e.run(ctx, j)
			check.Wrote(ctx)
			from = i + 1
		}
	}
	e.pool(ctx, jobs[from:], out[from:])
}

// pool runs jobs on at most e.width workers, and at least one, each taking
// the next job in order, and puts each result into out at the job's index.
// One worker runs the jobs one at a time, in order, as a run on one core
// does. A job that panics panics again here, on the caller's goroutine,
// once every worker is done (caught), so the caller's defers run as they
// did when the checks ran on it.
func (e *Engine) pool(ctx context.Context, jobs []job, out []check.Result) {
	next := make(chan int)
	var wg sync.WaitGroup
	var first caught
	for range max(1, min(e.width(), len(jobs))) {
		wg.Go(func() {
			for i := range next {
				first.run(func() { out[i] = e.run(ctx, jobs[i]) })
			}
		})
	}
	for i := range jobs {
		next <- i
	}
	close(next)
	wg.Wait()
	first.again()
}

// caught is the first panic of a pool's jobs, with the stack of the worker
// it happened on. Safe for concurrent use.
type caught struct {
	once  sync.Once
	panic *workerPanic
}

// run runs job, and keeps its panic when it is the pool's first.
func (c *caught) run(job func()) {
	defer func() {
		if v := recover(); v != nil {
			c.once.Do(func() { c.panic = &workerPanic{value: v, stack: debug.Stack()} })
		}
	}()
	job()
}

// again panics with the panic run kept, if any.
func (c *caught) again() {
	if c.panic != nil {
		panic(c.panic)
	}
}

// workerPanic is a panic of a job on the pool, raised again on the caller's
// goroutine: the value the job panicked with, and the stack of the worker
// it panicked on, which the caller's stack no longer shows.
type workerPanic struct {
	value any
	stack []byte
}

func (p *workerPanic) Error() string {
	return fmt.Sprintf("%v\n\nin a worker of the engine's pool:\n%s", p.value, p.stack)
}

// Unwrap is the value the job panicked with, when it is an error.
func (p *workerPanic) Unwrap() error {
	err, _ := p.value.(error)
	return err
}

// width is the most jobs that run at once: e.workers when a test sets it,
// else GOMAXPROCS, the cores the process may use.
func (e *Engine) width() int {
	if e.workers > 0 {
		return e.workers
	}
	return runtime.GOMAXPROCS(0)
}
