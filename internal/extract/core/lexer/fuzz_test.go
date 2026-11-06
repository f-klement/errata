package lexer

import (
	"bytes"
	"strings"
	"testing"
)

// Over any bytes and any row, the spans come in order and do not overlap,
// each lies inside the file with its body inside it, opens with the opener of
// a form of its kind, and a line comment holds no line feed. Stopping the
// walk after the first span stops it. A heredoc's body lies after its
// opener and no span starts inside it. Planted break: a heredoc's body
// starting at its opener's end.
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
	f.Add(row("ruby"), "f(<<~A, <<-'B') # c\n  a\n  A\nb\n  B\nx = <<C /* \"\nC\n")
	f.Add(row("php"), "<?php $a = <<<EOT\n  x\n  EOT; $b = <<<'N'\nN\n")
	f.Add(row("elixir"), "a = ~s(x \\) #) ~S\"\"\"\n#\n\"\"\" ~r/a/ # c")
	f.Add(row("php"), "<?<<<A\n")
	f.Add(row("php"), "<?xml v='1'?>\n<?php $a = 1; ?><? $b ?><?=$c")
	f.Add(row("python"), "f\"{n:#x} {x:'^10} {x:{w}} {x!r:>{w}} {a != '#'}\" # c")
	f.Add(row("scala"), "val a = \"${\"\nval b = s\"${ \"}\" }\" // c")
	f.Add(row("python"), "    (rf'(\\{{[{_dash}])(![^}}]*)(\\}}?)', # c\n")
	f.Add(row("ruby"), "x = %r{^/([a-z]):/}i + %w(a (b)) # c\nre = /\n a # x\n/x\ny = \"#{n ? %[-#{n}] : ''}\"\n")
	f.Add(row("lua"), "--[[\n")
	f.Fuzz(func(t *testing.T, row uint8, src string) {
		ls := Languages()
		l := ls[int(row)%len(ls)]
		data := []byte(src)
		prev, n := 0, 0
		var body [][2]int // the heredocs' bodies so far
		for s := range l.Spans(data) {
			n++
			if s.Start < prev || s.Start > s.BodyStart || s.BodyStart > s.BodyEnd || s.BodyEnd > s.End || s.End > len(data) || s.End == s.Start {
				t.Fatalf("%s: span %+v after %d in %d bytes", l.Name, s, prev, len(data))
			}
			// The text before a row's Begin opens with no form.
			opens := s.Kind == Text && s.Start == 0 && l.Begin != ""
			for i, f := range l.Forms {
				if f.Kind == s.Kind && bytes.HasPrefix(data[s.Start:], []byte(f.Open)) {
					opens = true
					// Only the form that opened the span says whether it is a
					// line comment: Lua's -- is a prefix of its --[[, whose
					// unclosed body runs to the end, line feeds and all.
					if i == s.Form && f.Kind == Comment && f.Close == "" && !f.ToEnd && bytes.IndexByte(data[s.Start:s.End], '\n') >= 0 {
						t.Fatalf("%s: line comment %+v holds a line feed", l.Name, s)
					}
				}
			}
			if !opens {
				t.Fatalf("%s: span %+v opens with no form of its kind: %q", l.Name, s, data[s.Start:min(s.End, s.Start+8)])
			}
			prev = s.End
			if h := s.Here; h.Close > 0 {
				// A heredoc's body lies after its opener, and the walk goes on
				// past its closer: no span starts inside it.
				if h.Start <= s.End || h.Start > h.End || h.End > h.Close || h.Close > len(data) {
					t.Fatalf("%s: heredoc %+v in %d bytes", l.Name, s, len(data))
				}
				body = append(body, [2]int{h.Start, h.Close})
			}
			for _, b := range body {
				if s.Start >= b[0] && s.Start < b[1] {
					t.Fatalf("%s: span %+v starts inside the heredoc body %v", l.Name, s, b)
				}
			}
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
