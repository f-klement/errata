package ini

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/core"
)

func gitSpec(file, section string, subsection *string, key string) Spec {
	return Spec{File: file, Section: section, Subsection: subsection, Key: key}
}

const gitmodules = `[submodule "vendor/lib"]
	path = vendor/lib
	url = https://example.com/lib.git
	branch = main
[Submodule "Docs"]
	path = docs
	URL = "https://example.com/docs.git" ; the docs
`

const gitconfig = `# user settings
[core]
	bare
	editor = vim
[remote "origin"]
	url = git@example.com:x.git
	fetch = +refs/heads/*:refs/remotes/origin/*
	fetch = +refs/tags/*:refs/tags/*
[Remote.Upstream]
	url = https://example.com/up.git
[alias]
	lg = log --graph \
	  --oneline
	st = "status  -s"
	esc = a\tb\\c\"d
`

func TestGitReadsGitmodulesAndGitconfig(t *testing.T) {
	for _, c := range []struct {
		file, body string
		spec       Spec
		want       string
	}{
		{".gitmodules", gitmodules, gitSpec(".gitmodules", "submodule", ptr("vendor/lib"), "url"), "https://example.com/lib.git@3"},
		{".gitmodules", gitmodules, gitSpec(".gitmodules", "SUBMODULE", ptr("Docs"), "url"), "https://example.com/docs.git@7"},
		{".gitmodules", gitmodules, gitSpec(".gitmodules", "submodule", ptr("docs"), "url"), ""},
		{".gitmodules", gitmodules, gitSpec(".gitmodules", "submodule.vendor/lib", nil, "Branch"), "main@4"},
		{".gitconfig", gitconfig, gitSpec(".gitconfig", "core", nil, "bare"), "true@3"},
		{".gitconfig", gitconfig, gitSpec(".gitconfig", "remote", ptr("origin"), "fetch"), "+refs/heads/*:refs/remotes/origin/*@7 +refs/tags/*:refs/tags/*@8"},
		{".gitconfig", gitconfig, gitSpec(".gitconfig", "remote", ptr("upstream"), "url"), "https://example.com/up.git@10"},
		{".gitconfig", gitconfig, gitSpec(".gitconfig", "remote", ptr("Upstream"), "url"), ""},
		{".gitconfig", gitconfig, gitSpec(".gitconfig", "alias", nil, "lg"), "log --graph \t  --oneline@12"},
		{".gitconfig", gitconfig, gitSpec(".gitconfig", "alias", nil, "st"), "status  -s@14"},
		{".gitconfig", gitconfig, gitSpec(".gitconfig", "alias", nil, "esc"), "a\tb\\c\"d@15"},
	} {
		if got := iniValues(t, c.file, c.body, c.spec); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.spec, got, c.want)
		}
	}
}

// Tabs where git before 2.45 reads them as later git does, which the git
// differential leaves out against old git (changesInOldGit): around a key
// and its =, at a value's ends, before a comment, in a header. Inside a
// value a tab is kept, as 2.45 and later keep it. Each was read by git 2.47.
// The planted break is an unquoted tab kept at a value's ends.
func TestGitReadsTabsAsGitDoes(t *testing.T) {
	for body, want := range map[string][]gitEntry{
		"[a]\nk=\t\n":           {{name: "a.k"}},
		"[a]\n\tk = v\n":        {{name: "a.k", value: "v"}},
		"[a]\nk\t=\tv\n":        {{name: "a.k", value: "v"}},
		"[a]\nk = v\t\t\n":      {{name: "a.k", value: "v"}},
		"[a]\nk = v\t; c\n":     {{name: "a.k", value: "v"}},
		"[a\t\"x y\"]\nk = v\n": {{name: "a.x y.k", value: "v"}},
		"\t[a]\n\tk\t\n":        {{name: "a.k", noValue: true}},
		"[a]\nk = \tv\t w \t\n": {{name: "a.k", value: "v\t w"}},
	} {
		got, err := readGitEntries([]byte(body))
		if err != nil || !equalEntries(got, want) {
			t.Errorf("%q: %v, %v; want %v", body, got, err, want)
		}
	}
}

