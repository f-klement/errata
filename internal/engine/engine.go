// Package engine runs a set of checks and produces a report. It decides
// whether each check can run; the protocol package decides what a program
// check returns, and the rule package what a rule returns.
package engine

import (
	"context"

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

// New returns an engine that runs a program check through the check protocol
// and a rule through the rule runner.
func New(root string, providers []provider.Provider) *Engine {
	return &Engine{Root: root, Providers: providers, Run: Dispatch}
}

// Dispatch is the production Runner: it sends a rule to rule.Run and a
// program check to protocol.Run.
func Dispatch(ctx context.Context, root string, c check.Check) check.Result {
	if c.IsRule() {
		return rule.Run(ctx, root, c)
	}
	return protocol.Run(ctx, root, c)
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
