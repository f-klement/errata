package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core/lexer"
)

// root is the module root, where make loc runs the command from.
const root = "../.."

// realTree is every file git tracks in this repository. A tree with no .git,
// such as an export or a tarball, has no index to list, and the test is
// skipped there, saying so.
func realTree(t *testing.T) []string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Skipf("the tree at %s has no .git, so git lists no file: %v", root, err)
	}
	files, err := tracked(root)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// Every tracked file lands in exactly one bucket and is counted once: the
// census's files add up to what git lists, none dropped and none twice. A
// file of a language the census has no rule for is still counted, as other.
// The planted break: census skipping a file of no known language.
func TestEveryTrackedFileIsCountedOnce(t *testing.T) {
	files := realTree(t)
	rows, err := census(root, files)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for bucket, langs := range rows {
		if !slices.Contains(bucketOrder, bucket) {
			t.Errorf("bucket %q is not one the census prints", bucket)
		}
		for _, c := range langs {
			total += c.files
		}
	}
	if total != len(files) {
		t.Errorf("the census counted %d files, git tracks %d", total, len(files))
	}
}

// source is the residual, so a new tree at the top falls into it unseen. It
// may hold only the product: cmd/, internal/ and go.mod. The planted break:
// the examples/ rule dropped.
func TestSourceIsOnlyTheProduct(t *testing.T) {
	for _, f := range realTree(t) {
		if bucketOf(f) != source {
			continue
		}
		if !strings.HasPrefix(f, "cmd/") && !strings.HasPrefix(f, "internal/") && f != "go.mod" {
			t.Errorf("%s falls through to source: give it a rule", f)
		}
	}
}

// Every rule is the first to claim some tracked file, so none is stale and
// none is shadowed by a rule above it. On the public mirror, the tree with no
// ci/public-tree.sh, a rule whose path or name is not in the tree at all is
// let off: the mirror has no ci/, .claude/ or CLAUDE.md, and its make test runs
// this test too. In this repository, where ci/public-tree.sh is, no rule is
// let off, so a rule for a path that is gone fails here. The planted breaks: a
// rule for a file that is there, shadowed by the rule for its directory, and a
// rule for gone.yaml.
func TestEveryRuleClaimsAFile(t *testing.T) {
	for _, r := range unclaimed(t, root, realTree(t)) {
		t.Errorf("rule %+v is the first to claim no tracked file", r)
	}
}

// The rules that are the first to claim none of files. When the tree at dir
// is the public mirror, which has no ci/public-tree.sh, those whose path or
// name the tree does not hold are left out. A rule of a suffix or a segment
// names no path, and is never left out.
func unclaimed(t *testing.T, dir string, files []string) []rule {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, "ci", "public-tree.sh"))
	public := err != nil
	var out []rule
	for i, r := range rules {
		claims := slices.ContainsFunc(files, func(f string) bool {
			return slices.IndexFunc(rules, func(o rule) bool { return o.matches(f) }) == i
		})
		if !claims && (!public || present(dir, files, r)) {
			out = append(out, r)
		}
	}
	return out
}

// present is whether the path rule r is written for is in the tree at dir,
// as a file or a directory, or for a rule of a name, whether a file of that
// name stands in the top directory or in one that holds a tracked file. Only
// those directories are looked in, so an ignored one, such as a module cache
// in .gopath, cannot hold the name for the tree.
func present(dir string, files []string, r rule) bool {
	switch {
	case r.exact != "" || r.prefix != "":
		_, err := os.Stat(filepath.Join(dir, r.exact+strings.TrimSuffix(r.prefix, "/")))
		return err == nil
	case r.name != "":
		dirs := map[string]bool{".": true}
		for _, f := range files {
			dirs[filepath.Dir(f)] = true
		}
		for d := range dirs {
			if _, err := os.Stat(filepath.Join(dir, d, r.name)); err == nil {
				return true
			}
		}
		return false
	}
	return true
}

