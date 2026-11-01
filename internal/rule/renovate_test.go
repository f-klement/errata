package rule

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"regexp/syntax"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"

	"github.com/f-klement/errata/internal/minimatch"
)

// renovateMatches is renovateMatchesCompiling as renovate_match's binding
// runs it: in the background, with each glob compiled at each call.
func renovateMatches(s, pattern string) (bool, error) {
	return renovateMatchesContext(context.Background(), s, pattern)
}

// renovateMatchesContext is renovateMatches under ctx.
func renovateMatchesContext(ctx context.Context, s, pattern string) (bool, error) {
	match, _, err := renovateMatchesCompiling(ctx, s, pattern, minimatch.CompileContext)
	return match, err
}

// renovate_match answers what Renovate's matchRegexOrGlob answers, for each
// case of testdata/renovate-cases.json: every pattern against every string,
// regexes, globs of every form minimatch reads and *, with the answer
// testdata/renovate-oracle.mjs gave from Renovate's own code, minimatch
// 10.2.6 and re2 1.27.0. Where Renovate throws, errata is unknown.
func TestRenovateMatchAgreesWithRenovate(t *testing.T) {
	data, err := os.ReadFile("testdata/renovate-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases [][3]any
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 6000 {
		t.Fatalf("%d cases; the file holds over 6000", len(cases))
	}
	matched, thrown := 0, 0
	for _, c := range cases {
		s, p := c[0].(string), c[1].(string)
		got, err := renovateMatches(s, p)
		if c[2] == "throw" {
			thrown++
			if err == nil || !strings.Contains(err.Error(), "minimatch throws on the glob") {
				t.Errorf("renovate_match(%q, %q) = %v, %v; Renovate throws", s, p, got, err)
			}
			continue
		}
		want := c[2].(bool)
		if err != nil || got != want {
			t.Errorf("renovate_match(%q, %q) = %v, %v; Renovate says %v", s, p, got, err, want)
		}
		if want {
			matched++
		}
	}
	// Both answers are common, so neither a matcher that always says true
	// nor one that always says false passes, and some globs throw.
	if matched < len(cases)/10 || matched > len(cases)*9/10 || thrown == 0 {
		t.Errorf("%d of %d cases match and %d throw", matched, len(cases), thrown)
	}
}

// renovate_match is a CEL helper: a rule over Argo CD's manager reads it
// as k3s does, every Application matched by one of the patterns.
func TestRenovateMatchInCEL(t *testing.T) {
	for _, c := range []struct {
		expr string
		want bool
	}{
		{`v.all(f, ['/^infra/argocd/applications/.*\\.yaml$/'].exists(p, renovate_match(f, p)))`, true},
		{`v.all(f, ['^infra/argocd/applications/.*\\.yaml$'].exists(p, renovate_match(f, p)))`, false},
		{`v.all(f, renovate_match(f, 'infra/argocd/**'))`, true},
		{`v.exists(f, renovate_match(f, '!/a\\.yaml$/'))`, true},
		{`renovate_match('anything at all', '*')`, true},
	} {
		got, err := eval(t, "size(v) == 2 && ("+c.expr+")",
			"infra/argocd/applications/a.yaml", "infra/argocd/applications/b.yaml")
		if err != nil || got != c.want {
			t.Errorf("%s = %v, %v; want %v", c.expr, got, err, c.want)
		}
	}
}

// What errata cannot compute as Renovate does makes the rule unknown,
// naming the pattern or the string and the reason.
func TestRenovateMatchRefuses(t *testing.T) {
	long := strings.Repeat("a", MaxArgument+1)
	for _, c := range []struct {
		s, p, want string
	}{
		{"a", "/(?=a)/", `"/(?=a)/" is written as a regex between slashes, and RE2 does not compile it`},
		{"a", "/\\u0061/", "RE2 does not compile it"},
		{"a x", "[[:alpha:]] x", `minimatch throws on the glob "[[:alpha:]] x", as JavaScript refuses the regex it builds: invalid regular expression`},
		{"a", strings.Repeat("{a,b}", 7), `the glob "` + strings.Repeat("{a,b}", 7) + `" is not matched against "a": its braces expand to more than 64 globs`},
		{"xa", "x" + strings.Repeat("!(a)", 12), "its tree passes 4096 nodes"},
		{strings.Repeat("a", 1000) + "b", strings.Repeat("*(a|aa)", 140), "matching takes more than the step budget"},
		{"\xff", "?", "a string that is not UTF-8"},
		{long, "*.yaml", "renovate_match: an argument is 1025 bytes; a helper reads at most 1024"},
		{"a", long, "renovate_match: an argument is 1025 bytes"},
	} {
		_, err := renovateMatches(c.s, c.p)
		if strings.Contains(c.want, "renovate_match:") {
			_, err = eval(t, "size(v) == 2 && renovate_match(v[0], v[1])", c.s, c.p)
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("renovate_match(%q, %q): error %v, want one containing %q", c.s, c.p, err, c.want)
		}
	}
	// * is every string, before anything is read.
	if got, err := renovateMatches("\xff", "*"); err != nil || !got {
		t.Errorf(`renovate_match("\\xff", "*") = %v, %v; want true`, got, err)
	}
}

// A call is charged its bytes and their product over renovateCostDivisor,
// with renovateCompileWeight for each byte of a glob and the regex term of
// renovateCallCost for a regex that ran. 100 calls of a string of 1024
// bytes and a regex of 1024, which is refused for its length before any
// parse, cost 204900 for their bytes and 1638400 for their products, so
// the cost limit stops them: a divisor above about 130 would let them
// run. 10 calls of the string and a glob of 1024 bytes cost about 369000
// and run: a divisor below about 13 would stop them. The two bound the
// divisor to roughly 13 to 130; they do not pin it.
func TestRenovateMatchIsCharged(t *testing.T) {
	long := strings.Repeat("a", MaxArgument)
	const ten = "[0, 1, 2, 3, 4, 5, 6, 7, 8, 9]"
	_, err := eval(t, "size(v) == 2 && "+ten+".all(i, "+ten+".all(j, renovate_match(v[0], v[1])))", long, "/["+long[4:]+"]/")
	want := "the evaluation exceeded the cost limit of 1000000"
	if err == nil || err.Error() != want {
		t.Errorf("100 calls of a regex, 2048 bytes: error %v, want %q", err, want)
	}
	if got, err := eval(t, "size(v) == 2 && "+ten+".all(i, renovate_match(v[0], v[1]))", long, "*"+long[1:]); err != nil || !got {
		t.Errorf("10 calls of a glob, 2048 bytes: %v, %v; want true", got, err)
	}
}

// renovateCallCost's terms, each on its own: * pays only its bytes; a glob
// pays 18 for each byte to compile; a regex that ran pays its string's
// bytes times its program's 4003 instructions over 160; a regex refused for
// its program pays none of that, since it did not run.
func TestRenovateCallCost(t *testing.T) {
	long := strings.Repeat("a", MaxArgument)
	atBound := "/" + strings.Repeat("(?:a?){1000}", 2) + "b/"
	pastBound := "/" + strings.Repeat(`(?:\pL?){999}`, 70) + "b/"
	for _, c := range []struct {
		name, s, p string
		result     ref.Val
		want       uint64
	}{
		{"*", "ab", "*", types.True, 1 + 2 + 1},
		{"a glob", "ab", "*.yaml", types.False, 1 + 2 + 6 + 6*18},
		{"a regex that ran", long, atBound, types.False, 1 + 1024 + 27 + 1024*27/64 + 1024*4003/160},
		{"a regex refused", long, pastBound, types.NewErr("refused"), 1 + 1024 + 913 + 1024*913/64},
	} {
		if got := renovateCallCost([]ref.Val{types.String(c.s), types.String(c.p)}, c.result); got != c.want {
			t.Errorf("%s: cost %d, want %d", c.name, got, c.want)
		}
	}
}

// A regex whose RE2 program passes maxRegexInsts is refused before it
// runs, naming the count. (?:a?){999} 23 times, then b, 254 bytes, would
// be 45955 instructions by programSize's estimate; 10 calls of it in one
// step are refused. Two groups of (?:a?){1000} are 4003 instructions and
// run; three, about 6001, are refused from the parsed tree, and so are
// three of (?:a?){1000,}, about 6004, which multiplies an open repeat by
// its minimum. Two groups, 45 of (?:a?) and b*c* are 4096 by the estimate
// and by the compile, and run. Two and 47 then b are estimated at 4095
// and compile to 4097, which the exact count after the compile refuses.
func TestRenovateMatchRefusesALargeRegex(t *testing.T) {
	long := strings.Repeat("a", MaxArgument)
	pastBound := "/" + strings.Repeat("(?:a?){999}", 23) + "b/"
	inOneStep := "size(v) == 2 && (" + strings.Repeat("renovate_match(v[0], v[1]) || ", 9) + "renovate_match(v[0], v[1]))"
	want := "is a regex, and its RE2 program would have about 45955 instructions; errata matches at most 4096"
	// Against b first, so a bound that no longer refuses fails at once
	// rather than in ten matches of a second each.
	if _, err := renovateMatches("b", pastBound); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("a regex of about 45955 instructions: error %v, want one containing %q", err, want)
	}
	_, err := eval(t, inOneStep, long, pastBound)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("10 calls of a regex of about 45955 instructions: error %v, want one containing %q", err, want)
	}
	two := strings.Repeat("(?:a?){1000}", 2)
	for _, p := range []string{"/" + two + "b/", "/" + two + "(?:a?){45}b*c*/"} {
		if got, err := renovateMatches("ab", p); err != nil || !got {
			t.Errorf("%s: %v, %v; want true", p, got, err)
		}
	}
	for _, c := range []struct{ name, pattern, want string }{
		{"three groups", "/" + strings.Repeat("(?:a?){1000}", 3) + "b/", "would have about 6001 instructions; errata matches at most 4096"},
		{"three open groups", "/" + strings.Repeat("(?:a?){1000,}", 3) + "b/", "would have about 6004 instructions; errata matches at most 4096"},
		{"two groups and 47", "/" + two + "(?:a?){47}b/", "has 4097 instructions; errata matches at most 4096"},
	} {
		if _, err := renovateMatches("ab", c.pattern); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want one containing %q", c.name, err, c.want)
		}
	}
}

