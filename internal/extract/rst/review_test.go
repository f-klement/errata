package rst

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// refused fails t unless s refuses body with want after the file's name.
func refused(t *testing.T, s Spec, body, want string) {
	t.Helper()
	_, err := extracted(s, body)
	extracttest.WantError(t, err, "a.rst: "+want)
}

// A directive may have one space before its ::, as docutils allows: a code
// block so written is code, and an admonition so written is prose, and
// neither is a comment (review of 60abfac, finding 1).
func TestASpaceBeforeTheDirectivesColons(t *testing.T) {
	body := "A\n=\n\n.. note :: Requires Go 1.27.\n\n   Body text.\n\n.. code-block :: python\n\n   x = 1\n\n.. a  :: two spaces\n"
	got := read(t, sel("comment"), body)
	want(t, "comments", got, "12:a  :: two spaces")
	got = read(t, sel("code", "language", "python"), body)
	want(t, "code", got, "10:x = 1")
	got = read(t, sel("section", "section", "A"), body)
	want(t, "prose", got, "1:Requires Go 1.27.\nBody text.")
}

// A simple table with no bottom border, at the end of the file or before a
// line indented less than it, is refused whatever the target, since docutils
// reads no table there and the rest of the file would read as its rows
// (finding 2).
func TestASimpleTableWithNoBottomBorder(t *testing.T) {
	body := "=====  =====\nA      B\n=====  =====\nx      y\n\nSection\n=======\n\nsome   prose here\n"
	for _, s := range []Spec{sel("table", "column", "B"), sel("comment"), sel("section", "section", "Section")} {
		refused(t, s, body, "line 1: the simple table that starts here has no bottom border")
	}
	refused(t, sel("code"), "x\n\n  ===  ===\n  A    B\n  ===  ===\n  1    2\nText\n", "line 3: the simple table that starts here has no bottom border")
	// A border followed by a line indented less than the table is its
	// bottom, as where the next list item follows with no blank line: the
	// item's block ends there, and docutils reads the table (rst delta
	// review 1, finding 1). The planted break: the indent of the line after
	// the border not compared, so the border reads as the header's.
	got := read(t, sel("comment"), "- item\n\n  =====  =====\n  x      y\n  =====  =====\n- next\n\n.. c\n")
	want(t, "after a headerless table in a list item", got, "8:c")
}

// spanned is a grid table whose third body row's top border holds a row span.
const spanned = "+---+---+\n| A | B |\n+===+===+\n| 1 | 2 |\n+---+---+\n| 3 | 4 |\n+---+   +\n| 5 |   |\n+---+---+\n| 7 | 8 |\n+---+---+\n"

// A grid table with a cell that spans rows or columns is refused at the
// line the top border's columns do not cut, where the table target reads
// it; any other target, or a table outside the path, passes over it
// (finding 3).
func TestAGridSpanIsRefused(t *testing.T) {
	refused(t, sel("table", "column", "B"), spanned, "line 7: a cell of the grid table here spans rows or columns")
	refused(t, sel("table", "column", "B"), strings.Replace(spanned, "| 1 | 2 |", "| 1  2  |", 1), "line 4: a cell of the grid table here spans rows or columns")
	got := read(t, sel("comment"), spanned+"\n.. c\n")
	want(t, "comment", got, "13:c")
	got = read(t, sel("table", "column", "B", "section", "X"), spanned+"\nX\n=\n\n"+strings.SplitN(spanned, "+---+---+\n| 3", 2)[0]+"+---+---+\n")
	want(t, "outside the path", got, "19:2")
}

// An admonition's text after its :: and the lines under it are its
// content's first paragraph: they join, and a :: at their end opens a
// literal block; a line not indented under it is none of it (finding 4).
// The lines after the first are dedented by their own indent, as
// docutils' get_first_known_indented does, so a block under a blank line
// and indented further is no literal block but the content's next
// paragraph.
func TestAnAdmonitionsFirstParagraph(t *testing.T) {
	got := read(t, sel("section", "section", "A", "pattern", `(This is a note)`), "A\n=\n\n.. note:: This is\n   a note.\n")
	want(t, "joined", got, "4:This is a note")
	body := "A\n=\n\n.. warning:: Look::\n   more::\n\n      code\n\n   after\n"
	got = read(t, sel("section", "section", "A"), body)
	want(t, "prose", got, "1:Look:: more:\nafter")
	got = read(t, sel("code"), body)
	want(t, "code", got, "7:code")
	got = read(t, sel("section", "section", "A"), "A\n=\n\n.. note:: Look::\n\n      code here\n")
	want(t, "dedented", got, "1:Look:\ncode here")
	// The content's column is the least indent of the lines after the
	// first, not the first's, so a line indented further than the ones
	// after it is a block quote of its own (rst delta review 1, finding 2).
	// The planted break: content taking the first line's column again.
	got = read(t, sel("section", "section", "A"), "A\n=\n\n.. note:: Text here\n      deeper\n   back\n")
	want(t, "least indent", got, "1:Text here\ndeeper\nback")
	got = read(t, sel("section", "section", "A"), ".. tip:: First\n\nA\n=\n\n.. hint:: x\n.. c\n")
	want(t, "not indented", got, "3:x")
}

