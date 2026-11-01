package minimatch

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// lookCounter is a context that counts its looks, through Err, and is
// done from the look after the first doneAfter, or never when doneAfter is
// negative.
type lookCounter struct {
	context.Context
	looks, doneAfter int
}

func (c *lookCounter) Err() error {
	c.looks++
	if c.doneAfter >= 0 && c.looks > c.doneAfter {
		return context.Canceled
	}
	return nil
}

// matchContext is Match under ctx: the compile and the match, as a caller
// of the package runs them.
func matchContext(ctx context.Context, file, glob string) (bool, error) {
	match, _, err := CompileContext(ctx, glob).MatchSteps(ctx, file)
	return match, err
}

// The glob of TestMatchBounds that spends every step: one segment, and a
// match that takes all MaxSteps.
var (
	stepsFile = strings.Repeat("a", 1000) + "b"
	stepsGlob = strings.Repeat("*(a|aa)", 140)
)

// A compile and its match look at their context once before each segment
// the compile parses, once for each node of a segment's tree as it builds
// the source, and once every 1024 steps of the match: 3 segments; a node each for x and y, and
// for the root of the third, whose 140 extglobs have two alternatives
// each; then 128 looks in the 131072 steps before the budget runs out. The
// count is written out, so a change to checkEvery fails here.
func TestCompileAndMatchLookAtTheirContext(t *testing.T) {
	ctx := &lookCounter{Context: context.Background(), doneAfter: -1}
	_, err := matchContext(ctx, "x/y/"+stepsFile, "x/y/"+stepsGlob)
	if err == nil || err.Error() != "matching takes more than the step budget" {
		t.Fatalf("error %v, want the step budget", err)
	}
	if want := 3 + 1 + 1 + 1 + 140*3 + 128; ctx.looks != want || MaxSteps != 131072 {
		t.Errorf("%d looks, want %d", ctx.looks, want)
	}
}

// A done context stops the compile before its first segment, before a
// later one, and the match at its next look, with an error that wraps the
// context's.
func TestCompileAndMatchStop(t *testing.T) {
	for _, c := range []struct {
		name      string
		doneAfter int
		file      string
		glob      string
		maxLooks  int
	}{
		{"before the first segment", 0, "a", "a*", 1},
		{"before the third segment", 2, "x/y/z", "x/y/z*", 3},
		{"in the match", 1, stepsFile, stepsGlob, 2},
	} {
		ctx := &lookCounter{Context: context.Background(), doneAfter: c.doneAfter}
		_, err := matchContext(ctx, c.file, c.glob)
		if !errors.Is(err, context.Canceled) || err.Error() != "the match was stopped: context canceled" {
			t.Errorf("%s: error %v, want the match stopped", c.name, err)
		}
		if ctx.looks != c.maxLooks {
			t.Errorf("%s: %d looks, want %d", c.name, ctx.looks, c.maxLooks)
		}
	}
}

// Match is a compile and match under a context that is never done.
func TestMatchRunsUnderTheBackground(t *testing.T) {
	if ok, err := Match("a/b.yaml", "a/*.yaml"); !ok || err != nil {
		t.Errorf("Match = %v, %v; want true", ok, err)
	}
	if _, err := Match(stepsFile, stepsGlob); err == nil || err.Error() != "matching takes more than the step budget" {
		t.Errorf("error %v, want the step budget", err)
	}
}

// Brace expansion looks at its context at each brace set, so a glob of
// many sets stops before it has expanded them all.
func TestBraceExpansionLooksAtItsContext(t *testing.T) {
	ctx := &lookCounter{Context: context.Background(), doneAfter: 1}
	_, err := braceExpandContext(ctx, toUnits(strings.Repeat("{a,b}", 200)))
	if !errors.Is(err, context.Canceled) || ctx.looks != 2 {
		t.Errorf("error %v after %d looks, want the expansion stopped at the second", err, ctx.looks)
	}
}

// A segment's source build looks at its context at each node, so one
// segment of thousands of nodes stops inside its build.
func TestSourceBuildLooksAtItsContext(t *testing.T) {
	ctx := &lookCounter{Context: context.Background(), doneAfter: 2}
	n := fromGlob(toUnits("x!(a)x!(a)" + strings.Repeat("a@(", 100) + "a" + strings.Repeat(")", 100)))
	n.flatten()
	n.fillNegs()
	_, _, _, _, err := n.rootSource(toUnits("x"), &sourceBudget{left: maxSource, ctx: ctx})
	if !errors.Is(err, context.Canceled) || ctx.looks != 3 {
		t.Errorf("error %v after %d looks, want the build stopped at the third", err, ctx.looks)
	}
}

// A class test can spend several steps at once, so the budget looks at its
// context whenever what is left reaches or passes a multiple of
// checkEvery, and not only when it lands on one: 3 steps from 1025 or 1026
// pass 1024, from 1027 they land on it, and from 1024 they pass none.
func TestSpendLooksPastAMultipleOfCheckEvery(t *testing.T) {
	for _, c := range []struct{ left, looks int }{
		{checkEvery + 1, 1}, {checkEvery + 2, 1}, {checkEvery + 3, 1}, {checkEvery, 0}, {checkEvery + 4, 0},
	} {
		ctx := &lookCounter{Context: context.Background(), doneAfter: -1}
		b := &budget{left: c.left, ctx: ctx}
		if !b.spend(3) || b.left != c.left-3 || ctx.looks != c.looks {
			t.Errorf("3 steps from %d: %d left after %d looks, want %d looks", c.left, b.left, ctx.looks, c.looks)
		}
	}
	// A class test that took little work spends no more steps, and does
	// not look, even with none left and the context done.
	for _, left := range []int{0, checkEvery} {
		ctx := &lookCounter{Context: context.Background(), doneAfter: 0}
		b := &budget{left: left, ctx: ctx}
		if !b.spend(0) || b.left != left || ctx.looks != 0 {
			t.Errorf("no steps from %d: %d left after %d looks, want no look", left, b.left, ctx.looks)
		}
	}
}
