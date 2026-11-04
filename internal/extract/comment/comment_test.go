package comment

import (
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/lexer"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// planted is, for each language, a file that holds the marker "port 1" in a
// string literal on line 1 and "port 2" in a comment on line 2; for a
// language with no string literal, such as a Dockerfile, the marker stands
// where the language reads no comment, after code on its line.
var planted = map[string]string{
	"go":         "s := \"// port 1\"\n// port 2\n",
	"c":          "char *s = \"/* port 1 */\";\n/* port 2 */\n",
	"cpp":        "auto s = R\"x(// port 1)x\";\n// port 2\n",
	"java":       "String s = \"\"\"\n// port 1\"\"\";\n// port 2\n",
	"kotlin":     "val s = \"\"\"// port 1\"\"\"\n/* port 2 */\n",
	"javascript": "const s = `// port 1`\n// port 2\n",
	"typescript": "const s: string = '// port 1'\n// port 2\n",
	"rust":       "let s = r#\"// port 1\"#;\n// port 2\n",
	"swift":      "let s = #\"// port 1\"#\n// port 2\n",
	"csharp":     "var s = @\"// port 1\";\n// port 2\n",
	"css":        "a::after { content: \"/* port 1 */\"; }\n/* port 2 */\n",
	"python":     "s = \"\"\"# port 1\"\"\"\n# port 2\n",
	"shell":      "echo '# port 1' a#b\n# port 2\n",
	"ruby":       "s = \"# port 1\"\n# port 2\n",
	"perl":       "my $s = \"# port 1\";\n# port 2\n",
	"yaml":       "a: '# port 1'\n# port 2\n",
	"toml":       "a = \"# port 1\"\n# port 2\n",
	"dockerfile": "RUN echo # port 1\n# port 2\n",
	"makefile":   "a:\n\techo \"# port 1\"\n# port 2\n",
	"hcl":        "a = \"// port 1\"\n# port 2\n",
	"sql":        "select '-- port 1';\n-- port 2\n",
	"lua":        "s = [[-- port 1]]\n-- port 2\n",
	"html":       "<a title=\"<!-- port 1 -->\">x</a>\n<!-- port 2 -->\n",
	"xml":        "<a><![CDATA[<!-- port 1 -->]]></a>\n<!-- port 2 -->\n",
}

// The file planted[name] lies at in the repository: its name chooses its
// language, as a rule leaves language: out.
var fileOf = map[string]string{
	"go": "a.go", "c": "a.c", "cpp": "a.cpp", "java": "A.java", "kotlin": "a.kt", "javascript": "a.js",
	"typescript": "a.ts", "rust": "a.rs", "swift": "a.swift", "csharp": "a.cs", "css": "a.css",
	"python": "a.py", "shell": "a.sh", "ruby": "a.rb", "perl": "a.pl", "yaml": "a.yaml", "toml": "a.toml",
	"dockerfile": "Dockerfile", "makefile": "Makefile", "hcl": "a.tf", "sql": "a.sql", "lua": "a.lua",
	"html": "a.html", "xml": "a.xml",
}

// family plants a comment marker inside a string literal in a file of every
// language of the family and checks that only the comment's marker matches,
// at its line.
func family(t *testing.T, name string) {
	t.Helper()
	n := 0
	for _, l := range lexer.Languages() {
		if l.Family != name {
			continue
		}
		n++
		src, ok := planted[l.Name]
		if !ok {
			t.Errorf("%s: no planted file", l.Name)
			continue
		}
		file := fileOf[l.Name]
		root := extracttest.Repo(t, map[string]string{file: src})
		got := extracttest.Texts(extracttest.MustRun(t, Build, Spec{OneFile: core.OneFile{File: file}, Pattern: `port (\d+)`}, root))
		line := strings.Count(strings.SplitN(src, "port 2", 2)[0], "\n") + 1
		if want := "2@" + strconv.Itoa(line); got != want {
			t.Errorf("%s: %q gave %s, want %s", l.Name, src, got, want)
		}
	}
	if n == 0 {
		t.Fatalf("no language of family %s", name)
	}
}

// A marker in a string literal is no comment, for every C-like language.
// Planted break: the strings of the C-like rows left out of the table.
func TestCLikeStringHoldsNoComment(t *testing.T) { family(t, "c") }

// The same for the hash family. Planted break: shell's # read anywhere.
func TestHashStringHoldsNoComment(t *testing.T) { family(t, "hash") }

// The same for SQL and Lua. Planted break: Lua's long string left out.
func TestDashStringHoldsNoComment(t *testing.T) { family(t, "dash") }

// The same for HTML and XML. Planted break: XML's CDATA left out.
func TestMarkupStringHoldsNoComment(t *testing.T) { family(t, "markup") }

// Every row of the table is in a family the tests above plant in.
func TestEveryLanguageIsPlanted(t *testing.T) {
	for _, l := range lexer.Languages() {
		switch l.Family {
		case "c", "hash", "dash", "markup":
		default:
			t.Errorf("%s: family %q is planted by no test", l.Name, l.Family)
		}
		if _, ok := planted[l.Name]; !ok {
			t.Errorf("%s: no planted file", l.Name)
		}
	}
}

// spec reads file with pattern, its language chosen by its name.
func spec(file, pattern string) Spec {
	return Spec{OneFile: core.OneFile{File: file}, Pattern: pattern}
}

// A value's line is the file's line where its match starts; the comments are
// joined with a line feed, so a pattern that names one reads on into the
// next comment; a CRLF reads as LF; and language: reads a file its name
// chooses no language for; a group that starts the text is a value.
// Planted breaks: offset mapping a match to the comment before its own; the
// line feed between two comments left out.
func TestValues(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		"a.go":    "package a\n\n/*\n  listens on\n  port 8080\n*/\nconst p = 8080 // keep in step with web/a.ts\n// next\n",
		"crlf.py": "x = 1\r\n# port 7\r\n",
		"conf":    "a = 1 # port 9\n",
		"b.go":    "// a\nx := 1\ny := 2\n// port 3\n",
		"z.go":    "//8080\n",
	})
	for _, c := range []struct {
		s    Spec
		want string
	}{
		{spec("a.go", `port (\d+)`), "8080@5"},
		{spec("a.go", `in step with (\S+)`), "web/a.ts@7"},
		{spec("a.go", `(?s)listens on\s+port (\d+)`), "8080@4"},
		{spec("a.go", `a.ts(\n) next`), "\n@7"},
		{spec("a.go", `package (\w+)`), ""},
		{spec("b.go", `port (\d+)`), "3@4"},
		{spec("z.go", `^(\d+)`), "8080@1"},
		{spec("crlf.py", `port (\d+)$`), "7@2"},
		{Spec{OneFile: core.OneFile{File: "conf"}, Pattern: `port (\d+)`, Language: "shell"}, "9@1"},
	} {
		if got := extracttest.Texts(extracttest.MustRun(t, Build, c.s, root)); got != c.want {
			t.Errorf("%+v: %s, want %s", c.s, got, c.want)
		}
	}
}

