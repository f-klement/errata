package core

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// contextSearch is the search of pattern keeping the matches with context
// c, as a regex extractor builds it.
func contextSearch(t *testing.T, pattern string, c Context) Search {
	t.Helper()
	e, err := compilePattern("regex", pattern)
	if err != nil {
		t.Fatal(err)
	}
	a, err := CompileContext("regex", c)
	if err != nil {
		t.Fatal(err)
	}
	e.TimeLimit = 10 * time.Second
	return e.Around(a)
}

func contextTexts(t *testing.T, e Search, data string) string {
	t.Helper()
	vs, err := e.FromData([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return extracttest.Texts(vs)
}

// contextLines is a file whose lines say which they are.
const contextLines = "a\nx1\nb\nx2\n\nx3\n"

// A match is kept when with matches within before lines before its line or
// after lines after it, the line itself included, and dropped when without
// does; a line one past the window does not count. Planted breaks: the
// window not widened by before, or by after; with's test inverted;
// without's ignored.
func TestContextWindow(t *testing.T) {
	for _, c := range []struct {
		c    Context
		want string
	}{
		{Context{With: `^a$`}, ""},
		{Context{Before: 1, With: `^a$`}, "1@2"},
		{Context{Before: 3, With: `^a$`}, "1@2 2@4"},
		{Context{After: 1, With: `^b$`}, "1@2"},
		{Context{Before: 1, After: 1, With: `^b$`}, "1@2 2@4"},
		{Context{With: `2`}, "2@4"},
		{Context{Before: 1, After: 1, Without: `^b$`}, "3@6"},
		{Context{Without: `^x[12]$`}, "3@6"},
		{Context{Before: 1, With: `^$`, Without: `^b$`}, "3@6"},
		{Context{Before: 1 << 40, After: 1 << 40, With: `^a$`}, "1@2 2@4 3@6"},
	} {
		if got := contextTexts(t, contextSearch(t, `^x(\d)$`, c.c), contextLines); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.c, got, c.want)
		}
	}
}

// A match over several lines has the context of the lines from its first
// to its last, and a match that ends with a newline ends on the line the
// newline ends. Planted breaks: the last line taken as the first; the
// newline at the end counted.
func TestContextOfAMatchOverLines(t *testing.T) {
	for _, c := range []struct {
		pattern string
		c       Context
		want    string
	}{
		{`(x1)\nb`, Context{After: 1, With: `^x2$`}, "x1@2"},
		{`(x1)\nb`, Context{With: `^x2$`}, ""},
		{`(x1)\nb`, Context{With: `^b$`}, "x1@2"},
		{`(x1)\n`, Context{With: `^b$`}, ""},
		{`(x1)\n`, Context{After: 1, With: `^b$`}, "x1@2"},
	} {
		if got := contextTexts(t, contextSearch(t, c.pattern, c.c), contextLines); got != c.want {
			t.Errorf("%s %+v: got %q, want %q", c.pattern, c.c, got, c.want)
		}
	}
}