// programSize is Go's calcSize, each rule on its own: an alternation adds
// one for each branch past the first, an open repeat x{0,} is 2 more than
// x and x{n,} is 1 more than n of x, a counted repeat x{n,m} is m of x and
// m-n more, a capture is 2 more and a plus 1. The compile adds 2.
func TestProgramSize(t *testing.T) {
	for re, want := range map[string]int64{
		"ab|cd": 5, "x|y|zz": 4, "(?:ab){0,}": 4, "(?:ab){2,}": 5, "(?:ab){2,5}": 13,
		"(ab)": 4, "a+": 2, "a?": 2, "a*": 3, "a*b": 4, "[a-z]": 1, "^": 1, "": 1,
	} {
		parsed, err := syntax.Parse(re, syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		if got := programSize(parsed); got != want {
			t.Errorf("programSize(%q) = %d, want %d", re, got, want)
		}
	}
}

// A regex is refused before the slow steps of a call run, so calls
// in one step that each refuse a different regex end well inside the
// timeout. Each pattern differs, so the cache cannot answer for them. Go's
// parser folds each code point of a class range under (?i): 20 ranges of
// a-\x{10FFFF} would take about 0.2 s each to parse, and are refused for
// their folded span, and so are 21 of --\x{1E942}, which Go reads as the
// range - to U+1E942 after [ or after the range before. The review's 1017
// byte (?i) entry, which took 0.79 s, and [^\pL] with 337 of them, about
// 57 ms, are refused for their length. (?:a*) 120 times under {1000}
// would compile to 360000 instructions and is refused from the parsed
// tree. Each runs in an exists, whose steps CEL interrupts at the timeout,
// and in one expression of 135 calls joined by ||, which has no step to
// interrupt and ran 22 s with the ranges of --. Eval returns at the
// timeout and leaves the evaluation running, so this times the
// evaluation itself. A bound that no longer refuses fails fast: the
// first pattern of each shape is refused on its own first, and the
// evaluation runs under a timeout of a second, which the 135 refusals,
// a few ms, end well inside. The race detector and make test's coverage
// stretch what is timed, so under the race detector each shape's first
// pattern is refused on its own and the evaluations are not run.
func TestRenovateMatchRefusesSlowRegexesInsideTheTimeout(t *testing.T) {
	calls := make([]string, 135)
	for i := range calls {
		calls[i] = "renovate_match('b', v[" + strconv.Itoa(i) + "])"
	}
	for _, c := range []struct{ name, head, tail, want string }{
		{"20 folded ranges", "/(?i)[" + strings.Repeat(`a-\x{10FFFF}`, 20), "]/", "its ranges fold case over 2503100 code points; errata folds at most 4096"},
		{"21 ranges of --", "!/(?i)[" + strings.Repeat(`--\x{1E942}`, 21), "]/", "its ranges fold case over 2628906 code points; errata folds at most 4096"},
		{"the review's folded class", "!/(?i)[" + strings.Repeat(`a-\x{10FFFF}`, 84), "]/", "errata reads a regex of at most 256"},
		{"40 ranges after \\Q\\x{\\E", `/\Q\x{\E[` + strings.Repeat("a-\U0001E942", 40), "]/i", `it quotes text with \Q`},
		{"337 of \\pL", "/[^" + strings.Repeat(`\pL`, 337), "]/", "errata reads a regex of at most 256"},
		{"a large program", "/(?:" + strings.Repeat("a*", 120) + "){1000}", "/", "would have about 36000"},
	} {
		patterns := make([]string, 135)
		for i := range patterns {
			// A class item or a literal makes each pattern differ.
			patterns[i] = c.head + strconv.Itoa(i) + c.tail
		}
		if _, err := renovateMatches("b", patterns[0]); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: error %v, want one containing %q", c.name, err, c.want)
		}
		if raceEnabled {
			continue
		}
		for _, expr := range []string{"v.exists(p, renovate_match('b', p))", strings.Join(calls, " || ")} {
			a, err := compile(expr, []string{"v"}, limits{cost: CostLimit, timeout: time.Second, grace: stopGrace})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			start := time.Now()
			_, _, err = a.program.ContextEval(ctx, map[string]any{"v": patterns, logVariable: &errorLog{}})
			took := time.Since(start)
			cancel()
			t.Logf("%s, %.20s: 135 calls took %s", c.name, expr, took)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s, %.20s: error %v, want one containing %q", c.name, expr, err, c.want)
			}
			if took >= time.Second {
				t.Errorf("%s, %.20s: 135 refused calls took %s, not inside a second", c.name, expr, took)
			}
		}
	}
}

