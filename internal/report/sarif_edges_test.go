package report

import (
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

// The tests in this file each pin a boundary of the size bound that no other
// test reached: Gremlins changed the comparison or the arithmetic there and
// every test still passed.

// fits keeps a file whose compact size is exactly the plain budget without
// gzipping it, and one whose upload is exactly the gzipped budget.
func TestSARIFFitsAtExactBudgets(t *testing.T) {
	b := []byte(strconv.Quote(cjk(rand.New(rand.NewPCG(9, 10)), 300)))
	if ok, err := (budget{plain: len(b), gzipped: 1}).fits(b, 0); err != nil || !ok {
		t.Errorf("compact size equal to the plain budget: fits = %v, %v", ok, err)
	}
	up := uploadSize(b, 2)
	if ok, err := (budget{plain: 1, gzipped: up}).fits(b, 2); err != nil || !ok {
		t.Errorf("upload equal to the gzipped budget: fits = %v, %v", ok, err)
	}
	if ok, err := (budget{plain: 1, gzipped: up - 1}).fits(b, 2); err != nil || ok {
		t.Errorf("upload one over the gzipped budget: fits = %v, %v", ok, err)
	}
}

// most finds the largest k whose size fits, and it finds it in the steps the
// guess from both ends and the halving after a poor guess give. The counts
// are pinned, each traced by hand through the guesses and halvings: a search
// that still finds k but guesses worse takes more.
func TestSARIFMostSteps(t *testing.T) {
	for _, c := range []struct {
		n, limit, want, calls int
		size                  func(k int) int
	}{
		{100, 375, 37, 4, func(k int) int { return 10 * k }},
		{100, 370, 37, 4, func(k int) int { return 10 * k }},
		{100, 999, 99, 2, func(k int) int { return 10 * k }},
		{100, 9, 0, 2, func(k int) int { return 10 * k }},
		{1000, 50_000, 223, 12, func(k int) int { return k * k }},
		{1000, 400_000, 632, 9, func(k int) int { return k * k }},
		{2, 15, 1, 2, func(k int) int { return 10 * k }},
		// A guess that halves the interval exactly is not followed by a
		// halving step.
		{100, 505, 50, 3, func(k int) int { return 10 * k }},
	} {
		calls := 0
		size := func(k int) (int, error) { calls++; return c.size(k), nil }
		got, err := most(c.n, c.limit, c.size(c.n), size)
		if err != nil || got != c.want || calls != c.calls {
			t.Errorf("most(%d, %d) = %d, %v in %d calls; want %d in %d", c.n, c.limit, got, err, calls, c.want, c.calls)
		}
	}
}

// shrink at a budget exactly the size of a cut keeps that cut: every result
// when the full file is exactly the budget, and the whole help and every
// notification when the file without results is exactly the budget. The
// warning names the budget, so each size is found at the budget it is
// measured under: the budget is set to the size until the two agree.
func TestSARIFShrinkAtExactBudgets(t *testing.T) {
	p := build(manyErrata(rand.New(rand.NewPCG(11, 12)), 40, 163))
	exact := func(c cuts) int {
		t.Helper()
		p.budget = budget{gzipped: 1}
		for range 10 {
			b, err := p.marshal(c, false)
			if err != nil {
				t.Fatal(err)
			}
			if n := uploadSize(b, c.results); n != p.budget.gzipped {
				p.budget.gzipped = n
				continue
			}
			return p.budget.gzipped
		}
		t.Fatalf("no budget equals the size of %+v", c)
		return 0
	}
	all := cuts{results: len(p.results), notes: len(p.notes)}
	full := exact(all)
	if got, err := p.shrink(full); err != nil || got != all {
		t.Errorf("budget equal to the full file: cuts %+v, %v; want %+v", got, err, all)
	}
	none := cuts{notes: len(p.notes)}
	exact(none)
	if got, err := p.shrink(full); err != nil || got != none {
		t.Errorf("budget equal to the file without results: cuts %+v, %v; want %+v", got, err, none)
	}
}

// A cut at zero characters keeps none, and a negative limit keeps all.
func TestSARIFCutAtZero(t *testing.T) {
	if got := plainCut("ab", 0); got != "" {
		t.Errorf("plainCut at 0 = %q", got)
	}
	if got := plainCut("a{b", -1); got != "a{{b" {
		t.Errorf("plainCut at -1 = %q", got)
	}
	if got := cut("ab", "value", 0); got != " (cut: the value is 2 bytes)" {
		t.Errorf("cut at 0 = %q", got)
	}
	if got := cut("ab", "value", -1); got != "ab" {
		t.Errorf("cut at -1 = %q", got)
	}
}

// The warning counts notifications left out in the singular and the plural,
// and says nothing of them when none is.
func TestSARIFWarningCountsNotifications(t *testing.T) {
	p := pieces{notes: make([]sarifNotification, 3), budget: budget{gzipped: 5}}
	for kept, want := range map[int]string{
		0: "3 notifications left out",
		2: "1 notification left out",
		3: "",
	} {
		got := p.warning(cuts{notes: kept})
		if want == "" && got != "" || !strings.Contains(got, want) {
			t.Errorf("%d of 3 kept: warning %q, want %q", kept, got, want)
		}
	}
}
