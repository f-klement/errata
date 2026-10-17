// Package engine runs a set of checks and produces a report. It decides
// whether each check can run; the protocol package decides what it returns.
package engine

import (
	"context"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/protocol"
	"github.com/f-klement/errata/internal/provider"
)

// Runner executes one check. The engine takes it as a field so tests can
// replace the check protocol with a fake.
type Runner func(ctx context.Context, root string, c check.Check) check.Result

// Engine runs checks against a repository root with a set of providers.
type Engine struct {
	Root      string
	Providers []provider.Provider
	Run       Runner
}

// New returns an engine that runs checks through the check protocol.
func New(root string, providers []provider.Provider) *Engine {
	return &Engine{Root: root, Providers: providers, Run: protocol.Run}
}

// Execute runs every check that the available providers can reach and skips
// the rest with the reason. Checks run in order, one at a time; parallelism
// can come later without changing the report.
func (e *Engine) Execute(ctx context.Context, checks []check.Check) check.Report {
	caps := provider.Union(e.Providers)
	var rep check.Report
	for _, c := range checks {
		if ok, gap := provider.Reaches(caps, c.Scope); !ok {
			rep.Results = append(rep.Results, check.Result{Check: c, Gap: gap})
			continue
		}
		rep.Results = append(rep.Results, e.Run(ctx, e.Root, c))
	}
	return rep
}
