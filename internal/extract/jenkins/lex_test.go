package jenkins

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// tokens is the lexer's reading of src, one token a word: n for a name,
// 1 for a number, s<form> and its text for a string, the operator itself,
// ; for a line end, @ for an annotation; or the error.
func tokens(src string) string {
	l := newLexer([]byte(src), nil)
	var out []string
	for {
		t, err := l.next()
		if err != nil {
			return strings.Join(out, " ") + " error: " + err.Error()
		}
		switch t.kind {
		case tokEOF:
			return strings.Join(out, " ")
		case tokNewline:
			out = append(out, ";")
		case tokName:
			out = append(out, "n")
		case tokNumber:
			out = append(out, "1")
		case tokString:
			out = append(out, fmt.Sprintf("s%d%s", t.quote, t.text([]byte(src))))
		case tokAt:
			out = append(out, "@")
		default:
			out = append(out, t.op)
		}
	}
}

// The lexer knows Groovy's comments and string forms: what a comment or a
// string holds is no token, a / divides after a value and starts a slashy
// string elsewhere, and a string's ${ } may hold code with strings of its
// own, at any depth.
func TestLexerTokens(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{"a = 'b' // c = 'd'\n", "n = s1'b' ;"},
		{"a /* = 'x' \n */ b", "n n"},
		{"#!/usr/bin/env groovy\na", "; n"},
		{"x #!", "n # !"},
		{`a = "x ${ "y ${ 'z' } }" } w" + 1`, `n = s2"x ${ "y ${ 'z' } }" } w" + 1`},
		{`a = "${ m.collect { "{" } }"`, `n = s2"${ m.collect { "{" } }"`},
		{`a = "$b.c.d end"`, `n = s2"$b.c.d end"`},
		{`a = '${' + "}"`, `n = s1'${' + s2"}"`},
		{"a = '''x\n'y''\n'''", "n = s3'''x\n'y''\n'''"},
		{`a = """x"y"" \""" """`, `n = s4"""x"y"" \""" """`},
		{`a = 'it\'s'`, `n = s1'it\'s'`},
		{`a / b / c`, `n / n / n`},
		{`a = /b/ + c`, `n = s5/b/ + n`},
		{`f(/x\/y/)`, `n ( s5/x\/y/ )`},
		{`x = 4 / 2`, `n = 1 / 1`},
		{`(a) / 2`, `( n ) / 1`},
		{`return /re/`, `n / n /`},
		{`final /re/`, `n s5/re/`},
		{`a = $/x\/$ ; b = $/y/$`, `n = s6$/x\/$ ; n = s6$/y/$`},
		{"(a\n/ 2)\n", "( n ; / 1 ) ;"},
		{"[a\n/ 2]", "[ n ; / 1 ]"},
		{"f(a?[b\n/ 2])", "n ( n ?[ n ; / 1 ] )"},
		{"( { a\n/re/ } )", "( { n ; s5/re/ } )"},
		{"( { } a\n/ 2)", "( { } n ; / 1 )"},
		{"(\"${ a\n/re/ }\"\n/ 2)", "( s2\"${ a\n/re/ }\" ; / 1 )"},
		{"a \\\r\n b", "n n"},
		{"a = 'p\\\r\nq'", "n = s1'p\\\r\nq'"},
		{"a = '''${'''", "n = s3'''${'''"},
		{"a = $/ $a$/$", "n = s6$/ $a$/$"},
		{`a = "${ f { 1 } + "x" }"`, `n = s2"${ f { 1 } + "x" }"`},
		{"a\n/re/", "n ; s5/re/"},
		{`a = $/x/y$/$$/$`, `n = s6$/x/y$/$$/$`},
		{`a = $/x $b ${c} /$`, `n = s6$/x $b ${c} /$`},
		{`$x / 2`, `n / 1`},
		{"a \\\n b", "n n"},
		{`a == b`, `n == n`},
		{`a ==~ b`, `n ==~ n`},
		{`a =~ /x/`, `n =~ s5/x/`},
		{`a?.b`, `n ?. n`},
		{`@Library('x') _`, `@ n ( s1'x' ) n`},
		{`1.5e+3 1..2 0x1F 10L`, `1 1 .. 1 1 1`},
		{"é = 'v'", "n = s1'v'"},
		{"a = 'b\nc'", "n = error: line 1: a string in ' ends at the line's end; Groovy reads a string over several lines only in ''' or \"\"\""},
		{"a = \"b", "n = error: line 1: a string is not closed: errata looked for its closing \" to the end of the file"},
		{"a = /* x", "n = error: line 1: a /* comment is not closed"},
		{"\n\na = \"${ b ", "; ; n = error: line 3: a ${ in a string is not closed: errata looked for its } to the end of the file"},
		{"a = '''\n\n", "n = error: line 1: a string is not closed: errata looked for its closing ''' to the end of the file"},
		// Round 2: the correctness review's lexer breaks that no test
		// caught, a comment with nothing in it, a $ at the end of the file,
		// and $$ before ${, which is a $ and then the ${.
		{"f({ a = \"${b}\"\n/'/ })", "n ( { n = s2\"${b}\" ; s5/'/ } )"},
		{"y = (a)\n/'/", "n = ( n ) ; s5/'/"},
		{"y = c ?[1] : [2]\n/'/", "n = n ?[ 1 ] : [ 1 ] ; s5/'/"},
		{`case /re/`, `n / n /`},
		{`in /re/`, `n / n /`},
		{`do /re/`, `n s5/re/`},
		{"a /**/ b", "n n"},
		{"a = \"$", "n = error: line 1: a string is not closed: errata looked for its closing \" to the end of the file"},
		{`a = "$${'"'}"`, `n = s2"$${'"'}"`},
		{"a )", "n error: line 1: a ) closes no ( or ["},
		{"(a)\n]", "( n ) ; error: line 2: a ] closes no ( or ["},
		// Round 3: a } at the top keeps the top's count of brackets, which a
		// line end then reads; the delta review's R2-N3, a survivor.
		{"}\nx", "} ; n"},
	} {
		if got := tokens(c.src); got != c.want {
			t.Errorf("%q:\n got %s\nwant %s", c.src, got, c.want)
		}
	}
}