// A header of more than one row, a grid table's with a border of - above
// its border of =, or a simple table's with a second row or a rule under a
// cell that spans columns, is refused where the table target reads it; a
// simple header's continuation line, its first column blank, is one row
// (finding 5).
func TestHeadersOfMoreThanOneRow(t *testing.T) {
	grid := "+------+------+\n| Name | Port |\n+------+------+\n| (id) | (n)  |\n+======+======+\n| a    | 1    |\n+------+------+\n"
	refused(t, sel("table", "column", "Name"), grid, "line 5: the header of the grid table above has more than one row")
	simple := "=====  =====\nName   Port\nId     N\n=====  =====\na      1\n=====  =====\n"
	refused(t, sel("table", "column", "Name"), simple, "line 3: the header of the simple table that starts at line 1 has more than one row or a cell that spans columns")
	rule := "=====  =====\nInputs\n------------\nA      B\n=====  =====\na      1\n=====  =====\n"
	refused(t, sel("table", "column", "A"), rule, "line 3: the header of the simple table that starts at line 1")
	for body, line := range map[string]string{grid: "9", simple: "8", rule: "9"} {
		got := read(t, sel("comment"), body+"\n.. c\n")
		want(t, "comment", got, line+":c")
	}
	got := read(t, sel("table", "column", "Port (n)"), "=====  =====\nName   Port\n       (n)\n=====  =====\na      1\n=====  =====\n")
	want(t, "continued", got, "5:1")
	got = read(t, sel("table", "column", "Port"), "=====  =====\nx      1\ny      2\n=====  =====\n")
	want(t, "no header", got)
}

// Every admonition and container docs/reference.md#rst names is read through: a
// comment in its content is one, its content is prose, and an
// admonition's text after its :: is prose too, a container's not
// (finding 6).
func TestEveryAdmonitionAndContainer(t *testing.T) {
	for _, c := range []struct {
		names []string
		prose string
	}{{admonitions, "1:first\nsecond"}, {containers, "1:second"}} {
		for _, name := range c.names {
			body := "A\n=\n\n.. " + strings.ToUpper(name[:1]) + name[1:] + ":: first\n\n   .. c\n\n   second\n"
			want(t, name, read(t, sel("comment"), body), "6:c")
			want(t, name, read(t, sel("section", "section", "A"), body), c.prose)
		}
	}
	if len(admonitions) != 10 || len(containers) != 7 {
		t.Errorf("%d admonitions and %d containers; docs/reference.md names 10 and 7", len(admonitions), len(containers))
	}
}

// A title whose style is new below a section that is not the deepest, or
// that is more than one level below the section it is in, is refused, as
// docutils reads it as an error; a known style at any level up to one
// below is read (finding 8).
func TestInconsistentTitleLevels(t *testing.T) {
	refused(t, sel("comment"), "A\n=\n\nB\n-\n\nC\n=\n\nD\n~\n\nd text\n", "line 10: the title here is at a level inconsistent with the titles before it, which docutils reads as an error")
	refused(t, sel("section", "section", "E"), "A\n=\n\nB\n-\n\nC\n~\n\nD\n=\n\nE\n~\n", "line 13: the title here is at a level inconsistent")
	got := read(t, sel("section", "section", "C > D"), "A\n=\n\nB\n-\n\nC\n=\n\nD\n-\n\nd text\n\nE\n=\n")
	want(t, "consistent", got, "10:d text")
}

// An overline shorter than four characters is text, as docutils reads it,
// so its title's three lines are a paragraph (finding 9).
func TestAShortOverlineIsText(t *testing.T) {
	got := read(t, sel("section", "section", "A"), "A\n=\n\n==\nLong title\n==\n")
	want(t, "short", got, "1:== Long title ==")
}

// A line indented more than a directive's option goes on the option, and
// is no code (finding 10).
func TestAWrappedOption(t *testing.T) {
	got := read(t, sel("code"), ".. code-block:: sh\n   :caption: long\n      caption\n   :linenos:\n\n   echo hi\n")
	want(t, "wrapped", got, "6:echo hi")
}

// Where docutils reads otherwise than the forms before: a _ with a blank
// after it starts no target, a label of other characters no footnote or
// citation, interpreted text with no reference or role keeps its <...>, a
// bullet over an adornment is a list, and a wide title is measured in
// columns (finding 11).
func TestDocutilsEdges(t *testing.T) {
	got := read(t, sel("comment"), ".. _ not a target\n.. [x!] y\n.. [#a-b] z\n.. [*] w\n.. _a: v\n")
	want(t, "explicit", got, "1:_ not a target", "2:[x!] y")
	for _, c := range []struct{ in, want string }{
		{"`a <b>`", "a <b>"}, {"`a <b>`_", "a"}, {":r:`a <b>`", "a"},
	} {
		var b []byte
		inline([]byte(c.in), func(from, to int) { b = append(b, c.in[from:to]...) })
		if string(b) != c.want {
			t.Errorf("%q: %q, want %q", c.in, b, c.want)
		}
	}
	got = read(t, sel("section", "section", "- foo"), "- foo\n-----\n\nx\n")
	want(t, "bullet", got)
	got = read(t, sel("section", "section", "1. Intro"), "1. Intro\n========\n\nx\n")
	want(t, "enumerated", got, "1:x")
	got = read(t, sel("section", "section", "A"), "A\n=\n\n名前\n===\n\n名前\n====\n\nx\n")
	want(t, "wide", got, "1:名前 ===")
	got = read(t, sel("section", "section", "名前"), "A\n=\n\n名前\n===\n\n名前\n====\n\nx\n")
	want(t, "wide title", got, "7:x")
}

// The ranges of wide characters start and end where they say, and the
// characters just outside each are narrow, so no two ranges touch.
func TestWideRanges(t *testing.T) {
	for _, r := range wideRanges {
		if !wide(r[0]) || !wide(r[1]) || wide(r[0]-1) || wide(r[1]+1) {
			t.Errorf("%U to %U: wide %v %v, outside %v %v", r[0], r[1], wide(r[0]), wide(r[1]), wide(r[0]-1), wide(r[1]+1))
		}
	}
	if width([]byte("a名b")) != 4 {
		t.Errorf("a名b is %d columns, want 4", width([]byte("a名b")))
	}
}
