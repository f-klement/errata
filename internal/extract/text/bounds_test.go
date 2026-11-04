package text

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/core/mdparse"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// Each refusal of the parse reaches the text extractor as the parse words
// it, one case for each bound (core/mdparse's TestMarkdownBounds holds the
// bounds themselves), and the search's own bounds, time and values. A refusal is
// asserted by its message, whole unless it holds a count of matches, and a
// shape that took long before its bound must now be refused within its time.
// A case on a large input that -race slows past half a second is large, and
// make slow runs it; a smaller input that reaches the same limit stays here.
func TestTextBounds(t *testing.T) {
	markdownBounds(t, false)
}

// The large cases of TestTextBounds. A large case's time is held to a
// ratio, not a wall clock, so that it holds on a loaded node: a quarter of
// its input, of the same shape, is read too, and the case may take at most
// eight times as long, with a tenth of a second for a pause in either run. A
// linear read takes about four times, and a quadratic one sixteen. Each is
// timed twice and the faster run kept, so a moment of load on one run does
// not count. Planted break: definitions walking every line before each, a
// quadratic walk, which failed the 100000 definitions.
func TestTextBoundsOnLargeInputs(t *testing.T) {
	slowOnly(t)
	markdownBounds(t, true)
}

// markdownBounds runs the cases whose large is large.
func markdownBounds(t *testing.T, large bool) {
	t.Helper()
	quotes := func(n int) string { return strings.Repeat("> ", n) + "a\n" }
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
		{name: "999 block quotes", body: quotes(999), refused: "README.md: line 1: nested more than 1000 levels deep, the limit" + tail},
		{name: "5001 runs", body: "x\n\n" + runs(5001), refused: "README.md: line 3: a paragraph holds more than 5000 runs of * and _, the limit" + tail},
		{name: "3163 definitions", body: "x\n\n" + defs(3163), refused: "README.md: line 3: a paragraph of 3163 lines may hold 3163 link reference definitions, and the two multiplied are over the limit of 10000000; put blank lines between the definitions"},
		{name: "a line more", body: strings.Repeat("a\n", 124), limit: 1000,
			refused: "README.md: the Markdown parse makes more than 250 nodes, the limit, one for every 4 bytes of max_bytes; set max_bytes: higher to read a file this dense"},
		{name: "a continuation more", body: quoteLines(10, 102), limit: 4000,
			refused: "README.md: line 102: the Markdown parse goes on with more than 1000 open blocks from line to line, the limit, one for every 4 bytes of max_bytes; set max_bytes: higher to read a file this nested"},
		{name: "a piece more", body: strings.Repeat("a\t", 251), limit: 2000,
			refused: "README.md: line 1: the prose is made of more than 500 pieces of the file, the limit, one for every 4 bytes of max_bytes; set max_bytes: higher to read a file this broken up"},
		{name: "a line of HTML over the limit", body: "a\n\n<x" + strings.Repeat(" a=b", mdparse.MaxHTMLLine/4) + "\n", refused: "README.md: line 3: a line of HTML of 1048579 bytes, over the limit of 1048576" + tail},
		// Time: a comment opened on every line, which goldmark scans to the
		// end of the paragraph for each: quadratic, 54 s for 256 KB. The
		// guard looks at the clock on every call into a parser goldmark
		// makes, so the parse ends soon after its limit.
		{name: "unclosed comments", body: strings.Repeat("a <!-- b\n", 50_000), parse: 100 * time.Millisecond,
			refused: "README.md: parsing the Markdown took more than 100ms, the limit" + tail, bound: time.Second},
		// The search reuses the regex extractor's, its time limit and its
		// cap on values with it; TestExtractValueLimit reaches the cap in
		// make test.
		{name: "a search that scans the line after each match", body: strings.Repeat("1 ", 10000),
			refused: "README.md: matching took more than 100ms, the limit, and stopped after", bound: 1500 * time.Millisecond},
		// The quarter reads its 25001 values, as the large input reads its
		// first 100000.
		{name: "100001 values", body: strings.Repeat("a ", 100_001),
			refused: "README.md: the extractor selects more than 100000 values, the limit; narrow the path or the pattern so it selects fewer", large: true,
			quarter: strings.Repeat("a ", 25_001)},
	} {
		if c.large != large {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			pattern := `(a)`
			if strings.HasPrefix(c.name, "a search") {
				pattern = `(\d)(?:.*x)?`
			}
			e, err := Build(Spec{File: "README.md", Pattern: pattern, MaxBytes: c.limit})
			if err != nil {
				t.Fatal(err)
			}
			x := e.(textExtractor)
			if c.parse > 0 {
				x.parseTime = c.parse
			}
			if strings.HasPrefix(c.name, "a search") {
				x.search.TimeLimit = 100 * time.Millisecond
			}
			// read extracts body as the case's README.md, runs times, and
			// returns the fastest run, which wants refused.
			read := func(body, refused string, runs int) time.Duration {
				root := extracttest.Repo(t, map[string]string{"README.md": body})
				fastest := time.Duration(math.MaxInt64)
				for range runs {
					start := time.Now()
					got, err := x.Extract(t.Context(), root)
					fastest = min(fastest, time.Since(start))
					switch {
					case refused != "":
						if err == nil || !strings.HasPrefix(err.Error(), refused) {
							t.Errorf("error %v, want %q", err, refused)
						}
					case err != nil:
						t.Errorf("error %v, want values", err)
					case len(got) == 0:
						t.Errorf("no values")
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

// quoteLines is n lines of text in depth block quotes.
func quoteLines(depth, n int) string {
	return strings.Repeat(strings.Repeat(">", depth)+" a\n", n)
}

// The prose may be at most maxProseRatio times its file; the planted break
// is one byte more.
func TestCheckProse(t *testing.T) {
	if err := checkProse("README.md", 200, 100, 2); err != nil {
		t.Errorf("at the limit: %v", err)
	}
	err := checkProse("README.md", 201, 100, 2)
	want := "README.md: the prose is 201 bytes, over the limit of 200, 2 times the file's size; a regex extractor can still read the file"
	if err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
}
