package literal

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// A method called without parentheses changes its name, as one called with
// them does, in each row that calls without them. Review 1, finding 3.
// Planted break: the member's taint in a row with Command dropped.
func TestCommandMethodCallsChangeTheirName(t *testing.T) {
	cases(t, []struct{ lang, src, path, want string }{
		{"ruby", "LIST = ['a']\nLIST.push 'b'\nH = {a: 1}\nH.merge! b: 2\nS = 'x'\nS.replace 'y'\nK = 1\n", "$[*]", "~['a']@1, ~{a: 1}@3, ~'x'@5, 1@7"},
		{"groovy", "deps = ['a']\ndeps.add 'b'\nv = '1'\n", "$[*]", "~['a']@1, 1@3"},
		{"elixir", "x = [1]\nx.foo 2\ny = 3\n", "$[*]", "~[1]@1, 3@3"},
		{"python", "L = ['a']\nL.copy\nM = 1\n", "L[*]", "a@1"},
	})
}

// A regular expression holding a quote reads as one, so it does not turn a
// string's text into code: a value planted there is not read. Review 1,
// finding 4. Planted break: the regex forms left out of the rows.
func TestRegexQuotesHoldNoValue(t *testing.T) {
	cases(t, []struct{ lang, src, path, want string }{
		{"ruby", "R = /\"/\nS = \"\nPLANTED = 9\n\"\nT = /\"/\n", "PLANTED", ""},
		{"ruby", "R = /\"/\nS = \"\nPLANTED = 9\n\"\nT = /\"/\n", "R", "~/\"/@1"},
		{"javascript", "const R = /'/\nconst S = '\\\nPLANTED = 9'\nconst T = /'/\n", "PLANTED", ""},
		{"javascript", "module.exports = { test: /\\.css$/i, use: 'css' }\n", "module.exports.use", "css@1"},
		{"typescript", "const r = x.replace(/[\"']/g, '')\nconst y = 1\n", "y", "1@2"},
		{"groovy", "def p = ~/\"/\nS = \"\"\"\nPLANTED = 9\n\"\"\"\n", "PLANTED", ""},
	})
}

// Each row decodes the escapes its language decodes, and any other makes
// the string computed: Python keeps \e, \$, \/ and \# as written, and its
// bytes literals decode no \u; JavaScript drops the backslash of \a and \e
// and has no \U; Swift writes \u only with braces; Lua's \z skips blanks.
// Review 1, finding 6. Planted break: unescape asked with every row's
// escapes allowed.
func TestEscapesAreEachLanguages(t *testing.T) {
	cases(t, []struct{ lang, src, path, want string }{
		{"python", `a = '\e'`, "a", `~'\e'@1`},
		{"python", `a = '\$'`, "a", `~'\$'@1`},
		{"python", `a = '\/'`, "a", `~'\/'@1`},
		{"python", `a = '\#'`, "a", `~'\#'@1`},
		{"python", `a = b'\u0041'`, "a", `~b'\u0041'@1`},
		{"python", `a = '\u0041\U00000042\x43'`, "a", "ABC@1"},
		{"javascript", `const a = "\a"`, "a", `~"\a"@1`},
		{"javascript", `const a = "\e"`, "a", `~"\e"@1`},
		{"javascript", `const a = "\U00000041"`, "a", `~"\U00000041"@1`},
		{"javascript", "const a = `\\$\\``", "a", "$`@1"},
		{"ruby", `a = "\e\s\#"`, "a", "\x1b #@1"},
		{"swift", `let a = "\u{41}"`, "a", "A@1"},
		{"swift", `let a = "\u0041"`, "a", `~"\u0041"@1`},
		{"php", `<?php $a = "\$\e\u{41}";`, "a", "$\x1bA@1"},
		{"php", `<?php $a = "\'";`, "a", `~"\'"@1`},
		{"kotlin", `val a = "\$\b"`, "a", "$\b@1"},
		{"kotlin", `val a = "\a"`, "a", `~"\a"@1`},
		{"lua", `a = "x\z   y"`, "a", "xy@1"},
		{"lua", `a = "\65"`, "a", `~"\65"@1`},
	})
}

// A Python statement after a ; on its line is read, and one after a : on
// the line of an if is part of the block, which changes the name. Review
// 1, finding 9. Planted break: a statement in Python's column 0 test taken
// without first.
func TestPythonSemicolons(t *testing.T) {
	cases(t, []struct{ lang, src, path, want string }{
		{"python", "X = 1; Y = 2\n", "$[*]", "1@1, 2@1"},
		{"python", "Y = 1\nif x: Y = 2\n", "Y", "~1@1"},
		{"python", "if x: Z = 2\n", "Z", ""},
	})
}

// Ruby's __END__ ends the code: the data after it is not read. Review 1,
// finding 10.
func TestRubyEnd(t *testing.T) {
	cases(t, []struct{ lang, src, path, want string }{
		{"ruby", "X = 1\n__END__\nPLANTED = 9\n", "$[*]", "1@1"},
		{"ruby", "X = __END__\n", "X", "~__END__@1"},
	})
}

