package markdown

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/markup"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// read is the values s reads from a README.md that holds body, each as
// "line:text", read from the file under a repository as Extract reads it.
func read(t *testing.T, s Spec, body string) []string {
	t.Helper()
	s.File = "README.md"
	root := extracttest.Repo(t, map[string]string{"README.md": body})
	return show(extracttest.MustRun(t, Build, s, root))
}

// show writes each value as "line:text".
func show(values []check.Value) []string {
	out := []string{}
	for _, v := range values {
		out = append(out, fmt.Sprintf("%d:%s", v.At.Line, v.Text))
	}
	return out
}

// want fails t unless got is want.
func want(t *testing.T, name string, got []string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") || len(got) != len(want) {
		t.Errorf("%s: values %q, want %q", name, got, want)
	}
}

const comments = "# Ports\n\n" +
	"<!-- port: 8080 -->\n" + // an HTML block
	"Text <!-- port: 9090 --> more.\n\n" + // inline HTML
	"<!--\n  port: 7070\n-->\n\n" + // over three lines, at the line it starts
	"> a <!-- port:\n> 6060 -->\n\n" + // in a block quote, whose marker is no part of it
	"`<!-- port: 1 -->` and\n\n    <!-- port: 2 -->\n\n" + // a code span and a code block hold none
	"<script>\nif (a <!-- port: 3 -->) {}\n</script>\n\n" + // nor a script
	"<pre>\n<!-- port: 4040 -->\n</pre>\n\n" + // a pre does
	"<!--> <!---> <!-- port: 5050 --> x\n\n" + // two empty comments, then one
	"<!-- port: 5 no end\n"

// A comment is read wherever HTML is, in a block or inline, and nowhere
// else; its value is its text without the blanks at either end, and its
// line where it starts. The planted breaks: the inline comments left out
// (9090 and 6060 missing), a match's line read from the match (7070 at line
// 6), and a type 1 block read whole (port 3 found).
func TestCommentsAreReadWhereHTMLIs(t *testing.T) {
	got := read(t, Spec{Selector: markup.Selector{Target: "comment", Pattern: `port:\s*(\d+)`}}, comments)
	want(t, "pattern", got, "3:8080", "4:9090", "6:7070", "10:6060", "22:4040", "25:5050")
	got = read(t, Spec{Selector: markup.Selector{Target: "comment"}}, "<!-- a  b -->\n\n<!---->\n")
	want(t, "whole", got, "1:a  b", "3:")
}

const sections = "# errata\n\nIntro.\n\n" +
	"## Install\n\nRun make.\n\n" +
	"### Requirements\n\nGo *1.27* and\n`git`.\n\n" +
	"```sh\n# Requirements\nnot a heading\n```\n\n" +
	"> ## Requirements\n> in a quote\n\n" +
	"## Usage\n\nerrata check.\n\n" +
	"### Requirements\n\nNothing.\n\n" +
	"## Install\n\nAgain.\n"

// A section is the prose under a heading, to the next heading of the same
// or a higher level, read as the text extractor reads prose. A path names
// the headings a heading sits under, the nearest first, from any level. A
// heading in a code block is none, and one in a block quote is prose. Two headings the path
// names give a value each; a path that names none gives none. The planted
// breaks: a section run to the next heading of any level (Install stops at
// Requirements), the path matched as a subsequence (Usage > Requirements
// found under errata > Usage), and a heading in a code block read as one.
func TestSectionsByHeadingPath(t *testing.T) {
	got := read(t, Spec{Selector: markup.Selector{Target: "section", Section: "Install > Requirements"}}, sections)
	want(t, "path", got, "9:Go 1.27 and git.\nRequirements\nin a quote")
	got = read(t, Spec{Selector: markup.Selector{Target: "section", Section: "Install"}}, sections)
	want(t, "twice", got, "5:Run make.\nRequirements\nGo 1.27 and git.\nRequirements\nin a quote", "30:Again.")
	got = read(t, Spec{Selector: markup.Selector{Target: "section", Section: "errata > Requirements"}}, sections)
	want(t, "not a parent", got)
	got = read(t, Spec{Selector: markup.Selector{Target: "section", Section: "Nowhere"}}, sections)
	want(t, "none", got)
	got = read(t, Spec{Selector: markup.Selector{Target: "section", Section: "Requirements", Pattern: `(\w+)\.$`}}, sections)
	want(t, "match lines", got, "12:git", "28:Nothing")
}

