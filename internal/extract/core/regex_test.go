package core_test

import (
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/regex"
)

// The clocked search reads UTF-8 as Go's regexp does: a multibyte character
// is one character, and a byte from 0x80 up that is not UTF-8 is U+FFFD,
// never the character of that number. An empty match at the very start
// counts as one, as FindAllSubmatchIndex counts it.
func TestRegexReadsRunesAsRegexpDoes(t *testing.T) {
	for _, c := range []struct{ body, pattern, want string }{
		{"é\n", `(é)`, "é@1"},
		{"aéb\n", `a(.)b`, "é@1"},
		{"\x80\n", `(\x{80})`, ""},
		{"\x80\n", `(\x{FFFD})`, "\x80@1"},
		{"b", `(a*)`, "@1 @1"},
	} {
		root := extracttest.Repo(t, map[string]string{"Dockerfile": c.body})
		if got := extracttest.Texts(mustRun(t, regexSpec(c.pattern), root)); got != c.want {
			t.Errorf("%s over %q: got %q, want %q", c.pattern, c.body, got, c.want)
		}
	}
}

// A final newline ends the last line and starts no empty one, for the
// search as for the context, so no regex reports the empty match after it:
// values with and without a context, and records. An empty line before the
// final newline is still one, and a file without a final newline keeps its
// empty match at the end, on its last line. The text extractor's prose ends
// in no newline, so its search never meets one. The first case is the probe of the #160
// delta review, which found "" at f:2 of "a\n" with or without
// without: '^$'. The planted break: Search.each accepting the match at
// len(data) after a final newline (#205).
func TestNoEmptyMatchAfterTheFinalNewline(t *testing.T) {
	without := &core.Context{Without: `^$`}
	for _, c := range []struct {
		body    string
		context *core.Context
		want    string
	}{
		{"a\n", nil, "a@1"},
		{"a\n", without, "a@1"},
		{"a\n\n", nil, "a@1 @2"},
		{"a\n\n", without, "a@1"},
		{"a", nil, "a@1"},
		{"a\nb", nil, "a@1 b@2"},
		{"\n", nil, "@1"},
		{"", nil, "@1"},
	} {
		root := extracttest.Repo(t, map[string]string{"Dockerfile": c.body})
		s := extract.Of(regex.Kind, regex.Spec{File: "Dockerfile", Pattern: `^(.*)$`, Context: c.context})
		if got := extracttest.Texts(mustRun(t, s, root)); got != c.want {
			t.Errorf("^(.*)$ over %q, context %+v: got %q, want %q", c.body, c.context, got, c.want)
		}
	}
	root := extracttest.Repo(t, map[string]string{"Dockerfile": "a\n"})
	records := mustRun(t, extract.Of(regex.Kind, regex.Spec{File: "Dockerfile", Pattern: `^(?P<line>.*)$`, As: "record"}), root)
	if len(records) != 1 || records[0].Text != `{"line": "a"}` {
		t.Errorf("records over \"a\\n\": %v, want the one of line 1", records)
	}
}

func regexSpec(pattern string) extract.Spec {
	return extract.Of(regex.Kind, regex.Spec{File: "Dockerfile", Pattern: pattern})
}

func TestRegexReturnsGroupOneOfEveryMatch(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"Dockerfile": core.Dockerfile})
	for pattern, want := range map[string]string{
		`^FROM (\S+)`:               "golang:1.27@1 alpine:3.22@3",
		`FROM (\S+)`:                "golang:1.27@1 alpine:3.22@3 scratch@4",
		`(?-m)^FROM (\S+)`:          "golang:1.27@1",
		`(\d+\.\d+)$`:               "3.22@3",
		`version="([^"]*)"`:         "0.2.0@5",
		`(?s)make\n(FROM)`:          "FROM@2",
		`(nothing here)`:            "",
		`(x)?RUN`:                   "",
		`((?:golang|alpine)):(\d+)`: "golang@1 alpine@3",
	} {
		if got := extracttest.Texts(mustRun(t, regexSpec(pattern), root)); got != want {
			t.Errorf("%s: got %q, want %q", pattern, got, want)
		}
	}
}