// A name handed first to a function the row lists as changing it, such as
// Lua's table.insert, is computed; Python's case binds the name it
// captures. Review 1, findings 11 and 13. Planted break: the Mutators call
// not marking the argument.
func TestNamesHandedToAFunction(t *testing.T) {
	cases(t, []struct{ lang, src, path, want string }{
		{"lua", "deps = { 'a' }\ntable.insert(deps, 'b')\nv = 1\n", "$[*]", "~{ 'a' }@1, 1@3"},
		{"lua", "deps = { 'a' }\nprint(deps)\n", "deps[*]", "a@1"},
		{"lua", "deps = { 'a' }\ntable.insert(x, deps)\n", "deps[*]", "a@1"},
		{"python", "X = 1\nmatch y:\n    case X:\n        pass\n", "X", "~1@1"},
	})
}

// A dotted name of more parts than the depth limit is refused, and one at
// the limit read. Review 1, finding 16. Planted break: the parts not
// counted.
func TestDottedNamesAreBounded(t *testing.T) {
	name := func(n int) string { return strings.TrimSuffix(strings.Repeat("a.", n), ".") }
	if got := read(t, "ruby", name(core.MaxDepth)+" = 1\n", "a"); strings.HasPrefix(got, "error: f: line 1: nested") {
		t.Errorf("a name of %d parts: %.100s", core.MaxDepth, got)
	}
	if got := read(t, "ruby", name(core.MaxDepth+1)+" = 1\n", "a"); !strings.Contains(got, "nested more than 1000 levels deep") {
		t.Errorf("a name of %d parts: %.100s", core.MaxDepth+1, got)
	}
}

// Delta review 1's cases through the reader: an f-string with a format
// spec is computed, not refused; PHP decodes \u only in braces; a division
// on a line of its own is code; a PHP file that opens with <?xml has text
// before its code; Scala's plain "${" is a string.
func TestDelta1Values(t *testing.T) {
	cases(t, []struct{ lang, src, path, want string }{
		{"python", "A = f\"{x:#x}\"\nB = 1\n", "$[*]", "~f\"{x:#x}\"@1, 1@2"},
		{"python", "A = f\"{x:'^10}\"\nB = 1\n", "$[*]", "~f\"{x:'^10}\"@1, 1@2"},
		{"python", "A = f\"{x!r:>10} {y:{w}.{p}f}\"\nB = 2\n", "B", "2@2"},
		{"php", `<?php $a = "\u0041";`, "a", `~"\u0041"@1`},
		{"php", `<?php $a = "\u{41}";`, "a", "A@1"},
		{"javascript", "const A = (1\n  / 2)\nconst B = 3\n", "$[*]", "~(1\n  / 2)@1, 3@3"},
		{"php", "<?xml version=\"1.0\"?>\n<?php $a = 1;", "a", "error: f: line 1: text that is not code stands here, outside <?php ... ?>, so the literal reader lost its place and cannot tell values from code after it (docs/reference.md#literal)"},
		{"scala", "val a = \"${\"\nval b = s\"${x}\"\nval c = s\"plain\"\n", "$[*]", "${@1, ~s\"${x}\"@2, plain@3"},
	})
}

// Delta review 2's cases through the reader: the two pygments lines, raw
// f-strings whose \{{ is a backslash and a brace, are computed, and the
// statement after them is read; Ruby's percent literals are strings or
// computed, by their letter. Planted break: braceCode's escape skipping
// the brace.
func TestDelta2Values(t *testing.T) {
	cases(t, []struct{ lang, src, path, want string }{
		{"python", "X = rf'(\\{{[{_dash}])(![^}}]*)(\\}}?)'\nY = 1\n", "Y", "1@2"},
		{"python", "X = rf'(\\{{%)(-?\\s*)(filter)(\\s+)({_ident_inner})'\nY = 2\n", "$[*]", "~rf'(\\{{%)(-?\\s*)(filter)(\\s+)({_ident_inner})'@1, 2@2"},
		{"ruby", "A = %q(a 'b' #c)\nB = %w[x y]\nC = %Q{#{z}}\nD = %r{^/([a-z]):/}i\n", "$[*]", "a 'b' #c@1, ~%w[x y]@2, ~%Q{#{z}}@3, ~%r{^/([a-z]):/}i@4"},
	})
}

// Delta review 3's cases through the reader: Ruby's :/ is the symbol /,
// and the statement after it is read; a variable named p divides; a
// bracket after an operator's symbol is the list's. Planted breaks: the
// operator's symbol not read; [ and ] read as an operator's bytes; a
// symbol read straight after a quote.
func TestDelta3Values(t *testing.T) {
	cases(t, []struct{ lang, src, path, want string }{
		{"ruby", "X = :/\nY = 1\nZ = :<=>\n", "$[*]", "/@1, 1@2, <=>@3"},
		{"ruby", "p = 4\nq = p / 2\nr = 'it'\n", "r", "it@3"},
		{"ruby", "X = a \\\n  / b\nY = 2\n", "Y", "2@3"},
		// Delta 4's: a bracket closes or opens a list after an operator's
		// symbol, and stands in one only as [] or []=.
		{"ruby", "X = [:+, :-, :*]\n", "X[*]", "+@1, -@1, *@1"},
		{"ruby", "X = [:<=>, :[], :[]=]\n", "X[*]", "<=>@1, []@1, []=@1"},
		{"ruby", "X = {\"a\":[1, 2]}\n", "X.a[*]", "1@1, 2@1"},
		// Delta 5's H1: a : straight after a quote ends a label, and opens
		// no symbol, of an operator or of a name.
		{"ruby", "X = {\"b\":[], \"c\":1}\n", "X.c", "1@1"},
		{"ruby", "X = {\"b\":-1}\n", "X.b", "-1@1"},
		{"ruby", "X = {'b':c}\n", "X.b", "~c@1"},
	})
}