// The parse's bounds, each at its limit: a regex of 256 bytes is read and
// one of 257 refused; 16 Unicode classes are read and 17 refused; under
// the i flag or (?i), ranges over 4096 code points between minFold and
// maxFold are folded and 4097 refused, and without either they are read.
func TestRenovateMatchBoundsTheParse(t *testing.T) {
	big := `[\x{1000}-\x{2000}]`
	for _, c := range []struct{ pattern, want string }{
		{"/" + strings.Repeat("a", 256) + "/", ""},
		{"/" + strings.Repeat("a", 257) + "/", "its text is 257 bytes; errata reads a regex of at most 256"},
		{"/[" + strings.Repeat(`\pL`, 16) + "]/", ""},
		{"/[" + strings.Repeat(`\PL`, 17) + "]/", "it names 17 Unicode classes; errata reads at most 16"},
		{`/[\x{1000}-\x{1FFF}]/i`, ""},
		{"/" + big + "/i", "its ranges fold case over 4097 code points; errata folds at most 4096"},
		{"/(?i)" + big + "/", "its ranges fold case over 4097 code points"},
		{"/" + big + "/", ""},
		{`/\Qa.b\E/`, `it quotes text with \Q, which errata does not read in a regex`},
		{`/\x{41/`, `its escape "\\x{41" is malformed`},
		{`/\x{41}/`, ""},
	} {
		_, err := renovateMatches("b", c.pattern)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%.40s: %v; want it read", c.pattern, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%.40s: error %v, want one containing %q", c.pattern, err, c.want)
		}
	}
}