// The rules a tree reports unclaimed. In the public mirror, a tree with no
// ci/public-tree.sh, a rule for a path that is not there is let off, and the
// same rule is reported once its path is back holding no tracked file. In
// this repository, a tree with ci/public-tree.sh, a rule for a path that is
// not there is reported.
func TestARuleForAPathNotInTheTreeIsLetOff(t *testing.T) {
	stripped := func(r rule) bool { return r.prefix == "ci/" || r.exact == "renovate.json" || r.name == "CLAUDE.md" }
	for name, c := range map[string]struct {
		skip      func(rule) bool // the rules the tree holds no file for
		ci        bool            // ci/public-tree.sh is in the tree, and tracked
		untracked []string        // paths in the tree that git does not track
		want      []string
	}{
		"public, the stripped paths gone": {skip: stripped},
		"public, the stripped paths back": {skip: stripped, untracked: []string{"ci/x", "renovate.json", "docs/CLAUDE.md"},
			want: []string{"ci/", "renovate.json", "CLAUDE.md"}},
		"private, two paths gone": {skip: func(r rule) bool { return r.exact == "renovate.json" || r.name == "CLAUDE.md" }, ci: true,
			want: []string{"renovate.json", "CLAUDE.md"}},
	} {
		dir := t.TempDir()
		var files []string
		for _, r := range rules {
			f := r.exact + r.prefix
			switch {
			case r.prefix == "ci/" && c.ci:
				f = "ci/public-tree.sh"
			case r.prefix != "":
				f += "x"
			case r.suffix != "":
				f = "internal/a/a" + r.suffix
			case r.segment != "":
				f = "internal/a/" + r.segment + "/x"
			case r.name != "":
				f = "a/" + r.name
			}
			if !c.skip(r) {
				files = append(files, f)
			}
		}
		for _, f := range append(slices.Clone(files), c.untracked...) {
			if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		var got []string
		for _, r := range unclaimed(t, dir, files) {
			got = append(got, r.exact+r.prefix+r.suffix+r.segment+r.name)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: unclaimed %v, want %v", name, got, c.want)
		}
	}
}

// goGenerated is Go's marker of a generated file, on a line of its own
// anywhere before the package clause (go help generate).
var goGenerated = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// saysGenerated is whether the file at path says it is generated: a Go file
// by Go's marker before its package clause, any other file by a first line
// that says not to edit it.
func saysGenerated(path string, data []byte) bool {
	if !strings.HasSuffix(path, ".go") {
		first, _, _ := bytes.Cut(data, []byte("\n"))
		return strings.Contains(strings.ToLower(string(first)), "do not edit")
	}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if goGenerated.MatchString(line) {
			return true
		}
	}
	return false
}

