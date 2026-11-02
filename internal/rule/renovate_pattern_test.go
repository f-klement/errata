package rule

import (
	"context"
	"errors"
	"fmt"
	"regexp/syntax"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/minimatch"
)

// The cache bounds the regexes of an evaluation as it bounds its globs,
// by maxCachedPatterns entries and maxCachedUnits units of their size, so
// it holds a few MiB at most, where a regex cache of 256 entries for the
// whole process once held 2.39 GiB. Small regexes fill the count, and so
// do refused ones, which hold only their error. Empty captures in an
// anchored regex hold the most for each unit, about 46 bytes, since a
// onepass program holds more for a capture than for any other
// instruction. A chain of captures, carets or nested groups before a
// class, or before an alternation that holds one in its second leg,
// copies the class's runes into each link of the onepass program,
// as an anchored class repeated 200 times copies them into each
// instruction: each of those holds a MiB or more and is never kept. The
// review of 9d5e4c6 found the chains, which its count kept 19 to 30 of,
// 61 MiB. The class repeated 1000 times unanchored shares its runes among
// its instructions, about 100 KiB, and is kept until the units are full.
func TestPatternCacheBoundsRegexes(t *testing.T) {
	heap := func() int64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return int64(m.HeapAlloc)
	}
	for _, c := range []struct {
		name        string
		regex       func(i int) string
		calls, held int
	}{
		{"small regexes", func(i int) string { return fmt.Sprintf(`/(^|/)values%04d\.ya?ml$/`, i) }, maxCachedPatterns + 10, maxCachedPatterns},
		{"refused regexes", func(i int) string { return fmt.Sprintf(`/(?=a)%04d/`, i) }, maxCachedPatterns + 10, maxCachedPatterns},
		{"20 captures", func(i int) string { return fmt.Sprintf(`/^%04d`, i) + strings.Repeat("(x)", 20) + "$/" }, 200, 184},
		{"124 empty captures", func(i int) string { return fmt.Sprintf(`/^%04d`, i) + strings.Repeat("()", 124) + "$/" }, 70, 56},
		{"empty captures before a class", func(i int) string {
			return fmt.Sprintf(`/^%04d`, i) + strings.Repeat("()", 109) + `[\p{Lu}\p{Nd}\p{Mn}\p{Po}]$/`
		}, 25, 0},
		{"empty captures before an alternation", func(i int) string {
			return fmt.Sprintf(`/^%04d`, i) + strings.Repeat("()", 100) + `(?:ab|[\p{Lu}\p{Nd}\p{Mn}\p{Po}])$/`
		}, 25, 0},
		{"carets before a class", func(i int) string { return "/" + strings.Repeat("^", 245) + fmt.Sprintf(`\pL%04d$/`, i) }, 25, 0},
		{"nested captures around a class", func(i int) string {
			return "/^" + strings.Repeat("(", 123) + `\pL` + strings.Repeat(")", 123) + fmt.Sprintf(`%04d$/`, i)
		}, 25, 0},
		{"an anchored class", func(i int) string { return fmt.Sprintf(`/^%04d(?:\pL){200}$/`, i) }, 8, 0},
		{"an unanchored class", func(i int) string { return fmt.Sprintf(`/%04d(?:\pL?){1000}/`, i) }, 10, 8},
	} {
		log := &errorLog{}
		cache := log.patternCache()
		base := heap()
		for i := range c.calls {
			cache.compiled(context.Background(), c.regex(i))
		}
		held := heap() - base
		if len(cache.entries) != c.held || cache.units > maxCachedUnits {
			t.Errorf("%s: the cache holds %d regexes of %d units, want %d regexes and at most %d units",
				c.name, len(cache.entries), cache.units, c.held, maxCachedUnits)
		}
		t.Logf("%s: %d regexes of %d units hold %d bytes", c.name, len(cache.entries), cache.units, held)
		if held > 8<<20 {
			t.Errorf("%s: the full cache holds %d bytes, more than 8 MiB", c.name, held)
		}
		before := len(cache.entries)
		cache.compiled(context.Background(), c.regex(c.calls))
		if len(cache.entries) != before {
			t.Errorf("%s: a regex past the bound was kept", c.name)
		}
		runtime.KeepAlive(log)
	}
}

