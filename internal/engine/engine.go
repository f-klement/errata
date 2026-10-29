// Package engine runs a set of checks and produces a report. It decides
// whether each check can run, and runs it by the row of its kind in a
// dispatch table: the protocol package decides what a program check and a
// generated check return, and the rule package what a rule returns.
package engine

import (
	"context"
	"fmt"
	"reflect"

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
// the rest with the reason. Checks run in order, one at a time; parallelism
// can come later without changing the report.
func (e *Engine) Execute(ctx context.Context, checks []check.Check) check.Report {
	caps := provider.Union(e.Providers)
	rep := check.Report{SkipsFail: e.SkipsFail}
	for _, c := range checks {
		if ok, gap := provider.Reaches(caps, c.Scope); !ok {
			rep.Results = append(rep.Results, check.Result{Check: c, Gap: gap})
			continue
		}
		rep.Results = append(rep.Results, e.Run(ctx, e.Root, c))
	}
	return rep
}