func TestRegexZeroMatchesIsAnEmptyListNotAnError(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"Dockerfile": core.Dockerfile})
	got := mustRun(t, regexSpec(`(absent)`), root)
	if got == nil || len(got) != 0 {
		t.Errorf("got %#v, want an empty list", got)
	}
}

// The planted breaks: a pattern without a group, one whose only group does
// not capture, one that does not compile, one that looks back, with ^,
// nested as deeply as RE2 allows less one level (499 starred groups of two
// levels each), which the search after a match cannot wrap, and one with a
// capture group more than maxGroups. A pattern at the group limit builds.
func TestBuildRefusesPattern(t *testing.T) {
	for pattern, want := range map[string]string{
		`FROM \S+`:     `regex: pattern "FROM \\S+" has no capture group; the extractor returns group 1`,
		`FROM (?:\S+)`: `regex: pattern "FROM (?:\\S+)" has no capture group`,
		`FROM (\S+`:    "regex: pattern \"FROM (\\\\S+\": error parsing regexp: missing closing )",
		`(?<x>a)\1`:    `regex: pattern "(?<x>a)\\1": error parsing regexp: invalid escape sequence`,
		"^" + strings.Repeat("(?:a", 499) + strings.Repeat(")*", 499) + "()": "\"...: it nests within two levels of the deepest nesting RE2 allows; errata needs those two to search after a match",
		"(a)" + strings.Repeat("()", core.MaxGroups):                         "\"...: it has 33 capture groups, over the limit of 32; write (?:...) for a group that need not capture",
	} {
		_, err := extract.Build(regexSpec(pattern))
		extracttest.WantError(t, err, want)
	}
	if _, err := extract.Build(regexSpec("(a)" + strings.Repeat("()", core.MaxGroups-1))); err != nil {
		t.Errorf("%d groups: %v", core.MaxGroups, err)
	}
}

// A CRLF line ending reads as LF: $ matches before it, no value ends in its
// CR, and the lines are those of the file: ^()$ finds the empty line 3, and
// nothing after the final CRLF, which ends line 4 (#205). A lone CR is kept.
// The planted break is the CRLF file itself, which gave 1.2\r before.
func TestRegexReadsCRLFAsLF(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"Dockerfile": "v=1.2\r\nw=x\r\n\r\nv=3\rx\r\n"})
	for pattern, want := range map[string]string{
		`^v=(.*)$`:     "1.2@1 3\rx@4",
		`(?s)w=(.*)v=`: "x\n\n@2",
		`^()$`:         "@3",
	} {
		if got := extracttest.Texts(mustRun(t, regexSpec(pattern), root)); got != want {
			t.Errorf("%s: got %q, want %q", pattern, got, want)
		}
	}
}

// The regex time limit is 10 s for a file of up to the default size and
// grows in proportion to max_bytes above it. The planted break is one limit
// for every max_bytes, which refused an ordinary pattern over a large file.
func TestRegexTimeLimitScalesWithMaxBytes(t *testing.T) {
	for maxBytes, want := range map[int64]time.Duration{
		0:                    10 * time.Second,
		1 << 20:              10 * time.Second,
		core.DefaultMaxBytes: 10 * time.Second,
		48 << 20:             15 * time.Second,
		core.MaxMaxBytes:     20 * time.Second,
	} {
		e, err := extract.Build(withMaxBytes(regexSpec("(a)"), maxBytes))
		if err != nil {
			t.Fatal(err)
		}
		if got := e.(core.Search).TimeLimit; got != want {
			t.Errorf("max_bytes %d: time limit %v, want %v", maxBytes, got, want)
		}
	}
}
