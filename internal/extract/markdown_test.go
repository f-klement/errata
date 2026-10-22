package extract

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// treeString writes the tree from n with every offset in it: each node's
// kind and position, a text node's segment and line break, and a block's
// lines. Two parses that agree on it agree on every offset errata reads.
func treeString(n ast.Node, src []byte) string {
	var b strings.Builder
	var walk func(n ast.Node, depth int)
	walk = func(n ast.Node, depth int) {
		fmt.Fprintf(&b, "%*s%s@%d", depth, "", n.Kind(), n.Pos())
		switch t := n.(type) {
		case *ast.Text:
			fmt.Fprintf(&b, " %d:%d soft=%v hard=%v raw=%v", t.Segment.Start, t.Segment.Stop, t.SoftLineBreak(), t.HardLineBreak(), t.IsRaw())
		case *ast.AutoLink:
			fmt.Fprintf(&b, " %q", t.Label(src))
		case *ast.HTMLBlock:
			fmt.Fprintf(&b, " closure %d:%d", t.ClosureLine.Start, t.ClosureLine.Stop)
		}
		if n.Type() == ast.TypeBlock {
			for i := range n.Lines().Len() {
				s := n.Lines().At(i)
				fmt.Fprintf(&b, " %d:%d+%d", s.Start, s.Stop, s.Padding)
			}
		}
		b.WriteString("\n")
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			walk(c, depth+1)
		}
	}
	walk(n, 0)
	return b.String()
}

// markdownSamples are documents of every kind of node goldmark makes.
var markdownSamples = []string{
	withCode,
	"CI runs on node\n`builder` and *em* **strong** _u_ [link *text*](http://x) <http://auto.example> foo  \nbar\\\nbaz &amp; \\* x<br>y\n",
	"- item one\n  wraps\n- `code\nspan`\n\n> quote\nlazy\n\n1. a\n2. b\n\n   c\n",
	"# Head *x* #\nSetext\n===\n![alt *t*](img.png \"title\")\n- \tx\n\n\t\tcode\n\n***\n",
	"[ref]: https://example.com 'T'\n[a][ref], [ref][], [ref], [b](<c d> \"e\") <me@example.com>\n",
	"<!-- c -->\n<?p?>\n<!X>\n<![CDATA[x]]>\n<script>\nx\n</script>\n<div\n>\n\nend\n",
	"***a** b* __c _d_ e__ *f **g***\n\n`` a ` b `` ```\nunclosed\n",
	"> > > deep\n> > lazy\n>\n> - [x] item\n>   more\n",
}

