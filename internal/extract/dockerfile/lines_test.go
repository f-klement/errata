package dockerfile

import (
	"errors"
	"testing"

	"github.com/moby/buildkit/frontend/dockerfile/parser"

	"github.com/f-klement/errata/internal/extract/core"
)

// argsStart finds the arguments after the instruction and its flags, as
// the parser's splitCommand does: a flag's quotes and escapes keep its
// blanks, a word of -- alone ends the flags, and one - starts no flag.
// Planted breaks: the quote not read, the escape not read, -- not ending
// the flags.
func TestArgsStart(t *testing.T) {
	for _, c := range []struct {
		line string
		want string
	}{
		{"FROM a", "a"},
		{"  FROM \t a b", "a b"},
		{"FROM --platform=x a", "a"},
		{`FROM --p="a b" --q='c d' img`, "img"},
		{`FROM --p=a\ b img`, "img"},
		{"FROM --p=a` b img", "b img"},
		{"FROM -- --a", "--a"},
		{"FROM --a -- b", "b"},
		{"FROM -x a", "-x a"},
		{"FROM --a", ""},
		{"FROM", ""},
		{`FROM --p="a\" b" c`, "c"},
	} {
		if got := c.line[argsStart(c.line, '\\'):]; got != c.want {
			t.Errorf("%q: %q, want %q", c.line, got, c.want)
		}
	}
	if got := "FROM --p=a` b img"[argsStart("FROM --p=a` b img", '`'):]; got != "img" {
		t.Errorf("the ` escape: %q", got)
	}
}

// trimContinuation drops the escape that continues a line, and the blanks
// after it, unless an escape comes before it. Planted breaks: the blanks
// after it kept, an escaped escape read as a continuation.
func TestTrimContinuation(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`a \`, "a "},
		{"a \\ \t ", "a "},
		{`\`, ""},
		{`a\\`, `a\\`},
		{`a`, `a`},
		{``, ``},
		{`a\b`, `a\b`},
	} {
		if got := string(trimContinuation([]byte(c.in), '\\')); got != c.want {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
}

// An instruction whose lines do not join to its Original, which the
// parser never gives, has every word at its first line, and a word it
// cannot find sits where it would start. Planted breaks: the join's check
// dropped, a word not found taken as found.
func TestPositionsFallBack(t *testing.T) {
	data := []byte("x\nLABEL a=1 \\\n b=2\n")
	r := &reading{lines: newLines(data), escape: '\\'}
	n := &parser.Node{Original: "LABEL a=1 c=2", StartLine: 2, EndLine: 3}
	if r.parts(n) != nil {
		t.Fatal("parts of lines that do not join")
	}
	at := r.positions(n)
	for _, w := range []string{"a=1", "c=2"} {
		if off, err := at.next(w, nil); err != nil || off != 2 {
			t.Errorf("%s at %d, %v", w, off, err)
		}
	}
	n.Original = "LABEL a=1  b=2"
	at = r.positions(n)
	for w, want := range map[string]int{"zz": 8} {
		if off, err := at.next(w, nil); err != nil || off != want {
			t.Errorf("%s at %d, want %d, %v", w, off, want, err)
		}
	}
	if off, _ := at.next("=2", nil); off != 16 {
		t.Errorf("a word past white space at %d", off)
	}
	if r.parts(&parser.Node{Original: "LABEL a=1 b=2 c", StartLine: 2, EndLine: 3}) != nil {
		t.Error("parts that join to less than Original")
	}
}

// A search past the white space is charged the bytes it may read, so a
// file of words the search must look for is refused for work. Planted
// break: the charge left out.
func TestPositionsChargeTheSearch(t *testing.T) {
	m := &core.Meter{Budget: 4}
	at := positions{text: "LABEL a=1 b", pos: 6, parts: []part{{0, 0}}}
	if _, err := at.next("x", m); !errors.Is(err, core.ErrWork) {
		t.Errorf("a search of 5 bytes on a budget of 4 gives %v, spent %d", err, m.Spent)
	}
	m = &core.Meter{Budget: 100}
	at = positions{text: "LABEL a=1 b", pos: 6, parts: []part{{0, 0}}}
	if _, err := at.next("b", m); err != nil || m.Spent != 5 {
		t.Errorf("a search from byte 6 of 11 spent %d, want 5, %v", m.Spent, err)
	}
	m = &core.Meter{Budget: 5}
	at = positions{text: "LABEL a=1 b", pos: 6, parts: []part{{0, 0}}}
	if _, err := at.next("a=1", m); err != nil || m.Spent != 0 {
		t.Errorf("a word at its place spent %d, %v", m.Spent, err)
	}
}

// lines gives each line with its line end, and the end of the file past
// the last, which is also the long line of a file with none. Planted
// break: a \n at the very end starting a line.
func TestLines(t *testing.T) {
	l := newLines([]byte("a\nbc\n"))
	if string(l.line(1)) != "a\n" || string(l.line(2)) != "bc\n" || len(l.line(3)) != 0 || len(l.line(0)) != 0 {
		t.Errorf("%q %q %q", l.line(1), l.line(2), l.line(3))
	}
	if l.longLine() != 5 {
		t.Errorf("a file of short lines has its long line at %d", l.longLine())
	}
	if l.offset(3) != 5 || l.offset(0) != 5 || l.offset(2) != 2 {
		t.Errorf("offsets %d %d %d", l.offset(3), l.offset(0), l.offset(2))
	}
}
