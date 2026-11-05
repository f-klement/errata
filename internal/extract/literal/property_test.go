package literal

import (
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/core"
)

// pieces are what a generated file of l is made of: every opener, closer
// and tag byte of its forms, its words and separators, brackets, numbers,
// names, blanks and every kind of line end.
func pieces(l core.Language) []string {
	out := []string{"\n", "\r\n", "\r", " ", "\t", "\\", "X", "x", "=", ",", ":", ";", ".", "1", "-2.5e3", "0x1F",
		"(", ")", "[", "]", "{", "}", "%", "+", "é", "EOS", "<<", "$", "#{", "${", "\\(", "@"}
	for _, f := range l.Forms {
		out = append(out, f.Open, f.Close, f.Inner, f.Outer, f.Interp)
		if f.Tag != "" {
			out = append(out, f.Tag[:1])
		}
	}
	s := l.Syntax
	for _, words := range [][]string{s.Declare, s.Keywords, s.Rebind, s.Assign, s.Entry, s.Keyword, s.True, s.Null,
		s.Blocks, s.Lead, s.Casts, {s.Do, s.End, s.Repeat, s.Until, s.Return, s.Default, s.ArrayCall, "<?php", "?>"}} {
		for _, w := range words {
			if w != "" {
				out = append(out, w, w+" ")
			}
		}
	}
	for _, p := range s.Prefixes {
		out = append(out, p.Word)
	}
	return out
}

// literalRows are the rows the reader reads.
func literalRows() []core.Language {
	var out []core.Language
	for _, l := range core.Languages() {
		if l.Syntax != nil {
			out = append(out, l)
		}
	}
	return out
}

// checkFile fails t unless reading data as l either is refused or gives a
// tree whose every node lies on a line of the file, and the values a path
// selects from its top do too.
func checkFile(t interface{ Fatalf(string, ...any) }, l core.Language, data []byte) {
	root, err := Parse("f", l, data)
	if err != nil {
		if !strings.HasPrefix(err.Error(), "f: ") {
			t.Fatalf("%s %q: a refusal that names no file: %v", l.Name, data, err)
		}
		return
	}
	last := core.LastLine(data)
	var walk func(n *core.Node, depth int)
	walk = func(n *core.Node, depth int) {
		if n.Line < 1 || n.Line > last {
			t.Fatalf("%s %q: a node %q at line %d, outside the file's %d lines", l.Name, data, n.Text, n.Line, last)
		}
		if depth > core.MaxDepth+2 {
			t.Fatalf("%s %q: nested past the limit", l.Name, data)
		}
		for _, v := range n.Vals {
			walk(v, depth+1)
		}
		for _, v := range n.Items {
			walk(v, depth+1)
		}
	}
	walk(root, 0)
	x, err := Build(Spec{OneFile: core.OneFile{File: "f"}, Path: "$[*]", Language: l.Name})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	vs, err := x.(extractor).FromData(data)
	if err != nil {
		return // a member that is a list or a mapping
	}
	for _, v := range vs {
		if v.At.Line < 1 || v.At.Line > last {
			t.Fatalf("%s %q: value %q at line %d, outside the file's %d lines", l.Name, data, v.Text, v.At.Line, last)
		}
	}
}

// Every value's line lies inside its file, for files made of each row's own
// delimiters, words and line ends; a file the reader cannot read is refused
// by name, never read past.
func TestEveryValueLiesInsideItsFile(t *testing.T) {
	rows := literalRows()
	rapid.Check(t, func(t *rapid.T) {
		l := rapid.SampledFrom(rows).Draw(t, "language")
		parts := rapid.SliceOfN(rapid.SampledFrom(pieces(l)), 0, 60).Draw(t, "pieces")
		checkFile(t, l, []byte(strings.Join(parts, "")))
	})
}

// fuzz runs checkFile over any bytes as the row named name, seeded with its
// planted file and its cases in TestValues.
func fuzz(f *testing.F, name string, seeds ...string) {
	f.Helper()
	l, ok := core.LanguageNamed(name)
	if !ok || l.Syntax == nil {
		f.Fatalf("no literal row %s", name)
	}
	f.Add(planted[name])
	if src, ok := plantedInCode[name]; ok {
		f.Add(src)
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		checkFile(t, l, []byte(src))
	})
}

func FuzzPython(f *testing.F) {
	fuzz(f, "python", "D = {'a': [1, (2,)], **x}\nsetup(name='a', *b)\nif x:\n    D = 1\nS = f'{a}' r'\\d' \\\n  'c'\n",
		"A = f\"{n:#x} {x:'^10} {y!r:>{w}} {(z:=1)} {d['k':'v']}\"\nB = 1\n")
}

