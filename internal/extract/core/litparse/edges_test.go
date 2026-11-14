package litparse

import (
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// cases reads each case's path and fails on a value other than its want.
func cases(t *testing.T, cs []struct{ lang, src, path, want string }) {
	t.Helper()
	for _, c := range cs {
		if got := read(t, c.lang, c.src, c.path); got != c.want {
			t.Errorf("%s %q at %s:\n got %s\nwant %s", c.lang, c.src, c.path, got, c.want)
		}
	}
}

// A string's escapes, its code and its indentation, at their edges: an
// escape at the body's end, Ruby's and PHP's single quotes, an octal
// escape, \x up to 0x7F, \u in braces and its bad forms, \U, a $ that opens
// code only before a name or {, an escaped marker, a heredoc's blank lines
// and the indentation its lines share, and adjacent strings with and
// without a prefix. It kills the mutants of decode.go that the values test
// leaves.
func TestDecodeEdges(t *testing.T) {
	cases(t, []struct{ lang, src, path, want string }{
		{"python", `a = "x\\"`, "a", `x\@1`},
		{"ruby", `a = 'a\\b\'c\d'`, "a", `a\b'c\d@1`},
		{"php", `<?php $a = '\n\\';`, "a", `\n\@1`},
		{"python", `a = "\01"`, "a", `~"\01"@1`},
		{"python", `a = "\0x"`, "a", "\x00x@1"},
		{"python", `a = "\x7f"`, "a", "\x7f@1"},
		{"python", `a = "\x80"`, "a", `~"\x80"@1`},
		{"python", `a = "\x4"`, "a", `~"\x4"@1`},
		{"python", `a = "\xg1"`, "a", `~"\xg1"@1`},
		{"javascript", `const a = "\u{1F600}!"`, "a", "\U0001F600!@1"},
		{"javascript", `const a = "\u{}"`, "a", `~"\u{}"@1`},
		{"javascript", `const a = "\u{110000}"`, "a", `~"\u{110000}"@1`},
		{"javascript", `const a = "\u{12"`, "a", `~"\u{12"@1`},
		{"javascript", `const a = "\u{1234567}"`, "a", `~"\u{1234567}"@1`},
		{"javascript", `const a = "\uD800"`, "a", `~"\uD800"@1`},
		{"python", `a = "\U0001F600"`, "a", "\U0001F600@1"},
		{"python", `a = "\U0001F60"`, "a", `~"\U0001F60"@1`},
		{"python", "a = 'x\\\ny'", "a", "xy@1"},
		{"kotlin", `val a = "$1 $$ a$"`, "a", "$1 $$ a$@1"},
		{"kotlin", `val a = "${x}"`, "a", `~"${x}"@1`},
		{"kotlin", `val a = "\$x"`, "a", "$x@1"},
		{"ruby", `a = "\#{x} #x"`, "a", "#{x} #x@1"},
		{"php", "<?php $a = <<<EOT\n  a\n\n   \n  b\n  EOT;", "a", "a\n\n \nb@1"},
		{"ruby", "a = <<~EOS\n\t  a\n\n\t b\nEOS\n", "a", " a\n\nb\n@1"},
		{"ruby", "a = <<~EOS\na\n  b\nEOS\n", "a", "a\n  b\n@1"},
		{"ruby", "a = <<~EOS\n\n  \nEOS\n", "a", "\n  \n@1"},
		{"swift", "let a = \"\"\"\n  a\n  b\"\"\"", "a", "  a\n  b@1"},
		{"python", `a = 'a' f'b' r'\d'`, "a", `ab\d@1`},
		{"python", `a = 'a' f'{x}'`, "a", `~'a' f'{x}'@1`},
		{"python", `a = 'a' x'b'`, "a", `~'a' x'b'@1`},
		{"python", `a = f'{x}' 'b'`, "a", `~f'{x}' 'b'@1`},
		{"python", "a = b\"\\n\"", "a", "\n@1"},
		{"python", "a = b\"\\u0041\"", "a", "~b\"\\u0041\"@1"},
		{"python", "a = \"\\00\"", "a", "~\"\\00\"@1"},
		{"python", "a = \"\\09\"", "a", "~\"\\09\"@1"},
		{"php", "<?php $a = <<<EOT\na\\\nEOT;\n", "a", "a\\@1"},
	})
}

// A prefix's Interp, where it has one, opens code in the body in place of
// the form's. No row of the table writes one, so a copy of Python's row
// gets a prefix t that opens code at {. Planted break: decoding taking the
// prefix's Interp only when it is empty, which reads t'{x}' as the text
// {x} and u'{x}' as computed.
func TestAPrefixInterpReplacesTheForms(t *testing.T) {
	l, _ := core.LanguageNamed("python")
	syn := *l.Syntax
	syn.Prefixes = append(slices.Clone(syn.Prefixes), core.Prefix{Word: "t", Interp: "{"})
	l.Syntax = &syn
	for _, c := range []struct{ src, want string }{
		{"a = t'{x}'\n", "~t'{x}'"},
		{"a = u'{x}'\n", "{x}"},
	} {
		root, err := Parse("f", l, []byte(c.src))
		if err != nil {
			t.Fatalf("%q: %v", c.src, err)
		}
		got := root.Field["a"]
		if text := got.Text; got.NotLiteral {
			got.Text = "~" + text
		}
		if got.Text != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got.Text, c.want)
		}
	}
}