// A regex is compiled once in an evaluation, by its first call, which pays
// for the compile: a later call matches with the pattern the cache holds,
// pays no compile, and both are priced by that pattern's instructions.
// Before the cache was the evaluation's, the process kept one regex, so
// two entries called in turn compiled at every call.
func TestPatternCacheCompilesARegexOnce(t *testing.T) {
	cache := (&errorLog{}).patternCache()
	entries := []string{`/a\.yaml$/`, `/b\.yaml$/`}
	first := map[string]pattern{}
	for call := range 3 {
		for _, e := range entries {
			match, work, err := cache.match(context.Background(), "a.yaml", e)
			paid := work.compiled && work.compileCost > 0
			if err != nil || match != (e == entries[0]) || work.insts == 0 || paid != (call == 0) {
				t.Errorf("%s, call %d: %v, %+v, %v", e, call+1, match, work, err)
			}
			if call == 0 {
				first[e] = cache.entries[e]
			} else if cache.entries[e] != first[e] {
				t.Errorf("%s, call %d: compiled again", e, call+1)
			}
		}
	}
}

// A refusal reads the same from both adapters in the rule's unknown
// message: the form of the entry, the entry, the string, and the
// adapter's reason. Each adapter refuses an entry its engine does not
// compile and one past errata's bounds.
func TestRenovateMatchRefusalsReadTheSame(t *testing.T) {
	_, thrown := minimatch.Match("a x", "[[:alpha:]] x")
	if thrown == nil {
		t.Fatal("minimatch no longer throws on [[:alpha:]] x")
	}
	long := "/" + strings.Repeat("a", 257) + "/"
	for _, c := range []struct {
		form, s, p, reason string
	}{
		{"regex", "a", "/(?=a)/", "RE2 does not compile the regex, which Renovate would then read as a glob: error parsing regexp: invalid or unsupported Perl syntax: `(?=`"},
		{"glob", "a x", "[[:alpha:]] x", "minimatch throws on the glob, as JavaScript refuses the regex it builds: " + thrown.Error()},
		{"regex", "a", long, "its text is 257 bytes; errata reads a regex of at most 256"},
		{"glob", "a", strings.Repeat("{a,b}", 7), "its braces expand to more than 64 globs or 16384 characters"},
	} {
		_, err := eval(t, "size(v) == 2 && renovate_match(v[0], v[1])", c.s, c.p)
		want := fmt.Sprintf("renovate_match: the %s %q is not matched against %q: %s", c.form, c.p, c.s, c.reason)
		if err == nil || err.Error() != want {
			t.Errorf("%.20s: error %v, want %q", c.p, err, want)
		}
	}
}

// A glob that its context stopped returns the stop, which wraps the
// context's error, and not a refusal: the rule is unknown on the timeout,
// not on the glob.
func TestAStoppedGlobIsNoRefusal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	glob := classGlob(12, "ab")
	_, _, err := compileGlob(context.Background(), glob).match(ctx, "xy")
	var refused *refusal
	want := fmt.Sprintf("the evaluation was stopped while the glob %q ran: the match was stopped: context canceled", glob)
	if !errors.Is(err, context.Canceled) || errors.As(err, &refused) || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
}

