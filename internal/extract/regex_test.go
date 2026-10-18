package extract

import (
	"strings"
	"testing"
	"time"
)

func regexSpec(pattern string) Spec { return Spec{Regex: &Regex{File: "Dockerfile", Pattern: pattern}} }

const dockerfile = `FROM golang:1.27 AS build
RUN make
FROM alpine:3.22
# FROM scratch
LABEL version="0.2.0"
`

func TestRegexReturnsGroupOneOfEveryMatch(t *testing.T) {
	root := repo(t, map[string]string{"Dockerfile": dockerfile})
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
		if got := texts(mustRun(t, regexSpec(pattern), root)); got != want {
			t.Errorf("%s: got %q, want %q", pattern, got, want)
		}
	}
}

func TestRegexZeroMatchesIsAnEmptyListNotAnError(t *testing.T) {
	root := repo(t, map[string]string{"Dockerfile": dockerfile})
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
		"(a)" + strings.Repeat("()", maxGroups):                              "\"...: it has 33 capture groups, over the limit of 32; write (?:...) for a group that need not capture",
	} {
		_, err := Build(regexSpec(pattern))
		wantError(t, err, want)
	}
	if _, err := Build(regexSpec("(a)" + strings.Repeat("()", maxGroups-1))); err != nil {
		t.Errorf("%d groups: %v", maxGroups, err)
	}
}

// A CRLF line ending reads as LF: $ matches before it, no value ends in its
// CR, and the lines are those of the file. A lone CR is kept. The planted
// break is the CRLF file itself, which gave 1.2\r before.
func TestRegexReadsCRLFAsLF(t *testing.T) {
	root := repo(t, map[string]string{"Dockerfile": "v=1.2\r\nw=x\r\n\r\nv=3\rx\r\n"})
	for pattern, want := range map[string]string{
		`^v=(.*)$`:     "1.2@1 3\rx@4",
		`(?s)w=(.*)v=`: "x\n\n@2",
		`^()$`:         "@3 @5",
	} {
		if got := texts(mustRun(t, regexSpec(pattern), root)); got != want {
			t.Errorf("%s: got %q, want %q", pattern, got, want)
		}
	}
}

// The regex time limit is 10 s for a file of up to the default size and
// grows in proportion to max_bytes above it. The planted break is one limit
// for every max_bytes, which refused an ordinary pattern over a large file.
func TestRegexTimeLimitScalesWithMaxBytes(t *testing.T) {
	for maxBytes, want := range map[int64]time.Duration{
		0:               10 * time.Second,
		1 << 20:         10 * time.Second,
		defaultMaxBytes: 10 * time.Second,
		48 << 20:        15 * time.Second,
		maxMaxBytes:     20 * time.Second,
	} {
		e, err := Build(withMaxBytes(regexSpec("(a)"), maxBytes))
		if err != nil {
			t.Fatal(err)
		}
		if got := e.(regexExtractor).timeLimit; got != want {
			t.Errorf("max_bytes %d: time limit %v, want %v", maxBytes, got, want)
		}
	}
}
