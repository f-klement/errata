package yaml

import (
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

func literal(line, col int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Style: yaml.LiteralStyle, Line: line, Column: col}
}

// The cursor finds the | of each block in the file's order, also when asked
// about twice, and gives 0 for a block asked about after a later one, for a
// line or a column the file does not hold, and for a column that a line
// break or a byte other than an anchor, a tag, a comment or a blank stands
// before. The planted breaks: the out of order test off by one, which
// refused a block asked twice; the cursor not moving to the line asked; the
// end of the file and a break before the column not tested; and the column
// walk not taken.
func TestBlocksLineOf(t *testing.T) {
	data := []byte("a: |\n  x\nb: &c\n  |\n  y\nd: e |\nf:  é |\n")
	b := newBlocks(data)
	for _, c := range []struct {
		name string
		y    *yaml.Node
		want int32
	}{
		{"a", literal(1, 4), 1},
		{"a again", literal(1, 4), 1},
		{"b, an anchor on the line above", literal(3, 4), 4},
		{"a after b", literal(1, 4), 0},
		{"d, a letter before the |", literal(6, 4), 0},
		{"f, a column past a letter of two bytes", literal(7, 6), 7},
		{"a column past the end of its line", literal(8, 9), 0},
		{"a line the file does not hold", literal(99, 1), 0},
	} {
		if got := b.lineOf(c.y); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
	if got := newBlocks([]byte("a: |\n")).lineOf(&yaml.Node{Kind: yaml.ScalarNode, Line: 1, Column: 4}); got != 0 {
		t.Errorf("a scalar that is no literal: got %d, want 0", got)
	}
	if got := newBlocks([]byte("a\n|\n")).lineOf(literal(2, 1)); got != 2 {
		t.Errorf("a | at column 1: got %d, want 2", got)
	}
	if got := newBlocks([]byte("a\n|\n")).lineOf(literal(1, 3)); got != 0 {
		t.Errorf("a column past a line break: got %d, want 0", got)
	}
	if got := newBlocks([]byte("a: x\n")).lineOf(literal(1, 9)); got != 0 {
		t.Errorf("a column past the end of the file: got %d, want 0", got)
	}
}

// A UTF-8 byte order mark takes no column, a UTF-16 file is not looked in,
// though yaml.v3's column, read in its bytes, lands on the | of the key s|,
// and a block that holds an LS or a PS has no line of its own. The planted
// breaks: the mark not skipped, UTF-16 not set off, and the LS and PS test
// removed.
func TestBlocksFileKinds(t *testing.T) {
	if got := newBlocks([]byte("\xef\xbb\xbf|\n  x\n")).lineOf(literal(1, 1)); got != 1 {
		t.Errorf("after a byte order mark: got %d, want 1", got)
	}
	if got := newBlocks([]byte(extracttest.UTF16LE("s|: |\n  x\n"))).lineOf(literal(1, 5)); got != 0 {
		t.Errorf("UTF-16: got %d, want 0", got)
	}
	for _, sep := range []string{"\u2028", "\u2029"} {
		y := literal(1, 4)
		y.Value = "a" + sep + "b\n"
		if got := newBlocks([]byte("a: |\n  a" + sep + "  b\n")).lineOf(y); got != 0 {
			t.Errorf("a block that holds %q: got %d, want 0", sep, got)
		}
	}
}

// indicatorLine passes anchors, tags, blanks, comments and every kind of
// line break before the |, counting the lines, and gives 0 for anything
// else and for an end without a |. The planted breaks: each state's test
// negated, a blank or a break not ending a property, and a | inside a
// comment or a property taken for the indicator.
func TestIndicatorLine(t *testing.T) {
	for _, c := range []struct {
		rest string
		want int32
	}{
		{"|", 5},
		{"&a |", 5},
		{"&a\t|", 5},
		{"!!str |", 5},
		{"\t !t  &a  |", 5},
		{"&a !!str # c |\n\n  |", 7},
		{"&a\r\n  |", 6},
		{"&a\r  |", 6},
		{"!t\u0085|", 6},
		{"!t\u2028|", 6},
		{"&a\u2029|", 6},
		{"# c\n|", 6},
		{"&a|b\n|", 6},
		{"x |", 0},
		{"&a x |", 0},
		{"&a", 0},
		{"# |", 0},
		{"", 0},
	} {
		if got := indicatorLine([]byte(c.rest), 5); got != c.want {
			t.Errorf("%q: got %d, want %d", c.rest, got, c.want)
		}
	}
}

// breakAt knows each line break yaml.v3 breaks on and nothing else, a CR LF
// as one. The planted breaks: CR before CR LF, and each break left out.
func TestBreakAt(t *testing.T) {
	for _, c := range []struct {
		rest string
		want int
	}{
		{"\r\nx", 2}, {"\rx", 1}, {"\nx", 1}, {"\u0085x", 2}, {"\u2028", 3}, {"\u2029", 3},
		{"\xc2", 0}, {"\xc2\x86", 0}, {"\xe2\x80", 0}, {"\xe2\x80\xaa", 0}, {"x\n", 0}, {"", 0},
	} {
		if got := breakAt([]byte(c.rest)); got != c.want {
			t.Errorf("%q: got %d, want %d", c.rest, got, c.want)
		}
	}
}
