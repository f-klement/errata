package litparse

import (
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// read is what the literal extractor reads at path in src, a file of lang:
// each value as text@line, a computed one after a ~, joined by ", ", or
// "error: " and the message.
func read(t *testing.T, lang, src, path string) string {
	t.Helper()
	l, ok := core.LanguageNamed(lang)
	p, err := core.CheckPath("literal", path)
	if !ok || err != nil {
		t.Fatalf("language %s, path %s: %v", lang, path, err)
	}
	root, err := Parse("f", l, []byte(src))
	if err != nil {
		return "error: " + err.Error()
	}
	vs, err := core.Source{File: "f", Limit: core.DefaultMaxBytes}.Values(p, root)
	if err != nil {
		return "error: " + err.Error()
	}
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Text + "@" + strconv.Itoa(v.At.Line)
		if v.NotLiteral {
			out[i] = "~" + out[i]
		}
	}
	return strings.Join(out, ", ")
}

// planted is, for each language the reader reads, a file that assigns X
// once, and writes an assignment of PLANTED, or planted in Elixir, in a
// comment, a block comment where the language has one, a string, and a
// heredoc, or for a language with no heredoc its string over lines. Each
// file also assigns S and H that string and that heredoc.
var planted = map[string]string{
	"python": "X = 1\n# PLANTED = 9\nS = 'PLANTED = 9'\nH = \"\"\"\nPLANTED = 9\n\"\"\"\n",
	"ruby": "X = 1\n# PLANTED = 9\n=begin\nPLANTED = 9\n=end\nS = 'PLANTED = 9'\n" +
		"H = <<~EOS\n  PLANTED = 9\nEOS\n",
	"javascript": "const X = 1\n// PLANTED = 9\n/*\nPLANTED = 9\n*/\nconst S = 'PLANTED = 9'\nconst H = `\nPLANTED = 9\n`\n",
	"typescript": "const X: number = 1\n// PLANTED = 9\n/*\nPLANTED = 9\n*/\nconst S = 'PLANTED = 9'\nconst H = `\nPLANTED = 9\n`\n",
	"php": "<?php\n$X = 1;\n// PLANTED = 9\n# PLANTED = 9\n/*\n$PLANTED = 9;\n*/\n$S = '$PLANTED = 9';\n" +
		"$H = <<<'EOT'\n  $PLANTED = 9;\n  EOT;\n",
	"groovy": "X = 1\n// PLANTED = 9\n/*\nPLANTED = 9\n*/\nS = 'PLANTED = 9'\nH = '''\nPLANTED = 9\n'''\n",
	"kotlin": "val X = 1\n// PLANTED = 9\n/*\nPLANTED = 9\n*/\nval S = \"PLANTED = 9\"\nval H = \"\"\"\nPLANTED = 9\n\"\"\"\n",
	"scala":  "val X = 1\n// PLANTED = 9\n/*\nPLANTED = 9\n*/\nval S = \"PLANTED = 9\"\nval H = \"\"\"\nPLANTED = 9\n\"\"\"\n",
	"swift":  "let X = 1\n// PLANTED = 9\n/*\nPLANTED = 9\n*/\nlet S = \"PLANTED = 9\"\nlet H = \"\"\"\n  PLANTED = 9\n  \"\"\"\n",
	"elixir": "x = 1\n# planted = 9\ns = \"planted = 9\"\nh = \"\"\"\n  planted = 9\n  \"\"\"\n",
	"lua":    "X = 1\n-- PLANTED = 9\n--[[\nPLANTED = 9\n]]\nS = 'PLANTED = 9'\nH = [[\nPLANTED = 9\n]]\n",
}

