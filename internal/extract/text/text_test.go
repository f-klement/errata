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
	src, err := core.CheckSource("regex", "README.md", 0)
	if err != nil {
		return nil, err
	}
	return core.CompileSearchAt("regex", src, pattern)
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

// Code blocks, front matter and the comment's lines are left out of the
// prose unless the extractor sets code:, and with it each is in at its own
// line; the div's text is prose either way (#86). The
// planted break is code: true, which finds the claims that the prose leaves
// out.
func TestTextLeavesOutCodeUnlessAsked(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"README.md": withCode})
	if got, want := extracttest.Texts(mustRun(t, textSpec(claim), root)), "builder@6 html@15"; got != want {
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
