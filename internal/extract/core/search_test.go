package core

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The regex extractor finds exactly the matches FindAllSubmatchIndex finds,
// though it runs each search itself through a clocked reader, and every value it returns is text of
// the file, with CRLF read as LF, at a line inside it.
func FuzzRegex(f *testing.F) {
	f.Add(`^FROM (\S+)`, dockerfile)
	f.Add(`(x*)$`, "a\n\nb\n")
	f.Add(`^v=(.*)$`, "v=1\r\nv=2\r\n")
	f.Add(`\b(\w)`, "ab cd\xffé e")
	f.Add(`(?-m)^(a)|(b)`, "aab\nab")
	f.Add(`(\Aa|b)`, "abab")
	f.Add(`()`, "é\xff\n")
	f.Add(`(a)\Q)(`, "a)(a)(")
	// Patterns with a literal prefix, which find looks for before it
	// searches: occurrences that overlap, one at the very end, a group that
	// matches empty, flags and anchors that leave no prefix or one anchored
	// at the start of the file, a \b after the prefix, and occurrences far
	// enough apart that find tries the anchored pattern at each.
	f.Add(`aa([ab])`, "aaaab\naab aa")
	f.Add(`v=(\d*)`, "v=\nv=1v=")
	f.Add(`(?i)ab(c)`, "ABC abc aBc")
	f.Add(`a(?i)b(c)`, "aBC Abc abC")
	f.Add(`(?-m)^ab(c)`, "abc\nabc")
	f.Add(`(?-m)\Aab(c)`, "xabc abc")
	f.Add(`^ab(c)`, "xabc\nabc")
	f.Add(`ab\b(c)?`, "abc ab abab")
	f.Add(`ab\B(\w)`, "abc ab é abé")
	f.Add(`é(.)`, "\xc3\xc3\xa9x é\xff")
	f.Add(`x(\d)`, "x "+strings.Repeat(" ", 70)+"x5"+strings.Repeat(" ", 70)+"x")
	f.Add(`(?-m)\Ax(\d)`, "x "+strings.Repeat(" ", 70)+"x5")
	f.Add(`foo\b(\w*)`, "foobar"+strings.Repeat(" ", 70)+"foo bar"+strings.Repeat("\n", 70)+"foo")
	// A try that reads to where find cut it off, at maxTry bytes, would
	// match before the cut: $ matches at the end of the text it reads.
	f.Add(`x(a*)$`, "x"+strings.Repeat("a", 5000)+"b")
	// The deepest nesting errata takes without and with \b, which find
	// anchors without nesting it deeper, and a group at the top that
	// anchoring would nest too deeply, so find skips to the prefix but
	// makes no tries.
	f.Add("x"+strings.Repeat("(?:a", 499)+strings.Repeat(")*", 499)+"(y)", "xaay xy x")
	f.Add("x"+strings.Repeat("(?:a", 498)+strings.Repeat(")*", 498)+`\b(y)`, "xaay xy x")
	f.Add(tooDeepToAnchor, "q xaaa y xy x")
	// Before the first match, a pattern that looks back searches on from
	// the start of the line of a place too near the last try, or from the
	// start of the text when it holds \A.
	f.Add(`x\b(\d)`, "x x\nx x5 x1\nx")
	f.Add(`ab\B(\w*)`, "ab ab\nabab abc\nab")
	f.Add(`x(?:\A|y)(\d)`, "xy x xy1\nxy2")
	f.Add(`(?-m)\Ax(\d)`, "x x x\nx1")
	f.Fuzz(func(t *testing.T, pattern, data string) {
		r, err := CompileSearch("regex", "f", pattern, 0)
		if err != nil {
			return
		}
		lf := strings.ReplaceAll(data, "\r\n", "\n")
		got, err := r.Matches([]byte(lf))
		want := r.re.FindAllSubmatchIndex([]byte(lf), MaxValues+1)
		if err != nil {
			if len(want) <= MaxValues || !strings.Contains(err.Error(), "values, the limit") {
				t.Fatalf("error %v with %d matches", err, len(want))
			}
			return
		}
		if len(got) != len(want) {
			t.Fatalf("matches %v, FindAll %v", got, want)
		}
		for i, m := range want {
			if got[i] != Match(m[:4]) {
				t.Fatalf("match %d is %v, FindAll's %v", i, got[i], m[:4])
			}
		}
		values, err := r.FromData([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		count := strings.Count(data, "\n") + 1
		for _, v := range values {
			if v.At.Line < 1 || v.At.Line > count || !strings.Contains(lf, v.Text) {
				t.Fatalf("value %q at line %d of %d", v.Text, v.At.Line, count)
			}
		}
	})
}

// tooDeepToAnchor nests as deeply as RE2 allows inside a group, which
// anchoring would nest one level deeper. It does not look back, so no
// after refuses it first.
var tooDeepToAnchor = "(x" + strings.Repeat("(?:a", 499) + strings.Repeat(")*", 499) + ")"

// A pattern too deep to anchor is searched with its prefix and no tries,
// and finds what FindAll finds. The planted break, anchors without their
// programs, panics.
func TestRegexTooDeepToAnchorSearchesWithoutTries(t *testing.T) {
	e, err := CompileSearch("regex", "f", tooDeepToAnchor, 0)
	if err != nil || e.prefix == nil || e.anchored() != nil {
		t.Fatalf("prefix %q, anchors %v, error %v; want the prefix x, no anchors, no error", e.prefix, e.anchored(), err)
	}
	data := []byte("q xaaa y xy x")
	got, err := e.Matches(data)
	want := e.re.FindAllSubmatchIndex(data, -1)
	if err != nil || len(got) != len(want) || len(got) == 0 || got[0] != Match(want[0][:4]) {
		t.Errorf("matches %v, %v; FindAll %v", got, err, want)
	}
}

// The tries read no part of the file that a search of every place would
// not, but for the maxTry bytes a search after them may read again, or a
// few bytes where none is cut off. Counted in bytes read, so a mutant of
// the gap or the cut that reads more fails without a clock: a try at every
// a reads 44 times the file, one never cut off twice, and a try that reads
// to the end taken as cut off reads the a twice. The fourth is a search
// whose tries are cut off on every line, each read twice when the spare
// bytes are a find's and not a Matches', and a longer cut each time when a
// cut adds to them. The fifth looks back, so before its first match the
// search after the tries reads them again from the start, all of them if
// find did not count them. Tries that fail where they start read only the
// bytes they look at, 3 of each 128 for an x every 128 bytes: after the
// first match, or for a pattern that does not look back, counting them
// against the spare bytes would read all, and before it so would counting
// them from the start of the text rather than of the line, or adding to
// them. The next searches on unanchored once, where the x are dense, and
// tries again after: charging that search for more than it reads again
// would read the rest. The last reads its tries again before its first
// match and cuts a try after it: the two take from one maxTry, and not
// charging the first would read 7 KiB again. The tests take Matches' own
// clock, so its spare bytes are the ones tested. It took 6.3 s under -race
// with coverage on the build node, so it runs in make slow.
func TestRegexTriesReadTheFileAboutOnce(t *testing.T) {
	slowOnly(t)
	n := 1 << 16
	sparse := func(n int) string { return strings.Repeat("x"+strings.Repeat(" ", 127), n) }
	for _, c := range []struct {
		pattern, data string
		extra         int
	}{
		{`a(a{0,40}b)`, strings.Repeat("a", n) + "ab", 64},
		{`a(a*c)`, strings.Repeat("a", n) + "bac", maxTry},
		{`x(a*)b`, strings.Repeat(" ", 100) + "x" + strings.Repeat("a", 3000) + "b", 8},
		{`x(b)(?:[ab]*[ab]{0,8}z)?`, strings.Repeat("x"+strings.Repeat("b", 20000)+"\n", 20), maxTry + 64},
		{`x\B(a*b)`, strings.Repeat("x"+strings.Repeat("a", 1000)+strings.Repeat(" ", 70), 20) + "x x", maxTry + 64},
		{`x(\d)`, sparse(8192), 4<<13 - 1<<20},
		{`x\b(\d)`, strings.Repeat(sparse(2)+"\n", 4096), 4<<13 - 1<<20 - 4096},
		{`x(\d)`, sparse(512) + "x x5" + sparse(8192), 1<<16 - 8704<<7 - 4},
		{`x\B(a*b)`, strings.Repeat("x"+strings.Repeat("a", 1000)+strings.Repeat(" ", 70), 3) + "x xab\nx" + strings.Repeat("a", 20000) + "\n", maxTry + 64},
	} {
		e, err := CompileSearch("regex", "f", c.pattern, 0)
		if err != nil {
			t.Fatal(err)
		}
		clock := newClock(time.Minute)
		got, err := e.matches([]byte(c.data), clock)
		want := e.re.FindAllSubmatchIndex([]byte(c.data), -1)
		if err != nil || len(got) != len(want) {
			t.Errorf("%s: %d matches, %v; FindAll %d", c.pattern, len(got), err, len(want))
		}
		if clock.read > len(c.data)+c.extra || c.extra >= 0 && clock.read < len(c.data)-128 {
			t.Errorf("%s: read %d bytes of %d, want at most %d more and, unless fewer, all but 128", c.pattern, clock.read, len(c.data), c.extra)
		}
	}
}

// Before its first match a pattern that looks back is searched with re, as
// a search of every place does, not with after, which costs about twice as
// much: from the start of the text, or of the line of the place too near
// the last try. The last search of each reads from there to the end.
func TestRegexFirstSearchAfterTriesStartsAtTheLine(t *testing.T) {
	gap := strings.Repeat(" ", 64)
	for _, c := range []struct {
		pattern, data string
		from          int
	}{
		{`x\b(\d)`, strings.Repeat("x"+gap+" ", 100), 0},
		{`x\b(\d)`, strings.Repeat("x"+gap+"\n", 100), 66},
		{`x\b(\d)`, strings.Repeat("x\n"+gap[1:], 100), 2},
		{`(?-m)\Ax(\d)`, strings.Repeat("x"+gap+"\n", 100), 0},
	} {
		e, err := CompileSearch("regex", "f", c.pattern, 0)
		if err != nil {
			t.Fatal(err)
		}
		clock := newClock(time.Minute)
		if got, err := e.matches([]byte(c.data), clock); len(got) != 0 || err != nil {
			t.Fatalf("%s: %v, %v", c.pattern, got, err)
		}
		if from := len(c.data) - len(clock.data); from != c.from {
			t.Errorf("%s: the last search read from %d, want %d", c.pattern, from, c.from)
		}
	}
}

// A pattern compiles its anchored form on its first try, so one whose
// prefix never occurs costs no third program: CompileSearch compiles the
// pattern twice, once as written to check it and once with the m flag, and
// the planted break, the anchors compiled with them, three times.
func TestRegexAnchorsCompileOnTheFirstTry(t *testing.T) {
	pattern := "x(" + strings.Repeat("a{100}", 300) + ")"
	alone := extracttest.Allocated(func() { regexp.MustCompile("(?m)" + pattern) })
	var e Search
	var err error
	n := extracttest.Allocated(func() { e, err = CompileSearch("regex", "f", pattern, 0) })
	if err != nil || n > alone*5/2 {
		t.Errorf("CompileSearch allocated %d bytes, %d for the pattern alone; error %v", n, alone, err)
	}
	if got, err := e.Matches([]byte(strings.Repeat("a", 1000))); len(got) != 0 || err != nil {
		t.Fatal(got, err)
	}
	if n := extracttest.Allocated(func() { _, _ = e.Matches([]byte("x" + strings.Repeat("a", 30000))) }); n < alone/2 {
		t.Errorf("the first try allocated %d bytes, under half the %d of the pattern", n, alone)
	}
}

const dockerfile = `FROM golang:1.27 AS build
RUN make
FROM alpine:3.22
# FROM scratch
LABEL version="0.2.0"
`

// The regex search counts lines with a cursor that only moves forward, so a
// file of newlines costs no memory for each one: the planted break is the
// index of every newline, 8 bytes each. It allocates nothing of the file's
// size, since the file has no CRLF to copy away.
// TestJSONLinesCostNoMemoryForEachNewline is the same for JSON.
func TestRegexLinesCostNoMemoryForEachNewline(t *testing.T) {
	data := []byte("1" + strings.Repeat("\n", 1<<18) + "x")
	e, err := CompileSearch("regex", "f", "(x)", 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []check.Value
	n := extracttest.Allocated(func() { got, err = e.FromData(data) })
	if err != nil || extracttest.Texts(got) != "x@262145" {
		t.Errorf("regex: %s, %v", extracttest.Texts(got), err)
	}
	if bound := uint64(len(data)); n > bound {
		t.Errorf("%d bytes allocated for a file of %d, over %d", n, len(data), bound)
	}
}
