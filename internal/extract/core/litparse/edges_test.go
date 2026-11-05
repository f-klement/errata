package litparse

import (
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
	deep := func(n int, open, close string) string {
		return "a = " + strings.Repeat(open, n) + "1" + strings.Repeat(close, n)
	}
	for _, c := range []struct{ open, close string }{{"[", "]"}, {"f(", ")"}} {
		if got := read(t, "python", deep(core.MaxDepth, c.open, c.close), "a"); strings.HasPrefix(got, "error") && strings.Contains(got, "levels deep") {
			t.Errorf("%s nested %d deep: %.120s", c.open, core.MaxDepth, got)
		}
		if got := read(t, "python", deep(core.MaxDepth+1, c.open, c.close), "a"); !strings.Contains(got, "nested more than 1000 levels deep") {
			t.Errorf("%s nested %d deep: %.120s", c.open, core.MaxDepth+1, got)
		}
	}
}
