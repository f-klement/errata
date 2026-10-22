package text

import (
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// run builds s and extracts from root.
func run(t *testing.T, s Spec, root string) ([]check.Value, error) {
	t.Helper()
	return extracttest.Run(t, Build, s, root)
}

// mustRun is run for an extraction that must succeed.
func mustRun(t *testing.T, s Spec, root string) []check.Value {
	t.Helper()
	return extracttest.MustRun(t, Build, s, root)
}

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; make slow runs it from the Makefile's
// SLOW_TESTS.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}

// rawSearch is the regex extractor of pattern over the raw README.md.
func rawSearch(pattern string) (check.Extractor, error) {
	return core.CompileSearch("regex", "README.md", pattern, 0)
}

func textSpec(pattern string) Spec { return Spec{File: "README.md", Pattern: pattern} }

func codeSpec(pattern string) Spec {
	return Spec{File: "README.md", Pattern: pattern, Code: true}
}

// readme puts body after two lines, so the claim it holds starts at line 3
// unless the body moves it.
func readme(body string) map[string]string {
	return map[string]string{"README.md": "# Build\n\n" + body}
}

const claim = `CI runs on node (\w+)`

// rawFinds lists the cases that a regex over the raw file finds as well: the
// control, and the claim in a heading and in an image's text, which show
// that those are prose.
var rawFinds = map[string]bool{"plain": true, "heading": true, "image": true}

// The same claim is found however the file writes it, at the line where the
// match starts. The planted break for each normalisation is the claim
// written that way, which the test shows a regex over the raw file misses.
func TestTextFindsTheClaimHoweverItIsWritten(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		want string
	}{
		"plain":                   {"CI runs on node builder.\n", "builder@3"},
		"wrapped":                 {"CI runs on\nnode builder.\n", "builder@3"},
		"wrapped with indent":     {"CI runs on   \n   node builder.\n", "builder@3"},
		"wrapped twice":           {"CI\nruns\non\nnode\nbuilder.\n", "builder@3"},
		"starting on a wrap":      {"Our pipeline says so.\nCI runs on node\nbuilder.\n", "builder@4"},
		"hard break":              {"CI runs on node\\\nbuilder.\n", "builder@3"},
		"hard break of spaces":    {"CI runs on node  \nbuilder.\n", "builder@3"},
		"backticked":              {"CI runs on node `builder`.\n", "builder@3"},
		"backticked twice":        {"CI runs on ``node`` ```builder```.\n", "builder@3"},
		"backticks across a wrap": {"CI runs on node\n`builder`.\n", "builder@3"},
		"code span wrapped":       {"CI runs on `node\nbuilder`.\n", "builder@3"},
		"emphasised":              {"CI runs on *node* _builder_.\n", "builder@3"},
		"strong":                  {"CI runs on **node** __builder__.\n", "builder@3"},
		"strong emphasis":         {"CI runs on ***node*** builder.\n", "builder@3"},
		"emphasis in a word":      {"CI runs on node b*uild*er.\n", "builder@3"},
		"emphasis across a wrap":  {"CI runs *on\nnode* builder.\n", "builder@3"},
		"linked":                  {"CI runs on [node builder](https://ci.example/node).\n", "builder@3"},
		"reference link":          {"CI runs on [node][n] builder.\n\n[n]: https://ci.example\n", "builder@3"},
		"image":                   {"![CI runs on node builder](ci.png)\n", "builder@3"},
		"everything at once":      {"*CI* runs\n[on `node`](x)\n**`builder`**.\n", "builder@3"},
		"inline HTML":             {"CI runs on <b>node</b> builder.\n", "builder@3"},
		"heading":                 {"## CI runs on node builder\n", "builder@3"},
		"setext heading":          {"CI runs\non node builder\n---\n", "builder@3"},
		"list item":               {"- CI runs on\n  node builder\n", "builder@3"},
		"loose list item":         {"- a\n\n- CI runs on\n  node builder\n", "builder@5"},
		"block quote":             {"> CI runs on\n> node builder\n", "builder@3"},
		"lazy line":               {"> CI runs on\nnode builder\n", "builder@3"},
		"nested":                  {"> 1. - CI runs on\n>      node `builder`\n", "builder@3"},
	} {
		root := extracttest.Repo(t, readme(c.body))
		got := extracttest.Texts(mustRun(t, textSpec(claim), root))
		if got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
		raw := extracttest.Texts(extracttest.MustRun(t, rawSearch, claim, root))
		if (raw == c.want) != rawFinds[name] {
			t.Errorf("%s: a regex over the raw file gives %q", name, raw)
		}
	}
}

