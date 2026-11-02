package minimatch

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// meter is the work one compile or one match may do, and the context that
// stops it: the only place the package reads a context. A compile spends
// regex source from it and looks at it at each brace set it expands, before
// it parses each segment, and at each node of a segment's tree as it builds
// the source. A match spends steps from it, and it looks at its context
// every checkEvery steps. Once the step bound, the source bound or the
// context stops the work, err holds why, and every later look and spend
// refuses without looking. The brace bound (errExpansion) and the node
// bound (errNodes) are returned where they are found and do not set err.
// The node bound is the segment tree's (node.count), not the meter's: it
// resets for each segment, where a meter spans one call.
type meter struct {
	ctx context.Context
	err error
	// steps is what is left of MaxSteps.
	steps int
	// source is what is left of maxSource for the regexes of the glob's
	// segments, and written counts every unit their builds appended,
	// refused ones included.
	source  int
	written int
}

// MaxSteps is the most steps one Match may take: each try of a regex
// node, and each segment the globstar matcher compares. A class test
// counts more steps for a class that takes it more work (classWork).
const MaxSteps = 1 << 17

// maxSource is the most regex source the segments of one glob may build
// in all, its brace expansions' included.
const maxSource = 1 << 15

var errSource = errors.New("its segments build more than " + strconv.Itoa(maxSource) + " characters of regex source")

// errBudget is the error of a match that ran out of steps.
var errBudget = errors.New("matching takes more than the step budget")

// checkEvery is how many steps a match takes between two looks at its
// context. Through the CI runner on the build node the slowest step
// measured took 134 ns (classWork), so 1024 steps take about 0.14 ms.
const checkEvery = 1 << 10

// newMeter is the meter of one compile or one match under ctx, with all
// of MaxSteps and maxSource left.
func newMeter(ctx context.Context) *meter {
	return &meter{ctx: ctx, steps: MaxSteps, source: maxSource}
}

// look looks at the context, and is whether the work may go on.
func (w *meter) look() bool {
	if w.err == nil {
		if err := w.ctx.Err(); err != nil {
			w.err = fmt.Errorf("the match was stopped: %w", err)
		}
	}
	return w.err == nil
}

// step spends one step, and reports false once none is left or the
// context is done.
func (w *meter) step() bool { return w.spend(1) }

// spend spends n steps, and reports false once fewer than n are left or
// the context is done. It looks at the context each time what is left
// reaches or passes a multiple of checkEvery.
func (w *meter) spend(n int) bool {
	if n <= 0 {
		return true
	}
	if w.err != nil {
		return false
	}
	if w.steps < n {
		w.steps = 0
		w.err = errBudget
		return false
	}
	before := w.steps
	w.steps -= n
	if w.steps <= (before-1)/checkEvery*checkEvery {
		return w.look()
	}
	return true
}