// The planted breaks: each line git refuses, and a NUL byte, which errata
// refuses where git would read up to it. Each was run through git 2.47,
// which refuses it too ("bad config line").
func TestGitRefusesWhatGitRefuses(t *testing.T) {
	for body, want := range map[string]string{
		"[a]\nb ; c\n":           "f: line 2: this is no line git config reads",
		"[a]\n1k = 1\n":          "f: line 2: this is no line git config reads",
		"[a]\nk = \\q\n":         "f: line 2: the value has an unknown escape or an unclosed double quote",
		"[a]\nk = \"x\n":         "f: line 2: the value has an unknown escape or an unclosed double quote",
		"[a \"b\"  ]\nk = 1\n":   "f: line 1: this is no section header git config reads",
		"[a \"b\\\n\"]\nk = 1\n": "f: line 1: this is no section header git config reads",
		"[]\nk = 1\n":            "f: line 1: this is no section header git config reads",
		"[a_b]\nk = 1\n":         "f: line 1: this is no section header git config reads",
		"[a":                     "f: line 1: this is no section header git config reads",
		"\xef\xbb[a]\n":          "f: line 1: a part of a UTF-8 byte order mark",
		"[a]\nk = x\x00y\n":      "f: line 2: a NUL byte, which git reads as the end of a name or a value",
	} {
		s := gitSpec("f", "a", nil, "k")
		s.Dialect = "git"
		if got := iniError(t, "f", body, s); got != want {
			t.Errorf("%q: error %q, want %q", body, got, want)
		}
	}
}

// gitEntry is one entry as git config --list -z prints it: the name, and
// the value unless the key has no =.
type gitEntry struct {
	name    string
	value   string
	noValue bool
}

// readGitEntries is every entry errata reads from data, named as git names
// them.
func readGitEntries(data []byte) ([]gitEntry, error) {
	var out []gitEntry
	err := readGit(data, nil, func(e *core.Entry) error {
		name := string(e.Key)
		if len(e.Section) > 0 {
			name = string(e.Section) + "." + name
		}
		g := gitEntry{name: name, noValue: e.NoValue}
		if !e.NoValue {
			g.value = string(e.Value)
		}
		out = append(out, g)
		return nil
	})
	return out, err
}

// gitList runs git config --list -z on the file at p and returns its
// entries, or ok false when git refuses the file.
func gitList(t *rapid.T, git, p string) ([]gitEntry, bool) {
	cmd := gitCommand(t.Context(), git, p)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if !strings.Contains(stderr.String(), "bad config line") {
			t.Fatalf("git config: %v: %s", err, stderr.String())
		}
		return nil, false
	}
	var entries []gitEntry
	for _, rec := range strings.Split(string(out), "\x00") {
		if rec == "" {
			continue
		}
		name, value, hasValue := strings.Cut(rec, "\n")
		entries = append(entries, gitEntry{name: name, value: value, noValue: !hasValue})
	}
	return entries, true
}

// The parts the generated files are made of: headers of each form, keys in
// each case, values of every escape, quote and comment, line breaks, and
// the parts git refuses. A line is a header, a key with or without a
// value, a comment, or pieces of any of them.
var (
	gitHeaders = []string{
		"[a]", "[A]", "[a-1]", "[a.B]", "[a \"b\"]", "[a \"B\"]", "[a\t\"x y\"]", "[a \"q\\\"\\\\z\"]", "[a \"\\k\"]",
		"[ \"x\"]", "[a.]", "[.a]", "[a \"\"]", "[]", "[a", "[a \"b\" ]", "[a_b]", "[a \"b\"x]",
	}
	gitKeys   = []string{"k", "K", "k-2", "kE", "a1", "1k", "_k", "k_"}
	gitValues = []string{
		"v", "V w", "x;y", "x#y", "\"", "\"q s\"", "\"a;#b\"", "\\", "\\n", "\\t", "\\b", "\\\"", "\\\\", "\\\n", "\\q",
		" ", "\t", "\f", "\r", " ;c", " # c", "é", "=", "[", "]", "\"a\tb\"", "\"a\rb\"",
	}
	gitBreaks = []string{"\n", "\n", "\r\n", "\r", ""}
	gitAny    = []string{"[", "]", ".", "=", "\"", "\\", ";", "#", " ", "\t", "\n", "a", "K"}
)

