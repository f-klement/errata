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
		{`([0-9]{3}:[A-Za-z0-9_-]{3,})`, []string{":"}, 0},
		{`([0-9]{6,}\n:)`, []string{"\n:"}, 1},
		{`([0-9]{6,}ab)`, []string{"ab"}, 0},
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

// The run of a class a seeker looks for, its length and the literals it
// looks for with it, for a pattern whose literals are a byte long or none:
// the narrower class first, then the longer run, at least minRun bytes, of
// a class all ASCII of at most maxClass bytes. "" is no run. A pattern that
// may cross a line, or has a literal of minLit bytes, keeps the literals of
// TestSeekerLiterals.
func TestSeekerRuns(t *testing.T) {
	const digits, ap = "0123456789", "abcdefghijklmnop"
	for _, c := range []struct {
		pattern string
		class   string // the bytes of the class, in order
		k       int
		lits    []string
	}{
		{`([0-9]{6,}:[A-Za-z0-9_-]{25,})`, digits, 6, []string{":"}},
		{`(\d{4}x)`, digits, 4, []string{"x"}},
		{`([ab]\d{4})`, digits, 4, []string{"a", "b"}},
		{`([a-p]{4}[0-9]{4})`, digits, 4, nil},
		{`([a-p]{9}\d{4,})`, digits, 4, nil},
		{`([0-9]{4}[a-p]{9})`, digits, 4, nil},
		{`([0-9a-f]{5}[a-f]{5})`, "abcdef", 5, []string{"a", "b", "c", "d", "e", "f"}},
		{`([a-p]{4}[a-p]{6}|)`, "", 0, nil},
		{`([a-p]{4}[b-p]{6})`, ap[1:], 6, nil},
		{`([a-p]{6}[a-p]{4})`, ap, 6, nil},
		{`([a-p]{4}[a-p]{6})`, ap, 6, nil},
		{`([a-p]{4}[0-5a-j]{4})`, ap, 4, nil},
		{`([0-9a-f]{4})`, "0123456789abcdef", 4, nil},
		{`([0-9a-g]{4})`, "", 0, nil},
		{`(\w{4}\.)`, "", 0, nil},
		{`(\w{5}[^\w\s]{3})`, "", 0, nil},
		{`([0-9\x{7f}]{4})`, digits + "\x7f", 4, nil},
		{`([0-9\x{80}]{4})`, "", 0, nil},
		{`([a-p]{4})`, ap, 4, nil},
		{`([a-z]+[0-9]{4})`, digits, 4, nil},
		{`((?:[0-9]{4}x)+)`, digits, 4, []string{"x"}},
		{`(\b[0-9]{4,8}\b)`, digits, 4, nil},
		{`([a-p]{3})`, "", 0, nil},
		{`([a-p]{0,6}x)`, "", 0, nil},
		{`([a-p]*x)`, "", 0, nil},
		{`((?:[0-9]{4}x)*y)`, "", 0, nil},
		{`((?:[0-9]{4}x)?y)`, "", 0, nil},
		{`((?:[0-9]{4}x){0,3}y)`, "", 0, nil},
		{`(?:[0-9]{6}|[a-p]{6})(x)`, "", 0, nil},
		{`([^x]{6})`, "", 0, nil},
		{`([a-p\x{e9}]{6})`, "", 0, nil},
		{`(?i)([k-l]{6})`, "", 0, nil},
		{`(([0-9]){6})`, "", 0, nil},
		{`([0-9\n]{6})`, "", 0, nil},
		{`([0-9]{6}(?s:.))`, "", 0, nil},
		{`(\A[0-9]{6})`, "", 0, nil},
	} {
		s := seekerOf("(?m)" + c.pattern)
		got, k := runClass(s)
		if got != c.class || k != c.k {
			t.Errorf("%s: run of %d of %q, want %d of %q", c.pattern, k, got, c.k, c.class)
		}
		if s == nil || s.run == nil {
			continue
		}
		lits := []string{}
		for _, l := range s.lits {
			lits = append(lits, string(l))
		}
		if s.run.size != len(got) || !slices.Equal(lits, append([]string{}, c.lits...)) || s.span != 0 {
			t.Errorf("%s: size %d of %q, literals %q, span %d; want literals %q", c.pattern, s.run.size, got, lits, s.span, c.lits)
		}
	}
}

