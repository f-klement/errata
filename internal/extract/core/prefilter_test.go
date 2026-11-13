package core

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"reflect"
	"regexp/syntax"
	"slices"
	"strings"
	"testing"
	"time"
)

// The literals a seeker looks for and the newlines a match may hold, for
// the shapes of the Endpaper rules that have no literal prefix and for each
// rule of litsOf. nil is no seeker: the pattern is searched everywhere.
// Each row pins a choice a mutant of the rules would change: a class or
// alternation over the cap, a product that would pass it, a fold that cuts
// a literal short, a U+FFFD that matches a byte that is not UTF-8.
func TestSeekerLiterals(t *testing.T) {
	for _, c := range []struct {
		pattern string
		lits    []string
		span    int
	}{
		{`([\x{2013}\x{2014}])`, []string{"–", "—"}, 0},
		{`(\x{2013}|\x{2014})`, []string{"–", "—"}, 0},
		{`([\x{061C}\x{200E}\x{200F}\x{202A}-\x{202E}\x{2066}-\x{2069}])`, []string{"\xd8\x9c", "\xe2\x80", "\xe2\x81"}, 0},
		{`(\w*[Gg]etFeatureFlags\w*)`, []string{"GetFeatureFlags", "getFeatureFlags"}, 0},
		{`([\w:./\[\]-]*text-paper-[45]00)`, []string{"text-paper-400", "text-paper-500"}, 0},
		{`([\w:./\[\]-]*focus(?:-visible)?:(?:outline-none|ring-[\w./#%\[\]-]+))`, []string{"focus-visible:", "focus:"}, 0},
		{`(?:^|\\[A-Za-z0-9._%+-]|[^A-Za-z0-9._%+\\-])([A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,})`, []string{"@"}, 1},
		{`([0-9]{6,}:[A-Za-z0-9_-]{25,})`, []string{":"}, 0},
		{"^ {0,3}(```)", []string{"```"}, 0},
		{`(?i)(focus)`, []string{"FOC", "FOc", "FoC", "Foc", "fOC", "fOc", "foC", "foc"}, 0},
		{`(?i)(k)`, []string{"K", "k", "K"}, 0},
		{`([ab]x{2})`, []string{"axx", "bxx"}, 0},
		{`([ab]x{0,1}y)`, []string{"axy", "ay", "bxy", "by"}, 0},
		{`([ab](?:xy){1,2})`, []string{"xy"}, 0},
		{`([ab]+c)`, []string{"c"}, 0},
		{`(\d(?:abc)+)`, []string{"abc"}, 0},
		{`([a-z]x)`, []string{"x"}, 0},
		{`([ab]\x{FFFD}c)`, []string{"a", "b"}, 0},
		{`([ab]c\x{FFFD})`, []string{"c"}, 0},
		{`([^x]{2}c)`, []string{"c"}, 2},
		{`((?s).x)`, []string{"x"}, 1},
		{`(.*\n.*x)`, []string{"\n"}, 1},
		{`([ab]\n[cd])`, []string{"a\nc", "a\nd", "b\nc", "b\nd"}, 1},
		{`([ab](?:\n|x){3})`, []string{"\n\n\n", "\n\nx", "\nx\n", "\nxx", "x\n\n", "x\nx", "xx\n", "xxx"}, 3},
		{`([a-p]x)`, []string{"x"}, 0},
		{`([a-h][ab]q)`, []string{"q"}, 0},
		{`([ab](?:\n|x|y){3})`, []string{"a", "b"}, 3},
		{`([ab]|cd)`, []string{"a", "b", "cd"}, 0},
		{`([ab]\n\n\nc)`, []string{"a\n\n\nc", "b\n\n\nc"}, 3},
		{`([ab](?:\n){0})`, []string{"a", "b"}, 0},
		{`([ab][a-h][ab]x)`, []string{"ax", "bx"}, 0},
		{`([ab]x|y+z)`, []string{"ax", "bx", "y"}, 0},
		{`([a-h]x)`, []string{"ax", "bx", "cx", "dx", "ex", "fx", "gx", "hx"}, 0},
		{`([a-h]x|[i-p]y)`, nil, 0},
		{`(\b[ab]{3})`, []string{"aaa", "aab", "aba", "abb", "baa", "bab", "bba", "bbb"}, 0},
		{`(\b[ab]{4})`, []string{"aaa", "aab", "aba", "abb", "baa", "bab", "bba", "bbb"}, 0},
		{`(a*)`, nil, 0},
		{`([ab]x|y*)`, nil, 0},
		{`([ab]|)`, nil, 0},
		{`([a-q])`, nil, 0},
		{`([^x]{4}c)`, nil, 0},
		{`([^x]+c)`, nil, 0},
		{`(?s)([ab].*)`, nil, 0},
		{`(\A[ab]c)`, nil, 0},
		{`([ab]c\z)`, nil, 0},
		{`(?-m)([ab]c$)`, nil, 0},
		{`(\x{FFFD}|[ab]x)`, nil, 0},
		{`(x{0}[ab]{0})`, nil, 0},
	} {
		s := seekerOf("(?m)" + c.pattern)
		if c.lits == nil {
			if s != nil {
				t.Errorf("%s: seeker %q, want none", c.pattern, s.lits)
			}
			continue
		}
		got := []string{}
		if s != nil {
			for _, l := range s.lits {
				got = append(got, string(l))
			}
		}
		if s == nil || !slices.Equal(got, c.lits) || s.span != c.span {
			t.Errorf("%s: seeker %q span %v, want %q span %d", c.pattern, got, s, c.lits, c.span)
		}
	}
}

