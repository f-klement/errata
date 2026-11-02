package rule

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"

	"github.com/f-klement/errata/internal/minimatch"
)

// ADR 0014's example shape, 200 files against 10 glob entries that mostly
// miss, is 2000 calls of 10 globs. Charged a compile at each call, they
// cost 1055807 and the rule ended unknown on the cost limit; the cache
// charges each glob once, they cost 171053, and the rule has its verdict.
// The globs are long enough that the first figure passes the limit, so
// the test fails when every call pays. Under a cost limit of 171053 the
// rule has its verdict, and under one less it ends on the limit, so the
// figure is exact.
func TestGlobCacheGivesTheADRShapeAVerdict(t *testing.T) {
	patterns := []string{
		"deploy/charts/**/Chart.yaml", "infra/**/kustomization.yaml", "**/values{,-*}.ya?ml",
		"deploy/templates/**/*.yaml", "infra/*/helmfile.yaml", "docker/**/Dockerfile",
		"config/**/docker-compose.y?ml", "infra/argocd/projects/*.yaml", "**/.gitlab-ci.yml",
		"infra/argocd/applications/*.yaml",
	}
	files := make([]string, 200)
	for i := range files {
		files[i] = fmt.Sprintf("infra/argocd/applications/app-%03d.yaml", i)
	}
	a, err := compileNamed("size(patterns) > 0 && by_file(apps).all(f, patterns.exists(p, renovate_match(f, p)))",
		[]string{"patterns", "apps"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	extracted := asExtracted(map[string][]string{"patterns": patterns, "apps": files}, map[string][]string{"apps": files}, nil)
	got, err := a.Eval(context.Background(), extracted)
	if err != nil || !got {
		t.Errorf("200 files against 10 globs: %v, %v; want true", got, err)
	}
	for _, c := range []struct {
		limit uint64
		want  string
	}{{171053, ""}, {171052, "the evaluation exceeded the cost limit of 171052"}} {
		a, err := compile("size(patterns) > 0 && by_file(apps).all(f, patterns.exists(p, renovate_match(f, p)))",
			[]string{"patterns", "apps"}, limits{cost: c.limit, timeout: EvalTimeout, grace: stopGrace})
		if err != nil {
			t.Fatal(err)
		}
		got, err := a.Eval(context.Background(), extracted)
		if c.want == "" && (err != nil || !got) || c.want != "" && (err == nil || err.Error() != c.want) {
			t.Errorf("under a cost limit of %d: %v, %v; want %q", c.limit, got, err, c.want)
		}
	}
}

// The cache lives in the evaluation's log, so it ends with the evaluation:
// under a cost limit of 10000, one call of a glob of 1024 bytes, about
// 19500 with its compile and 1050 without, is stopped in every
// evaluation, and not only in the first.
func TestGlobCacheIsPerEvaluation(t *testing.T) {
	a, err := compile("size(v) == 2 && renovate_match(v[0], v[1])", []string{"v"},
		limits{cost: 10000, timeout: EvalTimeout, grace: stopGrace})
	if err != nil {
		t.Fatal(err)
	}
	want := "the evaluation exceeded the cost limit of 10000"
	for i := range 2 {
		_, err := a.Eval(context.Background(), asExtracted(map[string][]string{"v": {"b", strings.Repeat("a", MaxArgument)}}, nil, nil))
		if err == nil || err.Error() != want {
			t.Errorf("evaluation %d: error %v, want %q", i+1, err, want)
		}
	}
}

// A call through the cache returns its verdict and its cost: the first
// call of a glob or a regex pays its compile, and a later call of it does
// not; * pays no compile, so it costs the same twice. A glob through
// no cache pays at every call.
func TestCachedCallCost(t *testing.T) {
	cache := (&errorLog{}).patternCache()
	for _, c := range []struct {
		name, s, p   string
		verdict      ref.Val
		first, again uint64
	}{
		{"a glob", "ab", "*.yaml", types.False, 1 + 2 + 6 + 6*18, 1 + 2 + 6},
		{"a regex", "ab", "/a/", types.True, 1 + 2 + 3 + 2*4/160 + 3*18 + 3 + 1, 1 + 2 + 3},
		{"*", "ab", "*", types.True, 1 + 2 + 1, 1 + 2 + 1},
	} {
		for call, want := range []uint64{c.first, c.again} {
			v, cost := renovateCall(context.Background(), types.String(c.s), types.String(c.p), cache)
			if v != c.verdict || cost != want {
				t.Errorf("%s, call %d: %v at cost %d, want %v at %d", c.name, call+1, v, cost, c.verdict, want)
			}
		}
	}
	for call := range 2 {
		if v, cost := renovateCall(context.Background(), types.String("ab"), types.String("*.yml"), nil); v != types.False || cost != 1+2+5+5*18 {
			t.Errorf("a glob through no cache, call %d: %v at cost %d", call+1, v, cost)
		}
	}
}

// The cache holds at most maxCachedPatterns globs and maxCachedUnits
// units of their Size. A glob past either bound is compiled and charged at
// every call. Full, it holds at most 8 MiB: filled with small globs, each
// with its fixed overhead, or with the shape that held the most for each
// unit, 200 segments of /**/1, about 61.7 bytes a unit, ahead of 200 of
// /**/a, about 59.5, and 500 segments of one letter under four brace sets,
// about 48 (under nocase each letter is a regex of 3 units, and one for
// the segment). Globs whose regex sources come near minimatch's bound of
// 32768 units fill the units as well, and so do refused globs: ten
// negations then a class JavaScript refuses is 56 bytes, whose error keeps
// a source of 26092, and Size counts it. Under the race detector, where
// the seven shapes took 7.9 s on the build node with make test's coverage,
// it fills the cache with three of them: small globs for the count, and
// refused globs and globstar segments for the units.
func TestGlobCacheIsBounded(t *testing.T) {
	heap := func() int64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return int64(m.HeapAlloc)
	}
	for _, c := range []struct {
		name        string
		glob        func(i int) string
		calls, held int
		underRace   bool
	}{
		{"small globs", func(i int) string { return fmt.Sprintf("**/x%04d/*.yaml", i) }, maxCachedPatterns + 10, maxCachedPatterns, true},
		{"one letter segments", func(i int) string {
			return fmt.Sprintf("{a,b}{a,b}{a,b}{a,%04d}", i) + strings.Repeat("/a", 500)
		}, 4, 2, false},
		{"literal segments", func(i int) string {
			return fmt.Sprintf("{a,b}{a,b}{a,b}{a,%04d}", i) + strings.Repeat("/1", 500)
		}, 8, 4, false},
		{"regex sources", func(i int) string {
			return fmt.Sprintf("{a,b}{a,b}{a,b}{a,%04d}", i) + strings.Repeat("*a", 280)
		}, 4, 2, false},
		{"refused globs", func(i int) string {
			return strings.Repeat("!(*)", 10) + fmt.Sprintf("[[:alpha:]] x%03d", i)
		}, 4, 2, true},
		{"globstar segments", func(i int) string {
			return fmt.Sprintf("x%04d", i) + strings.Repeat("/**/a", 200)
		}, 70, 65, true},
		{"globstar and literal segments", func(i int) string {
			return fmt.Sprintf("x%04d", i) + strings.Repeat("/**/1", 200)
		}, 110, 107, false},
	} {
		if raceEnabled && !c.underRace {
			continue
		}
		log := &errorLog{}
		cache := log.patternCache()
		base := heap()
		for i := range c.calls {
			cache.compiled(context.Background(), c.glob(i))
		}
		held := heap() - base
		if len(cache.entries) != c.held || cache.units > maxCachedUnits {
			t.Errorf("%s: the cache holds %d globs of %d units, want %d globs and at most %d units",
				c.name, len(cache.entries), cache.units, c.held, maxCachedUnits)
		}
		t.Logf("%s: %d globs of %d units hold %d bytes", c.name, len(cache.entries), cache.units, held)
		if held > 8<<20 {
			t.Errorf("%s: the full cache holds %d bytes, more than 8 MiB", c.name, held)
		}
		past := c.glob(c.calls)
		for call := range 2 {
			if _, paid := cache.compiled(context.Background(), past); !paid {
				t.Errorf("%s: a glob past the bound did not pay at call %d", c.name, call+1)
			}
		}
		runtime.KeepAlive(log)
	}
}

// The steps charge raises the cost of some ordinary globs: an extglob, a
// class, or a * in a segment the port matches as a regex can take more
// steps over 8 than the product of the lengths over 64. A glob whose
// segments minimatch tests without running a regex, such as **/*.yaml,
// costs the product, as before the charge.
func TestRenovateMatchChargesAnOrdinaryGlobByItsSteps(t *testing.T) {
	for _, c := range []struct {
		s, glob              string
		verdict              ref.Val
		steps, cost, product uint64
	}{
		{".gitlab-ci.yml", "**/!(*.md)", types.True, 86, 35, 27},
		{strings.Repeat("a", 1000), "**/*([[:word:]]).yml", types.False, 7006, 1896, 1333},
		{"infra/argocd/applications/app-001.yaml", "**/*.yaml", types.True, 1, 53, 53},
	} {
		v, cost, steps := cachedCall(t, c.s, c.glob)
		product := renovateWork{s: len(c.s), pattern: len(c.glob), pair: true}.cost()
		if v != c.verdict || uint64(steps) != c.steps || cost != c.cost || product != c.product {
			t.Errorf("%s against %.20s: %v, %d steps cost %d, by the product %d; want %v, %d, %d and %d",
				c.glob, c.s, v, steps, cost, product, c.verdict, c.steps, c.cost, c.product)
		}
	}
}

// cachedCall is renovate_match(s, glob) through a cache that has already
// compiled glob, so the call pays no compile: its verdict, its cost, and
// the steps its match took.
func cachedCall(t *testing.T, s, glob string) (ref.Val, uint64, int) {
	t.Helper()
	cache := (&errorLog{}).patternCache()
	cache.compiled(context.Background(), glob)
	_, work, err := renovateMatchesCompiling(context.Background(), s, glob, cache)
	if err != nil {
		t.Fatalf("%s against %.20s: %v", glob, s, err)
	}
	v, cost := renovateCall(context.Background(), types.String(s), types.String(glob), cache)
	return v, cost, work.steps
}

// A glob that fills the cache's units exactly is kept: 65535 ones are one
// literal segment of Size 65536. One more unit is not.
func TestGlobCacheHoldsAGlobThatFillsIt(t *testing.T) {
	for _, c := range []struct {
		ones int
		held int
	}{{maxCachedUnits - 1, 1}, {maxCachedUnits, 0}} {
		cache := (&errorLog{}).patternCache()
		cache.compiled(context.Background(), strings.Repeat("1", c.ones))
		if len(cache.entries) != c.held {
			t.Errorf("%d ones: the cache holds %d globs, want %d", c.ones, len(cache.entries), c.held)
		}
	}
}

// stepsGlob is x, then k of @(|), then a class of m Cyrillic code points:
// against xy every one of the 2^k paths tests the class, so a match spends
// its steps against a string of two bytes, and k of 16 spends all of
// MaxSteps (glob-cache review, finding 1).
func stepsGlob(k, m int) string {
	class := make([]rune, m)
	for i := range class {
		class[i] = rune(0x400 + i)
	}
	return "x" + strings.Repeat("@(|)", k) + "[" + string(class) + "]"
}

// oneStepOf is calls of renovate_match(v[0], v[1]) in one step, as the
// most an expression holds: CEL looks at the timeout only between
// comprehension steps.
func oneStepOf(calls int) string {
	return "size(v)==2&&cel.bind(f,v[0],cel.bind(p,v[1]," + strings.Repeat("renovate_match(f,p)||", calls-1) + "renovate_match(f,p)))"
}

// A glob compiled once still pays for each match by the steps it took, so
// the cost limit stops a step of one repeated glob that spends its steps.
// Charged by the lengths alone, 40 calls of it against xy cost about
// 57000, under the limit of 100000 here; charged by the steps, each match
// costs 16384 more, and the fifth call is stopped.
func TestRenovateMatchChargesAMatchByItsSteps(t *testing.T) {
	a, err := compile(oneStepOf(40), []string{"v"}, limits{cost: 100000, timeout: time.Minute, grace: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Eval(context.Background(), asExtracted(map[string][]string{"v": {"xy", stepsGlob(16, 450)}}, nil, nil))
	// Each match spends every step and is refused, which the message
	// names after the cost limit.
	if want := "the evaluation exceeded the cost limit of 100000; before it, renovate_match: the glob "; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error %v, want one starting %q", err, want)
	}
}

// classGlob is x, then k of @(|), then the class: against a string of
// two characters each of the 2^k paths tests the class once.
func classGlob(k int, class string) string {
	return "x" + strings.Repeat("@(|)", k) + "[" + class + "]"
}

// apartClass is 470 code points of which no two touch: 470 ranges that no
// merge shrinks.
func apartClass() string {
	var apart strings.Builder
	for i := range 470 {
		apart.WriteRune(rune(0x100 + 2*i))
	}
	return apart.String()
}

// A class test is charged by the work it takes (minimatch's classWork):
// with 12 of @(|) the class is tested 4096 times, and [ab] costs a step a
// test. The 470 ranges apart cost 3 more steps a test, so the call costs
// 4066 where a step a test made it 2530; 119 of [:word:] name its 4
// properties once and cost 2 more, 3568, where 476 properties would spend
// every step, 17392.
func TestRenovateMatchChargesAClassTestByItsWork(t *testing.T) {
	for _, c := range []struct {
		name, class, s string
		verdict        ref.Val
		cost           uint64
	}{
		{"[ab]", "ab", "xy", types.False, 1 + 2 + 53 + 12288/8},
		{"470 ranges apart", apartClass(), "xy", types.False, 1 + 2 + 991 + (12288+4096*3)/8},
		{"119 of [:word:]", strings.Repeat("[:word:]", 119), "xⒶ", types.False, 1 + 4 + 1003 + (12288+4096*2)/8},
	} {
		if v, got, _ := cachedCall(t, c.s, classGlob(12, c.class)); v != c.verdict || got != c.cost {
			t.Errorf("%s: %v at cost %d, want %v at %d", c.name, v, got, c.verdict, c.cost)
		}
	}
}

// So a step of calls of one cached glob whose class is slow to test ends
// on the cost limit soon, as one whose class is fast does. Through the CI
// runner on the build node, 119 of [:word:] after x and 16 of @(|) took
// 2.15 s a match against xⒶ when each test cost one step and asked every
// property it named, and with both the work and the naming once planted
// out, 40 calls of it ran 49 s under CostLimit; with them, 190 calls end
// on the limit after 0.65 s. The limit of 150000 here lets 8 matches
// start, which planted out ran past the timeout of 5 s on the build node.
// Its point is that wall time, so it is slow only: under the race detector
// it took 2.2 s on the build node, and 21.5 s with make test's coverage
// too. TestRenovateMatchChargesAClassTestByItsWork holds the charge itself
// in make test and in each mutation pass.
func TestRenovateMatchStopsAClassOnTheCostLimit(t *testing.T) {
	slowOnly(t)
	a, err := compile(oneStepOf(40), []string{"v"}, limits{cost: 150000, timeout: EvalTimeout, grace: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, class, s string }{
		{"119 of [:word:]", strings.Repeat("[:word:]", 119), "xⒶ"},
		{"470 ranges apart", apartClass(), "xy"},
	} {
		_, err = a.Eval(context.Background(), asExtracted(map[string][]string{"v": {c.s, classGlob(16, c.class)}}, nil, nil))
		// Each match spends every step and is refused, which the message
		// names after the cost limit.
		if want := "the evaluation exceeded the cost limit of 150000; before it, renovate_match: the glob "; err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: error %v, want one starting %q", c.name, err, want)
		}
	}
}

// A compile the timeout stopped is not cached: its Glob holds the stop's
// error, which no later evaluation of the glob should read.
func TestGlobCacheKeepsNoStoppedCompile(t *testing.T) {
	cache := (&errorLog{}).patternCache()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g, _ := cache.compiled(ctx, "a/*.yaml")
	if _, _, err := g.match(context.Background(), "a/b.yaml"); !errors.Is(err, context.Canceled) {
		t.Errorf("a stopped compile: %v, want the stop", err)
	}
	if len(cache.entries) != 0 {
		t.Errorf("the cache holds %d globs after a stopped compile, want none", len(cache.entries))
	}
	if ok, work, err := cache.match(context.Background(), "a/b.yaml", "a/*.yaml"); !ok || !work.compiled || err != nil {
		t.Errorf("the glob compiled again: %v, %v, %v; want true, compiled", ok, work.compiled, err)
	}
}

// A glob the cache compiled under one call's context matches under the
// next call's: a cached glob that spends its steps stops once that
// context is done, and not at the end of its step budget. How often the
// match looks is the meter's, and its tests hold it.
func TestCachedGlobMatchesUnderTheCallsContext(t *testing.T) {
	cache := (&errorLog{}).patternCache()
	glob := stepsGlob(16, 450)
	cache.compiled(context.Background(), glob)
	// The first look is the call's own, before it starts; the later ones
	// are the match's, one every 1024 steps. So the third look, from which
	// the context is done, falls inside the match.
	ctx := &doneOnLook{Context: context.Background(), look: 3}
	_, work, err := renovateMatchesCompiling(ctx, "xy", glob, cache)
	if !errors.Is(err, context.Canceled) || work.steps <= 0 || work.steps >= minimatch.MaxSteps {
		t.Errorf("error %v after %d steps, want the match stopped inside it, before its %d", err, work.steps, minimatch.MaxSteps)
	}
}