// A seeker of a literal and a run reports only the lines that hold both,
// whichever of the two is on every line: so a search near them reads no
// more lines than a search near the rarer would. Each text has 1000 lines
// with one of them, two with both, of which one matches. Planted break: a
// seeker of both that looks for its run alone.
func TestSeekerOfBothReportsTheLinesWithBoth(t *testing.T) {
	s := seekerOf(`(?m)(\d{4}x)`)
	if s == nil || s.run == nil || len(s.lits) != 1 {
		t.Fatalf("seeker %v", s)
	}
	every := func(line string) []byte {
		lines := slices.Repeat([]string{line}, 1000)
		lines[300], lines[700] = "x 9999 a", "a 1234x"
		return []byte(strings.Join(lines, "\n"))
	}
	for _, line := range []string{"12345,678", "x, y and z"} {
		data := every(line)
		clock := newClock(time.Minute)
		var got []int
		for pos := 0; pos < len(data); {
			c := s.next(data, pos, clock)
			if c < 0 {
				break
			}
			got = append(got, bytes.Count(data[:c], newline))
			pos = lineEnd(data, c) + 1
		}
		if !slices.Equal(got, []int{300, 700}) {
			t.Errorf("%q on every line: lines %v reported, want 300 and 700", line, got[:min(len(got), 10)])
		}
	}
}

// runClass is the bytes of the class of the run s looks for, in order, and
// the run's length; "" and 0 when s looks for no run.
func runClass(s *seeker) (string, int) {
	if s == nil || s.run == nil {
		return "", 0
	}
	var class strings.Builder
	for b := range 256 {
		if s.run.set[b] {
			class.WriteByte(byte(b))
		}
	}
	return class.String(), s.run.k
}

// A run's index is the first place at or after pos where k bytes of its
// class start in a row: inside a longer run, at the very end of the text,
// past a run cut short, and nowhere when the text is shorter than k.
func TestClassRunIndex(t *testing.T) {
	run := classRunOf([]rune{'0', '9'}, 3)
	for _, c := range []struct {
		data string
		pos  int
		want int
	}{
		{"123", 0, 0},
		{"x123", 0, 1},
		{"12x123", 0, 3},
		{"1x2x345", 0, 4},
		{"12345", 2, 2},
		{"12345", 3, -1},
		{"12", 0, -1},
		{"", 0, -1},
		{"x12x12x", 0, -1},
		{"ab9x1234", 1, 4},
		{"\xd9\xb12345", 0, 2},
		{"12\u010034\xff56", 0, -1},
	} {
		if got := run.index([]byte(c.data), c.pos); got != c.want {
			t.Errorf("%q from %d: %d, want %d", c.data, c.pos, got, c.want)
		}
	}
}