// The value is the prose's text, so a group that spans a wrap holds one
// space for it, and none of the markup.
func TestTextValueIsTheProse(t *testing.T) {
	root := extracttest.Repo(t, readme("CI runs on *node*\n  `builder` [today](x).\n"))
	for pattern, want := range map[string]string{
		`CI runs on (.*)\.`:    "node builder today@3",
		`(node builder)`:       "node builder@3",
		`^(.*)$`:               "Build@1 CI runs on node builder today.@3",
		`(?s)(Build.CI)`:       "Build\nCI@1",
		`(builder today)`:      "builder today@4",
		`(\S+)$`:               "Build@1 today.@4",
		`(nothing like this)`:  "",
		`(?:x)?(https?://\S+)`: "",
	} {
		if got := extracttest.Texts(mustRun(t, textSpec(pattern), root)); got != want {
			t.Errorf("%s: got %q, want %q", pattern, got, want)
		}
	}
}

// An autolink keeps its address, and the file's own text keeps a backslash
// escape and an entity as written.
func TestTextKeepsAutolinksAndEscapes(t *testing.T) {
	root := extracttest.Repo(t, readme("See <https://ci.example/a> and \\*not emphasis\\* &amp; more.\n"))
	for pattern, want := range map[string]string{
		`See (\S+)`:     "https://ci.example/a@3",
		`and (.*) more`: `\*not emphasis\* &amp;@3`,
	} {
		if got := extracttest.Texts(mustRun(t, textSpec(pattern), root)); got != want {
			t.Errorf("%s: got %q, want %q", pattern, got, want)
		}
	}
}

const withCode = `---
title: CI runs on node frontmatter
---
# Build

CI runs on node builder.

` + "```sh" + `
# CI runs on node fenced
` + "```" + `

    CI runs on node indented

<div>
CI runs on node html
</div>

<!--
CI runs on node comment
CI runs on node closing -->

~~~
CI runs on node tilde
~~~
`

// Code blocks, HTML blocks and front matter are left out of the prose unless
// the extractor sets code:, and with it each is in at its own line. The
// planted break is code: true, which finds the claims that the prose leaves
// out.
func TestTextLeavesOutCodeUnlessAsked(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"README.md": withCode})
	if got, want := extracttest.Texts(mustRun(t, textSpec(claim), root)), "builder@6"; got != want {
		t.Errorf("prose: got %q, want %q", got, want)
	}
	want := "frontmatter@2 builder@6 fenced@9 indented@12 html@15 comment@19 closing@20 tilde@23"
	if got := extracttest.Texts(mustRun(t, codeSpec(claim), root)); got != want {
		t.Errorf("code: got %q, want %q", got, want)
	}
	// A code block's lines stay lines, as the file has them.
	if got := extracttest.Texts(mustRun(t, codeSpec(`^(#.*)$`), root)); got != "# CI runs on node fenced@9" {
		t.Errorf("code lines: got %q", got)
	}
}

// A code block's line keeps the spaces left of a tab that its indent
// splits, and a match in which group 1 took no part gives no value.
func TestTextCodeLinesAndEmptyGroups(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"README.md": ">\t\tx\n\nCI runs on node builder\n"})
	if got := extracttest.Texts(mustRun(t, codeSpec(`^( *x)$`), root)); got != "  x@1" {
		t.Errorf("code line: got %q", got)
	}
	if got := mustRun(t, textSpec(`(x)?CI`), root); len(got) != 0 {
		t.Errorf("empty group: got %s", extracttest.Texts(got))
	}
}

