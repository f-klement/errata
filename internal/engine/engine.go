// Package engine runs a set of checks and produces a report. It decides
// whether each check can run, and runs it by the row of its kind in a
// dispatch table: the protocol package decides what a program check and a
// generated check return, and the rule package what a rule returns.
package engine

import (
	"context"
	"fmt"
	"reflect"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/protocol"
	"github.com/f-klement/errata/internal/provider"
	"github.com/f-klement/errata/internal/rule"
	"github.com/f-klement/errata/internal/treecopy"
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
	// workers bounds the pool for a test; 0 is the CPUs the process may
	// use (width).
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
// order they finish in. Rules and their witnesses run at once on a pool as
// wide as the CPUs the process may use (width, read once for the run), and
// so do program and generated checks that each run in a copy of the tree
// of their own (inCopies, ADR 0019), whose durations the run then records
// for the next. A check that runs in the repository
// runs alone, after every check before it and before any after it, since
// a program may write the files a rule reads. The checks share one run
// (check.WithRun), and each job that runs alone ends what the run kept of
// its files (check.Wrote) before the next job starts.
func (e *Engine) Execute(ctx context.Context, checks []check.Check) check.Report {
	jobs := plan(provider.Union(e.Providers), checks)
	width := e.width()
	home, err := treecopy.Home(e.Root)
	timed := err == nil && e.inCopies(ctx, width, home, jobs)
	rep := check.Report{SkipsFail: e.SkipsFail, Results: make([]check.Result, len(jobs))}
	// The checks of the run share what a reader computes once for all of
	// them, such as the files a marker walk reads (check.Shared).
	e.runAll(check.WithRun(ctx), width, jobs, rep.Results)
	if timed {
		// A record that cannot be written leaves the last one, or none,
		// and the next run copies as that one says.
		_ = writeDurations(home, jobs, rep.Results)
	}
	return rep
}

// job is one result of a report to compute: a check skipped with its gap,
// a run of the check, or a run of it on its witness. copies, when set, is
// the tree the job runs in a copy of.
type job struct {
	c       check.Check
	gap     string
	witness string
	copies  *treecopy.Tree
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
// the repository (mayWrite), on the repository or on a witness, and runs
// there rather than in a copy. A rule only reads, and a skip runs nothing.
func (j job) alone() bool {
	return j.gap == "" && mayWrite(j.c) && j.copies == nil
}

// inCopies sets the tree of the repository at e.Root on each job that
// would run alone, whose check runs a program (runsProgram), does not keep
// it in the repository (check.Check.InPlace) and ran for copyFloor or more
// the last time, or has no record in home, so the job runs on the pool in
// a copy of its own (ADR 0019). It does so only when copies can pay: when
// there are two such jobs or more and width, the pool's, is two or more,
// so they can run at once. Otherwise, or when the tree cannot be copied
// (treecopy.Open), every job runs as before, alone in the repository.
// timed is whether the run is to record its durations: whether copies
// could pay but for them.
func (e *Engine) inCopies(ctx context.Context, width int, home string, jobs []job) (timed bool) {
	movable := candidates(jobs)
	if len(movable) < 2 || width < 2 {
		return false
	}
	last := readDurations(home)
	movable = slices.DeleteFunc(movable, func(i int) bool { return !last.pays(jobs[i].name()) })
	if len(movable) < 2 {
		return true
	}
	tree, err := treecopy.Open(ctx, e.Root, home, treecopy.Budget)
	if err != nil {
		return true
	}
	for _, i := range movable {
		jobs[i].copies = tree
	}
	return true
}

// candidates is the index of each job that would run alone, whose check
// runs a program (runsProgram) and does not keep it in the repository.
func candidates(jobs []job) []int {
	var movable []int
	for i, j := range jobs {
		if j.alone() && runsProgram(j.c) && !j.c.InPlace {
			movable = append(movable, i)
		}
	}
	return movable
}

// name is j's name in the report: its check's id, and its witness's.
func (j job) name() string {
	return check.Result{Check: j.c, Witness: j.witness}.Name()
}

// runsProgram reports whether c is a program check or a generated check,
// the kinds that start a program and so pay for a copy. A kind with no
// runner runs nothing and stays where it is.
func runsProgram(c check.Check) bool {
	switch c.Kind.(type) {
	case *check.Program, *check.Generated:
		return true
	}
	return false
}

// mayWrite reports whether c, once run, may have written to the
// repository: every kind but a rule runs a program there (ADR 0011), so a
// rule after it reads the files again rather than what the run kept.
func mayWrite(c check.Check) bool {
	_, rule := c.Kind.(*check.Rule)
	return !rule
}

// run is j's result. A job with a tree to copy runs in a copy made for it
// in the directory of slot, the pool's worker that runs it, and removed
// once it ends, in a run of its own, so the copy shares no read with the
// repository's checks. A copy that cannot be made leaves the check
// unknown, since it did not run (copyFailed). made is called once a copy
// is made, or fails to be: from then on j no longer reads the repository.
func (e *Engine) run(ctx context.Context, j job, slot int, made func()) check.Result {
	if j.gap != "" {
		return check.Result{Check: j.c, Gap: j.gap}
	}
	if j.copies == nil {
		return e.runAt(ctx, e.Root, j)
	}
	cp, err := j.copies.Make(ctx, slot)
	made()
	if err != nil {
		return check.Result{Check: j.c, Witness: j.witness, Verdict: check.VerdictUnknown, Message: copyFailed(ctx, err)}
	}
	// A copy that cannot be removed stays in the cache; the check's
	// result stands, since the check ran.
	defer func() { _ = cp.Remove() }()
	return e.runAt(check.WithRun(ctx), cp.Root(), j)
}

// copyFailed is the message of a check whose copy could not be made, with
// err, why. A copy cut short because the caller cancelled errata's run, as
// an interrupt does, says so, and gives no advice: in_place would not have
// run the check either.
func copyFailed(ctx context.Context, err error) string {
	if cause := context.Cause(ctx); cause != nil {
		return fmt.Sprintf("cancelled: errata's run stopped before the check's copy was made, so it did not run (%v)", cause)
	}
	return fmt.Sprintf("errata could not copy the repository to run the check in, so it did not run: %v; in_place: true runs it in the repository", err)
}

// runAt runs j's check, or its witness, with root as the repository's root.
func (e *Engine) runAt(ctx context.Context, root string, j job) check.Result {
	if j.witness != "" {
		return e.witness(ctx, root, j.c, j.witness)
	}
	return e.Run(ctx, root, j.c)
}

// runAll puts each job's result into out at the job's index, on at most
// width workers, and at least one, each taking the next job in order.
// Every stretch of jobs between two that run alone runs at once. A job
// alone starts once no job before it reads the repository any longer:
// every rule and skip before it has ended, and every job in a copy before
// it has made its copy, which may still be running, since a write in the
// repository no longer reaches it. No job after it starts until it ends.
// It may have written to the repository, so the run drops what it kept
// (check.Wrote), and the copies read the disk again
// (treecopy.Tree.Refresh), before any job after it starts. One worker
// runs the jobs one at a time, in order, as a run on one core does. A job
// that panics panics again here, on the caller's goroutine, once every
// worker is done (caught), so the caller's defers run as they did when
// the checks ran on it. A job that panics in the repository, or before
// its copy is made, lets go of the repository (task.after) only once
// caught has its panic, so no job alone starts after it. A job that
// panics in its copy let go when the copy was made, so a job alone after
// it may already run; the panic is raised here all the same.
func (e *Engine) runAll(ctx context.Context, width int, jobs []job, out []check.Result) {
	next := make(chan task)
	var workers, reading sync.WaitGroup
	var first caught
	for slot := range max(1, min(width, len(jobs))) {
		workers.Go(func() {
			for t := range next {
				first.run(func() { t.run(slot) })
				t.after()
			}
		})
	}
	for i, j := range jobs {
		if !j.alone() {
			reading.Add(1)
			after := sync.OnceFunc(reading.Done)
			next <- task{run: func(slot int) { out[i] = e.run(ctx, j, slot, after) }, after: after}
			continue
		}
		if reading.Wait(); first.failed.Load() {
			break
		}
		done := make(chan struct{})
		next <- task{run: func(slot int) { out[i] = e.run(ctx, j, slot, func() {}) }, after: func() { close(done) }}
		if <-done; first.failed.Load() {
			break
		}
		check.Wrote(ctx)
		refresh(ctx, jobs[i+1:])
	}
	close(next)
	workers.Wait()
	first.again()
}

// task is a job for a worker of runAll: run runs it on the worker's slot,
// and after, called once run has returned or its panic is caught, lets
// the dispatcher go on.
type task struct {
	run   func(slot int)
	after func()
}

// refresh reads the disk again for the copies of jobs, when any job runs
// in one. Every such job shares one tree. When the disk cannot be read
// again, as when a check in place grew the ignored files past the budget,
// each of them runs in the repository instead, alone, as every check does
// when the tree cannot be copied at all.
func refresh(ctx context.Context, jobs []job) {
	for _, j := range jobs {
		if j.copies == nil {
			continue
		}
		if j.copies.Refresh(ctx) != nil {
			for k := range jobs {
				jobs[k].copies = nil
			}
		}
		return
	}
}

// caught is the first panic of a pool's jobs, with the stack of the worker
// it happened on. Safe for concurrent use.
type caught struct {
	once   sync.Once
	panic  *workerPanic
	failed atomic.Bool
}

// run runs job, and keeps its panic when it is the pool's first.
func (c *caught) run(job func()) {
	defer func() {
		if v := recover(); v != nil {
			c.once.Do(func() { c.panic = &workerPanic{value: v, stack: debug.Stack()} })
			c.failed.Store(true)
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
