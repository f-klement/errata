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

// backgroundMeter is a meter whose context is never done, with all of
// MaxSteps and maxSource left.
func backgroundMeter() *meter { return newMeter(context.Background()) }

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

// A meter stops at the first look that finds its context done, with an
// error that wraps the context's, and from then on refuses every look and
// spend without looking again.
func TestTheMeterStopsAtTheFirstDoneLook(t *testing.T) {
	ctx := &lookCounter{Context: context.Background(), doneAfter: 2}
	w := newMeter(ctx)
	for i := range 2 {
		if !w.look() {
			t.Fatalf("look %d, before the context is done, stopped the meter: %v", i+1, w.err)
		}
	}
	if w.look() || !errors.Is(w.err, context.Canceled) || w.err.Error() != "the match was stopped: context canceled" {
		t.Fatalf("the third look: error %v, want the match stopped", w.err)
	}
	if w.look() || w.step() || w.spend(checkEvery) || ctx.looks != 3 {
		t.Errorf("a stopped meter went on, or looked again: %d looks", ctx.looks)
	}
}

// A meter spends exactly MaxSteps, and refuses the step after them; a
// spend of more than is left refuses and leaves none.
func TestTheMeterHoldsMaxSteps(t *testing.T) {
	w := backgroundMeter()
	for range MaxSteps / checkEvery {
		if !w.spend(checkEvery) {
			t.Fatalf("refused with %d steps left: %v", w.steps, w.err)
		}
	}
	if w.step() || !errors.Is(w.err, errBudget) {
		t.Errorf("the step past MaxSteps: error %v, want %v", w.err, errBudget)
	}
	w = backgroundMeter()
	w.steps = 2
	if w.spend(3) || w.steps != 0 || !errors.Is(w.err, errBudget) {
		t.Errorf("3 steps of 2: %d left, error %v; want none and %v", w.steps, w.err, errBudget)
	}
}

// A match looks at its context at most once every checkEvery steps, and a
// context done at any point stops it within checkEvery steps, plus the
// rest of the spend that crosses the look, whatever it spends at a time.
// ADR 0016 states the cadence as 1024 steps.
func TestTheMeterLooksEveryCheckEvery(t *testing.T) {
	if checkEvery != 1024 {
		t.Errorf("checkEvery is %d, and ADR 0016 says 1024", checkEvery)
	}
	for _, n := range []int{1, 3, 7} {
		counter := &lookCounter{Context: context.Background(), doneAfter: -1}
		w := newMeter(counter)
		for w.spend(n) {
		}
		if want := MaxSteps / checkEvery; counter.looks > want {
			t.Errorf("spends of %d: %d looks in MaxSteps, want at most %d", n, counter.looks, want)
		}
		for _, at := range []int{0, 1, checkEvery - 1, 5*checkEvery + 17} {
			ctx, cancel := context.WithCancel(context.Background())
			w := newMeter(ctx)
			for MaxSteps-w.steps < at {
				w.spend(n)
			}
			cancel()
			done := MaxSteps - w.steps
			for w.spend(n) {
			}
			if after := MaxSteps - w.steps - done; !errors.Is(w.err, context.Canceled) || after > checkEvery+n-1 {
				t.Errorf("spends of %d, done after %d steps: %d more steps, error %v; want at most %d and the stop",
					n, done, after, w.err, checkEvery+n-1)
			}
		}
	}
}

// A compile spends from its meter the source of the regexes it keeps, and
// may spend exactly maxSource: a meter with the source a glob's segments
// build compiles it, and one with a unit less refuses it. The refusal is
// the meter's, so its later looks refuse too.
func TestTheMeterHoldsTheSourceBound(t *testing.T) {
	glob := toUnits("a*/{b,c}?d/e")
	w := backgroundMeter()
	m, err := compile(w, glob)
	if err != nil {
		t.Fatal(err)
	}
	built := 0
	for _, segs := range m.set {
		for _, s := range segs {
			if s.re != nil {
				built += len(s.re.source)
			}
		}
	}
	if spent := maxSource - w.source; spent != built || built == 0 {
		t.Fatalf("spent %d units of source, and the regexes hold %d", spent, built)
	}
	exact := backgroundMeter()
	exact.source = built
	if _, err := compile(exact, glob); err != nil {
		t.Errorf("with the %d units it builds: %v", built, err)
	}
	short := backgroundMeter()
	short.source = built - 1
	if _, err := compile(short, glob); !errors.Is(err, errSource) || !errors.Is(short.err, errSource) || short.look() {
		t.Errorf("with one unit fewer: error %v, the meter's %v; want %v on both, and no later look", err, short.err, errSource)
	}
}