// The caps of litsOf hold at their bounds: a class, a fold, an alternation
// and a power of exactly maxLits texts are exact, and one more is not. The
// seeker would cut most of these to the same literals, so each is read
// from the set itself.
func TestLitsOfCapsAtTheirBounds(t *testing.T) {
	exact := func(pattern string) litSet {
		t.Helper()
		re, err := syntax.Parse(pattern, syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		return litsOf(re)
	}
	for _, c := range []struct {
		pattern string
		exact   int // how many texts it is exactly one of, 0 for not exact
		need    int // how many texts it needs one of, 0 for none
	}{
		{`[a-p]`, 16, 16},
		{`[a-q]`, 0, 0},
		{`[a-hj-q]`, 16, 16},
		{`[a-hj-r]`, 0, 0},
		{`(?i)abcd`, 16, 16},
		{`(?i)abcde`, 0, 16},
		{`[a-h]x|[i-p]y`, 16, 16},
		{`[a-h]x|[i-q]y`, 0, 9}, // [i-q]y needs y: nine 2-byte texts cut to no fewer than nine
		{`[ab]{4}`, 16, 16},
		{`[ab]{5}`, 0, 2},
		{`(?:[a-h]x|[i-o]y)?`, 16, 0},
		{`(?:[a-h]x|[i-p]y)?`, 0, 0},
	} {
		got := exact(c.pattern)
		if n := len(got.exact); got.isExact != (c.exact > 0) || got.isExact && n != c.exact || len(got.need) != c.need {
			t.Errorf("%s: exact %v of %d, need %d; want exact %d, need %d", c.pattern, got.isExact, n, len(got.need), c.exact, c.need)
		}
	}
	if p := power([]string{"a", "b"}, 4); len(p) != 16 {
		t.Errorf("power 4: %d texts, want 16", len(p))
	}
	if p := power([]string{"a", "b"}, 5); p != nil {
		t.Errorf("power 5: %d texts, want nil", len(p))
	}
}

// The window of lines around a line counts an empty line as a line.
func TestLinesAround(t *testing.T) {
	data := []byte("a\n\nb\nc")
	for _, c := range []struct{ start, end, span, first, last int }{
		{2, 2, 0, 2, 2},
		{2, 2, 1, 0, 4},
		{0, 1, 1, 0, 2},
		{3, 4, 1, 2, 6},
		{5, 6, 3, 0, 6},
	} {
		if first, last := linesAround(data, c.start, c.end, c.span); first != c.first || last != c.last {
			t.Errorf("line %d to %d, span %d: lines %d to %d, want %d to %d", c.start, c.end, c.span, first, last, c.first, c.last)
		}
	}
}

// A place on a newline is on the line that newline ends, an empty one
// included.
func TestLineBounds(t *testing.T) {
	data := []byte("a\n\nb")
	for _, c := range []struct{ i, start, end int }{{0, 0, 1}, {1, 0, 1}, {2, 2, 2}, {3, 3, 4}} {
		if start, end := lineStart(data, c.i), lineEnd(data, c.i); start != c.start || end != c.end {
			t.Errorf("place %d: line %d to %d, want %d to %d", c.i, start, end, c.start, c.end)
		}
	}
}

// The record of where literals occur is kept while the search moves on in
// one text, and started afresh for another text or a position behind the
// last. Keeping it is what makes the scans read the text once; a fresh one
// would find the same places, so the record is read here.
func TestSeekerKeepsItsRecordInOneText(t *testing.T) {
	s := seekerOf(`(?m)([ab]c)`)
	data := []byte("ac bc ac")
	clock := newClock(time.Minute)
	if got := s.next(data, 0, clock); got != 0 {
		t.Fatalf("next from 0: %d", got)
	}
	f := clock.found
	for _, pos := range []int{1, 1, 3} {
		if got := s.next(data, pos, clock); got != 3 || clock.found != f {
			t.Errorf("next from %d: %d, record kept %v; want 3, kept", pos, got, clock.found == f)
		}
	}
	other := []byte("bc ac ac")
	if got := s.next(other, 4, clock); got != 6 || clock.found == f {
		t.Errorf("next in another text: %d, record kept %v; want 6, a new record", got, clock.found == f)
	}
	f = clock.found
	if got := s.next(other, 0, clock); got != 0 || clock.found == f {
		t.Errorf("next from behind: %d, record kept %v; want 0, a new record", got, clock.found == f)
	}
}

// A pattern regexp does not take has no seeker; the search refuses it
// before it is asked for one.
func TestSeekerOfABadPatternIsNone(t *testing.T) {
	if s := seekerOf(`([ab]`); s != nil {
		t.Errorf("seeker %v", s)
	}
}

// prefilterCase is one pattern and text the property test or the fuzz
// target holds the search with a seeker to: each of prefilterOracles
// compares it with the same search with no seeker. It reports whether the
// pattern has a seeker; one with none is not compared.
func prefilterCase(t *testing.T, pattern, data string) bool {
	t.Helper()
	e, err := CompileSearchAt("regex", sourceOf("f"), pattern)
	if err != nil || e.seek == nil {
		return false
	}
	plain := e
	plain.seek = nil
	for _, o := range prefilterOracles {
		if got, want := o.read(t, e, []byte(data)), o.read(t, plain, []byte(data)); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %q in %q: %v, with no seeker %v", o.name, pattern, data, got, want)
		}
	}
	return true
}