// markedLines is the lines, counted from 1, in which the context pattern
// p finds a match.
func markedLines(t interface{ Fatal(...any) }, p string, data string) string {
	a, err := CompileContext("regex", Context{With: p})
	if err != nil {
		t.Fatal(err)
	}
	set, err := a.lines(a.with, "with", []byte(data), time.Now().Add(10*time.Second), "f", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for line := 1; line <= set.lines; line++ {
		if set.any(line, line) {
			out = append(out, strconv.Itoa(line))
		}
	}
	return strings.Join(out, " ")
}

// A context pattern matches in a line as it matches that line alone:
// nothing in it matches a line break, and \A and \z match at the ends of
// each line. Planted breaks, each seen failing here: . made to match \n
// under s; a class keeping \n; a literal \n kept; \A or \z left as they
// are.
func TestContextPatternStaysInItsLine(t *testing.T) {
	const data = "ab\ncd\n"
	for _, c := range []struct{ pattern, want string }{
		{`b\nc`, ""},
		{`b\sc`, ""},
		{`b[^x]c`, ""},
		{`b[\n]c`, ""},
		{`(?s)b.c`, ""},
		{`(?i)b\x0A?c`, ""},
		{`\Ac`, "2"},
		{`b\z`, "1"},
		{`(?-m)^c`, "2"},
		{`(?-m)b$`, "1"},
		{`^$`, ""},
		{`(c)|(a)`, "1 2"},
	} {
		if got := markedLines(t, c.pattern, data); got != c.want {
			t.Errorf("%s: lines %q, want %q", c.pattern, got, c.want)
		}
	}
}

// fileLinesOf is the lines of text as a file holds them: a final newline
// ends the last line and starts none, and an empty text is one empty line.
func fileLinesOf(text string) []string {
	lines := strings.Split(text, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// For any text and each of these patterns, the lines a context pattern
// finds a match in are those where the pattern matches the line alone,
// which is what ADR 0012 says it searches. Planted breaks: . made to
// match \n under s; a class keeping \n; a line counted after a final
// newline.
func TestContextLinesAreEachLineAlone(t *testing.T) {
	patterns := []string{`a`, `^a`, `a$`, `\Aa`, `a\z`, `\ba`, `a\B`, `a\s*b`, `[^x]+b`, `(?s)a.b`, `a\nb`,
		`^$`, `^\s*$`, `(?-m)^a`, `(?-m)a$`, `a|b\nx`, `x{2,}`, `(?i)A b`, `^\s*a`}
	rapid.Check(t, func(t *rapid.T) {
		text := rapid.StringOf(rapid.SampledFrom([]rune("ab x\n"))).Draw(t, "text")
		for _, p := range patterns {
			re := regexp.MustCompile("(?m)" + p)
			var want []string
			for i, line := range fileLinesOf(text) {
				if re.MatchString(line) {
					want = append(want, strconv.Itoa(i+1))
				}
			}
			if got := markedLines(t, p, text); got != strings.Join(want, " ") {
				t.Fatalf("%s over %q: lines %q, want %q", p, text, got, strings.Join(want, " "))
			}
		}
	})
}

// A range holds a set line on either side of a word of the set. Planted
// breaks: below counting the line itself; the range not cut to the text.
func TestLineSetRanges(t *testing.T) {
	s := newLineSet(200)
	for _, l := range []int{1, 64, 65, 129, 200} {
		s.add(l)
	}
	s.count()
	for _, c := range []struct {
		lo, hi int
		want   bool
	}{
		{1, 1, true}, {2, 63, false}, {2, 64, true}, {65, 65, true}, {66, 128, false},
		{66, 129, true}, {130, 199, false}, {199, 1 << 40, true}, {-5, 0, false}, {201, 300, false},
	} {
		if got := s.any(c.lo, c.hi); got != c.want {
			t.Errorf("any(%d, %d) = %v, want %v", c.lo, c.hi, got, c.want)
		}
	}
}

// A context needs a pattern, counts lines from 0 up, and a pattern that
// does not compile or references values does not load. Each refusal is
// asserted by its message, which quotes the pattern as the config wrote
// it, without the m flag errata adds. Planted breaks: each check removed;
// the message's parse given the m flag.
func TestContextRefusals(t *testing.T) {
	for _, c := range []struct {
		c    Context
		want string
	}{
		{Context{Before: 1}, "regex: context needs with, without or both"},
		{Context{Before: -1, With: "a"}, "regex: context before -1 and after 0 count lines and cannot be below 0"},
		{Context{After: -2, With: "a"}, "regex: context before 0 and after -2 count lines"},
		{Context{With: "("}, "regex: context with \"(\": error parsing regexp: missing closing ): `(`"},
		{Context{Without: "a{2000}"}, "regex: context without \"a{2000}\": error parsing regexp: invalid repeat count"},
		{Context{With: "(?{v})"}, "regex: context with \"(?{v})\" references an extractor's values"},
	} {
		_, err := CompileContext("regex", c.c)
		if err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%+v: error %v, want %q", c.c, err, c.want)
		}
	}
}

// The context's searches run under the deadline the matching ran under,
// and one past it is refused, naming the pattern. The pattern has a
// literal prefix, so its own search is a byte scan. A line of 'a's costs a
// counted repeat about 60 microseconds a rune, so 4 MB would take about
// 4 minutes unstopped. Planted break: the context's clock given no
// deadline of the search's.
func TestContextSharesTheTimeLimit(t *testing.T) {
	e := contextSearch(t, `k(1)`, Context{After: 1, With: `[ab]{0,1000}c`})
	e.TimeLimit = 100 * time.Millisecond
	data := "k1\n" + strings.Repeat("a", 4<<20) + "\n"
	start := time.Now()
	_, err := e.FromData([]byte(data))
	took := time.Since(start)
	want := "matching took more than 100ms, the limit, and stopped in the search of the context's with pattern; shorten a counted repeat"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error %v, want %q", err, want)
	}
	if took > 5*time.Second {
		t.Errorf("took %v past a limit of 100ms", took)
	}
}

// No match leaves the context unsearched: the same costly context over a
// file the pattern finds nothing in is no refusal. Planted break: keep
// searching with no match to keep.
func TestContextUnsearchedWithoutAMatch(t *testing.T) {
	e := contextSearch(t, `k(1)`, Context{With: `[ab]{0,1000}c`})
	e.TimeLimit = 100 * time.Millisecond
	if _, err := e.FromData([]byte(strings.Repeat("a", 4<<20) + "\n")); err != nil {
		t.Fatal(err)
	}
}

// contextShape is n lines of which every tenth is a match, and each line
// holds what with matches: the context's search stops at each line, the
// most calls it makes, and without, which matches nowhere, reads all of
// it. A window as wide as the file lets each match see every line.
func contextShape(n int) []byte {
	var b strings.Builder
	for i := range n {
		if i%10 == 0 {
			b.WriteString("x 7 b\n")
		} else {
			b.WriteString("a b\n")
		}
	}
	return []byte(b.String())
}

// The time and the memory of a context grow linearly with the file: four
// times the lines take at most six times the time, with a tenth of a
// second for the timer, and allocate at most five times the bytes. A
// ratio, not a time, so it holds on any node. Planted break: each match
// searching its whole window, which costs the matches times the lines
// and took 16 times the time. make slow runs it.
func TestContextIsLinear(t *testing.T) {
	slowOnly(t)
	e := contextSearch(t, `^x (\d)`, Context{Before: 1 << 30, After: 1 << 30, With: `b`, Without: `zzz`})
	e.TimeLimit = time.Minute
	measure := func(n int) (time.Duration, uint64, int) {
		data := contextShape(n)
		var took time.Duration
		var kept int
		alloc := extracttest.Allocated(func() {
			start := time.Now()
			vs, err := e.FromData(data)
			took = time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			kept = len(vs)
		})
		return took, alloc, kept
	}
	t1, a1, k1 := measure(250_000)
	t4, a4, k4 := measure(1_000_000)
	t.Logf("250000 lines: %v, %d bytes; 1000000 lines: %v, %d bytes", t1, a1, t4, a4)
	if k1 != 25_000 || k4 != 100_000 {
		t.Fatalf("kept %d and %d, want 25000 and 100000", k1, k4)
	}
	if limit := 6*t1 + 100*time.Millisecond*extracttest.RaceSlowdown; t4 > limit {
		t.Errorf("four times the lines took %v, over %v", t4, limit)
	}
	if a4 > 5*a1 {
		t.Errorf("four times the lines allocated %d bytes, over five times %d", a4, a1)
	}
}

// Whether a file ends with a newline does not change what a context
// holds: the newline ends the last line and starts no empty one, while an
// empty line in the file is still one, and an empty file is one empty
// line. Planted breaks: the line count one more after a final newline; an
// empty file counted as ending with one.
func TestContextFinalNewline(t *testing.T) {
	for _, c := range []struct {
		data string
		c    Context
		want string
	}{
		{"x 1\n", Context{After: 1, Without: `^$`}, "1@1"},
		{"x 1", Context{After: 1, Without: `^$`}, "1@1"},
		{"x 1\n", Context{After: 1, With: `^$`}, ""},
		{"x 1\n", Context{After: 1, Without: `^\s*$`}, "1@1"},
		{"x 1\n\n", Context{After: 1, With: `^$`}, "1@1"},
		{"x 1\n\n", Context{After: 1, Without: `^$`}, ""},
	} {
		if got := contextTexts(t, contextSearch(t, `x (\d)`, c.c), c.data); got != c.want {
			t.Errorf("%q %+v: got %q, want %q", c.data, c.c, got, c.want)
		}
	}
	if got := markedLines(t, `^$`, ""); got != "1" {
		t.Errorf("an empty text: ^$ marks lines %q, want its one line", got)
	}
}

// A context pattern that matches the empty text at the start or the end
// of every line holds in every line, so it does not load, named in the
// message; one that needs a character, or matches only an empty line,
// loads. Planted breaks: the check removed; ^ taken to hold at no line
// start; a star taken to need its operand.
func TestContextRefusesAPatternThatHoldsEverywhere(t *testing.T) {
	for _, p := range []string{`x*`, `^`, `$`, `\A`, `\z`, `(?:)`, `(foo)?`, `^\s*#?`, `a{0,3}`, `(?:a|)`, `(?-m)^`, `^|x`, `(x?)+`} {
		for _, key := range []string{"with", "without"} {
			c := Context{With: p}
			if key == "without" {
				c = Context{Without: p}
			}
			_, err := CompileContext("regex", c)
			want := "regex: context " + key + " " + Quote(p) + " matches the empty text at the start or the end of every line, so it holds in every line"
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("%s %s: error %v, want %q", key, p, err, want)
			}
		}
	}
	for _, p := range []string{`^$`, `^[^#]*$`, `\b`, `a?b`, `x+`, `^\s*#`, `#$`, `\B`, `^a*$`} {
		if _, err := CompileContext("regex", Context{With: p}); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

// holds asks of one match at a time what keep asks of a list, so a
// caller that hands each match on as it finds it can filter by context
// without collecting them, and a nil context holds for every match.
// Planted breaks: holds giving true before the line sets are found;
// a nil context's holds giving false.
func TestContextHoldsOneMatchAtATime(t *testing.T) {
	data := []byte(contextLines)
	e := contextSearch(t, `^x(\d)$`, Context{Before: 1, With: `^a$|^$`})
	matches, err := e.Matches(data)
	if err != nil {
		t.Fatal(err)
	}
	in := e.around.of(data, time.Now().Add(10*time.Second), "f", time.Second, "the limit")
	var got []string
	for _, m := range matches {
		ok, err := in.holds(m[0], m[1])
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, strconv.FormatBool(ok))
	}
	if strings.Join(got, " ") != "true false true" {
		t.Errorf("holds gave %v, want true false true", got)
	}
	var none *Around
	if ok, err := none.of(data, time.Now(), "f", 0, "").holds(0, 1); !ok || err != nil {
		t.Errorf("a nil context's holds gave %v, %v", ok, err)
	}
}