func TestSaysGenerated(t *testing.T) {
	for name, c := range map[string]struct {
		path, src string
		want      bool
	}{
		"Go, first line":               {"a.go", "// Code generated by x. DO NOT EDIT.\n\npackage a\n", true},
		"Go, after a license header":   {"a.go", "// Copyright 2026.\n// MIT.\n\n// Code generated by x. DO NOT EDIT.\n\npackage a\n", true},
		"Go, after the package":        {"a.go", "package a\n\n// Code generated by x. DO NOT EDIT.\n", false},
		"Go, not on a line of its own": {"a.go", "// x: Code generated by x. DO NOT EDIT.\npackage a\n", false},
		"Markdown, first line":         {"a.md", "This file is generated. Do not edit it.\n", true},
		"Markdown, later":              {"a.md", "# T\nDo not edit.\n", false},
	} {
		if got := saysGenerated(c.path, []byte(c.src)); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// A tracked file that says it is generated is counted as generated. The
// planted break: the rule for unicode_node.go dropped.
func TestEveryGeneratedFileIsGenerated(t *testing.T) {
	marked := 0
	for _, f := range realTree(t) {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		if !saysGenerated(f, data) {
			continue
		}
		marked++
		if b := bucketOf(f); b != generated {
			t.Errorf("%s says it is generated and is counted in %s", f, b)
		}
	}
	if marked == 0 {
		t.Error("no tracked file says it is generated: the test reads the wrong line")
	}
}

func TestBucketOf(t *testing.T) {
	for path, want := range map[string]string{
		"cmd/errata/main.go":                               source,
		"internal/rule/rule.go":                            source,
		"go.mod":                                           source,
		"internal/rule/rule_test.go":                       tests,
		"internal/cli/testdata/run.txtar":                  tests,
		"internal/extract/extracttest/extracttest.go":      tests,
		"ci/tests/testdata/fetch-tool/jq-attestation.json": tooling,
		"scripts/loccensus/main_test.go":                   tooling,
		"Makefile":                                         tooling,
		"examples/src/admin.py":                            examples,
		"docs/handbook.md":                                 docs,
		"README.md":                                        docs,
		"CLAUDE.md":                                        tooling,
		"docs/agents/waves.md":                             tooling,
		"internal/x/CLAUDE.md":                             tooling,
		"docs/cli.md":                                      generated,
		"internal/minimatch/unicode_node.go":               generated,
		"a/testdata":                                       source,
		"testdata/x":                                       tests,
	} {
		if got := bucketOf(path); got != want {
			t.Errorf("bucketOf(%q) = %s, want %s", path, got, want)
		}
	}
}

// code is the code lines of src in the lexer's row for file.
func code(t *testing.T, file, src string) int {
	t.Helper()
	row, ok := lexer.For(file)
	if !ok {
		t.Fatalf("no lexer row for %s", file)
	}
	return codeLines(row, []byte(src))
}

// /* opens a block comment wherever it stands, after ;, { and = too, where a
// regex may open: a regex body starting * does not parse. Reading these as
// regex literals moved the figure of the tree this was ported from by 60%,
// and passed a smoke test because the corrupted line already held code. The
// planted break: the lexer trying the shortest opener first, so the regex's /
// opens ahead of /*.
func TestABlockCommentIsNeverARegex(t *testing.T) {
	for name, c := range map[string]struct {
		src  string
		want int
	}{
		"after a semicolon": {"const a = 1;\n/* one\n   two\n   three */\nconst b = 2;\n", 2},
		"after a brace":     {"function f() {\n/* explanation\n   continued */\n  return 1;\n}\n", 3},
		"after an equals":   {"const a =\n/* why\n */ 1;\n", 2},
		"a real regex":      {"const r = /a*b/;\n", 1},
	} {
		if got := code(t, "a.js", c.src); got != c.want {
			t.Errorf("%s: %d code lines, want %d", name, got, c.want)
		}
	}
}

func TestCodeLines(t *testing.T) {
	for name, c := range map[string]struct {
		file, src string
		want      int
	}{
		"// in a string":                               {"a.js", "const url = \"http://example.test\";\nconst port = 443;\n", 2},
		"a template literal spans its lines":           {"a.js", "const q = `\nSELECT 1\n\nFROM t\n`;\nconst n = 1;\n", 6},
		"a line comment ends at the line":              {"a.js", "const a = 1; // trailing\n// whole line\nconst b = 2;\n", 2},
		"blank lines":                                  {"a.js", "const a = 1;\n\n\n\nconst b = 2;\n", 2},
		"a Go raw string":                              {"a.go", "package a\n\n// doc\nvar s = `\n\nx`\n", 4},
		"a shell comment":                              {"a.sh", "#!/bin/sh\n# why\necho '#no'\n", 1},
		"a heredoc's body":                             {"a.rb", "x = <<~EOS\n  # kept\n\nEOS\n", 4},
		"shell's heredoc, which its row does not read": {"a.sh", "cat <<EOF\n# lost\n\nEOF\n", 2},
	} {
		if got := code(t, c.file, c.src); got != c.want {
			t.Errorf("%s: %d code lines, want %d", name, got, c.want)
		}
	}
}

func python(t *testing.T, src string) [2]int {
	t.Helper()
	row, _ := lexer.Named("python")
	c, d := pythonLines(row, []byte(src))
	return [2]int{c, d}
}

// A docstring is its own column: in neither code nor comment. Each row's
// figures are those Python's ast gives, by the census this was ported from.
// The planted break: a statement of strings taken for a docstring wherever it
// stands.
func TestPythonDocstrings(t *testing.T) {
	for name, c := range map[string]struct {
		src  string
		want [2]int
	}{
		"a function's":                    {"def f():\n    \"\"\"One.\n\n    Two.\n    \"\"\"\n    return 1\n", [2]int{2, 6}},
		"a module's":                      {"\"\"\"Module.\"\"\"\n\nn = 1\n", [2]int{1, 2}},
		"after a comment and a decorator": {"# c\n'''Module.'''\n@dec\ndef f(a: int = 1) -> dict[str, int]:\n    'doc'\n    return {}\n", [2]int{3, 5}},
		"a class's, raw":                  {"class A(B):\n    r\"\"\"doc\"\"\"\n    x = 1\n", [2]int{2, 3}},
		"async, on the header's line":     {"async def f(): \"doc\"\n", [2]int{1, 1}},
		"concatenated":                    {"def f():\n    \"a\" \\\n    \"b\"\n    pass\n", [2]int{2, 4}},
		"not first in its body":           {"def f():\n    x = 1\n    \"\"\"not a docstring\"\"\"\n", [2]int{3, 3}},
		"after a statement":               {"x = 1\n\"\"\"not a docstring\"\"\"\n", [2]int{2, 2}},
		"a call on a string":              {"\"\"\"a\"\"\".strip()\n", [2]int{1, 1}},
		"bytes":                           {"def f():\n    b\"\"\"x\"\"\"\n", [2]int{2, 2}},
		"an f-string":                     {"def f():\n    f\"\"\"{x}\"\"\"\n", [2]int{2, 2}},
		"after an if":                     {"if x:\n    \"\"\"no\"\"\"\n", [2]int{2, 2}},
	} {
		if got := python(t, c.src); got != c.want {
			t.Errorf("%s: (code, +docstr) = %v, want %v", name, got, c.want)
		}
	}
}

func TestPythonLines(t *testing.T) {
	for name, c := range map[string]struct {
		src  string
		want [2]int
	}{
		"# in a string":           {"tag = \"#hashtag\"\nn = 1\n", [2]int{2, 2}},
		"comment lines":           {"# explanation\nn = 1\n# more\n", [2]int{1, 1}},
		"brackets over lines":     {"x = [\n    1,  # one\n\n    2,\n]\n", [2]int{4, 4}},
		"an open bracket":         {"def f(:\n", [2]int{0, 0}},
		"an unclosed string":      {"x = \"\"\"never closed\n", [2]int{0, 0}},
		"a closing bracket alone": {"x = 1)\n", [2]int{0, 0}},
	} {
		if got := python(t, c.src); got != c.want {
			t.Errorf("%s: (code, +docstr) = %v, want %v", name, got, c.want)
		}
	}
}

func TestMeasure(t *testing.T) {
	for name, c := range map[string]struct {
		path, src string
		lang      string
		want      counts
	}{
		"markdown, every line not blank": {"a.md", "# T\n\n<!-- c -->\ntext", "markdown", counts{1, 4, 3, 3}},
		"a hook with a shebang":          {".githooks/commit-msg", "#!/bin/sh\n# c\nexit 0\n", "shell", counts{1, 3, 1, 1}},
		"env python":                     {"tool", "#!/usr/bin/env python3\n'''d'''\nx = 1\n", "python", counts{1, 3, 1, 2}},
		"no language":                    {"LICENSE", "MIT\n\nthe text\n", "other", counts{1, 3, 2, 2}},
		"binary":                         {"z.zip", "PK\x00\x01\n", "binary", counts{1, 0, 0, 0}},
		"empty":                          {"a.txt", "", "text", counts{1, 0, 0, 0}},
	} {
		lang, got := measure(c.path, []byte(c.src))
		if lang != c.lang || got != c.want {
			t.Errorf("%s: %s %+v, want %s %+v", name, lang, got, c.lang, c.want)
		}
	}
}

// make loc runs the command on the checkout, and it prints a total of every
// file git tracks.
func TestMakeLocPrintsTheCensus(t *testing.T) {
	realTree(t)
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(makefile, []byte("\nloc:\n\t$(GO) run ./scripts/loccensus .\n")) {
		t.Error("the Makefile has no loc target that runs ./scripts/loccensus on the checkout")
	}
	var out, errs bytes.Buffer
	if code := run([]string{root}, &out, &errs); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs.String())
	}
	if !strings.Contains(out.String(), "\nTOTAL ") || !strings.Contains(out.String(), "\nsource ") {
		t.Errorf("no source row or total in:\n%s", out.String())
	}
}

// A wrong call is refused with the usage, and a directory git does not track
// with git's refusal.
func TestRunRefuses(t *testing.T) {
	var out, errs bytes.Buffer
	if code := run(nil, &out, &errs); code != 2 || !strings.Contains(errs.String(), "usage: loccensus") {
		t.Errorf("no argument: exit %d, stderr %q", code, errs.String())
	}
	errs.Reset()
	if code := run([]string{t.TempDir()}, &out, &errs); code != 1 || !strings.Contains(errs.String(), "git ls-files in") {
		t.Errorf("not a repository: exit %d, stderr %q", code, errs.String())
	}
	if out.Len() != 0 {
		t.Errorf("a refusal printed %q", out.String())
	}
}