// regexScan counts the Unicode classes a regex names and the code points
// its ranges cover between minFold and maxFold, for each way the text can
// write a range's ends: a character, \x with two digits or braces, octal,
// punctuation escaped, and an escaped letter, which stands below minFold.
// A character that is no digit inside \x{} is read as f.
func TestRegexScan(t *testing.T) {
	for _, c := range []struct {
		body   string
		named  int
		folded int64
	}{
		{"a-z", 0, 26},
		{"A-Z", 0, 26},
		{"z-a", 0, 0},
		{"a\\-z", 0, 0},
		{`\x41-\x5A`, 0, 26},
		{`\x{41}-\x{5a}`, 0, 26},
		{`\x00-\x42`, 0, 2},
		{`\x41-\x{1E943}`, 0, 0},
		{`\x40-\x{1E943}`, 0, 0},
		{`\x41-\x{1E942}`, 0, 125186},
		{`\x{1E940}-\x{1e943}`, 0, 4},
		{`a-\x{110000}`, 0, 125155},
		{`A-\x60`, 0, 32},
		{`A-\x59`, 0, 25},
		{`A-\x6a`, 0, 42},
		{`A-\x6A`, 0, 42},
		{`A-\x5f`, 0, 31},
		{`A-\x5F`, 0, 31},
		{`A-\x4g`, 0, 0},
		{`a-\x7A1`, 0, 26},
		{`A-\x4`, 0, 0},
		{`A-\x`, 0, 0},
		{`A-\101`, 0, 1},
		{`A-\1011`, 0, 1},
		{`A-\170`, 0, 56},
		{`A-\177`, 0, 63},
		{`A-\777`, 0, 447},
		{`A-\178`, 0, 0},
		{`A-\077-z`, 0, 58},
		{`A-\1`, 0, 0},
		{`A-\~`, 0, 62},
		{`A-\z`, 0, 0},
		{`A-\Z`, 0, 0},
		{`A-\A`, 0, 0},
		{`A-\9`, 0, 0},
		{"A-\\\x80", 0, 0},
		{`\t-z`, 0, 58},
		{`\d-z`, 0, 0},
		{`a-\d-z`, 0, 0},
		{`A--`, 0, 0},
		{`\pL\p{Greek}\PL\P{^Han}a-z`, 4, 26},
		{`a\p`, 1, 0},
		{`a\`, 0, 0},
		{`(?i)[--\x{1E942}]`, 0, 125186},
		{`(?i)[^--\x{1E942}]`, 0, 125186},
		{`(?i)[[:alpha:]--\x{1E942}]`, 0, 125186},
		{`\pL--\x{1E942}`, 1, 125186},
		{`\---\x{1E942}`, 0, 125186},
		{`a-b-z`, 0, 27},
		{"az", 0, 0},
		{`\p{L0}\p{L1}\p{Old_Italic}\p{^Greek}`, 4, 0},
		{`\p{Old-Italic}\p{ L }`, 2, 0},
		{`\p{Lu}-\x{1E942}`, 1, 0},
		{`\\Q[a-z]`, 0, 26},
	} {
		named, folded, refused := regexScan(c.body)
		if named != c.named || folded != c.folded || refused != "" {
			t.Errorf("regexScan(%q) = %d, %d, %q; want %d, %d", c.body, named, folded, refused, c.named, c.folded)
		}
	}
	// What the scan cannot read as Go does it stops at, and gives the
	// token: \Q, after which Go reads literals to \E, where the review's
	// \Q\x{\E and \Q\p{\E swallowed the class after them, and a \x{ or \p{
	// that does not close after its digits or name.
	for body, want := range map[string]string{
		"\\Q\\x{\\E[a-\U0001E942]": `\Q`,
		"\\Q\\p{\\E[a-\U0001E942]": `\Q`,
		`a-z\Q`:                    `\Q`,
		`\x{41`:                    `\x{41`,
		`\x{}`:                     `\x{`,
		`\x{1E93g}`:                `\x{1E93`,
		`\x{\E[a-z]`:               `\x{`,
		`\p{Greek`:                 `\p{Greek`,
		`\p{}`:                     `\p{`,
		`\p{L\E[a-z]`:              `\p{L`,
	} {
		if _, _, refused := regexScan(body); refused != want {
			t.Errorf("regexScan(%q) refuses %q, want %q", body, refused, want)
		}
	}
}

// clearRegexCache empties the cache, which other tests leave as they
// will, so a test of it holds under -shuffle.
func clearRegexCache() {
	regexCache.Lock()
	clear(regexCache.entries)
	regexCache.Unlock()
}

// The cache holds the entry of the last call and no other, and the
// memory a run of large onepass patterns leaves is that of one entry:
// ^(?:\pL){200}$ holds about 1.8 MiB, and ten of them, which 256 entries
// kept, leave less than one and a half of it.
func TestRegexCacheHoldsOneEntry(t *testing.T) {
	clearRegexCache()
	heap := func() int64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return int64(m.HeapAlloc)
	}
	pattern := func(i int) string { return `/^(?:\pL){200}` + strconv.Itoa(i) + "$/" }
	base := heap()
	compiledRegex(pattern(0))
	one := heap() - base
	for i := 1; i <= 10; i++ {
		compiledRegex(pattern(i))
		regexCache.Lock()
		n := len(regexCache.entries)
		regexCache.Unlock()
		if n != 1 {
			t.Fatalf("after %d patterns the cache holds %d entries, not 1", i+1, n)
		}
	}
	ten := heap() - base
	if one < 1<<20 {
		t.Fatalf("one entry left %d bytes; the pattern no longer makes a large onepass program", one)
	}
	if ten > one*3/2 {
		t.Errorf("ten patterns left %d bytes, one left %d: the cache holds more than one entry", ten, one)
	}
	regexCache.Lock()
	_, last := regexCache.entries[pattern(10)]
	n := len(regexCache.entries)
	regexCache.Unlock()
	if !last || n != 1 {
		t.Errorf("the cache holds %d entries, the last call's %v; want only the last", n, last)
	}
}

// Go's parser folds every code point of each range it reads in a class
// under (?i). Over generated patterns, regexScan never counts fewer than
// the ranges make Go fold (scanFloorOfGo). A pattern is one to three
// classes of ranges, - at either end, ^, escapes, POSIX and Perl classes,
// Unicode classes and negation, set in text the picks choose: literals,
// escapes, \Q quotes, malformed escapes, groups and flags set mid
// pattern. FuzzRegexScan runs the same check on patterns the fuzzer
// picks, in the weekly pass for longer. Under the race detector, where
// 30000 patterns took 5.8 s on the build node, it compares the first 3000.
func TestRegexScanNeverCountsFewerThanGoFolds(t *testing.T) {
	rng := rand.New(rand.NewPCG(239, 4))
	patterns := 30000
	if raceEnabled {
		patterns /= 10
	}
	checked := 0
	for range patterns {
		picks := make([]byte, 4+rng.IntN(20))
		for i := range picks {
			picks[i] = byte(rng.IntN(256))
		}
		if checkScanAgainstGo(t, picks) {
			checked++
		}
	}
	if checked < patterns/6 {
		t.Errorf("only %d of %d generated patterns were compared", checked, patterns)
	}
}

// FuzzRegexScan is TestRegexScanNeverCountsFewerThanGoFolds on the
// patterns the fuzzer's bytes pick.
func FuzzRegexScan(f *testing.F) {
	for _, seed := range [][]byte{{0, 1, 1, 3, 3, 3, 15, 0}, {0, 0, 0, 3, 3, 3, 15, 0}, {0, 15, 1, 3, 15, 0}, {1, 6, 0, 2, 3, 15, 4, 1, 3, 1, 8},
		// \[^\w], whose raw \ escapes the class's [, and .[:][^\x41],
		// which Go reads as a literal and a class.
		{0, 1, 27, 0, 0, 0, 0, 17}, {0, 1, 13, 1, 4, 34, 33, 32, 6, 13}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, picks []byte) {
		if len(picks) <= 64 {
			checkScanAgainstGo(t, picks)
		}
	})
}

// The items a generated class is made of: characters, which may end a
// range, and classes, which Go folds code point by code point, or by
// table without folding.
var (
	scanChars = []string{"a", "z", "A", "-", "-", "-", "^", "[", "]", ":", `\-`, `\]`, `\\`, `\x41`,
		`\x{100}`, `\x{1E942}`, `\x{1E9}`, `\101`, `\177`, `\t`, "é", "ſ", "K", "0", "\U0001E942"}
	scanFolded   = []string{`\d`, `\w`, `\s`, "[:alpha:]"}
	scanUnfolded = []string{`\p{Greek}`, `\pN`, `\D`, `\PL`, "[:^digit:]"}
	// scanContext is the text around a class. None of it is a class Go
	// would read, so each class node of Go's parse is a generated class.
	scanContext = []string{"", "ab", `\x41`, `\.`, "}", `\\`, "(?i)", "(?-i)", "(?i:", ")", "(?:", "^",
		"$", ".", "*", `\Q`, `\Q\x{\E`, `\Q\p{\E`, `\Qa\E`, `\x{`, `\p{`, `\x{41}`, "{", "a{2}", `\-`,
		`\E`, "\U0001E942"}
	// scanBytes are the single bytes a raw context piece is made of. [
	// and ] are left out, so a raw piece opens or closes no class.
	scanBytes = []string{`\`, "Q", "E", "x", "p", "P", "{", "}", ":", "-", "^", "a", "0", "7", "_", " ",
		"\xff", "\xf0\x9e"}
)

// scanPattern is a pattern the picks build, its classes and the text
// between them, with what each class's items add on their own.
type scanPattern struct {
	parts  []string
	class  []int // the index in parts of each class
	alone  []int64
	render func(keep int) string
}

// buildScanPattern reads picks a byte at a time: how many classes, and
// for each the text before it, whether it is negated, how many items and
// each item, then the text after the last.
func buildScanPattern(picks []byte) scanPattern {
	pos := 0
	next := func() int {
		if pos == len(picks) {
			return 0
		}
		pos++
		return int(picks[pos-1])
	}
	// context is up to three pieces, each a piece of scanContext or, one
	// time in 28, one or two bytes of scanBytes, each escaped with a \
	// half the time. The raw bytes add only short pieces between listed
	// ones, such as a \ that escapes the [ of the class after it.
	context := func() string {
		var b strings.Builder
		for range next() % 4 {
			k := next() % (len(scanContext) + 1)
			if k < len(scanContext) {
				b.WriteString(scanContext[k])
				continue
			}
			for range 1 + next()%2 {
				k := next()
				if k%2 == 1 {
					b.WriteString(`\`)
				}
				b.WriteString(scanBytes[k/2%len(scanBytes)])
			}
		}
		return b.String()
	}
	var p scanPattern
	for range 1 + next()%3 {
		p.parts = append(p.parts, context())
		var b strings.Builder
		b.WriteString("[")
		if next()%3 == 0 {
			b.WriteString("^")
		}
		var alone int64
		for range 1 + next()%6 {
			k := next()
			item := scanChars[k%len(scanChars)]
			switch k % 10 {
			case 7:
				item = scanFolded[k/10%len(scanFolded)]
			case 8, 9:
				item = scanUnfolded[k/10%len(scanUnfolded)]
			}
			b.WriteString(item)
			alone += codePointsAlone(item)
		}
		b.WriteString("]")
		p.class = append(p.class, len(p.parts))
		p.parts = append(p.parts, b.String())
		p.alone = append(p.alone, alone)
	}
	p.parts = append(p.parts, context())
	// render is the pattern with only the class at parts[keep] kept, the
	// others written as \a, or every class when keep is -1, and the
	// flags that would fold case set to s instead, so Go reads it without
	// folding.
	p.render = func(keep int) string {
		var b strings.Builder
		for i, part := range p.parts {
			if keep >= 0 && i != keep && slices.Contains(p.class, i) {
				part = `\a`
			}
			b.WriteString(strings.NewReplacer("(?i)", "(?s)", "(?-i)", "(?-s)", "(?i:", "(?s:").Replace(part))
		}
		return b.String()
	}
	return p
}

// checkScanAgainstGo builds a pattern from picks and fails t when
// regexScan counts fewer folded code points than scanFloorOfGo. A
// pattern the scan refuses for anything but its count is fine as it is,
// and so is one Go does not parse; it says whether it compared.
func checkScanAgainstGo(t *testing.T, picks []byte) bool {
	t.Helper()
	p := buildScanPattern(picks)
	body := strings.Join(p.parts, "")
	if err := scanRegex(body, false); err != nil && !strings.Contains(err.Error(), "fold case") {
		return false
	}
	least, ok := scanFloorOfGo(p)
	if !ok {
		return false
	}
	if _, folded, _ := regexScan(body); folded < least {
		t.Errorf("regexScan(%q) counts %d folded code points; Go folds at least %d", body, folded, least)
	}
	return true
}

// codePointsAlone is what an item adds between minFold and maxFold on its
// own: a Unicode class by table, without folding, and a character, or a
// POSIX or Perl class, in at most 63 code points, which the bound on the
// text holds. A character that ends a range is counted too, which only
// lowers the floor.
func codePointsAlone(item string) int64 {
	if ranges, ok := classNodes("[" + item + "]"); ok && len(ranges) == 1 && len(item) > 1 {
		return foldRegion(ranges[0])
	}
	return 1
}

// scanFloorOfGo is a floor of the code points Go folds one at a time for
// the ranges of p, summed over every class node of Go's parse: for each
// class, Go parses the pattern with only that class kept, and the floor
// is the code points between minFold and maxFold of the class it reads,
// or of its complement for [^...], which is what Go folded before
// negating, less what the items add on their own. No item reaches
// maxFold, so Go's fast path for a range over the whole span never
// applies. A pattern whose class nodes do not add up, one class to each
// node, each the class Go reads on its own, is not compared.
func scanFloorOfGo(p scanPattern) (int64, bool) {
	all, ok := classNodes(p.render(-1))
	if !ok {
		return 0, false
	}
	var least int64
	nodes := 0
	for i, at := range p.class {
		ranges, ok := classNodes(p.render(at))
		if !ok || len(ranges) > 1 {
			return 0, false
		}
		// A node that is not the class itself is not compared either: a
		// raw \ before it escapes its [, or a ] item closes it early, as
		// in [:][^\x41], so that Go reads a literal and then a class.
		if len(ranges) == 1 && !isClassNode(p.parts[at], ranges[0]) {
			return 0, false
		}
		nodes += len(ranges)
		for _, r := range ranges {
			if p.parts[at][1] == '^' {
				// A ^ item first negates it too.
				r = negateRanges(r)
			}
			least += foldRegion(r) - p.alone[i]
		}
	}
	return least, nodes == len(all)
}

// isClassNode is whether Go reads class as one class node, of ranges.
func isClassNode(class string, ranges []rune) bool {
	re, err := syntax.Parse(class, syntax.Perl)
	return err == nil && re.Op == syntax.OpCharClass && slices.Equal(re.Rune, ranges)
}

// classNodes is the ranges of every class node of Go's parse of body,
// without folding.
func classNodes(body string) ([][]rune, bool) {
	re, err := syntax.Parse(body, syntax.Perl)
	if err != nil {
		return nil, false
	}
	var out [][]rune
	var walk func(*syntax.Regexp)
	walk = func(re *syntax.Regexp) {
		if re.Op == syntax.OpCharClass {
			out = append(out, re.Rune)
		}
		for _, sub := range re.Sub {
			walk(sub)
		}
	}
	walk(re)
	return out, true
}

// foldRegion counts the code points of ranges between minFold and
// maxFold.
func foldRegion(ranges []rune) int64 {
	var n int64
	for i := 0; i+1 < len(ranges); i += 2 {
		lo, hi := max(ranges[i], minFold), min(ranges[i+1], maxFold)
		n += max(int64(hi)-int64(lo)+1, 0)
	}
	return n
}

// negateRanges is the complement of sorted, merged ranges over every code
// point.
func negateRanges(r []rune) []rune {
	var out []rune
	next := rune(0)
	for i := 0; i+1 < len(r); i += 2 {
		if r[i] > next {
			out = append(out, next, r[i]-1)
		}
		next = r[i+1] + 1
	}
	if next <= utf8.MaxRune {
		out = append(out, next, utf8.MaxRune)
	}
	return out
}

// A string or a pattern of exactly MaxArgument bytes is read.
func TestRenovateMatchReadsAnArgumentAtTheBound(t *testing.T) {
	long := strings.Repeat("a", MaxArgument)
	for _, c := range []struct {
		s, p string
		want bool
	}{{long, "*a", true}, {"a", long, false}} {
		got, err := eval(t, "size(v) == 2 && renovate_match(v[0], v[1])", c.s, c.p)
		if err != nil || got != c.want {
			t.Errorf("renovate_match of %d and %d bytes: %v, %v; want %v", len(c.s), len(c.p), got, err, c.want)
		}
	}
}

// Compiling a glob is charged for each of its bytes, so the cost limit
// stops the calls of one step, where CEL does not look at the timeout. A
// glob of 1024 bytes against a string of 1 costs 1042 and 18432 for
// compiling: 40 calls of distinct globs in one step cost about 779000 and
// run, and 60 cost about 1168000 and are stopped, which they were not when
// compiling was free. The two bound renovateCompileWeight to roughly 16 to
// 23. 60 calls of one glob compile it once (globCache) and run.
// TestRenovateCallCost holds a regex to no compile weight.
func TestRenovateMatchChargesCompilingAGlob(t *testing.T) {
	inOneStep := func(n int, distinct bool) string {
		calls := make([]string, n)
		for i := range calls {
			calls[i] = "renovate_match(v[0], v[1])"
			if distinct {
				calls[i] = "renovate_match(v[0], v[" + strconv.Itoa(i+1) + "])"
			}
		}
		return "size(v) == " + strconv.Itoa(n+1) + " && (" + strings.Join(calls, " || ") + ")"
	}
	values := func(n int, distinct bool) []string {
		v := []string{"b"}
		for i := range n {
			glob := strings.Repeat("a", MaxArgument)
			if distinct {
				glob = glob[4:] + fmt.Sprintf("%04d", i)
			}
			v = append(v, glob)
		}
		return v
	}
	for _, c := range []struct {
		name     string
		calls    int
		distinct bool
		want     string
	}{
		{"40 calls of distinct globs", 40, true, ""},
		{"60 calls of distinct globs", 60, true, "the evaluation exceeded the cost limit of 1000000"},
		{"60 calls of one glob", 60, false, ""},
	} {
		got, err := eval(t, inOneStep(c.calls, c.distinct), values(c.calls, c.distinct)...)
		switch {
		case c.want == "" && (err != nil || got):
			t.Errorf("%s: %v, %v; want false", c.name, got, err)
		case c.want != "" && (err == nil || err.Error() != c.want):
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
		}
	}
}