func FuzzRuby(f *testing.F) {
	fuzz(f, "ruby", "gem 'a', require: false\nV = <<~A + <<-'B'\n  x\nA\n  y\n  B\nH = {a: 1, 'b' => :c}\nx = 1 if y\nwhile z do\nend\n")
}

func FuzzJavaScript(f *testing.F) {
	fuzz(f, "javascript", "export default defineConfig({ a: [1, 2], b: `x${y}` })\nmodule.exports = { ...c }\n")
}

func FuzzTypeScript(f *testing.F) {
	fuzz(f, "typescript", "export const X: Record<string, number> = { a: 1 } as const;\n")
}

func FuzzPHP(f *testing.F) {
	fuzz(f, "php", "<?php\nreturn ['a' => env('X', 1), 'h' => <<<EOT\n  $y\n  EOT, array(1)];\n?>\n",
		"<p>a</p><?php $a = \"{$b[\"x\"]}\"; // ?>\n$PLANTED = 9;\n")
}

func FuzzGroovy(f *testing.F) {
	fuzz(f, "groovy", "version = '1.0'\napply plugin: 'java'\ndef m = [a: 1, b: [:]]\ns = $/x/$\n")
}

func FuzzKotlin(f *testing.F) {
	fuzz(f, "kotlin", "val v: String = \"\"\"a$b\"\"\".trimIndent()\nconst val P = listOf(1, 2)\n")
}

func FuzzScala(f *testing.F) {
	fuzz(f, "scala", "name := \"x\"\nval v = s\"$a\" + raw\"b\"\nval t = (1, \"a\")\n")
}

func FuzzSwift(f *testing.F) {
	fuzz(f, "swift", "let p = Package(name: \"x\", deps: [.package(url: \"u\", from: \"1\")], m: [:])\nlet s = #\"\"\"\n  a\n  \"\"\"#\n")
}

func FuzzElixir(f *testing.F) {
	fuzz(f, "elixir", "config :app, port: 4000, repo: MyApp.Repo\nx = %{\"a\" => ~s(b), c: ~r/d/}\ndefmodule M do\nend\n")
}

func FuzzLua(f *testing.F) {
	fuzz(f, "lua", "local t = { a = 1, [[\nx]], { 'b' } }\nrepeat\n  y = 1\nuntil z\nreturn t\n",
		"a = \"x\\z\nPLANTED = 9 --\"\ntable.insert(a, 'b')\n")
}

// assignment is how each language assigns value to name as a statement.
func assignment(lang, name, value string) string {
	switch lang {
	case "php":
		return "$" + name + " = " + value + ";"
	case "javascript", "typescript":
		return "const " + name + " = " + value
	case "kotlin", "scala":
		return "val " + name + " = " + value
	case "swift":
		return "let " + name + " = " + value
	}
	return name + " = " + value
}

// Each value lies on the line its statement is written on, for files of
// statements with blank lines, comments and every kind of line end between
// them, the last with no line end after it. Planted break: a scalar's line
// one past its own.
func TestValueLinesAreTheirStatements(t *testing.T) {
	rows := literalRows()
	rapid.Check(t, func(t *rapid.T) {
		l := rapid.SampledFrom(rows).Draw(t, "language")
		comment := map[string]string{"python": "#", "ruby": "#", "elixir": "#", "lua": "--"}[l.Name]
		if comment == "" {
			comment = "//"
		}
		var b strings.Builder
		if l.Name == "php" {
			b.WriteString("<?php\n")
		}
		n := rapid.IntRange(1, 6).Draw(t, "statements")
		want := map[string]int{}
		for i := range n {
			for range rapid.IntRange(0, 2).Draw(t, "gap") {
				b.WriteString(rapid.SampledFrom([]string{"\n", "\r\n", comment + " x = 1\n"}).Draw(t, "between"))
			}
			name := "v" + strconv.Itoa(i)
			value := rapid.SampledFrom([]string{"1", "-2", "0x1F", "'a'", "\"b\"", "true", "f(1)"}).Draw(t, "value")
			want[name] = core.LastLine([]byte(b.String()))
			b.WriteString(assignment(l.Name, name, value))
			if i < n-1 {
				b.WriteString(rapid.SampledFrom([]string{"\n", "\r\n"}).Draw(t, "end"))
			}
		}
		src := b.String()
		root, err := Parse("f", l, []byte(src))
		if err != nil {
			t.Fatalf("%s %q: %v", l.Name, src, err)
		}
		for name, line := range want {
			n := root.Field[name]
			if n == nil || n.Line != line {
				t.Fatalf("%s %q: %s at %+v, want line %d", l.Name, src, name, n, line)
			}
		}
	})
}
