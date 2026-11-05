// Command loccensus prints how many lines of this repository are code, once
// comments, blank lines and the trees that are not the product are taken out.
//
// Usage: loccensus <module root>
//
// It reads every file git tracks under the root, sorts each into one bucket by
// the first rule of rules that matches its path, and prints per bucket and
// language: files, raw lines, code lines, and code plus docstrings. source is
// the residual bucket, and the one the question is usually about. make loc runs
// it on the checkout.
//
// It is a tool rather than a command in a note because the answer moves with
// every commit, and the plausible ways to take it by hand disagree with each
// other by more than they measure. A figure written into prose is stale on the
// next push; this prints what it counted.
//
// A code line is a physical line that holds a byte outside a comment and not
// blank. A string literal marks every line it spans as code, a JavaScript
// template literal's or a Python triple quoted string's continuation lines too,
// and a heredoc's body counts every line from its opener to its closer: those
// lines carry the literal's content, which is neither comment nor blank. The
// comments and literals are those of errata's own lexer table
// (internal/extract/core/lexer), the table the comment extractor reads, so the
// census reads a language as errata's rules do, and misses what the table
// misses: the shell row reads no heredoc, so a line of a shell heredoc's body
// that starts with # counts as a comment. A backslash that joins a line to the
// next is not code by itself, as Python's tokenizer reads it. A file of a
// language the table has no row for, such as Markdown, JSON or awk, has no
// comment rule here, and its code lines are its lines that are not blank. A
// script with no extension takes its row from its #! line.
//
// Two rules come from the census this one was ported from, where each produced
// a wrong figure before it held.
//
// First, /* always opens a block comment, never a regular expression literal.
// A regex body that starts with * is a quantifier with nothing to quantify and
// does not parse. A lexer that asks whether the token before permits a regex
// reads ordinary block comments as regex literals wherever they follow ;, { or
// =, which is most of them: in the tree this was ported from that moved the
// frontend figure from 31,323 lines to 50,019 and changed 465 of 490 files. The
// lexer tries the forms that open with a byte longest opener first, so /*
// opens the block comment before the regex's / is tried, wherever it stands.
// // inside a regex is the real ambiguity, and it is harmless: a line comment
// ends at the line's end, and a line that holds a regex is already code by the
// bytes before it.
//
// Second, Python docstrings are found from tokens, never by a pattern over the
// text, and reported as their own column, folded into neither code nor
// comment: a # inside a string is no comment, and a docstring is not a comment
// at all. The census this was ported from found them with Python's ast. Go has
// no Python parser, so pythonDocstrings reads the lexer's string spans and the
// code between them as a tokenizer would: brackets, backslash joins, and the
// statement ends at a line's end, a ; or a def's or class's colon. A docstring
// is a statement of string literals alone, with no prefix but u or r, that is
// the first statement of the file or follows a def, async def or class header.
// What it misses against ast is in pythonDocstrings's comment.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/f-klement/errata/internal/extract/core/lexer"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run prints the census of the module root args names, and is main's exit
// status.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: loccensus <module root>")
		return 2
	}
	files, err := tracked(args[0])
	if err != nil {
		fmt.Fprintln(stderr, "loccensus:", err)
		return 1
	}
	rows, err := census(args[0], files)
	if err != nil {
		fmt.Fprintln(stderr, "loccensus:", err)
		return 1
	}
	fmt.Fprint(stdout, render(rows))
	return 0
}

// The buckets, in the order the census prints them.
const (
	source    = "source"
	tests     = "tests"
	tooling   = "tooling"
	examples  = "examples"
	docs      = "docs"
	generated = "generated"
)

var bucketOrder = []string{source, tests, tooling, examples, docs, generated}

// A rule claims the paths that start with prefix, end with suffix, hold
// segment as a whole directory, or whose base name is name; an exact path is a
// prefix with no slash at its end that must match whole.
type rule struct {
	bucket                string
	exact, prefix, suffix string
	segment, name         string
}