// A regex too big for the cache is compiled at every call, and each call
// pays its compile, so a step of calls of it ends on the cost limit and
// not on the timeout. Before the compile was charged, 27000 calls of the
// first two regexes here (review of 9d5e4c6, finding 2) ran to the
// timeout: each compile took 0.7 to 12 ms and cost about 27. The third is
// of the slowest shapes measured, which ran past the timeout at 72 and 128
// a byte (review of 5635925, finding 1). Charged by regexCompileCost, they
// cost 3129, 12965 and 18603 a call, so the limit stops them after about
// 320, 77 and 54 compiles. Its point is how soon they end, so it is slow
// only.
func TestRenovateMatchStopsARegexTooBigForTheCacheOnTheCostLimit(t *testing.T) {
	slowOnly(t)
	thirty := "[" + strings.TrimSuffix(strings.Repeat("0, ", 30), ", ") + "]"
	expr := "size(v) == 2 && " + thirty + ".all(i, " + thirty + ".all(j, " + thirty + ".all(k, renovate_match(v[0], v[1]))))"
	for _, p := range []string{`/^(?:\pL){200}$/`, `/(?i)^(?:[^\pL\pN]){990}$/`, `/^(?:\pL\PL+){330}$/`} {
		if compileRegex(p).size() <= maxCachedUnits {
			t.Fatalf("%s fits the cache; the test needs a regex that does not", p)
		}
		start := time.Now()
		_, err := eval(t, expr, "", p)
		took := time.Since(start)
		t.Logf("%s: ended after %s: %v", p, took, err)
		if want := "the evaluation exceeded the cost limit of 1000000"; err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", p, err, want)
		}
		if took >= EvalTimeout {
			t.Errorf("%s: took %s, not inside the timeout of %s", p, took, EvalTimeout)
		}
	}
}

