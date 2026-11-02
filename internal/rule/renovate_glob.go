package rule

import (
	"context"
	"errors"
	"fmt"

	"github.com/f-klement/errata/internal/minimatch"
)

// globPattern is a glob entry compiled by internal/minimatch, which holds
// why the port refuses it where it does.
type globPattern struct {
	entry string
	g     *minimatch.Glob
}

// compileGlob compiles a glob entry under ctx, which minimatch looks at
// as it compiles (ADR 0016).
func compileGlob(ctx context.Context, entry string) *globPattern {
	return &globPattern{entry: entry, g: minimatch.CompileContext(ctx, entry)}
}

// match is s matched against the glob under ctx, which the match looks at
// every 1024 steps whatever context the glob was compiled under, and the
// steps it took. A glob on which minimatch throws, one past the port's
// bounds, and a string that is not UTF-8 are refusals. A match or a
// compile that ctx stopped is not: its error wraps ctx's.
func (p *globPattern) match(ctx context.Context, s string) (bool, matchWork, error) {
	match, steps, err := p.g.MatchSteps(ctx, s)
	work := matchWork{steps: steps}
	if err != nil && ctx.Err() != nil {
		return false, work, fmt.Errorf("the evaluation was stopped while the glob %q ran: %w", p.entry, err)
	}
	var thrown *minimatch.SyntaxError
	if errors.As(err, &thrown) {
		err = fmt.Errorf("minimatch throws on the glob, as JavaScript refuses the regex it builds: %w", err)
	}
	if err != nil {
		return false, work, &refusal{form: "glob", entry: p.entry, s: s, reason: err}
	}
	return match, work, nil
}

// size is the glob's minimatch.Glob.Size: one unit for each segment and
// each code unit of its literals and regex sources, and for a refused glob
// each byte of the source its error keeps.
func (p *globPattern) size() int { return p.g.Size() }

// compileCost is renovateCompileWeight for each byte of the glob: its
// compile takes time that grows with the glob.
func (p *globPattern) compileCost() uint64 {
	return uint64(len(p.entry)) * renovateCompileWeight
}
