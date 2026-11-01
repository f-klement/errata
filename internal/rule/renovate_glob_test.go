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
// the test fails when every call pays.
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
	got, err := a.Eval(context.Background(), asExtracted(map[string][]string{"patterns": patterns, "apps": files},
		map[string][]string{"apps": files}, nil))
	if err != nil || !got {
		t.Errorf("200 files against 10 globs: %v, %v; want true", got, err)
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

// A cached call costs renovateCallCost less the 1 CEL charged for reading
// the log; a later call of the same glob costs that less its compile; a
// regex and * pay no compile in either case, so they cost the same twice.
func TestCachedCallCost(t *testing.T) {
	log := &errorLog{}
	call := func(s, p string) []ref.Val {
		args := []ref.Val{types.String(s), types.String(p), log}
		renovateCall(context.Background(), args[0], args[1], log.globCache().compiled)
		return args
	}
	for _, c := range []struct {
		name, s, p   string
		first, again uint64
	}{
		{"a glob", "ab", "*.yaml", 1 + 2 + 6 + 6*18 - 1, 1 + 2 + 6 - 1},
		{"a regex", "ab", "/a/", 1 + 2 + 3 + 2*4/160 - 1, 1 + 2 + 3 - 1},
		{"*", "ab", "*", 1 + 2 + 1 - 1, 1 + 2 + 1 - 1},
	} {
		if got := hiddenCallCost(call(c.s, c.p), types.False); got != c.first {
			t.Errorf("%s, first call: cost %d, want %d", c.name, got, c.first)
		}
		if got := hiddenCallCost(call(c.s, c.p), types.False); got != c.again {
			t.Errorf("%s, again: cost %d, want %d", c.name, got, c.again)
		}
	}
	// A glob the cache does not hold pays. A call with an error argument
	// never comes here: cel-go returns before it reads the log, and the
	// call is not charged at all.
	if got := hiddenCallCost([]ref.Val{types.String("ab"), types.String("*.yml"), log}, types.False); got != 1+2+5+5*18-1 {
		t.Errorf("a glob the cache does not hold: cost %d", got)
	}
}

// The cache holds at most maxCachedGlobs globs and maxCachedGlobUnits
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
		{"small globs", func(i int) string { return fmt.Sprintf("**/x%04d/*.yaml", i) }, maxCachedGlobs + 10, maxCachedGlobs, true},
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
		cache := log.globCache()
		base := heap()
		for i := range c.calls {
			cache.compiled(context.Background(), c.glob(i))
		}
		held := heap() - base
		if len(cache.entries) != c.held || cache.units > maxCachedGlobUnits {
			t.Errorf("%s: the cache holds %d globs of %d units, want %d globs and at most %d units",
				c.name, len(cache.entries), cache.units, c.held, maxCachedGlobUnits)
		}
		t.Logf("%s: %d globs of %d units hold %d bytes", c.name, len(cache.entries), cache.units, held)
		if held > 8<<20 {
			t.Errorf("%s: the full cache holds %d bytes, more than 8 MiB", c.name, held)
		}
		past := c.glob(c.calls)
		cache.compiled(context.Background(), past)
		for call := range 2 {
			if !cache.pays(past) {
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
		steps, cost, product uint64
	}{
		{".gitlab-ci.yml", "**/!(*.md)", 86, 35, 27},
		{strings.Repeat("a", 1000), "**/*([[:word:]]).yml", 7006, 1896, 1333},
		{"infra/argocd/applications/app-001.yaml", "**/*.yaml", 1, 53, 53},
	} {
		_, steps, err := renovateMatchesCompiling(context.Background(), c.s, c.glob, minimatch.CompileContext)
		if err != nil {
			t.Fatal(err)
		}
		args := []ref.Val{types.String(c.s), types.String(c.glob)}
		paid := func(string) bool { return false }
		cost, product := renovateCallCostPaying(args, types.False, paid, steps), renovateCallCostPaying(args, types.False, paid, 0)
		if uint64(steps) != c.steps || cost != c.cost || product != c.product {
			t.Errorf("%s against %.20s: %d steps cost %d, by the product %d; want %d, %d and %d",
				c.glob, c.s, steps, cost, product, c.steps, c.cost, c.product)
		}
	}
}

// A glob that fills the cache's units exactly is kept: 65535 ones are one
// literal segment of Size 65536. One more unit is not.
func TestGlobCacheHoldsAGlobThatFillsIt(t *testing.T) {
	for _, c := range []struct {
		ones int
		held int
	}{{maxCachedGlobUnits - 1, 1}, {maxCachedGlobUnits, 0}} {
		cache := (&errorLog{}).globCache()
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
	if want := "the evaluation exceeded the cost limit of 100000"; err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
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
		cost           uint64
	}{
		{"[ab]", "ab", "xy", 1 + 2 + 53 + 12288/8},
		{"470 ranges apart", apartClass(), "xy", 1 + 2 + 991 + (12288+4096*3)/8},
		{"119 of [:word:]", strings.Repeat("[:word:]", 119), "xⒶ", 1 + 4 + 1003 + (12288+4096*2)/8},
	} {
		glob := classGlob(12, c.class)
		_, steps, err := renovateMatchesCompiling(context.Background(), c.s, glob, minimatch.CompileContext)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		args := []ref.Val{types.String(c.s), types.String(glob)}
		if got := renovateCallCostPaying(args, types.False, func(string) bool { return false }, steps); got != c.cost {
			t.Errorf("%s: cost %d, want %d", c.name, got, c.cost)
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
		if want := "the evaluation exceeded the cost limit of 150000"; err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", c.name, err, want)
		}
	}
}

// A compile the timeout stopped is not cached: its Glob holds the stop's
// error, which no later evaluation of the glob should read.
func TestGlobCacheKeepsNoStoppedCompile(t *testing.T) {
	cache := (&errorLog{}).globCache()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := cache.compiled(ctx, "a/*.yaml").MatchSteps(context.Background(), "a/b.yaml"); !errors.Is(err, context.Canceled) {
		t.Errorf("a stopped compile: %v, want the stop", err)
	}
	if len(cache.entries) != 0 {
		t.Errorf("the cache holds %d globs after a stopped compile, want none", len(cache.entries))
	}
	if ok, _, err := cache.compiled(context.Background(), "a/*.yaml").MatchSteps(context.Background(), "a/b.yaml"); !ok || err != nil {
		t.Errorf("the glob compiled again: %v, %v; want true", ok, err)
	}
}

// A glob the cache compiled under one call's context matches under the
// next call's: a cached glob that spends its steps stops at the look
// after that context is done, and not at the end of its step budget.
func TestCachedGlobMatchesUnderTheCallsContext(t *testing.T) {
	cache := (&errorLog{}).globCache()
	glob := stepsGlob(16, 450)
	cache.compiled(context.Background(), glob)
	// The call looks once before it starts, then the match every 1024
	// steps; the context is done from the third look.
	ctx := &doneOnLook{Context: context.Background(), look: 3}
	_, steps, err := renovateMatchesCompiling(ctx, "xy", glob, cache.compiled)
	if !errors.Is(err, context.Canceled) || steps != 2048 {
		t.Errorf("error %v after %d steps, want the match stopped after 2048", err, steps)
	}
}