// A heading path keeps every other target to its sections, once each where
// sections of the path nest. The planted break: the nested scope read again,
// which gives b twice.
func TestAPathScopesEveryTarget(t *testing.T) {
	body := "# A\n\n<!-- a -->\n\n## A\n\n<!-- b -->\n\n# B\n\n<!-- c -->\n"
	got := read(t, Spec{Selector: markup.Selector{Target: "comment", Section: "A"}}, body)
	want(t, "nested", got, "3:a", "7:b")
}

const code = "# Build\n\n```sh title=x\nmake build\n```\n\n" +
	"## Test\n\n~~~ sh\n  go test \\\n  ./...\n~~~\n\n" +
	"```yaml\nport: 8080\n```\n\n```\n```\n\n    indented\n"

// A code target reads the fenced code blocks whose language, the first
// word of the info string, is language, each line as the block holds it, at
// the line of its first. The planted breaks: the info string compared whole
// (sh title=x missed), and an indented block read as fenced.
func TestCodeBlocksByLanguage(t *testing.T) {
	got := read(t, Spec{Selector: markup.Selector{Target: "code", Language: "sh"}}, code)
	want(t, "sh", got, "4:make build", "10:  go test \\\n  ./...")
	got = read(t, Spec{Selector: markup.Selector{Target: "code", Section: "Test", Pattern: `port: (\d+)`}}, code)
	want(t, "in a section", got, "15:8080")
	got = read(t, Spec{Selector: markup.Selector{Target: "code"}}, code)
	want(t, "every fenced", got, "4:make build", "10:  go test \\\n  ./...", "15:port: 8080", "18:")
}

const table = "# Hosts\n\n" +
	"| Host | Port | Path |\n|---|--:|---|\n" +
	"| web | 8080 | `/a\\|b` |\n" +
	"| *db* | 5432 \\| 5433 |\n" +
	"| web | 9090 | x |\n\n" +
	"| Host | Other |\n|-|-|\n| web | 1 |\n\n" +
	"| a | b |\n| c | d |\n" // no delimiter row: a paragraph

// A table target reads the cell under the header column of each row, or of
// the rows whose key cell holds row, read as prose, at its row's line, and
// the cell left out is empty. A \| is a pipe in a cell, in a code span too.
// A table without the column gives nothing. The planted breaks: the key cell
// compared before its emphasis is read (db missed), and the backslash of
// \| kept.
func TestTableCellsByColumnAndKey(t *testing.T) {
	got := read(t, Spec{Selector: markup.Selector{Target: "table", Column: "Port", Key: "Host", Row: "web"}}, table)
	want(t, "web", got, "5:8080", "7:9090")
	got = read(t, Spec{Selector: markup.Selector{Target: "table", Column: "Path"}}, table)
	want(t, "path", got, "5:/a|b", "6:", "7:x")
	got = read(t, Spec{Selector: markup.Selector{Target: "table", Column: "Port", Key: "Host", Row: "db", Pattern: `(\d+)`}}, table)
	want(t, "pattern", got, "6:5432", "6:5433")
	got = read(t, Spec{Selector: markup.Selector{Target: "table", Column: "b"}}, table)
	want(t, "no table", got)
}

// Build refuses a spec that names no target or one errata does not read, a
// field its target does not read, and one its target needs left out, by
// what is wrong. Each case was seen passing Build before its check.
func TestBuildRefuses(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{}, "markdown: target is required; write comment, section, code or table"},
		{Spec{Selector: markup.Selector{Target: "heading"}}, `markdown: target "heading" is not one of comment, section, code and table`},
		{Spec{Selector: markup.Selector{Target: "comment", Language: "sh"}}, "markdown: language is read only by target code, and this one is comment"},
		{Spec{Selector: markup.Selector{Target: "code", Column: "a"}}, "markdown: column is read only by target table, and this one is code"},
		{Spec{Selector: markup.Selector{Target: "section", Key: "a"}}, "markdown: key is read only by target table, and this one is section"},
		{Spec{Selector: markup.Selector{Target: "code", Row: "a"}}, "markdown: row is read only by target table, and this one is code"},
		{Spec{Selector: markup.Selector{Target: "section"}}, "markdown: target section needs section, the heading path"},
		{Spec{Selector: markup.Selector{Target: "table"}}, "markdown: target table needs column"},
		{Spec{Selector: markup.Selector{Target: "table", Column: "a", Key: "b"}}, "markdown: key and row go together"},
		{Spec{Selector: markup.Selector{Target: "table", Column: "a", Row: "b"}}, "markdown: key and row go together"},
		{Spec{Selector: markup.Selector{Target: "code", Section: "A >  > B"}}, `markdown: section "A >  > B" names an empty heading`},
		{Spec{Selector: markup.Selector{Target: "code", Pattern: "x"}}, "markdown: pattern \"x\" has no capture group"},
		{Spec{OneFile: core.OneFile{MaxBytes: -1}, Selector: markup.Selector{Target: "code"}}, "markdown: max_bytes is -1"},
	} {
		if c.spec.File == "" {
			c.spec.File = "README.md"
		}
		_, err := Build(c.spec)
		extracttest.WantError(t, err, c.want)
	}
	_, err := Build(Spec{Selector: markup.Selector{Target: "code"}})
	extracttest.WantError(t, err, "markdown: file is required")
}