// plantedInCode is, for each language that interpolates, a file whose
// string holds code that holds a string, written over lines, with an
// assignment of PLANTED in it: review 1's probes. The outer string is
// computed and the planted name is not read.
var plantedInCode = map[string]string{
	"javascript": "const A = `${ `\nPLANTED = 9\n` }`\nconst X = 1\n",
	"typescript": "export const A = `${ `\nexport const PLANTED = 9\n` }`\nexport const X = 1\n",
	"ruby":       "A = \"#{ \"\nPLANTED = 9\n\" }\"\nX = 1\n",
	"elixir":     "a = \"#{ \"\nplanted = 9\n\" }\"\nx = 1\n",
	"python":     "A = f\"\"\"{ \"\"\"\nPLANTED = 9\n\"\"\" }\"\"\"\nX = 1\n",
	"kotlin":     "val A = \"\"\"${ \"\"\"\nval PLANTED = 9\n\"\"\" }\"\"\"\nval X = 1\n",
	"groovy":     "def A = \"\"\"${ \"\"\"\nPLANTED = 9\n\"\"\" }\"\"\"\nX = 1\n",
	"scala":      "val A = s\"\"\"${ \"\"\"\nval PLANTED = 9\n\"\"\" }\"\"\"\nval X = 1\n",
	"swift":      "let A = \"\"\"\n\\( \"\"\"\n  let PLANTED = 9\n  \"\"\" )\n\"\"\"\nlet X = 1\n",
}

// Each language reads the assignment written as code and none of the ones
// planted in a comment, a string or a heredoc: the planted name is absent,
// and the string and the heredoc are literal strings that hold it. Nor
// does it read one planted in a string inside an interpolation: the string
// around the code is computed. Planted breaks: the tokenizer reading a
// comment's span as code; the lexer's walk of an interpolation skipped.
func TestPlantedLiteralsAreNotRead(t *testing.T) {
	for name, src := range plantedInCode {
		x, a, planted := "X", "A", "PLANTED"
		if name == "elixir" {
			x, a, planted = "x", "a", "planted"
		}
		if got := read(t, name, src, planted); got != "" {
			t.Errorf("%s: %s, planted in a string inside an interpolation, read as %s", name, planted, got)
		}
		if got := read(t, name, src, a); !strings.HasPrefix(got, "~") {
			t.Errorf("%s: %s is %s, want it computed", name, a, got)
		}
		if got := read(t, name, src, x); got != "1@"+strconv.Itoa(strings.Count(src, "\n")) {
			t.Errorf("%s: %s is %s, want 1 on the last line", name, x, got)
		}
	}
	for _, l := range literalRows(false) {
		src, ok := planted[l.Name]
		if !ok {
			t.Errorf("%s: no planted file", l.Name)
			continue
		}
		x, s, h, p := "X", "S", "H", "PLANTED"
		if l.Name == "elixir" {
			x, s, h, p = "x", "s", "h", "planted"
		}
		line := "1"
		if l.Name == "php" {
			line = "2"
		}
		if got := read(t, l.Name, src, x); got != "1@"+line {
			t.Errorf("%s: %s is %s, want 1@%s", l.Name, x, got, line)
		}
		if got := read(t, l.Name, src, p); got != "" {
			t.Errorf("%s: %s, planted in comments and strings, read as %s", l.Name, p, got)
		}
		for _, name := range []string{s, h} {
			got := read(t, l.Name, src, name)
			if strings.HasPrefix(got, "~") || !strings.Contains(got, p+" = 9") {
				t.Errorf("%s: %s is %s, want a literal string that holds the planted assignment", l.Name, name, got)
			}
		}
	}
}

