package lexer

import (
	"bytes"
	"strings"
	"testing"
)

// Over any bytes and any row, the spans come in order and do not overlap,
// each lies inside the file with its body inside it, opens with the opener of
// a form of its kind, and a line comment holds no line feed. Stopping the
// walk after the first span stops it.
func FuzzLexer(f *testing.F) {
	for i := range Languages() {
		f.Add(uint8(i), "x = \"a // b\" // c\n/* d /* e */ f */ 'g' `h` # i\n-- j <!-- k --> [==[ l ]==]\n")
	}
	f.Add(row("rust"), "r##\"a\"# \"##")
	f.Add(row("csharp"), "\"\"\"\"x\"\"\" \"\"\"\"")
	f.Add(row("shell"), "a \\# b # c '\n")
	f.Add(row("sql"), "'it''s' \"\"\" $a$ $$ E'\\'' [x] `y`")
	f.Add(row("cpp"), "R\"abcdefghijklmnopq(x)abcdefghijklmnopq\"")
	f.Add(row("rust"), "'\\u{10FFFF}' '\xff' '\n'")
	f.Add(row("swift"), "#\"\"\"\na\n\"\"\"# "+strings.Repeat("#", 40)+"\"x\"")
	f.Add(row("kotlin"), "\"\"\"a\"\"\"\"\"\" // c")
	f.Add(row("yaml"), "- a: |-\n    x\n\n  b: >\n  c # d\n|\n")
	f.Fuzz(func(t *testing.T, row uint8, src string) {
		ls := Languages()
		l := ls[int(row)%len(ls)]
		data := []byte(src)
		prev, n := 0, 0
		for s := range l.Spans(data) {
			n++
			if s.Start < prev || s.Start > s.BodyStart || s.BodyStart > s.BodyEnd || s.BodyEnd > s.End || s.End > len(data) || s.End == s.Start {
				t.Fatalf("%s: span %+v after %d in %d bytes", l.Name, s, prev, len(data))
			}
			opens := false
			for _, f := range l.Forms {
				if f.Kind == s.Kind && bytes.HasPrefix(data[s.Start:], []byte(f.Open)) {
					opens = true
					if f.Close == "" && !f.Indented && bytes.IndexByte(data[s.Start:s.End], '\n') >= 0 {
						t.Fatalf("%s: line comment %+v holds a line feed", l.Name, s)
					}
				}
			}
			if !opens {
				t.Fatalf("%s: span %+v opens with no form of its kind: %q", l.Name, s, data[s.Start:min(s.End, s.Start+8)])
			}
			prev = s.End
		}
		first := 0
		for range l.Spans(data) {
			first++
			break
		}
		if first != min(n, 1) {
			t.Fatalf("%s: a walk stopped after one span yielded %d", l.Name, first)
		}
	})
}

// row is the number of the row named name, so a seed names its language and
// a row added above it does not move it to another.
func row(name string) uint8 {
	for i, l := range Languages() {
		if l.Name == name {
			return uint8(i)
		}
	}
	panic("no row " + name)
}
