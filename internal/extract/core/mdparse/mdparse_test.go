package mdparse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/extension"
	east "github.com/yuin/goldmark/v2/extension/ast"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/renderer/html"
	"github.com/yuin/goldmark/v2/text"

	"github.com/f-klement/errata/internal/extract/core"

	"pgregory.net/rapid"
)

// treeString writes the tree from n with every offset in it: each node's
// kind and position, a text node's index and line break, a code span's and
// an inline HTML's indices, an autolink's label, and a block's lines. Two
// parses that agree on it agree on every offset errata reads.
func treeString(n ast.Node, src []byte) string {
	var b strings.Builder
	segments := func(segs []text.Segment) {
		for _, s := range segs {
			fmt.Fprintf(&b, " %d:%d+%d", s.Start, s.Stop, s.Padding)
		}
	}
	var walk func(n ast.Node, depth int)
	walk = func(n ast.Node, depth int) {
		fmt.Fprintf(&b, "%*s%s@%d", depth, "", n.Kind(), n.Pos())
		switch t := n.(type) {
		case *ast.Text:
			fmt.Fprintf(&b, " %d:%d soft=%v hard=%v", t.Value.Index().Start, t.Value.Index().Stop, t.SoftLineBreak(), t.HardLineBreak())
		case *ast.CodeSpan:
			fmt.Fprintf(&b, " %v %q", t.Value.Indices(), t.Value.Bytes(src))
		case *ast.RawHTML:
			fmt.Fprintf(&b, " %v", t.Value.Indices())
		case *ast.AutoLink:
			fmt.Fprintf(&b, " %v", t.Label.Index())
		case *ast.CodeBlock:
			segments(t.Value.Segments())
		case *ast.HTMLBlock:
			fmt.Fprintf(&b, " %s", t.HTMLBlockKind)
			segments(t.Value.Segments())
		}
		if bn, ok := n.(ast.BlockNode); ok {
			segments(bn.Source())
		}
		b.WriteString("\n")
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			walk(c, depth+1)
		}
	}
	walk(n, 0)
	return b.String()
}

// withCode is a document of every kind of block, front matter too.
const withCode = `---
title: CI runs on node frontmatter
---
# Build

CI runs on node builder.

` + "```sh" + `
# CI runs on node fenced
` + "```" + `

    CI runs on node indented

<div>
CI runs on node html
</div>

<!--
CI runs on node comment
CI runs on node closing -->

~~~
CI runs on node tilde
~~~
`

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

// goldmarkRuns is goldmark's CommonMark parser reading runs of * and _ as
// errata does, through parseRun: the parse errata's guarded one must equal.
var goldmarkRuns = parser.New(parser.WithParseDelimiterFunc(parseRun))

// goldmarkTables is goldmarkRuns with the table transformer ParseTables
// reads tables with.
var goldmarkTables = parser.New(parser.WithParseDelimiterFunc(parseRun), tableTransformers)

// The guarded parser makes the same tree as goldmark's own with parseRun,
// offsets and all: the wrappers only look at the guard. The planted break
// was a guardedBlock whose CanInterruptParagraph said false, which changed
// the samples' trees.
func TestGuardedParseIsGoldmarks(t *testing.T) {
	for _, s := range markdownSamples {
		doc, err := Parse("f", []byte(s), core.DefaultMaxBytes, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		// doc.Src is the file as both parses read it, its front matter blanked.
		want := treeString(goldmarkRuns.Parse(doc.Src), doc.Src)
		if got := treeString(doc.Root, doc.Src); got != want {
			t.Errorf("%q:\nguarded\n%s\ngoldmark\n%s", s, got, want)
		}
	}
}

// A file whose last line has no newline reads as CommonMark reads it, the
// end of the file ending the line: "<div" there is an HTML block, and so is
// "<div" ended by a lone CR. goldmark v2.1.9's scan for an HTML block opening
// indexed past a last line such as "<a" that has no newline, which crashed
// errata (testdata/fuzz/FuzzText/html-open-at-end-of-file), and errata gave
// such a file a newline until v2.1.11 fixed the scan. goldmark v2.1.8 does
// not crash but reads "<div" with a newline as a paragraph, so this test
// holds each reading: a goldmark that crashes again, or reads one of these
// otherwise, fails it.
func TestLastLineWithoutNewlineReads(t *testing.T) {
	for _, c := range []struct {
		src   string
		code  bool
		block string // the line of the tree for the file's last block
		prose string
	}{
		{"<a", false, "Paragraph@0", "<a"},
		{"<div", false, "HTMLBlock@0 Kind6", ""},
		{"<div\r", false, "HTMLBlock@0 Kind6", ""},
		{"</p", false, "HTMLBlock@0 Kind6", ""},
		{"   <div", false, "HTMLBlock@3 Kind6", ""},
		{"> <div", false, "HTMLBlock@2 Kind6", ""},
		{"- <p", false, "HTMLBlock@2 Kind6", ""},
		{"x\n\n<div", false, "HTMLBlock@3 Kind6", "x"},
		{"<div\n\n<a", false, "Paragraph@6", "<a"},
		{"    code", true, "CodeBlock@4", "code"},
	} {
		doc, err := Parse("f", []byte(c.src), core.DefaultMaxBytes, time.Minute)
		if err != nil {
			t.Fatalf("%q: %v", c.src, err)
		}
		tree := treeString(doc.Root, doc.Src)
		if !strings.Contains(tree, " "+c.block+" ") {
			t.Errorf("%q: no %s in\n%s", c.src, c.block, tree)
		}
		p, err := doc.Prose(c.code)
		if err != nil {
			t.Fatal(err)
		}
		if string(p.Text) != c.prose {
			t.Errorf("%q: prose %q, want %q", c.src, p.Text, c.prose)
		}
	}
}

// An empty file, or one that holds only a BOM, has no last line, and no
// byte of it is read before the first.
func TestAnEmptyFileReadsAsEmpty(t *testing.T) {
	for _, src := range []string{"", "\xef\xbb\xbf"} {
		doc, err := Parse("f", []byte(src), core.DefaultMaxBytes, time.Minute)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if len(doc.Src) != 0 {
			t.Errorf("%q: src %q, want it empty", src, doc.Src)
		}
		p, err := doc.Prose(false)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Text) != 0 {
			t.Errorf("%q: prose %q, want none", src, p.Text)
		}
	}
}