func (r rule) matches(path string) bool {
	switch {
	case r.exact != "":
		return path == r.exact
	case r.prefix != "":
		return strings.HasPrefix(path, r.prefix)
	case r.suffix != "":
		return strings.HasSuffix(path, r.suffix)
	case r.name != "":
		return filepath.Base(path) == r.name
	default:
		return strings.HasPrefix(path, r.segment+"/") || strings.Contains(path, "/"+r.segment+"/")
	}
}

// rules sorts every tracked path; the first that matches wins, and a path none
// claims is source. The order matters: generated is first, so docs/cli.md is
// not docs and unicode_node.go is not source; tooling comes before tests, so a
// script's testdata under ci/ is tooling; tests come last, after every tree
// that is not the product, so they claim only the tests of cmd/ and internal/.
// A rule that matches no tracked file fails TestEveryRuleClaimsAFile, unless
// the path or name it is written for is not in the tree at all, as ci/ and the
// agent material are not in the public mirror. A path outside cmd/ and
// internal/ that falls through to source fails TestSourceIsOnlyTheProduct, so
// a new top level tree is placed here on purpose.
var rules = []rule{
	// Written by a program and committed; nobody edits these lines.
	// TestEveryGeneratedFileIsGenerated fails on a tracked file that says it is
	// generated and that no rule here claims.
	{bucket: generated, exact: "docs/cli.md"},                        // make cli-doc, from the help
	{bucket: generated, exact: "internal/minimatch/unicode_node.go"}, // testdata/unicode/gen.mjs
	{bucket: generated, exact: "go.sum"},                             // go mod tidy
	{bucket: generated, exact: "internal/rule/zoneinfo.zip"},         // copied from the Go toolchain
	{bucket: generated, exact: "internal/rule/zoneinfo.version"},     // the toolchain's tz version line

	// The tree's own instruments: written by hand and maintained, and not
	// the product. checks/ holds the claims errata.yaml makes about this
	// repository's docs; errata.yaml is that dogfood configuration.
	{bucket: tooling, prefix: "ci/"},
	{bucket: tooling, prefix: "scripts/"},
	{bucket: tooling, prefix: ".claude/"},
	{bucket: tooling, prefix: ".githooks/"},
	{bucket: tooling, prefix: "checks/"},
	{bucket: tooling, exact: "Makefile"},
	{bucket: tooling, exact: "Dockerfile"},
	{bucket: tooling, exact: ".gitlab-ci.yml"},
	{bucket: tooling, exact: ".golangci.yml"},
	{bucket: tooling, exact: ".goreleaser.yaml"},
	{bucket: tooling, exact: ".gremlins.yaml"},
	{bucket: tooling, exact: ".trivyignore.yaml"},
	{bucket: tooling, exact: ".gitignore"},
	{bucket: tooling, exact: "renovate.json"},
	{bucket: tooling, exact: "errata.yaml"},
	// Agent material: the guidance agents load and the configuration of
	// their tracker and labels. ci/public-tree.sh strips it with .claude/,
	// as the same concern.
	{bucket: tooling, name: "CLAUDE.md"},
	{bucket: tooling, prefix: "docs/agents/"},

	// The example repository the docs and the dogfood run read: data in a
	// dozen languages, none of it errata.
	{bucket: examples, prefix: "examples/"},

	// Prose. docs/internal/reference holds a few Python files kept for
	// reference, which count here with the prose that cites them.
	{bucket: docs, prefix: "docs/"},
	{bucket: docs, exact: "README.md"},
	{bucket: docs, exact: "ROADMAP.md"},
	{bucket: docs, exact: "GLOSSARY.md"},
	{bucket: docs, exact: "CODING_STANDARDS.md"},
	{bucket: docs, exact: "DOCKERHUB.md"},
	{bucket: docs, exact: "LICENSE"},

	// Go's tests, their fixtures and fuzz corpora, and the two packages that
	// only tests import (extracttest, coretest).
	{bucket: tests, suffix: "_test.go"},
	{bucket: tests, segment: "testdata"},
	{bucket: tests, prefix: "internal/extract/extracttest/"},
	{bucket: tests, prefix: "internal/extract/coretest/"},
}