// gitRefused are the parts git refuses wherever they are: a file drawn
// without them is mostly one git reads, so the entries are compared too.
var gitRefused = map[string]bool{
	"[]": true, "[a": true, "[a \"b\" ]": true, "[a_b]": true, "[a \"b\"x]": true, "1k": true, "_k": true, "k_": true, "\"": true, "\\q": true,
}

// pick draws one of from, without the parts git refuses unless junk is set,
// and without those git before 2.45 reads differently when old is set.
func pick(t *rapid.T, from []string, junk, old bool, label string) string {
	from = slices.DeleteFunc(slices.Clone(from), func(s string) bool {
		return (!junk && gitRefused[s]) || (old && changesInOldGit(s, junk))
	})
	return rapid.SampledFrom(from).Draw(t, label)
}

// changesInOldGit reports whether part could put a tab or a carriage return
// outside quotes inside a value. Git before 2.45 reads each such character
// as a space (the bug 2.45 fixed: "git config" corrupted literal HT
// characters), where errata keeps it, as later git does. A line break
// joins lines, so any part could end up inside a value; a quoted part is
// safe unless junk, whose unpaired quotes can leave it outside quotes. A
// lone \ goes too, since it escapes the quote of a quoted part after it. A
// \r\n is a line end to both.
func changesInOldGit(part string, junk bool) bool {
	quoted := len(part) > 2 && part[0] == '"' && part[len(part)-1] == '"'
	return part == "\\" || (part != "\r\n" && strings.ContainsAny(part, "\t\r") && (junk || !quoted))
}

// gitLine draws one line of a generated file.
func gitLine(t *rapid.T, junk, old bool) string {
	var b strings.Builder
	b.WriteString(pick(t, []string{"", " ", "\t", "  "}, true, old, "indent"))
	line := rapid.IntRange(0, 9).Draw(t, "line")
	if line == 3 && !junk {
		line = 4
	}
	switch line {
	case 0, 1:
		b.WriteString(pick(t, gitHeaders, junk, old, "header"))
		if rapid.IntRange(0, 3).Draw(t, "inline") == 0 {
			b.WriteString(" k = v")
		}
	case 2:
		b.WriteString(rapid.SampledFrom([]string{"# c", "; c", "#", ";"}).Draw(t, "comment"))
	case 3:
		parts := slices.DeleteFunc(slices.Clone(gitAny), func(s string) bool { return old && changesInOldGit(s, true) })
		b.WriteString(strings.Join(rapid.SliceOfN(rapid.SampledFrom(parts), 0, 8).Draw(t, "any"), ""))
	default:
		b.WriteString(pick(t, gitKeys, junk, old, "key"))
		if rapid.IntRange(0, 4).Draw(t, "valueless") > 0 {
			b.WriteString(pick(t, []string{"=", " = ", "\t=\t", " ="}, true, old, "sep"))
			for range rapid.IntRange(0, 6).Draw(t, "pieces") {
				b.WriteString(pick(t, gitValues, junk, old, "value"))
			}
		}
		b.WriteString(pick(t, []string{"", "", " ", "\t"}, true, old, "trail"))
	}
	b.WriteString(pick(t, gitBreaks, true, old, "break"))
	return b.String()
}

// gitCommand runs git config --list -z on the file at p, in its directory,
// with no repository above it (GIT_CEILING_DIRECTORIES is the directory's
// parent), no system or global config, and no environment but PATH, a HOME
// in the directory, and the C locale. So git reads the generated file and
// nothing of the errata checkout or the machine.
func gitCommand(ctx context.Context, git, p string) *exec.Cmd {
	dir := filepath.Dir(p)
	cmd := exec.CommandContext(ctx, git, "config", "--file", p, "--list", "-z")
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "LC_ALL=C",
		"GIT_CEILING_DIRECTORIES=" + filepath.Dir(dir), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
	}
	return cmd
}

