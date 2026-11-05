package literal

import (
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// tokens is every token of src read as lang, each as its kind's letter and
// its text, after a / when a line end stands before it, a + when nothing
// stands between it and the token before, and with its column when it is
// first on its line; or the tokenizer's error.
func tokens(t *testing.T, lang, src string) string {
	t.Helper()
	l, _ := core.LanguageNamed(lang)
	data := []byte(src)
	tz := newTokenizer("f", l, data, core.LineIndex(data))
	defer tz.stop()
	var out []string
	for range len(src) + 2 {
		tok := tz.token()
		if tok.kind == tEOF {
			break
		}
		s := ""
		if tok.line {
			s += "/"
		}
		if tok.glued {
			s += "+"
		}
		s += string("EINTSPOC"[tok.kind]) + src[tok.start:tok.end]
		if tok.first {
			s += "@" + strconv.Itoa(tok.col)
		}
		out = append(out, s)
	}
	if tz.err != nil {
		out = append(out, "error: "+tz.err.Error())
	}
	return strings.Join(out, " ")
}

// The tokenizer reads numbers with their bases, exponents and suffixes,
// names with digits, _, $ and characters past ASCII, Ruby's names that end
// in ? or !, symbols, PHP's variables, the longest operator, a character
// past ASCII as one mark, line ends of each kind, a line continued with a
// backslash, a block comment over lines, and a heredoc's body, which it
// skips at its opener's line end. It kills the mutants of scan, number and
// token that the reader's tests leave.
func TestTokens(t *testing.T) {
	for _, c := range []struct{ lang, src, want string }{
		{"python", "x = 1e+5 0x1e+5 1.5 1..2 .5 1_000 1L 2E-3 7e",
			"+Ix@0 P= N1e+5 N0x1e +P+ +N5 N1.5 N1 +P.. +N2 N.5 N1_000 N1L N2E-3 N7e"},
		{"python", "a1 _b $c é d\\\ne\\\r\nf \\", "+Ia1@0 I_b I$c Ié Id Ie If P\\"},
		{"ruby", "a.empty? b.save! c != d e!=f",
			"+Ia@0 +P. +Iempty? Ib +P. +Isave! Ic P!= Id Ie +P!= +If"},
		{"ruby", ":sym :q? :b! x::Y a:b (:c) ? :", "+S:sym@0 S:q? S:b! Ix +P:: +IY Ia +P: +Ib O( +S:c +C) P? P:"},
		{"python", ":a", "+P:@0 +Ia"},
		{"php", "<?php $x $ y $1", "I$x@6 I$ Iy I$1"},
		{"python", "a <<= b ... === → ;", "+Ia@0 P<<= Ib P... P=== I→ P;"},
		{"python", "a\rb\r\nc\n\n  d\n\te", "+Ia@0 /Ib@0 /Ic@0 /Id@2 /Ie@1"},
		{"javascript", "a /* x\n */ b // c\nd", "+Ia@0 /Ib@4 /Id@0"},
		{"javascript", "a /* x\r */ b", "+Ia@0 /Ib@4"},
		{"ruby", "x = <<~A + <<~B\na\nA\nb\nB\ny", "+Ix@0 P= T<<~A P+ T<<~B /Iy@0"},
		{"php", "<?php $a = <<<A\nx\n  A;\n$y", "I$a@6 P= T<<<A /P; /I$y@0"},
	} {
		if got := tokens(t, c.lang, c.src); got != c.want {
			t.Errorf("%s %q:\n got %s\nwant %s", c.lang, c.src, got, c.want)
		}
	}
}