// The guarded parser makes the same tree as goldmark's own, offsets and all:
// the wrappers only look at the guard. The planted break was a guardedBlock
// whose CanInterruptParagraph said false, which changed the samples' trees.
func TestGuardedParseIsGoldmarks(t *testing.T) {
	for _, s := range markdownSamples {
		doc, err := parseMarkdown("f", []byte(s), defaultMaxBytes, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		// doc.src is the file as both parses read it, its front matter blanked.
		want := treeString(goldmark.DefaultParser().Parse(text.NewReader(doc.src)), doc.src)
		if got := treeString(doc.root, doc.src); got != want {
			t.Errorf("%q:\nguarded\n%s\ngoldmark\n%s", s, got, want)
		}
	}
}

// Each bound of the parse, with the planted break that crosses it and, where
// the bound is a size, the input at the bound that reads. A refusal is
// asserted by its message, whole unless it holds a count of matches, and a
// shape that took long before its bound must now be refused within its time.
// A case on a large input that -race slows past half a second is large, and
// make slow runs it; a smaller input that reaches the same limit stays here.
func TestMarkdownBounds(t *testing.T) {
	markdownBounds(t, false)
}

// The large cases of TestMarkdownBounds.
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
		// Runs that close nothing, which goldmark matched against every run
		// before them: 256 KB took over a minute.
		{name: "100000 runs that match nothing", body: strings.Repeat("*a_ ", 100_000), refused: "README.md: line 1: a paragraph holds more than 5000 runs of * and _, the limit" + tail, bound: time.Second},
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
		{name: "100000 definitions", body: defs(100_000), refused: "README.md: line 1: a paragraph of 100000 lines may hold 100000 link reference definitions, and the two multiplied are over the limit of 10000000; put blank lines between the definitions", bound: time.Second, large: true},
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
		{name: "pieces at the limit", body: strings.Repeat("a\t", 250), limit: 2000},
		{name: "a piece more", body: strings.Repeat("a\t", 251), limit: 2000,
			refused: "README.md: line 1: the prose is made of more than 500 pieces of the file, the limit, one for every 4 bytes of max_bytes; set max_bytes: higher to read a file this broken up"},
		{name: "double spaces", body: strings.Repeat("a  ", 600), limit: 2000,
			refused: "README.md: line 1: the prose is made of more than 500 pieces of the file, the limit"},
		{name: "lines over the node limit", body: strings.Repeat("a\n", 500), limit: 1000, refused: "README.md: the Markdown parse makes more than 250 nodes, the limit, one for every 4 bytes of max_bytes; set max_bytes: higher to read a file this dense"},
		// A line of HTML: goldmark's regular expressions run over it whole.
		// The line at the limit is large, and no smaller one reaches it; the
		// two over it are refused quickly, and stay.
		{name: "a line in an HTML block at the limit", body: "<pre>\n" + strings.Repeat("b", maxHTMLLine-1) + "\n</pre>\n\na\n", large: true},
		{name: "a line of HTML over the limit", body: "a\n\n<x" + strings.Repeat(" a=b", maxHTMLLine/4) + "\n", refused: "README.md: line 3: a line of HTML of 1048579 bytes, over the limit of 1048576" + tail},
		{name: "a line in an HTML block over the limit", body: "<pre>\n" + strings.Repeat("a", maxHTMLLine+1) + "\n", refused: "README.md: line 2: a line of HTML of 1048578 bytes, over the limit of 1048576" + tail},
		// Time: a comment opened on every line, which goldmark scans to the
		// end of the paragraph for each: quadratic, 54 s for 256 KB. The
		// guard looks at the clock on every call into a parser goldmark
		// makes, so the parse ends soon after its limit.
		{name: "unclosed comments", body: strings.Repeat("a <!-- b\n", 50_000), parse: 100 * time.Millisecond,
			refused: "README.md: parsing the Markdown took more than 100ms, the limit" + tail, bound: time.Second},
		{name: "unclosed link destinations", body: strings.Repeat("[a](<b ", 50_000), parse: 100 * time.Millisecond,
			refused: "README.md: parsing the Markdown took more than 100ms, the limit" + tail, bound: time.Second},
		// One tag whose attributes run over a line of 16 MiB: one match of a
		// regular expression, which reads through the guard. The planted
		// break, the parser given the block's own reader, took 14 s; at a
		// million attributes, 4 MiB, 1.3 s, and 26 s under -race.
		{name: "a tag of four million attributes", body: "z <x" + strings.Repeat(" a=b", 4<<20), parse: 100 * time.Millisecond,
			refused: "README.md: parsing the Markdown took more than 100ms, the limit" + tail, bound: time.Second, large: true},
		{name: "a tag of a million attributes", body: "z <x" + strings.Repeat(" a=b", 1<<20), parse: 100 * time.Millisecond,
			refused: "README.md: parsing the Markdown took more than 100ms, the limit" + tail, bound: time.Second},
		// The search reuses the regex extractor's, its time limit and its
		// cap on values with it; TestExtractValueLimit reaches the cap in
		// make test.
		{name: "a search that scans the line after each match", body: strings.Repeat("1 ", 10000),
			refused: "README.md: matching took more than 100ms, the limit, and stopped after", bound: 1500 * time.Millisecond},
		{name: "100001 values", body: strings.Repeat("a ", 100_001),
			refused: "README.md: the extractor selects more than 100000 values, the limit; narrow the path or the pattern so it selects fewer", bound: 3 * time.Second, large: true},
	} {
		if c.large != large {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			pattern := `(a)`
			if strings.HasPrefix(c.name, "a search") {
				pattern = `(\d)(?:.*x)?`
			}
			e, err := Build(Spec{Text: &Text{File: "README.md", Pattern: pattern, MaxBytes: c.limit}})
			if err != nil {
				t.Fatal(err)
			}
			x := e.(textExtractor)
			if c.parse > 0 {
				x.parseTime = c.parse
			}
			if strings.HasPrefix(c.name, "a search") {
				x.search.timeLimit = 100 * time.Millisecond
			}
			root := repo(t, map[string]string{"README.md": c.body})
			start := time.Now()
			got, err := x.Extract(root)
			took := time.Since(start)
			if c.bound > 0 && took > c.bound*raceSlowdown {
				t.Errorf("took %v, over %v", took, c.bound*raceSlowdown)
			}
			switch {
			case c.refused != "":
				if err == nil || !strings.HasPrefix(err.Error(), c.refused) {
					t.Errorf("error %v, want %q", err, c.refused)
				}
			case err != nil:
				t.Errorf("error %v, want values", err)
			case len(got) == 0:
				t.Errorf("no values")
			}
		})
	}
}

// quoteLines is n lines of text in depth block quotes.
func quoteLines(depth, n int) string {
	return strings.Repeat(strings.Repeat(">", depth)+" a\n", n)
}

// A block kind the prose does not know is refused, never read as no text:
// goldmark's table extension makes one. The planted break is the walk into
// its children, which read the table as nothing.
func TestProseRefusesABlockItDoesNotKnow(t *testing.T) {
	src := []byte("x\n\n| a | b |\n|---|---|\n| CI runs on node | builder |\n\nafter\n")
	md := goldmark.New(goldmark.WithExtensions(extension.Table))
	doc := &markdownDoc{src: src, root: md.Parser().Parse(text.NewReader(src)), maxPieces: 1000}
	_, err := doc.prose(false)
	want := "line 3: a block of kind Table, which the text extractor does not read"
	if err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
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

// An offset of the prose maps to the offset of the file it came from, a
// written byte to the offset it stands for, and the end of the text to the
// end of the last piece.
func TestProseOffset(t *testing.T) {
	src := []byte("ab\ncd ef")
	p := prose{maxPieces: 3}
	p.copyFrom(src[0:2], 0)
	p.write(' ', 2)
	p.copyFrom(src[3:5], 3)
	p.copyFrom(src[5:8], 5) // continues the piece before
	if string(p.text) != "ab cd ef" || len(p.pieces) != 3 {
		t.Fatalf("text %q, %d pieces", p.text, len(p.pieces))
	}
	for at, want := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8} {
		if got := p.offset(at); got != want {
			t.Errorf("offset(%d) = %d, want %d", at, got, want)
		}
	}
	if got := (&prose{}).offset(0); got != 0 {
		t.Errorf("empty prose: offset %d", got)
	}
	// The zero value has no bound on its pieces and takes text; the planted
	// break, a zero bound read as full, took nothing.
	var z prose
	z.copyFrom(src[0:2], 0)
	z.write(' ', 2)
	if string(z.text) != "ab " || z.offset(2) != 2 {
		t.Errorf("zero prose: text %q", z.text)
	}
}