// bucketOf is the bucket of the first rule that claims path, or source.
func bucketOf(path string) string {
	for _, r := range rules {
		if r.matches(path) {
			return r.bucket
		}
	}
	return source
}

// tracked is every file git tracks under root, by its path from root.
func tracked(root string) ([]string, error) {
	out, err := exec.CommandContext(context.Background(), "git", "-C", root, "ls-files", "-z").Output() //nolint:gosec // the module root the caller names, as make loc passes it
	if err != nil {
		return nil, fmt.Errorf("git ls-files in %s: %w", root, err)
	}
	var files []string
	for f := range strings.SplitSeq(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("git ls-files in %s lists no file", root)
	}
	return files, nil
}

// counts are one row of the census.
type counts struct{ files, raw, code, docs int }

func (c *counts) add(o counts) {
	c.files += o.files
	c.raw += o.raw
	c.code += o.code
	c.docs += o.docs
}

// census is every file's counts, summed by bucket and then language.
func census(root string, files []string) (map[string]map[string]counts, error) {
	rows := map[string]map[string]counts{}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f)) //nolint:gosec // a path git lists under the root
		if err != nil {
			return nil, err
		}
		lang, c := measure(f, data)
		b := bucketOf(f)
		if rows[b] == nil {
			rows[b] = map[string]counts{}
		}
		row := rows[b][lang]
		row.add(c)
		rows[b][lang] = row
	}
	return rows, nil
}

// plainText names the languages the lexer table has no row for, by
// extension. Their code lines are their lines that are not blank.
var plainText = map[string]string{
	".md": "markdown", ".json": "json", ".jsonl": "json", ".txtar": "txtar",
	".txt": "text", ".awk": "awk",
}

// measure is the language of the file at path holding data, and its counts:
// one file, its raw lines, its code lines and its code and docstring lines.
// A file holding a NUL byte is binary, and counts as a file with no lines.
func measure(path string, data []byte) (string, counts) {
	if bytes.IndexByte(data, 0) >= 0 {
		return "binary", counts{files: 1}
	}
	c := counts{files: 1, raw: rawLines(data)}
	row, ok := lexer.For(path)
	if !ok {
		row, ok = byShebang(data)
	}
	switch {
	case ok && row.Name == "python":
		c.code, c.docs = pythonLines(row, data)
		return row.Name, c
	case ok:
		c.code = codeLines(row, data)
		c.docs = c.code
		return row.Name, c
	}
	name := plainText[strings.ToLower(filepath.Ext(path))]
	if name == "" {
		name = "other"
	}
	c.code = nonBlank(data)
	c.docs = c.code
	return name, c
}

// shebang is the interpreter a #! line names, as /bin/sh or /usr/bin/env python3.
var shebang = regexp.MustCompile(`^#!\s*(?:\S*/)?([a-z]+)[0-9.]*(?:\s+([a-z]+)[0-9.]*)?`)

// byShebang is the row of a script with no extension, such as a git hook,
// by its #! line: shell for sh, bash, dash and zsh, python for python.
func byShebang(data []byte) (lexer.Language, bool) {
	m := shebang.FindSubmatch(data)
	if m == nil {
		return lexer.Language{}, false
	}
	name := string(m[1])
	if name == "env" {
		name = string(m[2])
	}
	switch name {
	case "sh", "bash", "dash", "zsh", "ksh":
		return lexer.Named("shell")
	case "python":
		return lexer.Named("python")
	}
	return lexer.Language{}, false
}