// git config reads the generated file, not the repository it runs in: the
// planted break is a repository above the file whose config git cannot
// read, which gitCommand without its directory and ceiling would read, and
// which makes git refuse every file.
func TestGitConfigReadsNoRepositoryAround(t *testing.T) {
	slowOnly(t) // it starts git for every generated file
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git is not on PATH: %v", err)
	}
	repo := t.TempDir()
	init := exec.CommandContext(t.Context(), git, "init", "-q", repo)
	init.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + repo, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	f, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[bad\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	dir := filepath.Join(repo, "sub", "dir")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config")
	if err := os.WriteFile(p, []byte("[a]\nk = v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := gitCommand(t.Context(), git, p)
	out, err := cmd.Output()
	if err != nil || string(out) != "a.k\nv\x00" {
		t.Fatalf("git config: %q, %v", out, err)
	}
	// The planted break, git run in the repository without the ceiling,
	// fails on the repository's config.
	cmd = exec.CommandContext(t.Context(), git, "config", "--file", p, "--list", "-z")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if msg, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(msg), ".git/config") {
		t.Fatalf("without the ceiling git read %q, %v; the witness needs it to read the repository's config", msg, err)
	}
}

// The git dialect reads every generated file as git config --list -z does:
// the same entries, names and values in order, and a refusal exactly where
// git refuses. Git must be on PATH: the CI image has it.
func TestGitAgreesWithGitConfig(t *testing.T) {
	slowOnly(t) // it starts git for every generated file
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git is not on PATH, and this test compares the git dialect with it: %v", err)
	}
	// errata reads values as git 2.45 and later do. Against older git the
	// files hold no tab or carriage return that could land outside quotes
	// in a value (changesInOldGit), and the comparison stays exact. make
	// slow shows the narrowed line. In a GitLab job the comparison must be
	// exact, so old git there fails: the CI image carries a newer one
	// (ci/image/Dockerfile), and an image that lost it would otherwise
	// narrow the comparison with only a log line to say so.
	old := !gitKeepsWhitespace(t, git)
	if old && os.Getenv("GITLAB_CI") == "true" {
		t.Fatal("git is older than 2.45 in a GitLab job; the CI image must carry 2.45 or later (ci/image/Dockerfile), or this comparison is narrowed")
	}
	if old {
		t.Log("narrowed: git is older than 2.45, so no tab or carriage return outside quotes is drawn inside a value")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	rapid.Check(t, func(t *rapid.T) {
		var b strings.Builder
		junk := rapid.IntRange(0, 3).Draw(t, "junk") == 0
		for range rapid.IntRange(0, 12).Draw(t, "lines") {
			b.WriteString(gitLine(t, junk, old))
		}
		data := []byte(b.String())
		if rapid.Bool().Draw(t, "bom") {
			data = append(append([]byte{}, bom...), data...)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		want, ok := gitList(t, git, p)
		got, err := readGitEntries(data)
		switch {
		case !ok && err == nil:
			t.Fatalf("git refuses %q and errata reads %v", data, got)
		case ok && err != nil:
			t.Fatalf("git reads %q as %v and errata refuses it: %v", data, want, err)
		case ok && !equalEntries(got, want):
			t.Fatalf("%q: errata reads %v, git %v", data, got, want)
		}
	})
}

// gitKeepsWhitespace reports whether git keeps a value's inner tabs and
// carriage returns as written, which git 2.45 and later do.
func gitKeepsWhitespace(t *testing.T, git string) bool {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), git, "version").Output()
	if err != nil {
		t.Fatalf("git version: %v", err)
	}
	var major, minor int
	if _, err := fmt.Sscanf(string(out), "git version %d.%d", &major, &minor); err != nil {
		t.Fatalf("git version %q: %v", out, err)
	}
	return major > 2 || major == 2 && minor >= 45
}

func equalEntries(a, b []gitEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