// PHP's array(...) is a list however many elements it holds, as its short
// form [...] is: one element in it is no value in parentheses. Planted
// break: listNode reading array( as parentheses reads array('x') as the
// scalar x, so its [*] selects nothing.
func TestArrayCallIsAList(t *testing.T) {
	src := "<?php\nreturn ['b' => array('x'), 'c' => array('x', 'y'), 'd' => ['x'], 'e' => array('x',), 'f' => array(array('x')), 'g' => array('k' => 'v')];\n"
	cases(t, []struct{ lang, src, path, want string }{
		{"php", src, "return.b[*]", "x@2"},
		{"php", src, "return.c[*]", "x@2, y@2"},
		{"php", src, "return.d[*]", "x@2"},
		{"php", src, "return.e[*]", "x@2"},
		{"php", src, "return.f[*][*]", "x@2"},
		{"php", src, "return.g.k", "v@2"},
	})
}

// The parser's edges: a statement that goes on over a line, a command's
// arguments, a symbol as a key, [:], parentheses, the depth limit through
// lists and through calls, and an import that binds several names.
func TestParseEdges(t *testing.T) {
	cases(t, []struct{ lang, src, path, want string }{
		{"javascript", "const a = 1 +\n  2\nconst b = 3", "a", "~1 +\n  2@1"},
		{"javascript", "const a = 1\n  .toString()\nconst b = 3", "a", "~1\n  .toString()@1"},
		{"javascript", "const a = 1\nconst b = 3", "b", "3@2"},
		{"ruby", "a = 1\n  and b\n", "a", "~1\n  and b@1"},
		{"ruby", "gem 'a', 'b'\n", "gem[0]['1']", "b@1"},
		{"ruby", "x ['a']\n", "x[0]['0'][0]", "a@1"},
		{"ruby", "x {a: 1}\n", "x", ""},
		{"ruby", "x -1\n", "x[0]['0']", "-1@1"},
		{"ruby", "x - 1\n", "x", ""},
		{"ruby", "x = 1\nputs 'a',\n  'b'\n", "puts[0]['1']", "b@3"},
		{"ruby", "a = {:a => 1}\n", "a.a", "1@1"},
		{"swift", "let e = [:]\n", "e", `error: f: path "e" selects a mapping at line 1, not a scalar; add [*] to take its members`},
		{"swift", "let e = [ : ]\n", "e", `error: f: path "e" selects a mapping at line 1, not a scalar; add [*] to take its members`},
		{"javascript", "const a = (1, 2)\nconst b = (3)\n", "$[*]", "~(1, 2)@1, ~(3)@2"},
		{"python", "a = (1,)\nb = (2)\n", "b", "2@2"},
		{"python", "import a, B\nB = 1\nfrom m import x as Y\nY = 2\n", "$[*]", "~1@2, ~2@4"},
		{"python", "B = 1\nimport a, B\nY = 2\nfrom m import x as Y\n", "$[*]", "~1@1, ~2@3"},
		{"php", "<?php\nreturn = 1;\n", "return", "1@2"},
		{"lua", "x = 1\nreturn x\n", "return", "~x@2"},
	})
	// Each way a value nests counts toward the depth limit: a list, a
	// call, a member's call, a prefixed list and an ArrayCall. Planted
	// break: level - 1 for level + 1 in memberLiteral, prefixedList or the
	// ArrayCall case reads 1001 levels without an error.
	for _, c := range []struct{ lang, head, path, open, close string }{
		{"python", "a = ", "a", "[", "]"},
		{"python", "a = ", "a", "f(", ")"},
		{"swift", "let a = ", "a", ".f(", ")"},
		{"elixir", "a = ", "a", "%{a: ", "}"},
		{"php", "<?php\nreturn ", "return", "array(", ")"},
	} {
		deep := func(n int) string {
			return c.head + strings.Repeat(c.open, n) + "1" + strings.Repeat(c.close, n)
		}
		if got := read(t, c.lang, deep(core.MaxDepth), c.path); strings.HasPrefix(got, "error") && strings.Contains(got, "levels deep") {
			t.Errorf("%s %s nested %d deep: %.120s", c.lang, c.open, core.MaxDepth, got)
		}
		if got := read(t, c.lang, deep(core.MaxDepth+1), c.path); !strings.Contains(got, "nested more than 1000 levels deep") {
			t.Errorf("%s %s nested %d deep: %.120s", c.lang, c.open, core.MaxDepth+1, got)
		}
	}
}

// The tokenizer's edges: the body of a heredoc on a later line is skipped
// as the first one's is, and a number's fraction may start with any digit.
// Planted breaks: resumeLine leaving the line of openers set (1 for -1),
// so the later heredoc's line is never noted and its body reads as code;
// pointDigit taking 0 or 9 for no digit, which ends 1.05 and 0.9 at their
// point.
func TestTokenEdges(t *testing.T) {
	cases(t, []struct{ lang, src, path, want string }{
		{"ruby", "A = <<~X\n  a\nX\nB = <<~Y\n  PLANTED = 9\nY\n", "PLANTED", ""},
		{"ruby", "A = <<~X\n  a\nX\nB = <<~Y\n  PLANTED = 9\nY\n", "B", "PLANTED = 9\n@4"},
		{"python", "X = 1.05\nY = 0.9\n", "$[*]", "1.05@1, 0.9@2"},
	})
}