// prefilterOracles are the ways errata reads a search, each a function of
// the search and the text whose results must not change with a seeker. An
// entry point that searches another way adds its read here.
var prefilterOracles = []struct {
	name string
	read func(t *testing.T, e Search, data []byte) any
}{
	{"every group of every match, line ends read as LF", func(t *testing.T, e Search, data []byte) any {
		t.Helper()
		return allGroups(t, e, LineFeeds(data))
	}},
	{"every group of every match in the raw bytes, as comment and text search", func(t *testing.T, e Search, data []byte) any {
		t.Helper()
		return allGroups(t, e, data)
	}},
	{"FindAll's matches", func(t *testing.T, e Search, data []byte) any {
		t.Helper()
		got, err := e.Matches(data)
		if err != nil {
			t.Fatal(err)
		}
		want := e.re.FindAllSubmatchIndex(data, -1)
		if n := len(want); n > 0 && want[n-1][0] == len(data) && bytes.HasSuffix(data, newline) {
			want = want[:n-1]
		}
		if len(got) != len(want) || len(got) > 0 && got[0] != Match(want[0][:4]) {
			t.Fatalf("matches %v, FindAll %v", got, want)
		}
		return got
	}},
	{"FromData's values and lines", func(t *testing.T, e Search, data []byte) any {
		t.Helper()
		values, err := e.FromData(data)
		return fmt.Sprint(values, err)
	}},
	{"MatchesIn over pieces of one buffer, repeated, and an empty one", func(t *testing.T, e Search, data []byte) any {
		t.Helper()
		cut := len(data) * 2 / 5
		got, err := e.MatchesIn([][]byte{data[:cut], data[cut:], data, data[:cut], {}, data[cut:]})
		return fmt.Sprint(got, err)
	}},
	{"the lines a context search finds", func(t *testing.T, e Search, data []byte) any {
		t.Helper()
		set, err := (&Around{}).lines(&e, "with", data, time.Now().Add(time.Minute), "f", time.Minute, "")
		return fmt.Sprint(set, err)
	}},
	{"every group of every match with the comments blanked, as skip: [comments] reads a Go file", func(t *testing.T, e Search, data []byte) any {
		t.Helper()
		blanked, err := BlankComments("f.go", language(t, "go"), data)
		if err != nil {
			return err.Error()
		}
		return allGroups(t, e, blanked)
	}},
}