// Front matter is a block of --- or +++ lines at the very start of the file,
// closed by the same line, or by ... after ---. Anything else is Markdown.
func TestTextFrontMatter(t *testing.T) {
	for body, want := range map[string]string{
		"---\nCI runs on node a\n---\n":            "",
		"---\nCI runs on node a\n...\n":            "",
		"+++\nCI runs on node a\n+++\n":            "",
		"+++\nCI runs on node a\n...\n":            "a@2",
		"---\nCI runs on node a\n":                 "a@2",
		"\n---\nCI runs on node a\n---\n":          "a@3",
		"---\n---\nCI runs on node a\n":            "a@3",
		"---\nx\n---\n---\nCI runs on node a\n---": "a@5",
	} {
		root := extracttest.Repo(t, map[string]string{"README.md": body})
		if got := extracttest.Texts(mustRun(t, textSpec(claim), root)); got != want {
			t.Errorf("%q: got %q, want %q", body, got, want)
		}
	}
	// Every byte of the front matter is out, its closing fence too, and a
	// UTF-8 byte order mark before the file starts nothing. The planted
	// breaks are the closing fence left in, which read "... b", and the
	// mark read as text, which hid the front matter.
	for body, want := range map[string]string{
		"---\na\n...\nb\n":                "b@4",
		"---\na\n---\nb\n":                "b@4",
		"\xef\xbb\xbf---\nx: y\n---\nb\n": "b@4",
		"\xef\xbb\xbfb\n":                 "b@1",
		"\xef\xbb\xbf\xef\xbb\xbfb\n":     "\ufeffb@1",
	} {
		root := extracttest.Repo(t, map[string]string{"README.md": body})
		if got := extracttest.Texts(mustRun(t, textSpec(`^(\S+)`), root)); got != want {
			t.Errorf("%q: got %q, want %q", body, got, want)
		}
	}
}

