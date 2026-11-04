package rst

import (
	"strings"
	"testing"
)

// Where inline markup starts and ends, each case at an edge of docutils'
// rules: markup after a quote and before one, two of a kind in a row, a
// start with no end, a role with nothing or a blank after it, a role at
// the end, a start of two characters at the end of the text, which no
// search for its end may read past, a reference whose text ends in > with no target, and a suffix
// that is no role.
func TestInlineEdges(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"'*a*'", "'a'"},
		{"``a`` ``b``", "a b"},
		{"**a** **b**", "a b"},
		{"*a", "*a"},
		{"**", "**"},
		{"``", "``"},
		{"x **", "x **"},
		{"``a", "``a"},
		{"**a", "**a"},
		{"x :a:`", "x :a:`"},
		{":a:` x`", ":a:` x`"},
		{"`x`:r:", "x"},
		{":a-b:`x` :a.b+c:d_e:`y`", "x y"},
		{"`x`:a~ y", "x:a~ y"},
		{"a `x>`_ `a <b>`_", "a x> a"},
		{"*a\\\\* b*", "a\\* b"},
	} {
		var b []byte
		inline([]byte(c.in), func(from, to int) { b = append(b, c.in[from:to]...) })
		if string(b) != c.want {
			t.Errorf("%q: %q, want %q", c.in, b, c.want)
		}
	}
}

// A tab moves to the next multiple of 8, in an indent, in the dedent of a
// block, and in the columns of a table.
func TestTabs(t *testing.T) {
	got := read(t, sel("code"), ".. code:: sh\n\n\ta\n          b\n")
	want(t, "dedent", got, "3:a\n  b")
	got = read(t, sel("table", "column", "Port"), "=====  =====\nHost   Port\n=====  =====\nweb\t8080\n=====  =====\n")
	want(t, "table", got, "4:8080")
	got = read(t, sel("comment"), "x\n\n  \t.. a\n")
	want(t, "indent", got, "3:a")
}

// Titles at the edges: an underline of four under a longer title, any
// punctuation but not DEL, an overline title on the last lines with no
// newline, an adornment over two lines of text, and an overline at the
// end of the file. Each new style is a level below the one before, so each
// section holds the ones after it.
func TestTitleEdges(t *testing.T) {
	body := "Title\n----\n\nx\n\nBang\n!!!!\n\ny\n\nDel\n\x7f\x7f\x7f\x7f\n\n----\ntext\nmore\n\n====\nLast\n===="
	got := read(t, sel("section", "section", "Title"), body)
	want(t, "four", got, "1:x\nBang\ny\nDel \x7f\x7f\x7f\x7f\ntext more\nLast")
	got = read(t, sel("section", "section", "Bang"), body)
	want(t, "bang", got, "6:y\nDel \x7f\x7f\x7f\x7f\ntext more\nLast")
	got = read(t, sel("section", "section", "Last"), body)
	want(t, "last", got, "19:")
	got = read(t, sel("section", "section", "A"), "A\n=\n\nx\n\n====\nOver")
	want(t, "end", got, "1:x\nOver")
}

// List markers alone on a line, where "- " is an empty item, at the end of the line, and enumerators of
// 0 and 9.
func TestListMarkerEdges(t *testing.T) {
	got := read(t, sel("section", "section", "A"), "A\n=\n\n- \n\n(\n\n12\n\n(3\n\n0. x\n\n9) y\n\n#. z\n")
	want(t, "edges", got, "1:\n(\n12\n(3\nx\ny\nz")
}

// A paragraph's :: reads as : after text, as nothing after a blank, and is
// left out alone; each is followed by a literal block.
func TestLiteralMarkers(t *testing.T) {
	body := "A\n=\n\nPara ::\n\n   one\n\nEnd::\n\n   two\n\n::\n\n   three\n"
	got := read(t, sel("section", "section", "A"), body)
	want(t, "prose", got, "1:Para\nEnd:")
	got = read(t, sel("code"), body)
	want(t, "code", got, "6:one", "10:two", "14:three")
}

// Directive names at the edges of their characters, a one letter name, an
// option on the last line, and an admonition with no text after its ::,
// which adds no line to the prose.
func TestDirectiveEdges(t *testing.T) {
	got := read(t, sel("comment"), ".. a9:: 1\n.. zA:: 2\n.. Z0:: 3\n.. x:: 4\n.. a-b:: 5\n.. c\n")
	want(t, "names", got, "6:c")
	got = read(t, sel("code"), ".. code:: sh\n   :a: 1")
	want(t, "option at the end", got, "1:")
	got = read(t, sel("section", "section", "A"), "A\n=\n\n.. note::\n\n   text\n")
	want(t, "admonition", got, "1:text")
}

// Footnotes, citations and substitution definitions at their edges: an
// empty label is a comment, a label at the end of the line is none, a
// label with text right after it is a comment, and so are || and a
// substitution with nothing after it.
func TestLabelEdges(t *testing.T) {
	got := read(t, sel("comment"), ".. [] a\n.. [1]\n.. [1]b\n.. || c\n.. |d|\n.. |e| f\n")
	want(t, "labels", got, "1:[] a", "3:[1]b", "4:|| c", "5:|d|")
}

// Grid tables at their edges: one column one character wide, two borders
// in a row, a cell with an empty line between two others, and a table
// indented in a block quote.
func TestGridEdges(t *testing.T) {
	got := read(t, sel("table", "column", "H"), "+-+\n|H|\n+=+\n|a|\n+-+\n+-+\n|b|\n|c|\n| |\n|d|\n+-+\n")
	want(t, "narrow", got, "4:a", "7:b c d")
	got = read(t, sel("table", "column", "P"), "x\n\n  +---+---+\n  | H | P |\n  +===+===+\n  | a | 1 |\n  +---+---+\n  | b | 2 |\n  +---+---+\n")
	want(t, "indented", got, "6:1", "8:2")
}

// A simple table's cell with an empty line between two others reads as
// the two joined by one space.
func TestSimpleCellLines(t *testing.T) {
	body := "====  ====  ====\nHost  Port  Path\n====  ====  ====\nweb   80    /a\n            b\n      81\n====  ====  ====\n"
	got := read(t, sel("table", "column", "Port"), body)
	want(t, "port", got, "4:80 81")
	if !strings.Contains(body, "b\n") {
		t.Fatal("the body lost its middle line")
	}
}