// rawLines is the count of lines of data, the last one counted when no line
// feed ends it.
func rawLines(data []byte) int {
	n := bytes.Count(data, []byte("\n"))
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// nonBlank is the count of lines of data that hold a byte other than a blank.
func nonBlank(data []byte) int {
	n := 0
	for line := range bytes.Lines(data) {
		if len(bytes.TrimSpace(line)) > 0 {
			n++
		}
	}
	return n
}

// lines maps a byte offset of a file to its line, from 0.
type lines []int // the offset each line starts at

func newLines(data []byte) lines {
	starts := lines{0}
	for i, b := range data {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func (l lines) at(off int) int { return sort.SearchInts(l, off+1) - 1 }

// marked counts the lines of data that hold code, from the lexer's spans of
// the file. A comment span marks nothing. Any other span marks every line
// from its first byte to its last, as a heredoc's body does from its start
// to its closer's line, but a Text span, the text around a template's code,
// marks only its lines that are not blank. A byte outside every span that is
// not blank marks its line. docstring says which spans are docstrings: those
// mark doc instead of code.
func marked(data []byte, spans []lexer.Span, docstring func(int) bool) (code, doc []bool) {
	ls := newLines(data)
	code = make([]bool, len(ls))
	doc = make([]bool, len(ls))
	mark := func(set []bool, from, to int) { // the lines of the bytes from to to, to excluded
		if to <= from {
			return
		}
		for l := ls.at(from); l <= ls.at(to-1); l++ {
			set[l] = true
		}
	}
	plain := func(from, to int) {
		for i := from; i < to; i++ {
			switch data[i] {
			case '\\': // joins the lines when a line feed follows, as Python's tokenizer reads it
				if !bytes.HasPrefix(data[i+1:to], []byte("\n")) && !bytes.HasPrefix(data[i+1:to], []byte("\r\n")) {
					code[ls.at(i)] = true
				}
			case ' ', '\t', '\r', '\n', '\f', '\v':
			default:
				code[ls.at(i)] = true
			}
		}
	}
	at := 0
	for k, s := range spans {
		plain(at, s.Start)
		switch {
		case s.Kind == lexer.Comment:
		case s.Kind == lexer.Text:
			plain(s.Start, s.End)
		case docstring(k):
			mark(doc, s.Start, s.End)
		default:
			mark(code, s.Start, s.End)
			mark(code, s.Here.Start, s.Here.End)
		}
		at = max(at, s.End)
	}
	plain(at, len(data))
	return code, doc
}

func count(set []bool) int {
	n := 0
	for _, b := range set {
		if b {
			n++
		}
	}
	return n
}

// codeLines is the count of code lines of data in the language row.
func codeLines(row lexer.Language, data []byte) int {
	code, _ := marked(data, slices.Collect(row.Spans(data)), func(int) bool { return false })
	return count(code)
}

// pythonLines is the count of code lines of a Python file, and of its lines
// that are code or docstring. A file whose string runs off its end or whose
// brackets do not close counts as neither: Python would refuse it, and a
// partial count looks the same as a right one.
func pythonLines(row lexer.Language, data []byte) (code, withDocs int) {
	spans := slices.Collect(row.Spans(data))
	docstrings, ok := pythonDocstrings(data, spans)
	if !ok {
		return 0, 0
	}
	for k := range docstrings { // a docstring's prefix is part of its token, not code
		if s := &spans[k]; s.Start > 0 && strings.IndexByte("uUrR", data[s.Start-1]) >= 0 {
			s.Start--
		}
	}
	c, d := marked(data, spans, func(k int) bool { return docstrings[k] })
	for i := range d {
		d[i] = d[i] || c[i]
	}
	return count(c), count(d)
}

// docstringStatement is a statement of string literals alone: each is \x00, a
// span that opens with a quote, after an optional u or r prefix, the whole
// optionally in parentheses. Blanks are taken out first.
var docstringStatement = regexp.MustCompile(`^\(*(?:[uUrR]?\x00)+\)*$`)

// header is the start of a statement whose body may open with a docstring.
var header = regexp.MustCompile(`^(?:async\s+)?(?:def|class)\b`)

// pythonDocstrings is the set of indexes of spans that are docstrings, and
// false when the file's brackets do not balance or a span runs off its end.
//
// It reads the code between the lexer's spans as a tokenizer does: a
// backslash before a line feed joins the lines, a line feed inside brackets
// ends nothing, and a statement ends at a line feed outside brackets, at a ;,
// or at the colon that ends a def's or class's header. A statement made of
// string literals alone is a docstring when it is the file's first statement
// or the first after such a header.
//
// What it misses against Python's ast, which the census it was ported from
// used: a file that tokenizes but does not parse, such as one with an
// indentation error, is counted where ast counted it as nothing; a string
// first in a def whose header holds a colon outside brackets, which only a
// lambda in a default value without parentheses could, is not found; and it
// reads Python as the lexer table does, so an f-string nesting the quotes it
// opened with, legal since 3.12, ends early there.
func pythonDocstrings(data []byte, spans []lexer.Span) (map[int]bool, bool) {
	docstrings := map[int]bool{}
	var text []byte // the statement's code, a span as \x00 or \x01
	var members []int
	expect := true // the next statement may be a docstring
	depth := 0
	end := func() {
		stmt := bytes.Join(bytes.Fields(text), nil)
		if len(stmt) > 0 {
			if expect && docstringStatement.Match(stmt) {
				for _, k := range members {
					docstrings[k] = true
				}
			}
			expect = header.Match(bytes.TrimSpace(text)) // before the blanks go: "def f" joined is "deff"
		}
		text, members = text[:0], members[:0]
	}
	code := func(from, to int) {
		for i := from; i < to; i++ {
			b := data[i]
			switch {
			case b == '\\' && i+1 < to && data[i+1] == '\n':
				i++
			case b == '\\' && i+2 < to && data[i+1] == '\r' && data[i+2] == '\n':
				i += 2
			case b == '(' || b == '[' || b == '{':
				depth++
				text = append(text, b)
			case b == ')' || b == ']' || b == '}':
				depth--
				text = append(text, b)
			case depth == 0 && (b == '\n' || b == ';'):
				end()
			case depth == 0 && b == ':' && header.Match(bytes.TrimSpace(text)):
				text = append(text, b)
				end()
			default:
				text = append(text, b)
			}
		}
	}
	at := 0
	for k, s := range spans {
		if s.RunsOff(len(data)) && s.Kind != lexer.Comment {
			return nil, false
		}
		code(at, s.Start)
		if s.Kind != lexer.Comment {
			if q := data[s.Start]; q == '"' || q == '\'' {
				text = append(text, 0)
				members = append(members, k)
			} else {
				text = append(text, 1)
			}
		}
		at = s.End
	}
	code(at, len(data))
	end()
	return docstrings, depth == 0
}

// render is the census as a table: a row per bucket and language, a total
// per bucket, and the total of every bucket.
func render(rows map[string]map[string]counts) string {
	var b strings.Builder
	head := fmt.Sprintf("%-10s %-12s %6s %8s %8s %8s\n", "bucket", "language", "files", "raw", "code", "+docstr")
	sep := strings.Repeat("-", len(head)-1) + "\n"
	b.WriteString(head)
	var all counts
	for _, bucket := range bucketOrder {
		langs := rows[bucket]
		if len(langs) == 0 {
			continue
		}
		b.WriteString(sep)
		names := make([]string, 0, len(langs))
		for l := range langs {
			names = append(names, l)
		}
		sort.Strings(names)
		var total counts
		for _, l := range names {
			c := langs[l]
			total.add(c)
			fmt.Fprintf(&b, "%-10s %-12s %6d %8d %8d %8d\n", bucket, l, c.files, c.raw, c.code, c.docs)
		}
		fmt.Fprintf(&b, "%-10s %-12s %6d %8d %8d %8d\n", strings.ToUpper(bucket), "all", total.files, total.raw, total.code, total.docs)
		all.add(total)
	}
	b.WriteString(sep)
	fmt.Fprintf(&b, "%-10s %-12s %6d %8d %8d %8d\n", "TOTAL", "all", all.files, all.raw, all.code, all.docs)
	return b.String()
}