// renderHTML is goldmark's HTML of the tree root of src, rendered as
// goldmark's own test of the CommonMark spec renders it.
func renderHTML(t *testing.T, src []byte, root ast.Node) string {
	t.Helper()
	var b bytes.Buffer
	if err := html.New(html.WithXHTML(), html.WithUnsafe()).Render(&b, src, root); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// Every example of the CommonMark spec renders the same from errata's parse
// as from goldmark's own, which goldmark tests against the spec. The
// examples are the copy in goldmark's module, testdata/spec.json. This holds
// the parsers errata wraps to goldmark's CommonMark, and parseRun's reading
// of runs of * and _ to the spec's: its 132 examples of emphasis among them.
// The planted breaks: the HTML block parser left out, and parseRun reading
// _ by the rules of *.
func TestParseReadsTheSpecAsGoldmark(t *testing.T) {
	dir, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}", "github.com/yuin/goldmark/v2").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(dir)), "testdata", "spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	var examples []struct {
		Markdown string `json:"markdown"`
		Example  int    `json:"example"`
	}
	if err := json.Unmarshal(b, &examples); err != nil {
		t.Fatal(err)
	}
	if len(examples) < 600 {
		t.Fatalf("%d examples in spec.json", len(examples))
	}
	for _, e := range examples {
		doc, err := Parse("f", []byte(e.Markdown), core.DefaultMaxBytes, time.Minute)
		if err != nil {
			t.Errorf("example %d: %v", e.Example, err)
			continue
		}
		got := renderHTML(t, doc.Src, doc.Root)
		if want := renderHTML(t, doc.Src, parser.New().Parse(doc.Src)); got != want {
			t.Errorf("example %d, %q:\nerrata\n%s\ngoldmark\n%s", e.Example, e.Markdown, got, want)
		}
	}
}