// The prose is spaced as a browser shows it: an element of inline HTML,
// <br> among them, stands for one space, a run of spaces and tabs is one
// space, and a line of prose neither starts nor ends with one. The reported
// line is still the line where the match starts. The planted break for each
// case is the input itself, which joined two words or doubled a space.
func TestTextSpacesAsABrowserDoes(t *testing.T) {
	for name, c := range map[string]struct {
		body, pattern, want string
	}{
		"comment beside a claim": {"See: <!-- errata: ci --> CI runs on node builder.\n", `See: (CI runs on node \w+)`, "CI runs on node builder@3"},
		"comment across lines":   {"CI runs on node <!--\nx\n--> builder\n", claim, "builder@3"},
		"tag first":              {"<img src=x> CI runs on node builder.\n", `^(CI) runs`, "CI@3"},
		"br":                     {"CI runs on node<br>builder\n", claim, "builder@3"},
		"br closed":              {"CI runs on node<br/>builder\n", claim, "builder@3"},
		"spaces and tabs":        {"CI runs on  node \t builder\n", claim, "builder@3"},
		"value collapsed":        {"CI runs on  *node*   \t builder.\n", `on (.*)\.`, "node builder@3"},
		"trailing tag":           {"CI runs on node builder <b></b>\n", `(builder)$`, "builder@3"},
		"code span spaces":       {"CI runs on `node   builder`\n", claim, "builder@3"},
		"match after a comment":  {"x <!-- a\nb --> y\nCI runs on node builder\n", claim, "builder@5"},
		"wrap after a tag":       {"CI runs on <i>\nnode</i> builder\n", claim, "builder@3"},
		"inline code tag":        {"node <code>builder</code>.\n", `node (\w+)\.`, "builder@3"},
		"tag in a word":          {"CI runs on node b<i>uild</i>er\n", claim, "builder@3"},
		"kbd":                    {"Press <kbd>Ctrl</kbd>+<kbd>C</kbd>\n", `(Ctrl\+C)`, "Ctrl+C@3"},
		"sub":                    {"H<sub>2</sub>O\n", `(H2O)`, "H2O@3"},
		"link tag":               {"<a href=x>builder</a>, today\n", `(builder,)`, "builder,@3"},
		"comment in a word":      {"CI runs on node build<!-- x -->er\n", claim, "builder@3"},
		"cell tags":              {"CI runs on node</td><td>builder\n", claim, "builder@3"},
		"paragraph tag":          {"CI runs on node<P>builder\n", claim, "builder@3"},
		"menu":                   {"CI runs on node<menu>builder\n", claim, "builder@3"},
		"noscript":               {"CI runs on node</noscript>builder\n", claim, "builder@3"},
		"option":                 {"CI runs on node<option value=x>builder\n", claim, "builder@3"},
		"custom element":         {"CI runs on node build<h1-x>er</h1-x>\n", claim, "builder@3"},
		"br closed by a slash":   {"CI runs on node<br/>builder\n", claim, "builder@3"},
		"carriage return":        {"CI runs on\rnode builder\n", claim, "builder@3"},
		"form feed":              {"CI runs on\fnode builder\n", claim, "builder@3"},
	} {
		got := extracttest.Texts(mustRun(t, textSpec(c.pattern), extracttest.Repo(t, readme(c.body))))
		if got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

// A line that starts an HTML block hides nothing a browser shows after the
// block's end on that line: text after a comment, a processing instruction,
// a declaration, CDATA, or a script, style, pre or textarea element. Text
// inside a <div> and the other blocks that end at a blank line is left out,
// as is everything with code: true but the lines themselves. The planted
// break is the remainder left out, which hid every claim below; and the end
// looked for only after the opening, which hid the claim after <?>.
func TestTextReadsTextAfterAnHTMLBlockEnds(t *testing.T) {
	for name, c := range map[string]struct {
		body, want string
	}{
		"comment":            {"<!-- errata: ci --> CI runs on node builder.\n", "builder@3"},
		"comment over lines": {"<!--\nmarker\n--> CI runs on node builder\n", "builder@5"},
		"two comments":       {"<!-- a --> CI runs <!-- b --> on node builder\n", "builder@3"},
		"tags after":         {"<!-- a --> CI runs on <b>node</b> builder\n", "builder@3"},
		"processing":         {"<?x y?> CI runs on node builder\n", "builder@3"},
		"declaration":        {"<!X y> CI runs on node builder\n", "builder@3"},
		"cdata":              {"<![CDATA[x]]> CI runs on node builder\n", "builder@3"},
		"script":             {"<script>x</script> CI runs on node builder\n", "builder@3"},
		"pre over lines":     {"<pre>\nCI runs on node hidden\n</pre> CI runs on node builder\n", "builder@5"},
		"unclosed comment":   {"<!-- a\nCI runs on node hidden\n", ""},
		"comment in a quote": {"> <!-- a --> CI runs on node builder\n", "builder@3"},
		"div":                {"<div>\nCI runs on node hidden\n</div>\n", ""},
		"tag alone":          {"<span>\nCI runs on node hidden\n", ""},
		"empty comment":      {"<!--> CI runs on node builder\n", "builder@3"},
		"empty comment dash": {"<!---> CI runs on node builder\n", "builder@3"},
		"empty processing":   {"<?> CI runs on node builder\n", "builder@3"},
		"inline tag in tail": {"<!-- a --> CI runs on node b<i>uild</i>er\n", "builder@3"},
		"br in tail":         {"<!-- a --> CI runs on node<br>builder\n", "builder@3"},
		"folds longer":       {"<!-- ȺȺȺ -->x CI runs on node builder\n", "builder@3"},
		"folds shorter":      {"<!-- \u212a\u212a\u212a -->x CI runs on node builder\n", "builder@3"},
		"invalid UTF-8":      {"<!-- \xff\xfe -->x CI runs on node builder\n", "builder@3"},
		"script folds":       {"<script>Ⱥ\u212a</SCRIPT> CI runs on node builder\n", "builder@3"},
	} {
		got := extracttest.Texts(mustRun(t, textSpec(claim), extracttest.Repo(t, readme(c.body))))
		if got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
	root := extracttest.Repo(t, readme("<!-- a --> CI runs on node builder\n"))
	if got := extracttest.Texts(mustRun(t, codeSpec(`^(.*)$`), root)); got != "Build@1 <!-- a --> CI runs on node builder@3" {
		t.Errorf("code: got %q", got)
	}
	// A rune whose lower case is longer or shorter in UTF-8 moved the cut,
	// past the line's end in the first two: a panic, and comment bytes in
	// the prose of the others.
	for _, body := range []string{"# B\n\n<!-- ȺȺȺȺ -->\n", "<!-- ȺȺ -->", "<!-- ȺȺȺ -->x y\n", "<!-- \u212a\u212a\u212a -->x y\n"} {
		root := extracttest.Repo(t, map[string]string{"README.md": body})
		if got := extracttest.Texts(mustRun(t, textSpec(`(-->|\x{212a}|Ⱥ)`), root)); got != "" {
			t.Errorf("%q: comment bytes in the prose: %q", body, got)
		}
	}
}

// Without the table extension a table is a paragraph, its rows joined by a
// space, delimiter row and pipes included.
func TestTextReadsATableAsAParagraph(t *testing.T) {
	root := extracttest.Repo(t, readme("| CI | node |\n|---|---|\n| runs on | builder |\n"))
	if got := extracttest.Texts(mustRun(t, textSpec(`^(.*)$`), root)); got != "Build@1 | CI | node | |---|---| | runs on | builder |@3" {
		t.Errorf("got %q", got)
	}
	if got := extracttest.Texts(mustRun(t, textSpec(`runs on \| (\w+)`), root)); got != "builder@5" {
		t.Errorf("cell: got %q", got)
	}
}

// A CRLF line ending reads as LF, as in the regex extractor, so the lines
// are the file's.
func TestTextReadsCRLFAsLF(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"README.md": "# Build\r\n\r\nCI runs on\r\nnode builder\r\n\r\n```\r\nCI runs on node code\r\n```\r\n"})
	if got := extracttest.Texts(mustRun(t, codeSpec(claim+`$`), root)); got != "builder@3 code@7" {
		t.Errorf("got %q", got)
	}
}

// The planted breaks: each pattern the regex extractor refuses, named as the
// text extractor's, and the file refusals every kind shares.
func TestBuildTextRefuses(t *testing.T) {
	for pattern, want := range map[string]string{
		"":          "text: pattern is required",
		`FROM \S+`:  `text: pattern "FROM \\S+" has no capture group; the extractor returns group 1`,
		`FROM (\S+`: "text: pattern \"FROM (\\\\S+\": error parsing regexp: missing closing )",
		"(a)" + strings.Repeat("()", core.MaxGroups): "\"...: it has 33 capture groups, over the limit of 32",
	} {
		_, err := Build(textSpec(pattern))
		extracttest.WantError(t, err, want)
	}
	for _, s := range []Spec{
		{Pattern: "(a)"},
		{File: "../README.md", Pattern: "(a)"},
		{File: "README.md", Pattern: "(a)", MaxBytes: -1},
		{File: "README.md", Pattern: "(a)", MaxBytes: core.MaxMaxBytes + 1},
	} {
		_, err := Build(s)
		if err == nil || !strings.HasPrefix(err.Error(), "text: ") {
			t.Errorf("%+v: error %v, want one that starts with text:", s, err)
		}
	}
}

// The text extractor reads a file of exactly max_bytes and refuses one a byte
// over, and its search has the regex extractor's time limit for that size.
func TestTextFileSizeAndTimeLimits(t *testing.T) {
	const limit = 1000
	body := strings.Repeat("x", limit-len("\n\nCI runs on node a\n")) + "\n\nCI runs on node a\n"
	s := Spec{File: "README.md", Pattern: claim, MaxBytes: limit}
	if got := extracttest.Texts(mustRun(t, s, extracttest.Repo(t, map[string]string{"README.md": body}))); got != "a@3" {
		t.Errorf("at the limit: got %q", got)
	}
	_, err := run(t, s, extracttest.Repo(t, map[string]string{"README.md": " " + body}))
	want := "read README.md: the file is 1001 bytes, over the limit of 1000 bytes; set max_bytes: on the extractor to read a larger file"
	if err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
	e, err := Build(Spec{File: "README.md", Pattern: claim, MaxBytes: core.MaxMaxBytes})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.(textExtractor).parseTime; got != 60*time.Second {
		t.Errorf("parse time limit at 64 MiB: %v", got)
	}
	for maxBytes, want := range map[int64]time.Duration{0: 30 * time.Second, core.DefaultMaxBytes: 30 * time.Second, 48 << 20: 45 * time.Second} {
		e, err := Build(Spec{File: "README.md", Pattern: claim, MaxBytes: maxBytes})
		if err != nil {
			t.Fatal(err)
		}
		if got := e.(textExtractor).parseTime; got != want {
			t.Errorf("parse time limit at max_bytes %d: %v, want %v", maxBytes, got, want)
		}
	}
	if got := e.(textExtractor).search.TimeLimit; got != 20*time.Second {
		t.Errorf("time limit at 64 MiB: %v", got)
	}
}