// Every look stops the work: a compile looks at each brace set it
// expands, before each segment it parses and at each node of a segment's
// tree as it builds the source, and a match every checkEvery steps. So a
// context done from any of the first looks such a glob takes stops it,
// at that look, whatever looks come before. Each glob has at least looks
// of its kind: 200 brace sets; 51 segments of ** that build no source,
// between 50 of x; a segment of 101 nested nodes; and the match of
// TestMatchBounds that spends every step. The looks of one kind come from
// one line of code, so make test tries those lookTried picks, and
// TestEveryLookStopsTheWorkSlow the rest: trying all 128 of the match's
// took 8 s under make test's flags on the slowest node.
func TestEveryLookStopsTheWork(t *testing.T) { checkLooks(t, true) }

// The looks of TestEveryLookStopsTheWork that lookTried leaves to the slow
// target.
func TestEveryLookStopsTheWorkSlow(t *testing.T) {
	slowOnly(t)
	checkLooks(t, false)
}

// lookTried is whether make test tries look k of n: the first two, every
// eighth and the last.
func lookTried(k, n int) bool { return k <= 1 || k%8 == 0 || k == n-1 }

// checkLooks tries the looks of each kind that lookTried picks, or the
// others.
func checkLooks(t *testing.T, picked bool) {
	t.Helper()
	tried := 0
	for _, c := range []struct {
		name, file, glob string
		looks            int
	}{
		{"brace sets", "a", strings.Repeat("{a,b}", 200), 200},
		{"segments", "x", strings.Repeat("**/x/", 50) + "**", 101},
		{"nodes", "a", strings.Repeat("a@(", 100) + "a" + strings.Repeat(")", 100), 101},
		{"steps", stepsFile, stepsGlob, MaxSteps / checkEvery},
	} {
		for k := range c.looks {
			if lookTried(k, c.looks) != picked {
				continue
			}
			tried++
			ctx := &lookCounter{Context: context.Background(), doneAfter: k}
			_, err := matchContext(ctx, c.file, c.glob)
			if !errors.Is(err, context.Canceled) || ctx.looks != k+1 {
				t.Errorf("%s, done from look %d: error %v after %d looks, want the stop at that look", c.name, k+1, err, ctx.looks)
				break
			}
		}
	}
	if tried == 0 {
		t.Fatal("no look was tried: the test and its slow twin must split the looks")
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

// A class test can spend several steps at once, so the meter looks at its
// context whenever what is left reaches or passes a multiple of
// checkEvery, and not only when it lands on one: 3 steps from 1025 or 1026
// pass 1024, from 1027 they land on it, and from 1024 they pass none.
func TestSpendLooksPastAMultipleOfCheckEvery(t *testing.T) {
	for _, c := range []struct{ left, looks int }{
		{checkEvery + 1, 1}, {checkEvery + 2, 1}, {checkEvery + 3, 1}, {checkEvery, 0}, {checkEvery + 4, 0},
	} {
		ctx := &lookCounter{Context: context.Background(), doneAfter: -1}
		w := newMeter(ctx)
		w.steps = c.left
		if !w.spend(3) || w.steps != c.left-3 || ctx.looks != c.looks {
			t.Errorf("3 steps from %d: %d left after %d looks, want %d looks", c.left, w.steps, ctx.looks, c.looks)
		}
	}
	// A class test that took little work spends no more steps, and does
	// not look, even with none left and the context done.
	for _, left := range []int{0, checkEvery} {
		ctx := &lookCounter{Context: context.Background(), doneAfter: 0}
		w := newMeter(ctx)
		w.steps = left
		if !w.spend(0) || w.steps != left || ctx.looks != 0 {
			t.Errorf("no steps from %d: %d left after %d looks, want no look", left, w.steps, ctx.looks)
		}
	}
}