// Braces in code nest at most core.MaxDepth levels in the lexer too,
// which keeps a count of brackets for each. The planted break: no depth
// test, which read 100000 levels.
func TestLexerRefusesDeepBraces(t *testing.T) {
	if got := tokens(strings.Repeat("{", core.MaxDepth)); strings.Contains(got, "error") {
		t.Errorf("at the limit: %.200s", got)
	}
	want := fmt.Sprintf("error: line 1: nested more than %d levels deep, the limit; %s", core.MaxDepth, core.OtherWay)
	for _, n := range []int{core.MaxDepth + 1, 100000} {
		if got := tokens(strings.Repeat("{", n)); !strings.HasSuffix(got, want) {
			t.Errorf("%d levels: %s", n, got[max(0, len(got)-200):])
		}
	}
}

// A string's ${ } nests at most core.MaxDepth levels, counted as strings
// and the code inside them, and the lexer holds no more than that stack.
// The planted break: no depth test, which read 100000 levels.
func TestLexerRefusesDeepStrings(t *testing.T) {
	deep := func(n int) string {
		return "a = " + strings.Repeat(`"${`, n) + strings.Repeat(`}"`, n) + "\n"
	}
	if got := tokens(deep(core.MaxDepth / 2)); !strings.HasPrefix(got, "n = s2") {
		t.Errorf("at the limit: %.200s", got)
	}
	want := fmt.Sprintf("n = error: line 1: nested more than %d levels deep, the limit; %s", core.MaxDepth, core.OtherWay)
	if got := tokens(deep(core.MaxDepth/2 + 1)); got != want {
		t.Errorf("past the limit: %.300s", got)
	}
	if got := tokens(deep(100000)); got != want {
		t.Errorf("far past the limit: %.300s", got)
	}
}

// The lexer's helpers at the edges of what they accept: the first and
// last letter, digit and byte of each range, a name or number that ends
// the file, and an escape cut off by its end.
func TestLexerHelpers(t *testing.T) {
	for c, want := range map[byte]bool{
		'a': true, 'z': true, 'A': true, 'Z': true, '_': true, '$': true, 0x80: true, 0xff: true,
		'`': false, '{': false, '@': false, '[': false, '0': false, '9': false, 0x7f: false, '.': false,
	} {
		if isNameStart(c) != want {
			t.Errorf("isNameStart(%q) is %t", c, !want)
		}
	}
	for _, c := range []struct {
		f    func([]byte, int) int
		name string
		src  string
		want int
	}{
		{nameEnd, "nameEnd", "a0 ", 2},
		{nameEnd, "nameEnd", "a9", 2},
		{nameEnd, "nameEnd", "ab.c", 2},
		{dottedEnd, "dottedEnd", "a.b c", 3},
		{dottedEnd, "dottedEnd", "a.", 1},
		{dottedEnd, "dottedEnd", "a.1", 1},
		{dottedEnd, "dottedEnd", "a.$b", 1},
		{dottedEnd, "dottedEnd", "a.b.c", 5},
		{numberEnd, "numberEnd", "19 ", 2},
		{numberEnd, "numberEnd", "1e+5", 4},
		{numberEnd, "numberEnd", "1E+5", 4},
		{numberEnd, "numberEnd", "1e-5", 4},
		{numberEnd, "numberEnd", "1e+0", 4},
		{numberEnd, "numberEnd", "1e+9", 4},
		{numberEnd, "numberEnd", "1e+", 2},
		{numberEnd, "numberEnd", "1e+a", 2},
		{numberEnd, "numberEnd", "1.", 1},
		{numberEnd, "numberEnd", "1.0", 3},
		{numberEnd, "numberEnd", "1.9", 3},
		{numberEnd, "numberEnd", "1.a", 1},
		{numberEnd, "numberEnd", "1$", 1},
		{lineEnd, "lineEnd", "\n", 0},
		{lineEnd, "lineEnd", "ab", 2},
	} {
		if got := c.f([]byte(c.src), 0); got != c.want {
			t.Errorf("%s(%q) is %d, want %d", c.name, c.src, got, c.want)
		}
	}
	for _, c := range []struct {
		src  string
		q    quote
		want int
	}{
		{"\\\r", double, 2},
		{"\\\r\n", double, 3},
		{"\\\rx", single, 2},
		{"\\", double, 0},
		{"\\/", slashy, 2},
		{"\\x", slashy, 0},
		{"$$", dollarSlashy, 2},
		{"$/", dollarSlashy, 2},
		{"$x", dollarSlashy, 0},
	} {
		if got := escape([]byte(c.src), 0, c.q); got != c.want {
			t.Errorf("escape(%q, %d) is %d, want %d", c.src, c.q, got, c.want)
		}
	}
}