// goldmark matches runs of forty * in twenty steps, each a node, and the
// guard counts each match, so the tree holds no more nodes than it
// counted. The planted break, a match not counted, counted 12 for a tree
// of 23.
func TestGuardCountsEveryMatch(t *testing.T) {
	src := strings.Repeat("*", 40) + "a" + strings.Repeat("*", 40) + "\n"
	doc, err := Parse("f", []byte(src), core.DefaultMaxBytes, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n := countTree(doc.Root); n != 23 || int64(n) > doc.nodes {
		t.Errorf("the tree holds %d nodes and the guard counted %d", n, doc.nodes)
	}
}

// parseRun reads a run's neighbours by CommonMark's classes: a form feed
// and a space separator are whitespace and stop a run after them from
// closing, a vertical tab and a line separator are neither, and a symbol is
// punctuation. goldmark's own reading differs for the form feed and the
// line separator. The planted break, unicode.IsSpace for whitespace, reads
// the vertical tab and the line separator as spaces.
func TestCanOpenCloseByCommonMarksClasses(t *testing.T) {
	for _, c := range []struct {
		before            rune
		canOpen, canClose bool
	}{
		{'a', true, true}, {' ', true, false}, {'\f', true, false}, {'\u00a0', true, false},
		{'\v', true, true}, {'\u2028', true, true}, {'+', true, false}, {'.', true, false},
	} {
		if o, cl := canOpenClose(c.before, 'b', '*'); o != c.canOpen || cl != c.canClose {
			t.Errorf("a * after %q before b: open %v close %v, want %v %v", c.before, o, cl, c.canOpen, c.canClose)
		}
	}
}

// A code span's prose is its text less the space CommonMark strips from
// each end when both ends have one, so a space after the span maps to the
// file's space after it. A span with a space at one end only keeps it. The
// planted breaks: never stripping (the space maps to offset 5, in the span),
// stripping always (the second span loses its text), and either end moved
// the other way (a backtick in the prose).
func TestProseOfACodeSpan(t *testing.T) {
	for _, c := range []struct {
		src, text string
		at, from  int
	}{
		{"a ` b ` c", "a b c", 3, 7},
		{"x` a`y", "x ay", 1, 2},
		{"` a\nb ` c", "a b c", 3, 7},
	} {
		doc, err := Parse("f", []byte(c.src), core.DefaultMaxBytes, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		p, err := doc.Prose(false)
		if err != nil {
			t.Fatal(err)
		}
		if string(p.Text) != c.text || p.Offset(c.at) != c.from {
			t.Errorf("%q: prose %q, offset(%d) %d, want %q and %d", c.src, p.Text, c.at, p.Offset(c.at), c.text, c.from)
		}
	}
}

// Each byte of a code span's prose maps where goldmark's own strip puts it,
// in every goldmark: v2.1.15 stopped stripping the bytes Value.Bytes gives,
// and TestProseOfACodeSpan failed on it. The cases are the strip's edges: a
// span all blanks keeps its blanks (which the prose then folds), a span of
// one space keeps it, a space at one end only stays, two at each end lose
// one each, and a span whose text is a line end and a space is all blanks.
func TestProseOfACodeSpanAtTheStripsEdges(t *testing.T) {
	for _, c := range []struct {
		src, text string
		at        []int
	}{
		{"a `  ` b", "a b", []int{0, 1, 7}},
		{"a ` ` b", "a b", []int{0, 1, 6}},
		{"a `x ` b", "a x b", []int{0, 1, 3, 4, 7}},
		{"a ` x` b", "a x b", []int{0, 1, 4, 6, 7}},
		{"`\na\n` b", "a b", []int{2, 5, 6}},
		{"` \n` b", "b", []int{5}},
		{"``  a  `` b", "a b", []int{4, 5, 10}},
	} {
		doc, err := Parse("f", []byte(c.src), core.DefaultMaxBytes, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		p, err := doc.Prose(false)
		if err != nil {
			t.Fatal(err)
		}
		at := make([]int, len(p.Text))
		for i := range p.Text {
			at[i] = p.Offset(i)
		}
		if string(p.Text) != c.text || !slices.Equal(at, c.at) {
			t.Errorf("%q: prose %q at %v, want %q at %v", c.src, p.Text, at, c.text, c.at)
		}
	}
}

// A block kind the prose does not know is refused, never read as no text:
// goldmark's table extension makes one. The planted break is the walk into
// its children, which read the table as nothing.
func TestProseRefusesABlockItDoesNotKnow(t *testing.T) {
	src := []byte("x\n\n| a | b |\n|---|---|\n| CI runs on node | builder |\n\nafter\n")
	md := parser.New(parser.WithExtensions(extension.NewTableParser()))
	doc := &Doc{Src: src, Root: md.Parse(src), maxPieces: 1000}
	_, err := doc.Prose(false)
	want := "line 3: a block of kind Table, which the text extractor does not read"
	if err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
}

// An offset of the prose maps to the offset of the file it came from, a
// written byte to the offset it stands for, and the end of the text to the
// end of the last piece.
func TestProseOffset(t *testing.T) {
	src := []byte("ab\ncd ef")
	p := Prose{maxPieces: 3}
	p.CopyFrom(src[0:2], 0)
	p.write(' ', 2)
	p.CopyFrom(src[3:5], 3)
	p.CopyFrom(src[5:8], 5) // continues the piece before
	if string(p.Text) != "ab cd ef" || len(p.pieces) != 3 {
		t.Fatalf("text %q, %d pieces", p.Text, len(p.pieces))
	}
	for at, want := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8} {
		if got := p.Offset(at); got != want {
			t.Errorf("offset(%d) = %d, want %d", at, got, want)
		}
	}
	if got := (&Prose{}).Offset(0); got != 0 {
		t.Errorf("empty prose: offset %d", got)
	}
	// The zero value has no bound on its pieces and takes text; the planted
	// break, a zero bound read as full, took nothing.
	var z Prose
	z.CopyFrom(src[0:2], 0)
	z.write(' ', 2)
	if string(z.Text) != "ab " || z.Offset(2) != 2 {
		t.Errorf("zero prose: text %q", z.Text)
	}
}

// asciiLower lowers A to Z, both ends included, and leaves the bytes beside
// them, @ [ ` and {, as they are. The planted breaks are A or Z left out of
// the range.
func TestASCIILowerTakesAToZ(t *testing.T) {
	if got, want := string(asciiLower([]byte("AZ@[az`{"))), "az@[az`{"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A < starts a tag before /, !, ? or an ASCII letter, a to z and A to Z
// both ends included, and before nothing else, nor at the end of the text,
// as CommonMark's raw HTML has it. The planted breaks are each end of
// either letter range left out, ? read as its opposite, and a < at the end
// read past.
func TestTagAtTakesTheBytesCommonMarkDoes(t *testing.T) {
	for _, c := range []struct {
		b    string
		want bool
	}{
		{"<a", true}, {"<z", true}, {"<A", true}, {"<Z", true}, {"</", true}, {"<!", true}, {"<?", true},
		{"<@", false}, {"<[", false}, {"<`", false}, {"<{", false}, {"<1", false}, {"< ", false}, {"<", false}, {"a", false},
	} {
		if got := tagAt([]byte(c.b), 0); got != c.want {
			t.Errorf("tagAt(%q): %v, want %v", c.b, got, c.want)
		}
	}
}

// breaksLine needs the whole name of a line tag, ended by a blank, a / or a
// >, in either case, and reads nothing past the text. The planted breaks
// are a name taken as ended at the end of the text, and a < or a name at
// the end read past.
func TestBreaksLineReadsAWholeTagName(t *testing.T) {
	for _, c := range []struct {
		b    string
		want bool
	}{
		{"<div>", true}, {"</DIV>", true}, {"<br/>", true}, {"<p class=x>", true},
		{"<div", false}, {"<", false}, {"</", false}, {"<h1-x>", false}, {"<span>", false},
	} {
		if got := breaksLine([]byte(c.b)); got != c.want {
			t.Errorf("breaksLine(%q): %v, want %v", c.b, got, c.want)
		}
	}
}

// tableSamples are tables of every shape goldmark's extension reads, and
// paragraphs it reads as none.
var tableSamples = []string{
	"| a | b |\n|---|:-:|\n| *x* | `y` |\n| 1 |\n| 1 | 2 | 3 |\n",
	"text\n| a |\n|---|\n| b |\n\nafter\n",
	"a | b\n--|--\nc | d\n> e | f\n",
	"- | a |\n  |---|\n  | b |\n",
	"| a |\n| b |\n",
	"| a | b |\n|---|\n",
	"[x]: y\n| a |\n|-|\n| [x] |\n",
}

// The guarded parse with tables renders as goldmark's with its table
// extension, whose first option is tableTransformers: the extension has
// the two options this package expects. Planted break: tableTransformers
// taken as the second option, which reads no table.
func TestTablesParseAsGoldmark(t *testing.T) {
	if n := len(extension.NewTableParser().ParserOptions(nil)); n != 2 {
		t.Fatalf("the table extension has %d options, want 2", n)
	}
	md := parser.New(parser.WithExtensions(extension.NewTableParser()))
	for _, s := range tableSamples {
		doc, err := ParseTables("f", []byte(s), core.DefaultMaxBytes, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		r := html.New(html.WithXHTML(), html.WithUnsafe(), html.WithExtensions(extension.NewTableHTMLRenderer()))
		var got, want bytes.Buffer
		if err := r.Render(&got, doc.Src, doc.Root); err != nil {
			t.Fatal(err)
		}
		if err := r.Render(&want, doc.Src, md.Parse(doc.Src)); err != nil {
			t.Fatal(err)
		}
		if got.String() != want.String() {
			t.Errorf("%q:\nerrata\n%s\ngoldmark\n%s", s, got.String(), want.String())
		}
		if n := countTree(doc.Root); int64(n) > doc.nodes {
			t.Errorf("%q: the tree holds %d nodes and the guard counted %d", s, n, doc.nodes)
		}
	}
}

// A table's cells are counted against the node limit before goldmark makes
// them: 60 full rows of 100 cells, 12 KB, are 18000 nodes with the cells'
// text, over the 16384 of a max_bytes of 64 KiB, though no inline parser
// makes a node for a cell's text. The planted break: tableNodes counting no
// cells, which reads the table.
func TestTableNodesAreCountedBeforeTheTable(t *testing.T) {
	row := "|" + strings.Repeat("b|", 100) + "\n"
	src := "|" + strings.Repeat("a|", 100) + "\n|" + strings.Repeat("-|", 100) + "\n" + strings.Repeat(row, 60)
	_, err := ParseTables("f", []byte(src), 64<<10, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "f: the Markdown parse makes more than 16384 nodes, the limit") {
		t.Errorf("error %v, want the node limit", err)
	}
	if _, err := Parse("f", []byte(src), 64<<10, time.Minute); err != nil {
		t.Errorf("without tables: %v", err)
	}
	// The bound counts a row for each line from the one before the delimiter
	// row, whichever line that row is, three lines of three columns here.
	lines := []text.Segment{text.NewSegment(0, 2), text.NewSegment(2, 6), text.NewSegment(6, 8)}
	if got := tableNodes(lines, []byte("a\n|-|\nb\n")); got != 3*(3*3+1)+3 {
		t.Errorf("tableNodes %d, want %d", got, 3*(3*3+1)+3)
	}
	if got := tableNodes(lines[:1], []byte("a\n")); got != 0 {
		t.Errorf("one line: tableNodes %d, want 0", got)
	}
}

// A table cell's prose drops the backslash of \| in text and in a code
// span, and keeps every other backslash; each byte maps back to its file.
// The planted break: the pipe dropped with its backslash.
func TestCellDropsPipeEscapes(t *testing.T) {
	src := "| a |\n|---|\n| x \\| `y\\|z` \\* |\n"
	doc, err := ParseTables("f", []byte(src), core.DefaultMaxBytes, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p := doc.NewProse()
	_ = ast.Walk(doc.Root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && n.Kind() == east.KindTableCell && n.Parent().Kind() == east.KindTableRow {
			p.Cell(doc.Src, n)
		}
		return ast.WalkContinue, nil
	})
	if string(p.Text) != "x | y|z \\*" || p.Offset(2) != 17 || p.Offset(5) != 22 {
		t.Errorf("prose %q, offsets %d %d", p.Text, p.Offset(2), p.Offset(5))
	}
}

// A table may pad its short rows with at most as many empty cells as it
// has bytes, so its cells grow with its bytes and not with rows times
// columns: n columns over n rows of one byte are refused, and a table at the
// bound reads. The work of the square shape is held to a ratio: twice its
// size may allocate at most three times as much, where padding it all, a
// quadratic, allocates four times. Planted break: no bound on the padding,
// which reads the squares.
func TestTablePaddingIsBoundedByTheTable(t *testing.T) {
	square := func(n int) string {
		return "|" + strings.Repeat("a|", n) + "\n|" + strings.Repeat("-|", n) + "\n" + strings.Repeat("b\n", n)
	}
	_, err := ParseTables("f", []byte(square(100)), core.DefaultMaxBytes, time.Minute)
	want := "f: line 1: a table pads its short rows with at least 9900 empty cells, more than its 603 bytes, the limit; " + core.OtherWay
	if err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
	// Four columns over rows of one byte pad three cells a row of two
	// bytes: 15 rows pad 45 cells in a table of 45 bytes, its last newline
	// left out, and a 16th pads 48 in 47.
	at := "a|b|c|d\n-|-|-|-\n" + strings.Repeat("b\n", 15)
	if _, err := ParseTables("f", []byte(at), core.DefaultMaxBytes, time.Minute); err != nil {
		t.Errorf("at the bound: %v", err)
	}
	if _, err := ParseTables("f", []byte(at+"b\n"), core.DefaultMaxBytes, time.Minute); err == nil {
		t.Errorf("a row over the bound reads")
	}
	// A header row of more cells than the delimiter row's columns makes no
	// table in goldmark, so its paragraph reads, however short its lines;
	// headerCells counts an escaped pipe as no split. A header of fewer
	// cells is padded, and its table is refused. Planted break: the header's cells not compared
	// (review of 55c0780, finding 3), which refuses the first two.
	for _, header := range []string{strings.Repeat("a|", 51) + "\n", strings.Repeat("a|", 50) + strings.Repeat("\\|", 5) + "b|\n"} {
		src := header + "|" + strings.Repeat("-|", 50) + "\n" + strings.Repeat("b\n", 50)
		doc, err := ParseTables("f", []byte(src), core.DefaultMaxBytes, time.Minute)
		if err != nil {
			t.Errorf("%q: %v", header, err)
		} else if strings.Contains(treeString(doc.Root, doc.Src), "Table") {
			t.Errorf("%q: goldmark made a table", header)
		}
	}
	_, err = ParseTables("f", []byte("a\n|"+strings.Repeat("-|", 50)+"\n"+strings.Repeat("b\n", 50)), core.DefaultMaxBytes, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "empty cells, more than its") {
		t.Errorf("a padded header: error %v, want the padding bound", err)
	}
	// A | left at the end once the outer ones are gone starts no cell, as
	// in goldmark's loop: |a|b\|c|| is two cells, and a header of n cells
	// written so still pads a table, which the bound refuses (delta review
	// 2 of f3c34af). Planted break: every unescaped pipe counted, which
	// skipped the bound for that table.
	for line, want := range map[string]int{"  |a|b\\|c||  \n": 2, "|a|a||": 2, "a|": 1, "|": 0, "\\|a": 1, "a||b": 3} {
		if got := headerCells([]byte(line)); got != want {
			t.Errorf("headerCells(%q) %d, want %d", line, got, want)
		}
	}
	trailing := "|" + strings.Repeat("a|", 100) + "|\n|" + strings.Repeat("-|", 100) + "\n" + strings.Repeat("b\n", 100)
	_, err = ParseTables("f", []byte(trailing), core.DefaultMaxBytes, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "empty cells, more than its") {
		t.Errorf("a header ending in ||: error %v, want the padding bound", err)
	}
	if got := headerCells([]byte("||\n")); got != 0 {
		t.Errorf("headerCells of || %d, want 0", got)
	}
	allocs := func(src string) uint64 {
		least := uint64(math.MaxUint64)
		for range 2 {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, _ = ParseTables("f", []byte(src), core.DefaultMaxBytes, time.Minute)
			runtime.ReadMemStats(&after)
			least = min(least, after.TotalAlloc-before.TotalAlloc)
		}
		return least
	}
	small, large := allocs(square(200)), allocs(square(400))
	t.Logf("allocated %d bytes for 200 columns, %d for 400", small, large)
	if large > 3*small {
		t.Errorf("twice the table allocated %d bytes, over three times the %d of the table", large, small)
	}
}

// tableDecision fails through fail unless tableAt and tablePadding read
// the paragraph of header, delimiter and rows as goldmark's own table
// extension does: a table where goldmark makes one and none where it does
// not; where it makes one, a header of the cells goldmark reads from the
// header row, not counting those it pads; a padding of at most the empty
// cells goldmark adds to the body rows; and the table's bytes from its
// header to the end of its last row, where goldmark puts them, blanks at
// the end of the paragraph left out. Lines that
// do not make one paragraph, as a setext heading does not, are passed over.
func tableDecision(fail func(string, ...any), header, delimiter string, rows ...string) {
	if len(rows) == 0 {
		rows = []string{"b"}
	}
	src := []byte(header + "\n" + delimiter + "\n" + strings.Join(rows, "\n") + "\n")
	para, ok := goldmarkRuns.Parse(src).FirstChild().(*ast.Paragraph)
	if !ok || para.NextSibling() != nil || len(para.Source()) != 2+len(rows) {
		return
	}
	lines := para.Source()
	at, _ := tableAt(lines, src)
	var table ast.Node
	_ = ast.Walk(goldmarkWithTables.Parse(src), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && n.Kind() == east.KindTable && table == nil {
			table = n
		}
		return ast.WalkContinue, nil
	})
	if (table != nil) != (at != 0) {
		fail("%q over %q: tableAt %d, goldmark made a table: %v", header, delimiter, at, table != nil)
		return
	}
	if table == nil {
		return
	}
	read := 0
	for c := table.FirstChild().FirstChild(); c != nil; c = c.NextSibling() {
		if len(c.(ast.BlockNode).Source()) > 0 {
			read++
		}
	}
	if got := headerCells(lines[at-1].Bytes(src)); got != read {
		fail("%q over %q: headerCells %d, goldmark read %d", header, delimiter, got, read)
	}
	added, last := 0, table.Pos()
	if body := table.FirstChild().NextSibling(); body != nil {
		for tr := body.FirstChild(); tr != nil; tr = tr.NextSibling() {
			last = tr.Pos()
			for c := tr.FirstChild(); c != nil; c = c.NextSibling() {
				if len(c.(ast.BlockNode).Source()) == 0 {
					added++
				}
			}
		}
	}
	// The last row runs to its line's end, less the blanks a paragraph's
	// last line drops.
	end := last + len(bytes.TrimRight(src[last:last+bytes.IndexByte(src[last:], '\n')], " \t"))
	padded, size, start := tablePadding(lines, src)
	if padded > added || start != table.Pos() || size != end-start {
		fail("%q over %q, rows %q: padding %d of goldmark's %d, table at %d of %d bytes, goldmark's at %d of %d",
			header, delimiter, rows, padded, added, start, size, table.Pos(), end-table.Pos())
	}
}

// goldmarkWithTables is goldmark's parser with its own table extension,
// which tableAt and headerCells must agree with.
var goldmarkWithTables = parser.New(parser.WithExtensions(extension.NewTableParser()))

// tableAt and headerCells decide as goldmark's table extension does, on the
// cases three reviews found, and on generated rows: headers of pipes,
// escapes, blanks, code spans and the delimiter row's bytes, over delimiter
// rows of one to six columns, most of them valid. The
// planted break: headerCells counting every unescaped pipe, the count
// before delta review 2 of f3c34af, which |a|a|| fails.
func TestTableDecisionIsGoldmarks(t *testing.T) {
	for _, c := range [][2]string{
		{"|a|a||", "|-|-|"}, {"a", "|-|-|-|"}, {"a|b|c", "-|-"}, {"| a \\| b |", "|-|"},
		{"|a|b\\|c||", "|-|-|"}, {"||", "|-|"}, {"a||b", "-|-|-"}, {"`a|b`|c", "-|-"}, {"   |a|", " |:-:|"},
	} {
		tableDecision(t.Errorf, c[0], c[1])
	}
	// Ten runs of rapid's 100 checks, with half the headers ending in |:
	// with the old count of headerCells and the cases above taken out,
	// one run of 100 checks found it in 8 of 30 runs and 1000 checks in 10
	// of 10 (delta review 3 of c35446e).
	for range 10 {
		rapid.Check(t, func(rt *rapid.T) {
			header := rapid.StringMatching("[|a\\\\ `:-]{0,12}").Draw(rt, "header")
			if rapid.Bool().Draw(rt, "header trail") {
				header += "|"
			}
			// Most delimiter rows are valid, so that most cases decide a table;
			// a cell of a, or an empty one, makes the row none.
			cols := rapid.SliceOfN(rapid.SampledFrom([]string{"-", ":-", "-:", ":-:", " -- ", "a", ""}), 1, 6).Draw(rt, "cols")
			delimiter := strings.Join(cols, "|")
			if rapid.Bool().Draw(rt, "lead") {
				delimiter = "|" + delimiter
			}
			if rapid.Bool().Draw(rt, "trail") {
				delimiter += "|"
			}
			rows := rapid.SliceOfN(rapid.StringMatching("[|ab\\\\ ]{1,8}"), 1, 4).Draw(rt, "rows")
			tableDecision(rt.Fatalf, header, delimiter, rows...)
		})
	}
}

// The edges the mutation of c35446e found unguarded, each named by the
// mutant class it kills. Time is the 30 s limit up to the default size and
// in proportion above it (CONDITIONALS_BOUNDARY and ARITHMETIC_BASE in
// Time). A paragraph line longer than an HTML line may be reads, since only
// the HTML block parser checks it (CONDITIONALS_NEGATION on html).
func TestTimeAndTheHTMLLineCheck(t *testing.T) {
	for limit, want := range map[int64]time.Duration{
		1: 30 * time.Second, core.DefaultMaxBytes: 30 * time.Second,
		2 * core.DefaultMaxBytes: time.Minute, core.DefaultMaxBytes + core.DefaultMaxBytes/2: 45 * time.Second,
	} {
		if got := Time(limit); got != want {
			t.Errorf("Time(%d) %v, want %v", limit, got, want)
		}
	}
	if _, err := Parse("f", []byte(strings.Repeat("a", MaxHTMLLine+1)+"\n"), core.DefaultMaxBytes, time.Minute); err != nil {
		t.Errorf("a long paragraph line: %v", err)
	}
}

// tablePadding counts exactly the cells each body row lacks, the header
// and the delimiter row not counted, the table's bytes from its header to
// the end of its last row, and the header's offset (ARITHMETIC_BASE and
// INVERT_NEGATIVES in tablePadding). headerCells reads an empty or blank
// line as no cell (CONDITIONALS_BOUNDARY on its len checks), and
// delimiterColumns takes three spaces of indent and not four, and a blank
// line as none (CONDITIONALS_BOUNDARY on the indent and on the blank cells).
func TestTablePaddingCountsExactly(t *testing.T) {
	src := []byte("z\nx|y\n|-|-|-|-|\nb\nb|b|b|b\n")
	lines := []text.Segment{text.NewSegment(0, 2), text.NewSegment(2, 6), text.NewSegment(6, 16), text.NewSegment(16, 18), text.NewSegment(18, 26)}
	if padded, size, at := tablePadding(lines, src); padded != 3 || size != 24 || at != 2 {
		t.Errorf("tablePadding %d %d %d, want 3 24 2", padded, size, at)
	}
	for line, want := range map[string]int{"": 0, "   ": 0} {
		if got := headerCells([]byte(line)); got != want {
			t.Errorf("headerCells(%q) %d, want %d", line, got, want)
		}
	}
	for line, want := range map[string]int{"   |-|": 1, "    |-|": 0, " ": 0, "|": 0, "-": 0, "|-|:-:|": 2, "|a|": 0} {
		if got := delimiterColumns([]byte(line)); got != want {
			t.Errorf("delimiterColumns(%q) %d, want %d", line, got, want)
		}
	}
}

// A \| at the very start of a cell's text drops its backslash too, and the
// pipe maps to its own offset (CONDITIONALS_BOUNDARY on the search for \|,
// ARITHMETIC_BASE on the run before it).
func TestPipeEscapeAtTheStart(t *testing.T) {
	p := &Prose{dropPipeEscapes: true}
	p.copyText([]byte(`\|a\|b`), 10)
	if string(p.Text) != "|a|b" || p.Offset(0) != 11 || p.Offset(1) != 12 || p.Offset(2) != 14 {
		t.Errorf("text %q, offsets %d %d %d", p.Text, p.Offset(0), p.Offset(1), p.Offset(2))
	}
}

// The prose of an HTML block read by the HTML parse (#86), each line of it
// as "line:text", the line of the file its first byte maps to: text spaced
// as copyText spaces it, a line element ending a line, a pre's or a
// textarea's lines kept, nothing of a comment or a script, the markers of a
// block quote left out with every piece at its own line, and the tail of a
// pre after its end. The planted breaks: Text and Pre read alike, a pre's
// lines joined, a piece mapped from the first segment's offset.
func TestHTMLBlocksReadThroughTheHTMLParse(t *testing.T) {
	lines := func(src string) string {
		doc, err := Parse("f", []byte(src), core.DefaultMaxBytes, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		p, err := doc.Prose(false)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		at := 0
		for _, l := range strings.Split(string(p.Text), "\n") {
			out = append(out, fmt.Sprintf("%d:%s", LineOf(doc.Src, p.Offset(at)), l))
			at += len(l) + 1
		}
		return strings.Join(out, "|")
	}
	for src, want := range map[string]string{
		"<div>a  b<p>c\nd</p><!-- x --><script>y</script>e</div>\n": "1:a b|1:c d|2:e",
		"<pre>\n  one   two\nthree\n</pre> tail\n":                  "2:one two|3:three|4:tail",
		"<textarea>a\n\nb</textarea>\n":                             "1:a|3:b",
		"<pre>\na\n\nb\nc</pre>\n":                                  "2:a|4:b|5:c", // a blank line in a pre (review of 081070f, finding 6)
		"> <div>\n> a\n> b\n> c\n> d\n> </div>\n":                   "2:a b c d",
		"> <pre>\n> a\n> b\n> c\n> d e\n> </pre>\n":                 "2:a|3:b|4:c|5:d e",
	} {
		if got := lines(src); got != want {
			t.Errorf("%q: got %q, want %q", src, got, want)
		}
	}
}

// The tokens of every HTML block of a file together are bounded, and the
// refusal names the limit and the line of the token past it, in a block
// quote too. The planted break: the remaining budget named as the limit.
func TestHTMLTokensAreBoundedForTheFile(t *testing.T) {
	src := "<div>" + strings.Repeat("<b>a", 30) + "\n\n> <div>\n> <b>\n> <b>\n> <b>\n"
	doc, err := Parse("f", []byte(src), 256, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = doc.Prose(false)
	want := "line 5: the HTML holds more than 65 tokens, the limit, one for every 4 bytes of max_bytes; set max_bytes: higher to read a file this broken up"
	if err == nil || err.Error() != want {
		t.Errorf("got %v, want %q", err, want)
	}
	if _, err := doc.Prose(true); err != nil {
		t.Errorf("code: %v", err) // code: reads the lines, and no tokens
	}
}

// Every HTML block of a document is read by one scanner (htmlBlocks), so a
// file of many small blocks does not make a tokenizer, and its 4096 byte
// buffer, for each: 20000 blocks allocate under 1024 bytes a block for the
// prose, where a tokenizer each took over 4096 (review of 081070f, finding
// 5). The planted break: a scanner for each block.
func TestHTMLBlocksShareOneTokenizer(t *testing.T) {
	doc, err := Parse("f", []byte(strings.Repeat("<div>a\n\n", 20_000)), core.DefaultMaxBytes, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	p, err := doc.Prose(false)
	runtime.ReadMemStats(&after)
	if err != nil || !strings.HasPrefix(string(p.Text), "a\na\n") {
		t.Fatalf("prose %.20q, %v", p.Text, err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 20_000*1024 {
		t.Errorf("the prose of 20000 blocks allocated %d bytes, over 1024 a block", grew)
	}
}

// HTMLComments gives the comments the HTML parse found in a block it reads
// (a div, a pre), each with its <!-- offset and its text in the prose, an
// empty one too, and none in a script inside the block; for a block it
// does not read, a comment block, it reports false and reads nothing; a
// refusal of the parse is its refusal.
func TestHTMLComments(t *testing.T) {
	src := "<div><!--a--><script><!--s--></script><!---->x<!-- b\nc --></div>\n\n<!-- d -->\n"
	doc, err := Parse("f", []byte(src), core.DefaultMaxBytes, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p := doc.NewProse()
	var got []string
	for b := doc.Root.FirstChild(); b != nil; b = b.NextSibling() {
		ok, err := p.HTMLComments(doc.Src, b.(*ast.HTMLBlock), func(open, start, end int) {
			got = append(got, fmt.Sprintf("%d:%q", open, p.Text[start:end]))
		})
		got = append(got, fmt.Sprint(ok, err))
	}
	if want := `5:"a"|38:""|46:" b\nc "|true <nil>|false <nil>`; strings.Join(got, "|") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, "|"), want)
	}
	doc, err = Parse("f", []byte("<pre>a</div>\n"), core.DefaultMaxBytes, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := doc.NewProse().HTMLComments(doc.Src, doc.Root.FirstChild().(*ast.HTMLBlock), func(int, int, int) {})
	if !ok || err == nil || !strings.HasPrefix(err.Error(), "line 1: </div> may end the pre open inside it through an element opened before the HTML block") {
		t.Errorf("a refused block: %v %v", ok, err)
	}
}

// HTML blocks left open in a comment or a script before more HTML cost
// work linear in the file: the tokenizer reads such a token past its
// block's end, to the end of the HTML, and those bytes count against the
// tokens max_bytes allows (htmlparse.Scanner), so their reading stops at
// about max_bytes in all. Twice the blocks before twice the HTML may
// allocate at most three times as much, with max_bytes the file's size;
// reading to the end for each block, a quadratic, allocates four times
// (delta review 1 of 9f3cd3b, finding 1). The planted break: the bytes not
// counted.
func TestBlocksLeftOpenCostLinearWork(t *testing.T) {
	shape := func(open string, k, lines int) string {
		return strings.Repeat("<div>"+open+"\n\n", k) + "<div>\n" + strings.Repeat(strings.Repeat("a", 99)+"\n", lines)
	}
	// A quoted attribute's value left open reads into the next block, to
	// its quote.
	for _, open := range []string{"<!--", "<script>", `<a href="`} {
		alloc := func(src string) uint64 {
			least := uint64(math.MaxUint64)
			for range 2 {
				doc, err := Parse("f", []byte(src), int64(len(src)), time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				_, _ = doc.Prose(false) // read, or refused at the tokens' limit
				runtime.ReadMemStats(&after)
				least = min(least, after.TotalAlloc-before.TotalAlloc)
			}
			return least
		}
		small, large := alloc(shape(open, 100, 600)), alloc(shape(open, 200, 1200))
		t.Logf("%s: %d bytes allocated, %d for twice the file", open, small, large)
		if large > 3*small {
			t.Errorf("%s: twice the file allocated %d bytes, over three times the %d of the file", open, large, small)
		}
	}
}