// makeOnePass gives a folded single rune a pair for each rune of its case
// fold orbit, and consumedRunes counts them: k, K and the Kelvin sign are
// 6. An unfolded k is one pair, 2.
func TestConsumedRunesCountsAFoldOrbit(t *testing.T) {
	for body, want := range map[string]int{"(?i)k": 6, "k": 2} {
		parsed, err := syntax.Parse(body, syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		prog, err := syntax.Compile(parsed.Simplify())
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		for i := range prog.Inst {
			got += consumedRunes(&prog.Inst[i])
		}
		if got != want {
			t.Errorf("%s: %d runes, want %d", body, got, want)
		}
	}
}

// Go makes no onepass copy of a program of 1000 instructions or more, and
// onepassRunes counts none: the anchored class and its complement 332
// times are 1000 instructions, and 330 times are 994, which Go tries.
func TestOnepassRunesFollowsGosBound(t *testing.T) {
	for body, insts := range map[string]int{`^(?:\pL\PL?){332}$`: 1000, `^(?:\pL\PL?){330}$`: 994} {
		parsed, err := syntax.Parse(body, syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		prog, err := syntax.Compile(parsed.Simplify())
		if err != nil {
			t.Fatal(err)
		}
		if len(prog.Inst) != insts {
			t.Fatalf("%s: %d instructions, want %d", body, len(prog.Inst), insts)
		}
		if got := onepassRunes(prog); (got == 0) != (insts >= 1000) {
			t.Errorf("%s, %d instructions: %d onepass runes", body, insts, got)
		}
	}
}

// A config's ordinary regex entries cost little to compile: 100 distinct
// entries of 30 bytes spend under a tenth of the cost limit compiling, and
// a rule over 200 of them has its verdict. At 256 a byte, as the compile
// was first charged, the 100 spent 77% of the limit and 130 passed it
// (review of 8cac768, finding 1).
func TestOrdinaryRegexesCostLittleToCompile(t *testing.T) {
	entry := func(i int) string { return fmt.Sprintf(`/^apps/%03d/[^/]+/values\.yml$/`, i) }
	if n := len(entry(0)); n != 30 {
		t.Fatalf("an entry is %d bytes, want 30", n)
	}
	cache := (&errorLog{}).patternCache()
	var spent uint64
	for i := range 100 {
		_, work, err := cache.match(context.Background(), "x", entry(i))
		if err != nil || !work.compiled {
			t.Fatalf("%s: compiled %v, %v", entry(i), work.compiled, err)
		}
		spent += work.compileCost
	}
	t.Logf("100 entries of 30 bytes cost %d to compile", spent)
	if spent >= CostLimit/10 {
		t.Errorf("100 entries of 30 bytes cost %d to compile, a tenth of the limit or more", spent)
	}
	entries := make([]string, 200)
	for i := range entries {
		entries[i] = entry(i)
	}
	a, err := compile("v.exists(p, renovate_match('apps/b/c/values.yml', p))", []string{"v"}, limits{cost: CostLimit, timeout: EvalTimeout, grace: stopGrace})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := a.Eval(context.Background(), asExtracted(map[string][]string{"v": entries}, nil, nil)); err != nil || got {
		t.Errorf("200 entries of 30 bytes: %v, %v; want false", got, err)
	}
}

// regexCompileCost, each term on its own: /a\.yaml$/ is 18 for each of its
// 10 bytes and 15 for its 9 instructions and 6 runes; an unanchored
// (?:a?){1000} is 252 and 2003 for its 2002 instructions and one rune,
// with no onepass term; an anchored class of 200 \pL adds 256 for the
// class it names and the onepass term, 204 instructions times 204 and 1368
// runes over 256; 16 of \pL in an anchored class are 256 each, with case
// folding and without; a range of 4096 code points is 4096 over 4 under
// case folding and nothing without; and a regex refused after its parse,
// from the estimate of its program, is its bytes' share alone.
func TestRegexCompileCostTerms(t *testing.T) {
	large := "/(?:" + strings.Repeat("a*", 120) + "){1000}/"
	classes := "/^[" + strings.Repeat(`\pL`, 16) + "]$/"
	for p, want := range map[string]uint64{
		`/a\.yaml$/`:                         10*18 + 9 + 6,
		`/(?:a?){1000}/`:                     14*18 + 2002 + 1,
		`/^(?:\pL){200}$/`:                   16*18 + 256 + 204 + 1368 + 204*(204+1368)/256,
		classes:                              54*18 + 16*256 + 5 + 1368 + 5*(5+1368)/256,
		"/(?i)" + classes[1:]:                58*18 + 16*256 + 5 + 1370 + 5*(5+1370)/256,
		`/(?i)[A-\x{1040}]/`:                 18*18 + 4096/4 + 3 + 36,
		`/[A-\x{1040}]/`:                     14*18 + 3 + 2,
		large:                                uint64(len(large)) * 18,
		`/(?=a)/`:                            7 * 18,
		"/" + strings.Repeat("a", 300) + "/": 0,
	} {
		if got := compileRegex(p).compileCost(); got != want {
			t.Errorf("%.30s: compile cost %d, want %d", p, got, want)
		}
	}
}

// Distinct regexes slow to parse end on the cost limit too, not on the
// timeout: 16 of \pL in an anchored class under case folding, 14 ms a
// parse on a loaded node, cost 2504 a call before the named classes were
// charged, and the limit paid for 5.58 s of them; a range of 4096 code
// points under case folding, 382, paid for 4.16 s (review of a8c9648,
// finding 1). Each entry is distinct, so the cache compiles every one.
// Its point is how soon they end, so it is slow only.
func TestRenovateMatchStopsDistinctSlowParsesOnTheCostLimit(t *testing.T) {
	slowOnly(t)
	for _, shape := range []func(i int) string{
		func(i int) string { return fmt.Sprintf("/(?i)^%04d[", i) + strings.Repeat(`\pL`, 16) + "]$/" },
		func(i int) string { return fmt.Sprintf(`/(?i)%04d[A-\x{1040}]/`, i) },
	} {
		entries := make([]string, 2000)
		for i := range entries {
			entries[i] = shape(i)
		}
		a, err := compile("v.exists(p, renovate_match('', p))", []string{"v"}, limits{cost: CostLimit, timeout: EvalTimeout, grace: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, err = a.Eval(context.Background(), asExtracted(map[string][]string{"v": entries}, nil, nil))
		took := time.Since(start)
		t.Logf("%.30s: ended after %s: %v", entries[0], took, err)
		if want := "the evaluation exceeded the cost limit of 1000000"; err == nil || err.Error() != want {
			t.Errorf("%.30s: error %v, want %q", entries[0], err, want)
		}
	}
}