// allGroups is every group of every match each finds in data.
func allGroups(t *testing.T, e Search, data []byte) [][]int {
	t.Helper()
	var out [][]int
	err := e.each(data, newClock(time.Minute), func(m []int) error {
		out = append(out, slices.Clone(m))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// pieces are what randomPattern builds patterns of: literals, classes that
// hold a newline or a CR or not, . with and without (?s), anchors, folds
// with an orbit of three (k, s), U+FFFD, a lazy repeat, and text of a few
// lines in an alphabet they match, with CRs, CRLFs, broken UTF-8, and the
// marks of Go's comments and strings, so the blanked comments oracle has
// comments to blank.
var (
	atoms = []string{"a", "b", "ab", `\n`, "[ab]", "[^a]", ".", "(?s:.)", `\b`, `\B`, "^", "$",
		"(?i:a)", "(?i:k)", "(?i:s)", "(?i:ss)", "ſ", `\x{212A}`, "x", "é", "(?i:é)", `[éx]`, `\x{FFFD}`,
		`[a\n]`, `[\r\n]`, `\r`, `[^\n]`, `[^x]`, "[ax]", `\w`, `\s`, "(?U:a+)"}
	letters = []string{"a", "b", "x", "\n", "\n\n", " ", "A", "K", "s", "S", "ſ", "é", "É",
		"\xff", "\xc3", "\u212a", "\r", "\r\n", "//", "/*", "*/", `"`, "`"}
	repeats = []string{"*", "+", "?", "{2}", "{3}", "{1,2}", "{0,1}", "{0,3}", "*?", "+?", "??"}
)

// randomPattern is a random pattern of depth at most d over atoms.
func randomPattern(r *rand.Rand, d int) string {
	if d == 0 || r.IntN(3) == 0 {
		return atoms[r.IntN(len(atoms))]
	}
	switch r.IntN(7) {
	case 0:
		return randomPattern(r, d-1) + "|" + randomPattern(r, d-1)
	case 1:
		return "(?:" + randomPattern(r, d-1) + ")" + repeats[r.IntN(len(repeats))]
	case 2:
		return "(" + randomPattern(r, d-1) + ")"
	default:
		return randomPattern(r, d-1) + randomPattern(r, d-1)
	}
}

// randomText is a random text of at most n letters.
func randomText(r *rand.Rand, n int) string {
	var b strings.Builder
	for range r.IntN(n + 1) {
		b.WriteString(letters[r.IntN(len(letters))])
	}
	return b.String()
}

// The search with a seeker finds what the search of every place finds, on
// random patterns over random texts, by every read of prefilterOracles:
// each match and every group of it, in the text with line ends read as LF
// and in the raw bytes, FindAll's matches, each value's line, MatchesIn
// over pieces of one buffer, and the lines a context search finds. Of the
// patterns the generator builds, enough have a seeker that the property is
// not held on nothing. The planted breaks, searching only the literal's
// line when a match may cross one, starting at the literal rather than the
// start of its line, and keeping the record of where literals occur across
// texts, each fail it.
func TestPrefilterFindsWhatEveryPlaceFinds(t *testing.T) {
	prefilterProperty(t, 400, 300)
}

// The same at ten times the patterns, 7.7 s plain on a workstation, so it runs in
// make slow.
func TestPrefilterFindsWhatEveryPlaceFindsAtLength(t *testing.T) {
	slowOnly(t)
	prefilterProperty(t, 4000, 3000)
}

// prefilterProperty holds prefilterCase on n random patterns, each over up
// to three random texts, and asks that at least least of those cases had a
// seeker.
func prefilterProperty(t *testing.T, n, least int) {
	t.Helper()
	r := rand.New(rand.NewPCG(290, uint64(n)))
	seeking := 0
	for range n {
		pattern := "(" + randomPattern(r, 4) + ")"
		for range 3 {
			if !prefilterCase(t, pattern, randomText(r, 120)) {
				break
			}
			seeking++
		}
	}
	t.Logf("%d cases had a seeker", seeking)
	if seeking < least {
		t.Errorf("%d cases had a seeker, want %d or more", seeking, least)
	}
}

// prefilterCases are cases a planted break or a mutant once got wrong.
// TestPrefilterCases holds each to every oracle, and FuzzPrefilter starts
// from them; a case found later goes here.
var prefilterCases = []struct{ pattern, data string }{
	// A match found past the literal's line in a window of span 1 is not
	// the first: accepting it gives a\n at 9 where the first is xa's a.
	{`((\x{FFFD}\s|a(?:a|\n){0,1}))`, "K\naaAx\n\nxa\n \nxaa"},
	{`([ab](?:a|\n)?)`, "K\nbaAx\n\nxb\n \nxba"},
	{`((?:[ab]|\n){2}c)`, "x\n\nb\nc\nac\n\nbc"},
}

func TestPrefilterCases(t *testing.T) {
	for _, c := range prefilterCases {
		if !prefilterCase(t, c.pattern, c.data) {
			t.Errorf("%s has no seeker", c.pattern)
		}
	}
}

// FuzzPrefilter is TestPrefilterFindsWhatEveryPlaceFinds over any pattern
// and text.
func FuzzPrefilter(f *testing.F) {
	f.Add(`([ab]c)`, "xac\nbc\r\nc")
	f.Add(`(?i)(k[ab])`, "Ka Kb kx")
	f.Add(`([^x]a)`, "\na\nxa ba")
	f.Add(`(?:^|[^a])(b+)`, "b\nab cb\nb")
	f.Add(`([ab]\b)`, "a ab\nb")
	f.Add(`(\B[ab]+)$`, "xa\nab b\n")
	f.Add(`((?s).[ab])`, "\na\nb")
	f.Add(`([ab]\x{FFFD})`, "a\xff b\xef\xbf\xbd")
	f.Add(`([ab]\n[ab])`, "a\nb\na\n\nb")
	f.Add(`(?:[ab]|\n){2}(c)`, "a\n\nc\nac")
	for _, c := range prefilterCases {
		f.Add(c.pattern, c.data)
	}
	f.Fuzz(func(t *testing.T, pattern, data string) {
		prefilterCase(t, pattern, data)
	})
}

// The record of where literals occur is the text's: MatchesIn searches the
// same text twice and finds the same matches in each, and a text after one
// with no literal, or two empty texts, still finds its own. The planted break, keeping the
// record whatever the text and position, finds none the second time.
func TestPrefilterLooksAgainInEachText(t *testing.T) {
	e, err := CompileSearchAt("regex", sourceOf("f"), `([ab]c)`)
	if err != nil || e.seek == nil {
		t.Fatalf("seeker %v, %v", e.seek, err)
	}
	text := []byte("ac bc")
	got, err := e.MatchesIn([][]byte{text, text, {}, {}, []byte("xx"), []byte("bc")})
	want := [][]Match{{{0, 2, 0, 2}, {3, 5, 3, 5}}, {{0, 2, 0, 2}, {3, 5, 3, 5}}, nil, nil, nil, {{0, 2, 0, 2}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("matches %v, %v; want %v", got, err, want)
	}
}

// A new window looks at the clock: the scan for its lines' bounds is not a
// read of the clock's, so past the deadline the search stops there rather
// than search a line that holds a match in fewer than ClockEvery runes.
// Planted break: no look at the clock in windowOf.
func TestPrefilterNewWindowLooksAtTheClock(t *testing.T) {
	e, err := CompileSearchAt("regex", sourceOf("f"), `([ab]c)`)
	if err != nil || e.seek == nil {
		t.Fatalf("seeker %v, %v", e.seek, err)
	}
	clock := &clockedReader{deadline: time.Now().Add(-time.Second), spare: maxTry}
	if m := e.first([]byte("xx\nac"), 0, clock); m != nil || !clock.stopped {
		t.Errorf("match %v, stopped %v; want none, stopped", m, clock.stopped)
	}
}

// A search with a seeker stops at the time limit as any search does, though
// each line it searches is shorter than the runes between two looks at the
// clock.
func TestPrefilterStopsAtTheTimeLimit(t *testing.T) {
	e, err := CompileSearchAt("regex", sourceOf("f"), `([ab]c)`)
	if err != nil || e.seek == nil {
		t.Fatalf("seeker %v, %v", e.seek, err)
	}
	e.TimeLimit = time.Nanosecond
	_, err = e.FromData([]byte(strings.Repeat("ac\n", 4*ClockEvery)))
	if err == nil || !strings.Contains(err.Error(), "matching took more than") {
		t.Errorf("error %v, want the refusal on time", err)
	}
}
