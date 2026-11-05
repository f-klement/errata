package html

import (
	"fmt"
	"math"
	"runtime"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/markup"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// read is the values s reads from an index.html that holds body, each as
// "line:text", read from the file under a repository as Extract reads it.
func read(t *testing.T, s Spec, body string) []string {
	t.Helper()
	s.File = "index.html"
	root := extracttest.Repo(t, map[string]string{"index.html": body})
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

// sel is a spec of target with the fields of f set on its selector.
func sel(target string, f func(*markup.Selector)) Spec {
	s := Spec{Selector: markup.Selector{Target: target}}
	if f != nil {
		f(&s.Selector)
	}
	return s
}

const comments = "<!doctype html>\n<html><body>\n" +
	"<!-- port: 8080 -->\n" + // a comment
	"<p>Text <!-- port: 9090 --> more.</p>\n" + // inside text
	"<!--\n  port: 7070\n-->\n" + // over three lines, at the line it starts
	"<script>if (a <!-- port: 1 -->) {}</script>\n" + // a script holds none
	"<style>/* <!-- port: 2 --> */</style><textarea><!-- port: 3 --></textarea>\n" + // nor a style or a textarea
	"<template><!-- port: 4 --></template><title><!-- port: 5 --></title>\n" + // nor a template or a title
	"<img alt=\"<!-- port: 6 -->\">\n" + // nor an attribute's value
	"<pre><!-- port: 4040 --></pre>\n" + // a pre does
	"<!--> <!---> <!-- port: 5050 --!> x\n" + // two empty comments, then one that ends with --!>
	"<?php port: 7 ?><!DOCTYPE port: 8>\n" + // a processing instruction and a declaration are none
	"<!-- port: 9 no end\n"

// A comment is read wherever a browser keeps one, and nowhere else; its
// value is its text without the blanks at either end, and its line where it
// starts. The planted breaks: the scanner's hidden elements read (ports 1,
// 2, 5 found), a template read (4), a bogus comment read (7 and 8), an
// unclosed comment read (9), and a match's line read from the match (7070
// at line 6).
func TestCommentsAreReadWhereABrowserKeepsThem(t *testing.T) {
	got := read(t, sel("comment", func(s *markup.Selector) { s.Pattern = `port:\s*(\d+)` }), comments)
	want(t, "comments", got, "3:8080", "4:9090", "5:7070", "12:4040", "13:5050")
	got = read(t, sel("comment", nil), "<!--> <!---> <!--  a b  -->")
	want(t, "whole", got, "1:", "1:", "1:a b")
}

const sections = "<html><head><title>Install</title></head><body>\n" + // a title is no heading
	"<h1>Install</h1>\n" +
	"<p>Needs <b>Go</b> 1.27.</p>\n" +
	"<h2 id=r>Requirements</h2>\n" +
	"<ul><li>Go 1.27</li><li>git</li></ul>\n" +
	"<pre>  make\n  build</pre>\n" +
	"<h2>Run</h2>\n" +
	"<p>errata check &amp; done</p>\n" +
	"<h1>Other</h1>\n" +
	"<h3>Requirements</h3>\n" +
	"<p>none</p>\n"

// A section holds what lies under its heading, to the next heading of the
// same or a higher level, its subsections' headings included, one line for
// each element a browser starts a line at, and a pre's lines as lines. Its
// value is at its heading's line, a match at the line where it starts. A
// heading's text is what a browser shows of it. The planted breaks: a
// section ended at any heading (Run missing from Install), the path read as
// any heading of its last name (the h3 under Other found), an entity left
// as written, and the heading's own text read into its section.
func TestSectionsByHeadingPath(t *testing.T) {
	got := read(t, sel("section", func(s *markup.Selector) { s.Section = "Install" }), sections)
	want(t, "install", got, "2:Needs Go 1.27.\nRequirements\nGo 1.27\ngit\nmake\nbuild\nRun\nerrata check & done")
	got = read(t, sel("section", func(s *markup.Selector) { s.Section = "Install > Requirements"; s.Pattern = `(?m)^(\w+)` }), sections)
	want(t, "path", got, "5:Go", "5:git", "6:make", "7:build")
	got = read(t, sel("section", func(s *markup.Selector) { s.Section = "Requirements" }), sections)
	want(t, "two", got, "4:Go 1.27\ngit\nmake\nbuild", "11:none")
	got = read(t, sel("section", func(s *markup.Selector) { s.Section = "Nowhere" }), sections)
	want(t, "none", got)
}

// A heading path scopes every target: a comment, a code block or a table
// counts only inside a named section, and a nested named section is read
// once. A heading's start ends the heading open, as in a browser. The
// planted break: the scope left out, which reads the comment before.
func TestAPathScopesEveryTarget(t *testing.T) {
	body := "<!-- a -->\n<h1>A</h1>\n<!-- b -->\n<h2>A</h2><!-- c -->\n<h1>B<h2>x</h2>\n<!-- d -->\n<h1>A</h1>\n<!-- e -->"
	got := read(t, sel("comment", func(s *markup.Selector) { s.Section = "A" }), body)
	want(t, "comments", got, "3:b", "4:c", "8:e")
}

const code = "<h1>Install</h1>\n" +
	"<pre><code class=\"hljs language-sh\">echo a &amp;&amp; b\n  make &lt;x&gt;\n</code></pre>\n" +
	"<pre class=\"lang-go\">\npackage <span>main</span></pre>\n" +
	"<pre><code class=\"language-sh\"></code></pre>\n" +
	"<p>inline <code class=\"language-sh\">no</code></p>\n" +
	"<listing>old</listing>\n" +
	"<h1>Other</h1>\n<pre><code class=lang-sh>other</code></pre>\n"

// A code block is a pre element, its language the one a class of the pre
// or of a code element inside it names, its text as written with tags left
// out and character references decoded, and the line feed after <pre>
// dropped as a browser drops it. An inline code element is no block. The
// value is at its first line, the tag's for an empty block. The planted
// breaks: the language read only from the pre (sh blocks missing), the
// first line feed kept (go at line 3), references left as written.
func TestCodeBlocksByLanguage(t *testing.T) {
	got := read(t, sel("code", func(s *markup.Selector) { s.Language = "sh" }), code)
	want(t, "sh", got, "2:echo a && b\n  make <x>", "7:", "11:other")
	got = read(t, sel("code", func(s *markup.Selector) { s.Language = "go" }), code)
	want(t, "go", got, "6:package main")
	got = read(t, sel("code", func(s *markup.Selector) { s.Section = "Install" }), code)
	want(t, "all", got, "2:echo a && b\n  make <x>", "6:package main", "7:", "9:old")
	got = read(t, sel("code", func(s *markup.Selector) { s.Section = "Install"; s.Pattern = `(make|main)` }), code)
	want(t, "matches", got, "3:make", "6:main")
}

// A lone CR after <pre> is the pre's first line feed, which a browser
// drops before it reopens a formatting element, so the </code> after it
// ends a code the pre's start closed and is read, not refused. The find
// of FuzzHTML the round after 04c52e9, its seed 66ff48555d1e4d7c. A lone CR
// inside a pre is a line feed too, which ends its line. The planted breaks:
// only '\n' dropped after <pre>; a lone CR kept as a CR.
func TestPreFirstCarriageReturn(t *testing.T) {
	got := read(t, sel("code", nil), "<p ><Code ><pre>\r<p></Code>")
	want(t, "find", got, "1:")
	got = read(t, sel("code", nil), "<pre>\rx\n</pre>")
	want(t, "lone CR", got, "2:x")
	got = read(t, sel("code", nil), "<pre>a\rb\r</pre>\rc<pre>d</pre>")
	want(t, "lone CRs in a pre", got, "1:a\nb", "4:d")
}

const tables = "<table>\n" +
	"<thead><tr><th>Host</th><th>Port</th></tr></thead>\n" +
	"<tbody>\n<tr><td>web</td><td> <b>80</b> </td></tr>\n" +
	"<tr><td>db<td>5432\n" + // cells and rows a browser implies
	"<tr><td>web</td><td>8080<table><tr><th>Port</th></tr><tr><td>1</td></tr></table>after</td></tr>\n" + // a table in a cell is its own
	"<tr><td>short</td></tr>\n" +
	"</tbody></table>\n" +
	"<table><tr><td>Port</td><td>Port</td></tr><tr><td>a</td><td>b</td></tr></table>\n" // the first of two headers alike

// A table's first row holds its headers, and each row after it gives the
// cell under the column, or only the rows whose key cell holds the row, at
// the row's line; a cell is what a browser shows of it, and a table inside
// a cell is a table of its own, where the cell's text ends. The
// planted breaks: an implied cell not ended (db's port missing), the inner
// table's text read into the outer cell (8080 1), a short row read.
func TestTableCellsByColumnAndKey(t *testing.T) {
	got := read(t, sel("table", func(s *markup.Selector) { s.Column = "Port" }), tables)
	want(t, "column", got, "4:80", "5:5432", "6:1", "6:8080", "9:a")
	got = read(t, sel("table", func(s *markup.Selector) { s.Column = "Port"; s.Key = "Host"; s.Row = "web" }), tables)
	want(t, "key", got, "4:80", "6:8080")
	got = read(t, sel("table", func(s *markup.Selector) { s.Column = "Nope" }), tables)
	want(t, "no header", got)
}

// Build refuses what markup refuses, under the name html, and the file as
// every reader does.
func TestBuildRefuses(t *testing.T) {
	_, err := Build(Spec{OneFile: core.OneFile{File: "a.html"}})
	extracttest.WantError(t, err, "html: target is required; write comment, section, code or table")
	_, err = Build(Spec{OneFile: core.OneFile{File: "a.html"}, Selector: markup.Selector{Target: "comment", Language: "sh"}})
	extracttest.WantError(t, err, "html: language is read only by target code, and this one is comment")
	_, err = Build(Spec{Selector: markup.Selector{Target: "code"}})
	extracttest.WantError(t, err, "html: file is required")
}

// specs are a spec of each target, with and without a pattern and a path.
var specs = []Spec{
	sel("comment", nil), sel("comment", func(s *markup.Selector) { s.Pattern = `(\S+)`; s.Section = "A" }),
	sel("section", func(s *markup.Selector) { s.Section = "A" }), sel("section", func(s *markup.Selector) { s.Section = "Install"; s.Pattern = `(\S+)` }),
	sel("code", nil), sel("code", func(s *markup.Selector) { s.Pattern = `(?s)(.+)` }),
	sel("table", func(s *markup.Selector) { s.Column = "Port" }), sel("table", func(s *markup.Selector) { s.Column = "Port"; s.Key = "Host"; s.Row = "web"; s.Pattern = `(.)` }),
}

// fromData is the values s reads from body, which the test fails on a
// refusal of.
func fromData(t *testing.T, s Spec, body string) []check.Value {
	t.Helper()
	s.File = "index.html"
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

// Every value of every target is at a line inside its file, on samples of
// each target's construct and of a file with CRLF, a byte order mark and
// no ending newline.
func TestEveryValueLineIsInsideItsFile(t *testing.T) {
	for _, body := range []string{comments, sections, code, tables, "\xef\xbb\xbf<h1>A</h1>\r\n<!-- x -->\r\n<pre>y\r\n</pre>", "", "<h1>A", "<table><td>Port<tr><td>1"} {
		lines := core.LastLine([]byte(body))
		for _, s := range specs {
			for _, v := range fromData(t, s, body) {
				if v.At.Line < 1 || v.At.Line > lines {
					t.Errorf("%+v over %q: %q at line %d of %d", s, body, v.Text, v.At.Line, lines)
				}
			}
		}
	}
}

// lines is a file of each construct whose line a value could miss: CRLF
// endings, a comment after a script of many lines, a code block whose text
// starts on the line after its tag, and a section whose matches lie over
// several lines (#86).
const lines = "<h1>A</h1>\r\n<script>\r\n1\r\n</script><!-- c1 -->\r\n" +
	"<pre><code class=language-sh>\r\necho hi</code></pre>\r\n" +
	"<table><tr><th>Host<th>Port\r\n<tr><td>web<td>80\r\n<tr>\r\n<td>db<td>\r\n5432</table>\r\n" +
	"<h2>B</h2><p>one\r\ntwo 2.0</p><p>3.0\r\n</p>4.0\r\n"

// Every target gives the exact line of the file as it is, with CRLF and
// after a byte order mark. The planted break: a match's line counted in
// the text rather than mapped back to the file.
func TestExactLines(t *testing.T) {
	cases := []struct {
		spec Spec
		want []string
	}{
		{sel("comment", nil), []string{"4:c1"}},
		{sel("code", nil), []string{"5:\necho hi"}},
		{sel("table", func(s *markup.Selector) { s.Column = "Port" }), []string{"8:80", "9:5432"}},
		{sel("table", func(s *markup.Selector) { s.Column = "Host"; s.Key = "Port"; s.Row = "5432" }), []string{"9:db"}},
		{sel("section", func(s *markup.Selector) { s.Section = "A > B"; s.Pattern = `(two|\d\.\d)` }), []string{"13:two", "13:2.0", "13:3.0", "14:4.0"}},
	}
	for _, prefix := range []string{"", "\xef\xbb\xbf"} {
		for _, c := range cases {
			want(t, fmt.Sprintf("%q %+v", prefix, c.spec.Selector), read(t, c.spec, prefix+lines), c.want...)
		}
	}
}

// The text read may be at most maxTextRatio times its file; the planted
// break is one byte more.
func TestCheckRatio(t *testing.T) {
	if err := checkRatio("index.html", 1200, 100); err != nil {
		t.Errorf("at the limit: %v", err)
	}
	err := checkRatio("index.html", 1201, 100)
	extracttest.WantError(t, err, "index.html: the text read is 1201 bytes, over the limit of 1200, 12 times the file's size; "+core.OtherWay)
}

// The values of a target are held to the cap on values, with a pattern and
// without, and a reading stops once it has more items than the cap, before
// it reads the rest of the file. The planted break: the items not counted
// as the reading goes, which reads on to a token past the limit.
func TestValuesAreCapped(t *testing.T) {
	body := strings.Repeat("<!-- a -->", core.MaxValues+1)
	for _, s := range []Spec{sel("comment", nil), sel("comment", func(s *markup.Selector) { s.Pattern = "(a)" })} {
		s.File = "index.html"
		e, err := Build(s)
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.(extractor).FromData([]byte(body))
		extracttest.WantError(t, err, "index.html: the extractor selects more than 100000 values, the limit")
	}
	// 100002 comments, then tokens past the limit of 100006.
	_, err := fromLimit(sel("comment", nil), 4*100_005, strings.Repeat("<!---->", core.MaxValues+2)+strings.Repeat("<a>", 10))
	extracttest.WantError(t, err, "index.html: the extractor selects more than 100000 values, the limit")
}

// A file of more tokens than max_bytes allows, of more pieces of text, or
// of more than core.MaxDepth elements open, as 251 nested tables, is
// refused at the line where it passed the bound; at the bound it reads.
// The planted breaks: each bound taken away.
func TestBounds(t *testing.T) {
	refuse := func(s Spec, limit int64, body, wantErr string) {
		t.Helper()
		s.File, s.MaxBytes = "index.html", limit
		e, err := Build(s)
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.(extractor).FromData([]byte(body))
		extracttest.WantError(t, err, wantErr)
	}
	// 40 bytes allow 11 tokens: <a> and x five times and one more is 11.
	tokens := strings.Repeat("<a>x", 5) + "\n<a>"
	if _, err := fromLimit(sel("comment", nil), 40, tokens); err != nil {
		t.Errorf("at the token limit: %v", err)
	}
	refuse(sel("comment", nil), 40, tokens+"y", "index.html: line 2: the HTML holds more than 11 tokens, the limit, one for every 4 bytes of max_bytes; set max_bytes: higher to read a file this broken up")
	// 40 bytes allow 10 pieces; a run of blanks folded starts two.
	pieces := "<h1>A</h1>\na\tb\tc\td\te\t"
	if _, err := fromLimit(sel("section", func(s *markup.Selector) { s.Section = "A" }), 40, pieces); err != nil {
		t.Errorf("at the piece limit: %v", err)
	}
	refuse(sel("section", func(s *markup.Selector) { s.Section = "A" }), 40, pieces+"f", "index.html: line 2: the text is made of more than 10 pieces of the file, the limit, one for every 4 bytes of max_bytes; set max_bytes: higher to read a file this broken up")
	// A table nested in a cell opens four elements, table, tbody, tr and
	// td, the tbody and the tr implied when a page leaves them out.
	for _, unit := range []string{"<table><tr><td>\n", "<table><td>\n"} {
		deep := strings.Repeat(unit, core.MaxDepth/4)
		if _, err := fromLimit(sel("table", func(s *markup.Selector) { s.Column = "x" }), 0, deep); err != nil {
			t.Errorf("%q: 250 tables: %v", unit, err)
		}
		refuse(sel("table", func(s *markup.Selector) { s.Column = "x" }), 0, deep+"<table><tr>", "index.html: line 251: nested more than 1000 levels deep, the limit; "+core.OtherWay)
	}
}

// fromLimit is the values s reads from body under max_bytes limit.
func fromLimit(s Spec, limit int64, body string) ([]check.Value, error) {
	s.File, s.MaxBytes = "index.html", limit
	e, err := Build(s)
	if err != nil {
		return nil, err
	}
	return e.(extractor).FromData([]byte(body))
}

// language is the rest of the first class that starts with language- or
// lang-; a bare prefix names none.
func TestLanguage(t *testing.T) {
	for class, want := range map[string]string{"": "", "hljs language-go": "go", "lang-sh x": "sh", "language- lang-c": "c", "languages-x": "", "Language-x": ""} {
		if got := language(class); got != want {
			t.Errorf("%q: got %q, want %q", class, got, want)
		}
	}
}

// Every pass over a file is linear in it (ADR 0003): on each shape that
// could cost more than its size, nested tables, a row of many cells,
// sections nested six deep, comments, a comment or a tag with no end, a
// script of many </s, a tag of many attributes and many references, twice
// the file allocates at most three times as much for every target, where a
// quadratic pass allocates four. The planted break: the text copied whole
// for each piece added to it.
func TestReadIsLinear(t *testing.T) {
	shapes := map[string]func(n int) string{
		"nested tables": func(n int) string { return strings.Repeat("<table><tr><td>Port<tr><td>x", n/40) },
		"row of cells": func(n int) string {
			return "<table><tr>" + strings.Repeat("<th>Port", n) + "<tr>" + strings.Repeat("<td>a", n)
		},
		"nested sections": func(n int) string {
			return strings.Repeat("<h1>A</h1>a<h2>A</h2>b<h3>A</h3>c<h4>A</h4>d<h5>A</h5>e<h6>A</h6>f\n", n/8)
		},
		"comments":     func(n int) string { return strings.Repeat("<!-- a -->", n) },
		"open comment": func(n int) string { return "<!--" + strings.Repeat("a -", n) },
		"open tag":     func(n int) string { return strings.Repeat("<a ", n) },
		"script":       func(n int) string { return "<script>" + strings.Repeat("</s", n) },
		"attributes":   func(n int) string { return "<pre " + strings.Repeat("a=b ", n) + "class=lang-x>" },
		"references":   func(n int) string { return "<h1>A</h1><pre>" + strings.Repeat("&amp;&#65;\t", n) },
	}
	allocs := func(s Spec, src string) uint64 {
		least := uint64(math.MaxUint64)
		for range 2 {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, _ = fromLimit(s, 0, src)
			runtime.ReadMemStats(&after)
			least = min(least, after.TotalAlloc-before.TotalAlloc)
		}
		return least
	}
	for name, shape := range shapes {
		for _, s := range specs {
			small, large := allocs(s, shape(10_000)), allocs(s, shape(20_000))
			if large > 3*small {
				t.Errorf("%s, %+v: twice the file allocated %d bytes, over three times the %d of the file", name, s.Selector, large, small)
			}
		}
	}
}

// Edges that mutation found untested (#86). readOf sums the items' bytes;
// exactly as many values as the cap read; a key column after the column
// read, in a row too short to hold it, keeps the row out; text copied in
// one run is one piece, so a run with lone spaces reads under a bound of
// one piece where a piece for each word would not; a line whose last space
// is dropped keeps the line of its words; the newline that ends a line, and
// the space that stands for a run of blanks, are at the line of the bytes
// they stand for.
func TestMutationEdges(t *testing.T) {
	if got := readOf([]markup.Item{{Start: 2, End: 5}, {Start: 1, End: 10}}); got != 12 {
		t.Errorf("readOf: %d", got)
	}
	values, err := fromLimit(sel("comment", nil), 0, strings.Repeat("<!---->", core.MaxValues))
	if err != nil || len(values) != core.MaxValues {
		t.Errorf("at the cap: %d values, %v", len(values), err)
	}
	got := read(t, sel("table", func(s *markup.Selector) { s.Column = "Host"; s.Key = "Port"; s.Row = "80" }),
		"<table><tr><th>Host<th>Port<tr><td>web<td>80<tr><td>short</table>")
	want(t, "short key row", got, "1:web")
	if _, err := fromLimit(sel("section", func(s *markup.Selector) { s.Section = "A" }), 16, "<h1>A</h1>a b c d e f"); err != nil {
		t.Errorf("one run, one piece: %v", err)
	}
	section := func(body, pattern string) []string {
		return read(t, sel("section", func(s *markup.Selector) { s.Section = "A"; s.Pattern = pattern }), body)
	}
	want(t, "trailing space", section("<h1>A</h1>\n<p>one\n\ntwo </p>", `(two)`), "4:two")
	want(t, "newline after the last word", section("<h1>A</h1><p>x\na</p><p>b</p>", `(\nb)`), "2:\nb")
	want(t, "newline after a blank", section("<h1>A</h1>\n<p>x a\n</p><p>b</p>", `(\nb)`), "2:\nb")
	want(t, "space for blanks", section("<h1>A</h1>x\none\n\ntwo", `( two)`), "2: two")
}

// A heading's link to itself is no part of its text, so a path names the
// pages docs generators write: Sphinx's and MkDocs' ¶ after the text,
// Docusaurus's zero width space, rustdoc's § before it, pkg.go.dev's ¶
// after a space, GitHub's empty anchor, and 🔗. A link with other text
// stays, and so does a mark that links elsewhere, its href no #. The section's prose leaves the mark out too, in a subsection's
// heading. The planted break: the link's text kept (no value for any).
func TestPermalinksAreNoPartOfAHeading(t *testing.T) {
	for name, heading := range map[string]string{
		"sphinx":     `<h2>Install<a class="headerlink" href="#install" title="Link to this heading">¶</a></h2>`,
		"mkdocs":     `<h2 id="install">Install<a class="headerlink" href="#install" title="Permanent link">&para;</a></h2>`,
		"docusaurus": `<h2 class="anchor" id="install">Install<a href="#install" class="hash-link" aria-label="Direct link to Install">&#8203;</a></h2>`,
		"rustdoc":    `<h2 id="install" class="section-header"><a href="#install">§</a>Install</h2>`,
		"pkg.go.dev": `<h2 id="hdr-Install">Install <a class="Documentation-idLink" href="#hdr-Install">¶</a></h2>`,
		"github":     `<h2><a id="install" class="anchor" href="#install"></a>Install</h2>`,
		"emoji":      `<h2>Install <a href="#install">🔗</a></h2>`,
	} {
		got := read(t, sel("section", func(s *markup.Selector) { s.Section = "Install" }), heading+"\n<p>Go 1.27</p>")
		want(t, name, got, "1:Go 1.27")
	}
	got := read(t, sel("section", func(s *markup.Selector) { s.Section = "Install guide" }), `<h2>Install <a href="/guide">guide</a></h2><p>x</p>`)
	want(t, "a link with text", got, "1:x")
	got = read(t, sel("section", func(s *markup.Selector) { s.Section = "C#" }), `<h2>C<a href="/cs">#</a></h2><p>x</p>`)
	want(t, "a mark linking elsewhere", got, "1:x")
	got = read(t, sel("section", func(s *markup.Selector) { s.Section = "§ 3 Scope" }), `<h2><a href="/s3">§</a> 3 Scope</h2><p>x</p>`)
	want(t, "a § linking elsewhere", got, "1:x")
	got = read(t, sel("section", func(s *markup.Selector) { s.Section = "A" }), "<h1>A</h1>\n<h2>B<a href=\"#b\">¶</a></h2><p>x</p>")
	want(t, "subsection", got, "1:B\nx")
}

// A line element in a heading is a space in its text, as the markdown
// kind reads ## Foo<br>Bar, so one path fits both. The planted break: the
// space left out (FooBar).
func TestALineElementInAHeadingIsASpace(t *testing.T) {
	got := read(t, sel("section", func(s *markup.Selector) { s.Section = "Foo Bar" }), "<h2>Foo<br>Bar</h2><p>x</p>")
	want(t, "br", got, "1:x")
}

// A heading of more pieces than max_bytes allows is refused, never cut
// short and compared. The planted break: the heading's budget unchecked,
// which reads the cut heading and finds nothing.
func TestAHeadingPastItsPiecesIsRefused(t *testing.T) {
	_, err := fromLimit(sel("section", func(s *markup.Selector) { s.Section = "x" }), 40, "<h1>a\tb\tc\td\te\tf</h1>")
	extracttest.WantError(t, err, "index.html: line 1: the text is made of more than 10 pieces of the file, the limit")
}

// With a pattern, more comments than the cap on values read when the
// pattern matches few of them, as in the markdown kind; without one they
// are refused (TestValuesAreCapped). The planted break: the early stop for
// every selector.
func TestAPatternReadsPastTheCapOnItems(t *testing.T) {
	values, err := fromLimit(sel("comment", func(s *markup.Selector) { s.Pattern = "(b)" }), 0, strings.Repeat("<!--a-->", core.MaxValues+1)+"<!--b-->")
	if err != nil || len(values) != 1 {
		t.Errorf("got %d values, %v", len(values), err)
	}
}

// A browser's ends of a pre and a heading are followed: an ancestor's end
// tag inside the pre closes it, a heading's end tag of another level ends
// it, a table in a table outside a cell ends the outer one, CDATA in svg
// reads as its text. An end tag of an element open around the pre or the
// heading ends it too; one of no open element closes nothing; a heading
// opened inside a heading is refused at its line. The planted
// breaks: an end tag in the pre read as text (b in the block), the outer
// table kept open (9 and 1 both).
func TestBrowsersEndsAreFollowed(t *testing.T) {
	got := read(t, sel("code", nil), "<pre><div>a</div>b</pre><p>c</p>")
	want(t, "an end inside", got, "1:ab")
	got = read(t, sel("section", func(s *markup.Selector) { s.Section = "A" }), "<h2>A</h3>x<h2>B</h2>y")
	want(t, "another level", got, "1:x")
	got = read(t, sel("table", func(s *markup.Selector) { s.Column = "Port" }),
		"<table><tr><th>Port</th><tr><td>9</td><table><tr><th>Port<tr><td>1</table></table>")
	want(t, "a table outside a cell", got, "1:9", "1:1")
	got = read(t, sel("table", func(s *markup.Selector) { s.Column = "Port" }),
		"<table><caption><table><tr><th>Port<tr><td>1</table></caption><tr><th>Port<tr><td>2</table>")
	want(t, "a table in a caption nests", got, "1:1", "1:2")
	got = read(t, sel("section", func(s *markup.Selector) { s.Section = "A" }), "<h1>A</h1><svg><text><![CDATA[a > b]]></text></svg>after")
	want(t, "cdata", got, "1:a > bafter")
	// An end tag closes the pre or heading open inside the element it ends,
	// as in a browser; an end tag of no open element, </listing> in a pre,
	// closes nothing.
	got = read(t, sel("code", nil), "<div>\n<pre>a</div>b</pre>")
	want(t, "</div> ends the pre", got, "2:a")
	got = read(t, sel("section", func(s *markup.Selector) { s.Section = "A" }), "<div><h2>A\n</div>B</h2>")
	want(t, "</div> ends the heading", got, "1:B")
	got = read(t, sel("code", nil), "<div><table><tr><td>x</table><pre>code</div>more")
	want(t, "</table> past an open cell", got, "1:code")
	got = read(t, sel("code", nil), "<pre>a\n</listing>b</pre>")
	want(t, "</listing> in a pre", got, "1:a\nb")
	_, err := fromLimit(sel("code", nil), 0, "<li><h2>A<b>\n<h3>B</h3></h2>")
	if err == nil || !strings.HasPrefix(err.Error(), "index.html: line 2: a heading opens inside a heading") {
		t.Errorf("a heading in a heading: %v", err)
	}
}