// A search with a seeker of a run ends, on a text all of the class, one
// with none of it, a run at the end, and lines with a run and without. A
// scan that never moves on, or a place it reports where there is none,
// would search one window for ever; the test fails after five seconds
// rather than wait for the mutation's limit. Planted break: findNear
// moving pos back to the start of the window's line rather than past it.
func TestPrefilterRunSearchEnds(t *testing.T) {
	e, err := CompileSearchAt("regex", sourceOf("f"), `(\d{4}x)`)
	if err != nil || e.seek == nil || e.seek.run == nil {
		t.Fatalf("seeker %v, %v", e.seek, err)
	}
	for _, text := range []string{"1234", "abcd\nefgh\nijkl", "ab\ncd 1234", "1234x\nab\n9876\n5555x 12"} {
		done := make(chan error, 1)
		go func() {
			_, err := e.Matches([]byte(text))
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%q: %v", text, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%q: the search has not ended after 5 s", text)
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
// compares it with the same search with no seeker. It returns the
// pattern's seeker, nil for none; a pattern with none is not compared.
func prefilterCase(t *testing.T, pattern, data string) *seeker {
	t.Helper()
	e, err := CompileSearchAt("regex", sourceOf("f"), pattern)
	if err != nil || e.seek == nil {
		return nil
	}
	plain := e
	plain.seek = nil
	for _, o := range prefilterOracles {
		if got, want := o.read(t, e, []byte(data)), o.read(t, plain, []byte(data)); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %q in %q: %v, with no seeker %v", o.name, pattern, data, got, want)
		}
	}
	return e.seek
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
		// Every caller of lines hands it text with its line ends read as LF.
		set, err := (&Around{}).lines(&e, "with", LineFeeds(data), time.Now().Add(time.Minute), "f", time.Minute, "")
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
// with an orbit of three (k, s), U+FFFD, a lazy repeat, runs of a class
// long enough for a seeker to look for (an ASCII one, one past ASCII, one
// that holds a newline), and text of a few lines in an alphabet they match,
// with CRs, CRLFs, broken UTF-8, digits, and the marks of Go's comments and
// strings, so the blanked comments oracle has comments to blank.
var (
	atoms = []string{"a", "b", "ab", `\n`, "[ab]", "[^a]", ".", "(?s:.)", `\b`, `\B`, "^", "$",
		"(?i:a)", "(?i:k)", "(?i:s)", "(?i:ss)", "ſ", `\x{212A}`, "x", "é", "(?i:é)", `[éx]`, `\x{FFFD}`,
		`[a\n]`, `[\r\n]`, `\r`, `[^\n]`, `[^x]`, "[ax]", `\w`, `\s`, "(?U:a+)",
		`\d{4,}`, `[0-9ab]{4}`, `\d{4}`, `[0-9é]{4}`, `[0-9\n]{4}`}
	letters = []string{"a", "b", "x", "\n", "\n\n", " ", "A", "K", "s", "S", "ſ", "é", "É",
		"\xff", "\xc3", "\u212a", "\r", "\r\n", "//", "/*", "*/", `"`, "`", "1", "1234", "12345"}
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
// patterns the generator builds, enough have a seeker, and enough a seeker
// of a run, that the property is not held on nothing. The planted breaks,
// searching only the literal's line when a match may cross one, starting
// at the literal rather than the start of its line, keeping the record of
// where literals occur across texts, a run's scan that takes k-1 bytes of
// its class for k, and seeking a run in a pattern that may cross a line,
// each fail it.
func TestPrefilterFindsWhatEveryPlaceFinds(t *testing.T) {
	prefilterProperty(t, 400, 300, 30)
}

// The same at ten times the patterns, 7.7 s plain on a workstation, so it runs in
// make slow.
func TestPrefilterFindsWhatEveryPlaceFindsAtLength(t *testing.T) {
	slowOnly(t)
	prefilterProperty(t, 4000, 3000, 300)
}

// prefilterProperty holds prefilterCase on n random patterns, each over up
// to three random texts, and asks that at least least of those cases had a
// seeker and at least runs a seeker of a run.
func prefilterProperty(t *testing.T, n, least, runs int) {
	t.Helper()
	r := rand.New(rand.NewPCG(290, uint64(n)))
	seeking, running := 0, 0
	for range n {
		pattern := "(" + randomPattern(r, 4) + ")"
		for range 3 {
			s := prefilterCase(t, pattern, randomText(r, 120))
			if s == nil {
				break
			}
			seeking++
			if s.run != nil {
				running++
			}
		}
	}
	t.Logf("%d cases had a seeker, %d of a run", seeking, running)
	if seeking < least || running < runs {
		t.Errorf("%d cases had a seeker, %d of a run; want %d or more, %d or more", seeking, running, least, runs)
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
	// A class past ASCII has no run: a seeker of the bytes of its runes
	// below 256, as if each were one byte, finds no run in 1é12.
	{`([0-9é]{4}:)`, "1é12:\n"},
	// A seeker of both looks on from the start of the later one's line: from
	// the run at 3 it would miss the x of x1234 before it.
	{`([xy]\d{4})`, "x\nx1234\n"},
}

func TestPrefilterCases(t *testing.T) {
	for _, c := range prefilterCases {
		if prefilterCase(t, c.pattern, c.data) == nil {
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
	f.Add(`([0-9]{6,}:[A-Za-z0-9_-]{25,})`, "id 1234567:abcdefghijklmnopqrstuvwxyz0123\n12345678:short\n")
	f.Add(`(\b\d{4,})x`, "12345x 1234\n9 1234x")
	f.Add(`(?:^|[^a])(\d{4}[a-z])`, "12345a\na1234b\n\xff1234c")
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