// A file whose name chooses no language, and a language the table has no
// row of, are load errors that name the file or the language and list the
// languages; so is a pattern the regex extractor refuses. Planted break: a
// file with no language read as plain text.
func TestBuildRefuses(t *testing.T) {
	for _, c := range []struct {
		s    Spec
		want string
	}{
		{spec("README.md", `(x)`), "comment: README.md: its name chooses no language errata lexes; set language: to one of go, c, cpp"},
		{Spec{OneFile: core.OneFile{File: "a.go"}, Pattern: `(x)`, Language: "Go"}, `comment: language "Go" is not one errata lexes; write one of go, c,`},
		{spec("a.go", `x`), "comment: pattern \"x\" has no capture group"},
		{spec("../a.go", `(x)`), "comment:"},
	} {
		_, err := Build(c.s)
		extracttest.WantError(t, err, c.want)
	}
}

// Place and Resolve write out the language the file's name chooses, so a
// spec that names it reads the same place as one that leaves it out, and
// keep one the spec names; a file with none keeps none.
func TestPlaceAndResolveWriteTheLanguage(t *testing.T) {
	named := Spec{OneFile: core.OneFile{File: "./a.go", MaxBytes: 9}, Pattern: "(x)", Language: "go"}
	if Place(spec("a.go", "(x)")) != Place(named) {
		t.Errorf("%+v and %+v place apart", Place(spec("a.go", "(x)")), Place(named))
	}
	if r := Resolve(spec("a.go", "(x)")); r.Language != "go" || r.MaxBytes != core.DefaultMaxBytes {
		t.Errorf("Resolve: %+v", r)
	}
	if r := Resolve(Spec{OneFile: core.OneFile{File: "a.go"}, Language: "c"}); r.Language != "c" {
		t.Errorf("Resolve kept no named language: %+v", r)
	}
	if r := Resolve(spec("README.md", "(x)")); r.Language != "" {
		t.Errorf("Resolve chose %q for README.md", r.Language)
	}
}

// A file in which a span runs off the end is refused, and its rule unknown:
// the lexer lost its place at the line the span opens on, and code after it
// would read as comment. Here a JavaScript regular expression holds /*,
// which opens a block comment that swallows the code after it, and a quote
// in a shell heredoc opens a string that hides the comment after it.
// Planted break: comments not calling core.CheckSpan.
func TestASpanThatRunsOffIsRefused(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		"a.js": "const x = 1;\ns = s.replace(/\\/*$/, '');\nconst port = 80;\n",
		"b.sh": "cat <<EOF\nUsage: don't run\nEOF\n# port 8\n",
	})
	for file, want := range map[string]string{
		"a.js": "a.js: line 2: a comment opens here and runs to the end of the file, so the lexer lost its place",
		"b.sh": "b.sh: line 2: a string opens here and runs to the end of the file, so the lexer lost its place",
	} {
		_, err := extracttest.Run(t, Build, spec(file, `port = (\d+)|port (\d+)`), root)
		extracttest.WantError(t, err, want)
	}
}