// Every value of every target is at a line inside its file, on samples of
// each target's construct and of a file of every kind of block.
func TestEveryValueLineIsInsideItsFile(t *testing.T) {
	for _, body := range []string{comments, sections, code, table, "\xef\xbb\xbf---\na: 1\n---\n# A\r\n\r\n<!-- x -->\r\n", "", "# A"} {
		lines := core.LastLine([]byte(body))
		for _, s := range specs {
			values := fromData(t, s, body)
			for _, v := range values {
				if v.At.Line < 1 || v.At.Line > lines {
					t.Errorf("%+v over %q: %q at line %d of %d", s, body, v.Text, v.At.Line, lines)
				}
			}
		}
	}
}

// specs are a spec of each target, with and without a pattern and a path.
var specs = []Spec{
	{Selector: markup.Selector{Target: "comment"}}, {Selector: markup.Selector{Target: "comment", Pattern: `(\S+)`, Section: "A"}},
	{Selector: markup.Selector{Target: "section", Section: "A"}}, {Selector: markup.Selector{Target: "section", Section: "Install", Pattern: `(\S+)`}},
	{Selector: markup.Selector{Target: "code"}}, {Selector: markup.Selector{Target: "code", Pattern: `(?s)(.+)`}},
	{Selector: markup.Selector{Target: "table", Column: "Port"}}, {Selector: markup.Selector{Target: "table", Column: "Port", Key: "Host", Row: "web", Pattern: `(.)`}},
}

// fromData is the values s reads from body, which the test fails on a
// refusal of.
func fromData(t *testing.T, s Spec, body string) []check.Value {
	t.Helper()
	s.File = "README.md"
	e, err := Build(s)
	if err != nil {
		t.Fatal(err)
	}
	values, err := e.(extractor).FromData([]byte(body))
	if err != nil {
		t.Fatalf("%+v: %v", s, err)
	}
	return values
}

// The text read may be at most maxProseRatio times its file; the planted
// break is one byte more.
func TestCheckRatio(t *testing.T) {
	if err := checkRatio("README.md", 1200, 100); err != nil {
		t.Errorf("at the limit: %v", err)
	}
	err := checkRatio("README.md", 1201, 100)
	extracttest.WantError(t, err, "README.md: the text read is 1201 bytes, over the limit of 1200, 12 times the file's size; "+core.OtherWay)
}

// The values over every target together are held to the cap on values,
// with a pattern and without. The planted break: the count of a pattern's
// values started again for each comment.
func TestValuesAreCapped(t *testing.T) {
	body := strings.Repeat("<!-- a -->", core.MaxValues/2+1) + "\n\n" + strings.Repeat("<!-- a -->", core.MaxValues/2+1)
	for _, s := range []Spec{{Selector: markup.Selector{Target: "comment"}}, {Selector: markup.Selector{Target: "comment", Pattern: "(a)"}}} {
		s.File = "README.md"
		e, err := Build(s)
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.(extractor).FromData([]byte(body))
		extracttest.WantError(t, err, "README.md: the extractor selects more than 100000 values, the limit")
	}
}

// lines is a file of each construct whose line a value could miss: CRLF
// endings, a fenced block in a list item, a table in a block quote with a
// lazy continuation row, and matches on lazy lines of a paragraph and of a
// quote (review of 9908c3a, finding 4).
const lines = "# A\r\n\r\nx\r\n\r\n<!-- c1 -->\r\n\r\n- item\r\n\r\n  ```sh\r\n  echo hi\r\n  ```\r\n\r\n" +
	"> | Host | Port |\r\n> |---|---|\r\n> | web | 80 |\r\nlazy | 81\r\n\r\n## B\r\n\r\n" +
	"para one\r\nlazy two 2.0\r\n> quote 3.0\r\nlazy 4.0\r\n"