// What each language's literals read as: their text, their line, and
// whether the file computes them.
func TestValues(t *testing.T) {
	for _, c := range []struct{ lang, src, path, want string }{
		// Python: Django's settings, a call's keyword arguments, prefixes.
		{"python", "DATABASES = {\n  'default': {'ENGINE': 'django.db', 'PORT': 5432},\n}\n", "DATABASES.default.PORT", "5432@2"},
		{"python", "X = os.path.join(BASE, 'a')\n", "X", "~os.path.join(BASE, 'a')@1"},
		{"python", "setup(name='a', version='1.0')\n", "setup[0].version", "1.0@1"},
		{"python", "S = f'{x}'\nT = f'plain'\nR = r'\\d' 'e'\nB = b'x'\n", "$[*]", "~f'{x}'@1, plain@2, \\de@3, x@4"},
		{"python", "L = [1, -2, 3.5, None, True, 0x1F]\n", "L[*]", "1@1, -2@1, 3.5@1, None@1, True@1, 0x1F@1"},
		{"python", "T = (1,)\nG = (2)\nE = ()\n", "$[*]", "error: f: path \"$[*]\" selects a sequence at line 1, not a scalar; add [*] to take its members"},
		{"python", "G = (2)\nN = 'a' + 'b'\nC = 1 if x else 2\n", "$[*]", "2@1, ~'a' + 'b'@2, ~1 if x else 2@3"},
		{"python", "X: int = 8080\nY: Final[str] = 'a'\n", "$[*]", "8080@1, a@2"},
		{"python", "S = ('a'\n     'b')\n", "S", "ab@1"},
		// A lone CR ends a statement and a line, as core/lineend says.
		{"python", "A = 1\rB = 2\r\nC = 3", "B", "2@2"},
		{"python", "X = 'a\\n\\t\\u00e9\\x41'\nY = 'a\\d'\n", "$[*]", "a\n\té" + "A@1, ~'a\\d'@2"},
		{"python", "X = '\\xe9'\n", "X", "~'\\xe9'@1"},
		{"python", "X = {'a': 1, 'a': 2}\nY = {a: 1}\nZ = {1, 2}\n", "X", "~{'a': 1, 'a': 2}@1"},
		{"python", "X = {'a': 1, 'a': 2}\nY = {a: 1}\nZ = {1, 2}\n", "Y", "~{a: 1}@2"},
		{"python", "X = {'a': 1, 'a': 2}\nY = {a: 1}\nZ = {1, 2}\n", "Z[*]", "1@3, 2@3"},
		// Ruby: a Gemfile's calls, symbols, a heredoc, modifiers and blocks.
		{"ruby", "gem 'rails', '~> 7.1', require: false\ngem 'pg'\n", "gem[*]['0']", "rails@1, pg@2"},
		{"ruby", "gem 'rails', '~> 7.1', require: false\n", "gem[0].require", "false@1"},
		{"ruby", "V = <<~EOS\n  hello\n    world\nEOS\nW = <<-'X'\n  #{a}\n  X\n", "$[*]", "hello\n  world\n@1,   #{a}\n@5"},
		{"ruby", "V = <<~EOS\n  #{a}\nEOS\nC = <<~`EOS`\nls\nEOS\n", "$[*]", "~<<~EOS@1, ~<<~`EOS`@4"},
		{"ruby", "X = 1 if y\nH = { a: 1, 'b' => :c }\n", "H[*]", "1@2, c@2"},
		{"ruby", "X = 1 if y\n", "X", "~1 if y@1"},
		{"ruby", "V = 1\nif x\n  V = 2\nend\nW = 3\n", "$[*]", "~1@1, 3@5"},
		{"ruby", "V = 1\nwhile x do\n  y\nend\nW = 2\n", "$[*]", "1@1, 2@5"},
		{"ruby", "V = 'a\\nb' \"c\\n\"\nW = `ls`\n", "$[*]", "a\\nbc\n@1, ~`ls`@2"},
		{"ruby", "LIST = []\nLIST << 'a'\n", "LIST", "~[]@1"},
		{"ruby", "LIST = ['b']\nLIST << 'a'\n", "LIST[*]", ""},
		// JavaScript and TypeScript: a Vite config, module.exports, casts.
		{"javascript", "export default defineConfig({ server: { port: 3000 } })\n", "default['0'].server.port", "3000@1"},
		{"javascript", "module.exports = { a: [1, 2] }\n", "module.exports.a[*]", "1@1, 2@1"},
		{"javascript", "const x = `${a}`\nconst y = `b`\nlet z = undefined\n", "$[*]", "~`${a}`@1, b@2, undefined@3"},
		{"javascript", "const o = { a, b: 1 }\nconst p = { ...q }\n", "$[*]", "~{ a, b: 1 }@1, ~{ ...q }@2"},
		{"typescript", "export const X: number = 1;\nexport const C = { a: 'b' } as const\n", "C.a", "b@2"},
		{"typescript", "export const C = { a: 'b' } satisfies T;\n", "C.a", "b@1"},
		// PHP: a Laravel config file.
		{"php", "<?php\nreturn [\n  'name' => env('APP', 'L'),\n  'debug' => TRUE,\n];\n", "return.name['1']", "L@3"},
		{"php", "<?php\nreturn array('a' => 1, 'b' => [1, 2]);\n", "return.b[*]", "1@2, 2@2"},
		{"php", "<?php\n$x = 'a\\n';\n$y = \"a\\n\";\n$z = \"$y\";\ndefine('V', '1.0');\n", "$[*]", "error: f: path \"$[*]\" selects a sequence at line 5, not a scalar; add [*] to take its members"},
		{"php", "<?php\n$x = 'a\\n';\n$y = \"a\\n\";\n$z = \"$y\";\n", "$[*]", "a\\n@2, a\n@3, ~\"$y\"@4"},
		{"php", "<?php\n$h = <<<EOT\n    a\n      b\n    EOT;\n", "h", "a\n  b@2"},
		{"php", "<?php\n$a = [1, 'k' => 2];\n", "a", "~[1, 'k' => 2]@2"},
		{"php", "<?php\n$a = 1;\n?>\n", "a", "1@2"},
		// Groovy: a Gradle build.
		{"groovy", "version = '1.0'\napply plugin: 'java'\nimplementation 'a:b:1'\n", "$[*]", "error: f: path \"$[*]\" selects a sequence at line 2, not a scalar; add [*] to take its members"},
		{"groovy", "version = '1.0'\napply plugin: 'java'\n", "apply[0].plugin", "java@2"},
		{"groovy", "def x = \"$y\"\ndef m = [a: 1]\ndef e = [:]\n", "$[*]", "error"},
		{"groovy", "def x = \"$y\"\ndef m = [a: 1]\n", "m.a", "1@2"},
		{"groovy", "plugins {\n  version = '2'\n}\nversion = '1'\n", "version", "~'1'@4"},
		// Kotlin, Scala and Swift.
		{"kotlin", "val v = \"1.0\"\nconst val P = 8080\nval t = \"\"\"a\"\"\".trimIndent()\n", "$[*]", "1.0@1, 8080@2, ~\"\"\"a\"\"\".trimIndent()@3"},
		{"kotlin", "val v = listOf(1, 2)\nval m = mapOf(\"a\" to 1)\n", "$[*]", "error"},
		{"kotlin", "val v = listOf(1, 2)\n", "v['1']", "2@1"},
		{"scala", "name := \"x\"\nval v = s\"$a\"\nval w = \"\"\"raw\\n\"\"\"\nval u = s\"plain\"\n", "$[*]", "x@1, ~s\"$a\"@2, raw\\n@3, plain@4"},
		{"swift", "let package = Package(name: \"x\", dependencies: [.package(url: \"u\", from: \"1.0\")])\n", "package.dependencies[0].from", "1.0@1"},
		{"swift", "let s = \"\"\"\n    a\n      b\n    \"\"\"\nlet r = #\"\\d\"#\nlet i = \"\\(x)\"\n", "$[*]", "a\n  b@1, \\d@5, ~\"\\(x)\"@6"},
		{"swift", "let v: [String: Int] = [\"a\": 1]\nlet e = [:]\nlet m = .v13\n", "$[*]", "error"},
		{"swift", "let v: [String: Int] = [\"a\": 1]\nlet m = .v13\n", "v.a", "1@1"},
		// Elixir: a config.exs, sigils and heredocs.
		{"elixir", "import Config\nconfig :app, port: 4000, repo: MyApp.Repo\n", "config[0][*]", "app@2, 4000@2, MyApp.Repo@2"},
		{"elixir", "x = ~s(a)\ny = ~S(#{a})\nz = ~r/a/\nw = \"\"\"\n  hi\n  \"\"\"\n", "$[*]", "a@1, #{a}@2, ~~r/a/@3, hi\n@4"},
		{"elixir", "x = %{\"a\" => 1, b: 2}\ny = [c: 3]\nz = {1, :ok}\n", "$[*]", "error"},
		{"elixir", "x = %{\"a\" => 1, b: 2}\ny = [c: 3]\n", "x[*]", "1@1, 2@1"},
		{"elixir", "defmodule M do\n  x = 1\nend\ny = 2\n", "$[*]", "2@4"},
		// Lua: a rockspec, tables and blocks.
		{"lua", "local v = '1.0'\nfunction f()\n  v = 2\nend\n", "v", "~'1.0'@1"},
		{"lua", "t = { a = 1, b = { 'x', [[\nlong]] } }\nreturn { x = 1 }\n", "t.b[*]", "x@1, long@1"},
		{"lua", "t = { a = 1, 'b' }\nwhile x do y() end\nu = 1\n", "$[*]", "~{ a = 1, 'b' }@1, 1@3"},
		{"lua", "repeat\n  y = 1\nuntil x\nv = 2\n", "v", "2@4"},
	} {
		got := read(t, c.lang, c.src, c.path)
		if c.want == "error" {
			if !strings.HasPrefix(got, "error: ") {
				t.Errorf("%s %q at %s: %s, want an error", c.lang, c.src, c.path, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%s %q at %s:\n got %s\nwant %s", c.lang, c.src, c.path, got, c.want)
		}
	}
}

// A name the file changes after it assigns it is computed: an assignment
// in a block, a compound or element assignment, a method called on it, a
// name bound anew, every name an import binds, or a star import after it. A name assigned
// again at the top takes its last value, and a read of the name changes
// nothing. Planted breaks: the observer's taints dropped; own statements
// not excused; the star import ignored.
func TestChangedNamesAreComputed(t *testing.T) {
	for _, c := range []struct{ lang, src, want string }{
		{"python", "A = 1\nB = 2\nC = 3\nD = 4\nE = 5\nF = 6\nG = 7\nH = 8\nI = 9\n" +
			"if x:\n    A = 0\nB += 1\nC['k'] = 1\nD.append(1)\ndef E(): pass\nfor F in y: pass\nimport G\nH = 10\nJ = I\n",
			"~1@1, ~2@2, ~3@3, ~4@4, ~5@5, ~6@6, ~7@7, 10@18, 9@9, ~I@19"},
		{"python", "A = 1\nfrom m import *\nB = 2\n", "~1@1, 2@3"},
		{"python", "A = [1]\nA = A + [2]\n", "~A + [2]@2"},
		{"javascript", "const A = {}\nA.b = 1\nlet B = 1\nB++\nconst C = 1\nfunction f() { C = 2 }\n", "~A.b = 1@2, ~1@3, ~1@5"},
		{"php", "<?php\n$a = [];\n$a['x'] = 1;\n$b = 1;\n", "~[]@2, 1@4"},
		{"ruby", "A = 1\nA.freeze\nB = 2\n", "~1@1, 2@3"},
		{"ruby", "A = 'x'\nA.concat('y')\n", "~'x'@1"},
	} {
		if got := read(t, c.lang, c.src, "$[*]"); got != c.want {
			t.Errorf("%s %q:\n got %s\nwant %s", c.lang, c.src, got, c.want)
		}
	}
}

// A dotted name assigns a member of the mapping its first part names; one
// whose first part another statement assigned a value makes it computed.
func TestDottedNames(t *testing.T) {
	for _, c := range []struct{ lang, src, path, want string }{
		{"ruby", "s.name = 'a'\ns.version = '1.0'\n", "s[*]", "a@1, 1.0@2"},
		{"python", "a = 1\na.b = 2\n", "a", "~a.b = 2@2"},
		{"python", "a.b = 1\na.b.c = 2\n", "a", "~a.b.c = 2@2"},
		{"groovy", "gem 'x'\ngem.y = 1\n", "gem", "~gem.y = 1@2"},
		{"groovy", "a = 1\na 'x'\n", "a", "~a 'x'@2"},
	} {
		if got := read(t, c.lang, c.src, c.path); got != c.want {
			t.Errorf("%s %q at %s:\n got %s\nwant %s", c.lang, c.src, c.path, got, c.want)
		}
	}
}

// A file the reader loses its place in is refused, naming the line: a
// bracket that never closes or closes none, a block that never ends or an
// end with no block, a string that runs to the end of the file, a PHP file
// that does not start with <?php or has text after ?>. Planted breaks:
// closed not checked; a closer not matched to its opener; CheckSpan not
// called.
func TestRefusals(t *testing.T) {
	for _, c := range []struct{ lang, src, want string }{
		{"python", "X = [1,\nY = 2\n", "f: line 1: a bracket opens here and never closes"},
		{"python", "X = 1]\n", "f: line 1: a ] closes no bracket open"},
		{"python", "X = (1]\n", "f: line 1: a ] closes no bracket open"},
		{"ruby", "X = 1\nif y\n  z\n", "f: line 2: a block opens here and never ends"},
		{"ruby", "X = 1\nend\n", "f: line 2: end ends no block open"},
		{"lua", "X = 1\nuntil x\n", "f: line 2: until ends no block open"},
		{"python", "X = 1\nY = 'a\nZ = \"\"\"b\n", "f: line 2: a string opens here and ends unclosed at its line's end"},
		{"python", "X = 1\nZ = \"\"\"b\n", "f: line 2: a string opens here and runs to the end of the file"},
		{"php", "<html><?php $a = 1;", "f: line 1: text that is not code stands here, outside <?php ... ?>"},
		{"php", "<?php $a = 1; ?>\n<p>x</p>\n", "f: line 2: text that is not code stands here"},
		{"php", "<?php\n$a = 1; // ?>\n$PLANTED = 9;\n", "f: line 3: text that is not code stands here"},
		{"php", "<?php\n$a = 1; # x ?>\n$PLANTED = 9;\n", "f: line 3: text that is not code stands here"},
		{"lua", "a = \"x\\z\nPLANTED = 9 --\"\nb = 1\n", "f: line 1: a string opens here and ends unclosed at its line's end"},
		{"ruby", "x = <<~EOS\nno end\n", "f: line 1: a string opens here and runs to the end of the file"},
	} {
		got := read(t, c.lang, c.src, "$")
		if !strings.HasPrefix(got, "error: "+c.want) {
			t.Errorf("%s %q:\n got %s\nwant error: %s", c.lang, c.src, got, c.want)
		}
	}
}

// A file nested past MaxDepth is refused.
func TestBounds(t *testing.T) {
	deep := "X = " + strings.Repeat("[", core.MaxDepth+2) + strings.Repeat("]", core.MaxDepth+2) + "\n"
	if got := read(t, "python", deep, "X"); !strings.Contains(got, "nested more than 1000 levels deep") {
		t.Errorf("a file nested past the limit: %.200s", got)
	}
	at := strings.Repeat("[", core.MaxDepth-2) + strings.Repeat("]", core.MaxDepth-2)
	if got := read(t, "python", "X = "+at+"\n", "X"); strings.HasPrefix(got, "error") && !strings.Contains(got, "selects a sequence") {
		t.Errorf("a file nested just under the limit: %.200s", got)
	}
}
