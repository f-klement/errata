package mdparse

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// Each bound of the parse, with the planted break that crosses it and, where
// the bound is a size, the input at the bound that reads. A refusal is
// asserted by its message, whole unless it holds a count of matches, and a
// shape that took long before its bound must now be refused within its time.
// A case on a large input that -race slows past half a second is large, and
// make slow runs it; a smaller input that reaches the same limit stays here.
func TestMarkdownBounds(t *testing.T) {
	markdownBounds(t, false)
}

// The large cases of TestMarkdownBounds. A large case's time is held to a
// ratio, not a wall clock, so that it holds on a loaded node: a quarter of
// its input, of the same shape, is read too, and the case may take at most
// eight times as long, with a tenth of a second for a pause in either run. A
// linear read takes about four times, and a quadratic one sixteen. Each is
// timed twice and the faster run kept, so a moment of load on one run does
// not count. Planted break: definitions walking every line before each, a
// quadratic walk, which failed the 100000 definitions.
func TestMarkdownBoundsOnLargeInputs(t *testing.T) {
	slowOnly(t)
	markdownBounds(t, true)
}

// markdownBounds runs the cases whose large is large.
func markdownBounds(t *testing.T, large bool) {
	t.Helper()
	quotes := func(n int) string { return strings.Repeat("> ", n) + "a\n" }
	emphasis := func(n int) string { return strings.Repeat("*a ", n) + "b" + strings.Repeat(" a*", n) + "\n" }
	runs := func(n int) string { return strings.Repeat("_a ", n) + "\n" }
	defs := func(n int) string { return strings.Repeat("[x]: y\n", n) + "\na\n" }
	const tail = "; a regex extractor can still read the file"
	for _, c := range []struct {
		name    string
		body    string
		limit   int64
		parse   time.Duration
		refused string
		bound   time.Duration
		large   bool
		// quarter is a large case's input at a quarter of its size, and
		// quarterRefused the start of its refusal, "" for one that reads.
		quarter, quarterRefused string
	}{
		// Blocks and inline nodes nest at most maxDepth levels: the text of
		// a paragraph in 998 block quotes is at 1000. The guard refuses a
		// block deeper than that as it opens, before goldmark's walk of
		// open blocks for each line, quadratic in the depth, has run: 131072
		// block quotes took 51 s in goldmark alone.
		{name: "998 block quotes", body: quotes(998)},
		{name: "999 block quotes", body: quotes(999), refused: "README.md: line 1: nested more than 1000 levels deep, the limit" + tail},
		{name: "100000 block quotes", body: quotes(100_000), refused: "README.md: line 1: nested more than 1000 levels deep, the limit" + tail, bound: time.Second},
		{name: "998 levels of emphasis", body: "x\n" + emphasis(998)},
		{name: "999 levels of emphasis", body: "x\n" + emphasis(999), refused: "README.md: line 2: nested more than 1000 levels deep, the limit" + tail},
		// Runs of * and _ in one paragraph: the count starts again in each.
		{name: "5000 runs", body: runs(5000)},
		{name: "two paragraphs of 3000 runs", body: runs(3000) + "\n" + runs(3000)},
		{name: "5001 runs", body: "x\n\n" + runs(5001), refused: "README.md: line 3: a paragraph holds more than 5000 runs of * and _, the limit" + tail},
		// Runs that close nothing, which goldmark v1 matched against every run
		// before them: 256 KB took over a minute.
		{name: "5001 runs that match nothing", body: strings.Repeat("*a_ ", 2501), refused: "README.md: line 1: a paragraph holds more than 5000 runs of * and _, the limit" + tail, bound: time.Second},
		// Runs read for emphasis cost goldmark's pairing of them when the
		// paragraph closes, which the guard sees call by call, and nothing
		// before: errata reads each run itself (parseRun), so goldmark scans no
		// line ahead for a closing run. Lines of closed emphasis in one
		// paragraph are two runs for each emphasis.
		{name: "lines of emphasis", body: strings.Repeat(strings.Repeat("*ab* ", 25)+"\n", 100), limit: 100_000},
		{name: "lines of emphasis past the run limit", body: strings.Repeat(strings.Repeat("*ab* ", 50)+"\n", 100), limit: 100_000,
			refused: "README.md: line 51: a paragraph holds more than 5000 runs of * and _, the limit" + tail},
		// The slowest of ten shapes of 5000 runs on one line, measured when
		// goldmark scanned ahead from each run, at the run limit.
		{name: "5000 runs of the slowest shape", body: strings.Repeat("****_a*a", 1666) + strings.Repeat("b", 300) + "\n", bound: time.Second},
		// A long line of closed emphasis is runs that the run limit counts.
		{name: "emphasis along a long line", body: strings.Repeat("*ab* ", 20_000),
			refused: "README.md: line 1: a paragraph holds more than 5000 runs of * and _, the limit" + tail},
		// A table, one paragraph to CommonMark, of rows with four runs in
		// each: 1251 rows are over the run limit.
		{name: "a table of 1250 wide rows", body: "| a | b | c |\n|---|---|---|\n" + strings.Repeat("| **name** | _type_ | "+strings.Repeat("d", 280)+" |\n", 1250)},
		{name: "a table of 1251 wide rows", body: "| a | b | c |\n|---|---|---|\n" + strings.Repeat("| **name** | _type_ | "+strings.Repeat("d", 280)+" |\n", 1251),
			refused: "README.md: line 1253: a paragraph holds more than 5000 runs of * and _, the limit" + tail},
		{name: "100000 runs that match nothing", body: strings.Repeat("*a_ ", 100_000), refused: "README.md: line 1: a paragraph holds more than 5000 runs of * and _, the limit" + tail, bound: time.Second},
		// Paragraphs of runs that close nothing, which goldmark scanned to
		// the end of the line from each when it matched runs at once: each run
		// is a node now, and 1 MiB of them is over the node limit.
		{name: "lines of 85 runs that match nothing", body: strings.Repeat(strings.Repeat("*a ", 85)+"\n\n", 4000), limit: 1 << 20,
			refused: "README.md: the Markdown parse makes more than 262144 nodes, the limit", bound: time.Second},
		// Runs inside words may open and close, and goldmark walked the
		// paragraph's runs for an opener before it matched one of them at
		// once. Read by parseRun, each is a run that the run limit counts,
		// whatever the character before it.
		{name: "runs then lines of emphasis inside words", body: strings.Repeat("a* ", 4990) + "\n" + strings.Repeat(strings.Repeat("a*b*c ", 40)+"\n", 400), limit: 1 << 20,
			refused: "README.md: line 2: a paragraph holds more than 5000 runs of * and _, the limit" + tail, bound: time.Second},
		{name: "runs then lines of emphasis after a form feed", body: strings.Repeat("a* ", 4990) + "\n" + strings.Repeat(strings.Repeat("a\f*b*c ", 34)+"\n", 400), limit: 1 << 20,
			refused: "README.md: line 2: a paragraph holds more than 5000 runs of * and _, the limit" + tail, bound: time.Second},
		{name: "runs then unmatched runs inside words", body: strings.Repeat("a* ", 2500) + "\n" + strings.Repeat("a*b c\n", 2400), limit: 1 << 20, bound: time.Second},
		// Link reference definitions times the paragraph's lines.
		{name: "3162 definitions", body: defs(3162)},
		// 3125 definitions in 3200 lines multiply to the limit exactly, and
		// read: the planted break >= refuses them.
		{name: "3125 definitions in 3200 lines", body: strings.Repeat("[x]: y\n", 3125) + strings.Repeat("a\n", 75)},
		{name: "3163 definitions", body: "x\n\n" + defs(3163), refused: "README.md: line 3: a paragraph of 3163 lines may hold 3163 link reference definitions, and the two multiplied are over the limit of 10000000; put blank lines between the definitions"},
		{name: "2500 definitions in 4000 lines", body: strings.Repeat("[x]: y\n", 2500) + strings.Repeat("a\n", 1500), large: true},
		{name: "4000 definitions after a line of text", body: "a\n" + defs(4000), large: true},
		{name: "3163 definitions after a line of text", body: "a\n" + defs(3163)},
		{name: "3163 definitions indented three spaces", body: strings.ReplaceAll(defs(3163), "[x]", "   [x]"),
			refused: "README.md: line 1: a paragraph of 3163 lines may hold 3163 link reference definitions, and the two multiplied are over the limit of 10000000; put blank lines between the definitions"},
		{name: "100000 definitions", body: defs(100_000), refused: "README.md: line 1: a paragraph of 100000 lines may hold 100000 link reference definitions, and the two multiplied are over the limit of 10000000; put blank lines between the definitions", large: true,
			quarter: defs(25_000), quarterRefused: "README.md: line 1: a paragraph of 25000 lines may hold 25000 link reference definitions"},
		// Nodes: max_bytes/4 of them, counted as the parse makes them: two for
		// each line, one for the paragraph and three more.
		{name: "lines at the node limit", body: strings.Repeat("a\n", 123), limit: 1000},
		{name: "a line more", body: strings.Repeat("a\n", 124), limit: 1000,
			refused: "README.md: the Markdown parse makes more than 250 nodes, the limit, one for every 4 bytes of max_bytes; set max_bytes: higher to read a file this dense"},
		{name: "links over the node limit", body: strings.Repeat("[a", 500), limit: 1000, refused: "README.md: the Markdown parse makes more than 250 nodes, the limit, one for every 4 bytes of max_bytes; set max_bytes: higher to read a file this dense"},
		// Blocks that go on from line to line: goldmark records each one on
		// each line until a blank line, outside the node count. 32 MiB of
		// lines of 100 block quotes took 3.3 GB before.
		{name: "continuations at the limit", body: quoteLines(10, 101), limit: 4000},
		{name: "a continuation more", body: quoteLines(10, 102), limit: 4000,
			refused: "README.md: line 102: the Markdown parse goes on with more than 1000 open blocks from line to line, the limit, one for every 4 bytes of max_bytes; set max_bytes: higher to read a file this nested"},
		{name: "lines of 100 block quotes", body: quoteLines(100, 1010), limit: 400_000,
			refused: "README.md: line 1002: the Markdown parse goes on with more than 100000 open blocks from line to line, the limit", large: true},
		// Pieces of the prose: a run of blanks rewritten as one space starts
		// two, so text that alternates a letter and a tab is a piece a byte.
		// 32 MiB of it took 3.3 GB before.
		// Tokens of the HTML parse, which reads the HTML blocks a blank line
		// ends: max_bytes/4 and one more, for every block of the file
		// together (#86).
		{name: "HTML tokens at the limit", body: "<div>" + strings.Repeat("<b>a", 50) + "\n", limit: 400},
		{name: "an HTML token more", body: "<div>" + strings.Repeat("<b>a", 50) + "\n\n<div>\n", limit: 400,
			refused: "README.md: line 3: the HTML holds more than 101 tokens, the limit, one for every 4 bytes of max_bytes; set max_bytes: higher to read a file this broken up"},
		{name: "pieces at the limit", body: strings.Repeat("a\t", 250), limit: 2000},
		{name: "a piece more", body: strings.Repeat("a\t", 251), limit: 2000,
			refused: "README.md: line 1: the prose is made of more than 500 pieces of the file, the limit, one for every 4 bytes of max_bytes; set max_bytes: higher to read a file this broken up"},
		{name: "double spaces", body: strings.Repeat("a  ", 600), limit: 2000,
			refused: "README.md: line 1: the prose is made of more than 500 pieces of the file, the limit"},
		{name: "lines over the node limit", body: strings.Repeat("a\n", 500), limit: 1000, refused: "README.md: the Markdown parse makes more than 250 nodes, the limit, one for every 4 bytes of max_bytes; set max_bytes: higher to read a file this dense"},
		// A line of HTML: goldmark's regular expressions run over it whole.
		// The line at the limit is large, and no smaller one reaches it; the
		// two over it are refused quickly, and stay.
		{name: "a line in an HTML block at the limit", body: "<pre>\n" + strings.Repeat("b", MaxHTMLLine-1) + "\n</pre>\n\na\n", large: true},
		{name: "a line of HTML over the limit", body: "a\n\n<x" + strings.Repeat(" a=b", MaxHTMLLine/4) + "\n", refused: "README.md: line 3: a line of HTML of 1048579 bytes, over the limit of 1048576" + tail},
		{name: "a line in an HTML block over the limit", body: "<pre>\n" + strings.Repeat("a", MaxHTMLLine+1) + "\n", refused: "README.md: line 2: a line of HTML of 1048578 bytes, over the limit of 1048576" + tail},
		// Time: a comment opened on every line, which goldmark scans to the
		// end of the paragraph for each: quadratic, 54 s for 256 KB. The
		// guard looks at the clock on every call into a parser goldmark
		// makes, so the parse ends soon after its limit.
		{name: "unclosed comments", body: strings.Repeat("a <!-- b\n", 50_000), parse: 100 * time.Millisecond,
			refused: "README.md: parsing the Markdown took more than 100ms, the limit" + tail, bound: time.Second},
		{name: "unclosed link destinations", body: strings.Repeat("[a](<b ", 50_000), parse: 100 * time.Millisecond,
			refused: "README.md: parsing the Markdown took more than 100ms, the limit" + tail, bound: time.Second},
		// One tag whose attributes run over a line of 16 MiB. goldmark v1
		// matched it with a regular expression, which took 14 s, and errata
		// read it through a reader that looked at the guard. goldmark v2
		// scans the attributes by hand in one pass, about 0.07 s for 4 MiB on
		// the build node: the guard looks before and after that call, and
		// the parse ends soon after it. Each tag's time limit is well under
		// its scan, so the parse always runs past it.
		{name: "a tag of four million attributes", body: "z <x" + strings.Repeat(" a=b", 4<<20), parse: 10 * time.Millisecond,
			refused: "README.md: parsing the Markdown took more than 10ms, the limit" + tail, large: true,
			quarter: "z <x" + strings.Repeat(" a=b", 1<<20), quarterRefused: "README.md: parsing the Markdown took more than 10ms, the limit"},
		{name: "a tag of a million attributes", body: "z <x" + strings.Repeat(" a=b", 1<<20), parse: 10 * time.Millisecond,
			refused: "README.md: parsing the Markdown took more than 10ms, the limit" + tail, bound: time.Second},
	} {
		if c.large != large {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			limit, parse := core.MaxBytes(c.limit), Time(core.MaxBytes(c.limit))
			if c.parse > 0 {
				parse = c.parse
			}
			// read parses body as README.md and builds its prose, runs times,
			// and returns the fastest run, which wants refused; without a
			// refusal the prose holds an a.
			read := func(body, refused string, runs int) time.Duration {
				fastest := time.Duration(math.MaxInt64)
				for range runs {
					start := time.Now()
					err := readProse(body, limit, parse)
					fastest = min(fastest, time.Since(start))
					switch {
					case refused != "":
						if err == nil || !strings.HasPrefix(err.Error(), refused) {
							t.Errorf("error %v, want %q", err, refused)
						}
					case err != nil:
						t.Errorf("error %v, want prose", err)
					}
				}
				return fastest
			}
			if c.quarter == "" {
				if took := read(c.body, c.refused, 1); c.bound > 0 && took > c.bound*extracttest.RaceSlowdown {
					t.Errorf("took %v, over %v", took, c.bound*extracttest.RaceSlowdown)
				}
				return
			}
			quarter := read(c.quarter, c.quarterRefused, 2)
			whole := read(c.body, c.refused, 2)
			t.Logf("the input took %v, a quarter of it %v", whole, quarter)
			if limit := 8*quarter + 100*time.Millisecond; whole > limit {
				t.Errorf("the input took %v, over %v, eight times the %v of a quarter of it and a tenth of a second", whole, limit, quarter)
			}
		})
	}
}

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; the slow target of the Makefile runs it from
// SLOW_TESTS, which lists the tests that call it (scripts/slowtests).
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}

// Two survivors of the mutation of mdparse are equivalent, so no test
// kills them: a block opened one level deeper at open time (>= read as >
// on the depth check in guardedBlock.Open) is refused after the parse by
// checkDepth, with the same message and line, and the 100000 block quotes
// still hold the open-time check to its second; and FirstOffset's loop not
// run falls back to NodeStart, which gives the same line for every block
// kind the prose refuses.

// readProse parses body as README.md under limit and parse, builds its
// prose, and refuses it as the text extractor does, and when it holds no a.
func readProse(body string, limit int64, parse time.Duration) error {
	doc, err := Parse("README.md", []byte(body), limit, parse)
	if err != nil {
		return err
	}
	p, err := doc.Prose(false)
	if err != nil {
		return fmt.Errorf("README.md: %w", err)
	}
	if !bytes.Contains(p.Text, []byte("a")) {
		return errors.New("README.md: no a in the prose")
	}
	return nil
}

// quoteLines is n lines of text in depth block quotes.
func quoteLines(depth, n int) string {
	return strings.Repeat(strings.Repeat(">", depth)+" a\n", n)
}