// Every target gives the exact line of the unnormalised file: with CRLF,
// after a byte order mark, and after front matter, which moves every line
// by four; and under setext headings, nested lists and a heading on the
// last line with no newline. The planted break: the line of a match counted
// in the prose rather than mapped back to the file.
func TestExactLines(t *testing.T) {
	cases := []struct {
		spec Spec
		want []string
	}{
		{Spec{Selector: markup.Selector{Target: "comment"}}, []string{"5:c1"}},
		{Spec{Selector: markup.Selector{Target: "code"}}, []string{"10:echo hi"}},
		{Spec{Selector: markup.Selector{Target: "table", Column: "Port"}}, []string{"15:80", "16:81"}},
		{Spec{Selector: markup.Selector{Target: "table", Column: "Port", Key: "Host", Row: "lazy"}}, []string{"16:81"}},
		{Spec{Selector: markup.Selector{Target: "section", Section: "A > B", Pattern: `(one)`}}, []string{"20:one"}},
		{Spec{Selector: markup.Selector{Target: "section", Section: "B", Pattern: `(\d\.\d)`}}, []string{"21:2.0", "22:3.0", "23:4.0"}},
	}
	for _, prefix := range []struct {
		text  string
		lines int
	}{{"", 0}, {"\xef\xbb\xbf", 0}, {"---\r\na: 1\r\nb: 2\r\n---\r\n", 4}} {
		for _, c := range cases {
			got := read(t, c.spec, prefix.text+lines)
			var expect []string
			for _, w := range c.want {
				line, text, _ := strings.Cut(w, ":")
				var n int
				_, _ = fmt.Sscan(line, &n)
				expect = append(expect, fmt.Sprintf("%d:%s", n+prefix.lines, text))
			}
			want(t, fmt.Sprintf("%q %+v", prefix.text, c.spec.Selector), got, expect...)
		}
	}
	setext := "Title\n=====\n\n- a\nlazy 1.1\n\n  - b\n    c 1.2\n\nSub\n---\n\nz 1.3\n# Last"
	got := read(t, Spec{Selector: markup.Selector{Target: "section", Section: "Title", Pattern: `(\d\.\d)`}}, setext)
	want(t, "setext", got, "5:1.1", "8:1.2", "13:1.3")
	got = read(t, Spec{Selector: markup.Selector{Target: "section", Section: "Title > Sub"}}, setext)
	want(t, "setext path", got, "10:z 1.3")
	got = read(t, Spec{Selector: markup.Selector{Target: "section", Section: "Last"}}, setext)
	want(t, "last line", got, "14:")
}

// A section's prose holds the text of the HTML blocks under its heading,
// read by the HTML parse as the text extractor reads them (#86): a div's
// lines, a pre's lines, a details element's summary. A comment and a
// script in the block are no prose, and the comment target still finds the
// comment. The planted break: HTML blocks left out of the prose, as before.
func TestSectionsReadHTMLBlocks(t *testing.T) {
	body := "# A\n\n<div align=center>\n<p>Go 1.27</p><!-- c -->\n</div>\n\n<details><summary>More</summary>\n\nx\n</details>\n\n<pre>\nmake  build\n</pre>\n"
	got := read(t, Spec{Selector: markup.Selector{Target: "section", Section: "A"}}, body)
	want(t, "section", got, "1:Go 1.27\nMore\nx\nmake build")
	got = read(t, Spec{Selector: markup.Selector{Target: "section", Section: "A", Pattern: `(build|1\.27)`}}, body)
	want(t, "lines", got, "4:1.27", "13:build")
	got = read(t, Spec{Selector: markup.Selector{Target: "comment"}}, body)
	want(t, "comment", got, "4:c")
}

// In a block a blank line ends, and in a pre or a textarea block, comments
// are read by the HTML parse, as the html kind reads them: one in a
// script, a template or a textarea inside a div is none, as in a browser,
// and one in a quote's div reads without the markers, at its line (#86).
// The planted break: those blocks scanned for <!-- as before (s, t and a
// found).
func TestHTMLBlockCommentsAreTheBrowsers(t *testing.T) {
	body := "<div><script><!-- s --></script><template><!-- t --></template><textarea><!-- a --></textarea>\n<!-- kept --></div>\n\n> <div>\n> <!-- in\n> quote --></div>\n"
	got := read(t, Spec{Selector: markup.Selector{Target: "comment"}}, body)
	want(t, "comments", got, "2:kept", "5:in\nquote")
}

// A <br> in a heading is a space in its text, as the html kind reads it,
// so the path Foo Bar names it in both.
func TestALineElementInAHeadingIsASpace(t *testing.T) {
	got := read(t, Spec{Selector: markup.Selector{Target: "section", Section: "Foo Bar"}}, "## Foo<br>Bar\n\nx\n")
	want(t, "br", got, "1:x")
}
