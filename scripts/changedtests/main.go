// Command changedtests names the tests a change adds or edits, for two checks
// of ci/gate.sh that ci/changed-tests.sh runs: the tests that pass at the
// base, and the tests whose result differs between shuffle seeds.
//
// Usage:
//
//	changedtests list <base>   the tests, one a line: the package's directory
//	                           from the root, the test's name, and edited or
//	                           reaches, sorted
//	changedtests drop <dir>    reads a build's errors on stdin; see drop.go
//
// Run from the root of a git work tree. The change is the diff from <base> to
// HEAD, both read from git, so the work tree's own edits are not part of it.
// A test is a top-level function go test runs by name: Test, Fuzz or Example
// followed by nothing or by a character that is not lower case, as go test
// reads them, and never TestMain. list names a test as
//
//   - edited when no changed file at <base> declares it in its package's
//     directory, or the text of its declarations there differs, its doc
//     comment left out. A test moved to another file of its package,
//     unchanged, is not named for that;
//   - reaches when it is not edited but names, in its own declaration or
//     through other declarations of its directory's _test.go files, a
//     declaration of a _test.go file that is not a test and whose text the
//     change adds or edits: a helper, a table, a var, a const, a type, or a
//     method, which a test reaches by naming its receiver type. Names are
//     matched as identifiers, with no type check, so a local of the same name
//     counts too. Every test of a package whose TestMain the change adds or
//     edits reaches it, since TestMain runs around every test of the binary.
//
// A reaching test's own text did not change, so it passing at the base says
// less than an edited test passing there.
//
// A change to a file outside the _test.go files, such as testdata or the code
// under test, names no test. Files under a directory named testdata are left
// out, since go test builds none of them.
package main

import (
	"bytes"
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 2 || (args[0] != "list" && args[0] != "drop") {
		fmt.Fprintln(stderr, "usage: changedtests list <base> | changedtests drop <dir>")
		return 2
	}
	if args[0] == "drop" {
		return dropRun(args[1], stdin, stdout, stderr)
	}
	tests, err := changed(args[1])
	if err != nil {
		fmt.Fprintln(stderr, "changedtests:", err)
		return 1
	}
	for _, t := range tests {
		fmt.Fprintln(stdout, t)
	}
	return 0
}

// changed is each test list names for the diff from base to HEAD, as
// "<directory> <name> <edited|reaches>", sorted.
func changed(base string) ([]string, error) {
	out, err := git("diff", "--name-status", "--no-renames", "-z", base, "HEAD", "--", "*_test.go")
	if err != nil {
		return nil, err
	}
	before, after := decls{}, decls{}
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		if err := readBoth(fields[i], fields[i+1], base, before, after); err != nil {
			return nil, err
		}
	}
	found := map[string]string{}
	support := map[string][]string{}
	for _, key := range after.differFrom(before) {
		dir, name, _ := strings.Cut(key, " ")
		if isTest(name) {
			found[key] = "edited"
		} else {
			support[dir] = append(support[dir], name)
		}
	}
	for dir, names := range support {
		if err := addReaching(dir, names, found); err != nil {
			return nil, err
		}
	}
	lines := make([]string, 0, len(found))
	for key, kind := range found {
		lines = append(lines, key+" "+kind)
	}
	slices.Sort(lines)
	return lines, nil
}

// readBoth adds the declarations of the file p at base to before and at HEAD
// to after, reading each side the status says the file has: A is HEAD's
// only, D the base's only.
func readBoth(status, p, base string, before, after decls) error {
	if inTestdata(p) {
		return nil
	}
	if status != "A" {
		if _, err := before.read(base, p); err != nil {
			return err
		}
	}
	if status != "D" {
		_, err := after.read("HEAD", p)
		return err
	}
	return nil
}

func inTestdata(p string) bool {
	return slices.Contains(strings.Split(path.Dir(p), "/"), "testdata")
}

// dirOf is how a directory is named in the list: "./internal/p", or "." for
// the root.
func dirOf(p string) string {
	if path.Dir(p) == "." {
		return "."
	}
	return "./" + path.Dir(p)
}

// decls holds the text of every top-level declaration read, by "<directory>
// <name>". A name declared more than once, in files of different build
// constraints or as methods of different types, holds each text.
type decls map[string][]string

// read adds the declarations of the file p as the commit rev holds it, and
// returns them with the identifiers each names.
func (d decls) read(rev, p string) ([]decl, error) {
	src, err := git("show", rev+":"+p)
	if err != nil {
		return nil, err
	}
	f, err := parser.ParseFile(token.NewFileSet(), p, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	found := topLevel(f, src)
	for _, dc := range found {
		key := dirOf(p) + " " + dc.name
		d[key] = append(d[key], dc.text)
	}
	return found, nil
}

// differFrom is each key of d whose texts, in any order, are not those of
// the same key in before, sorted.
func (d decls) differFrom(before decls) []string {
	var keys []string
	for key, texts := range d {
		slices.Sort(texts)
		old := before[key]
		slices.Sort(old)
		if !slices.Equal(texts, old) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// addReaching marks in found, as "reaches", each test of dir not already
// marked that names one of changed, directly or through other declarations of
// dir's _test.go files at HEAD; every test of dir when changed holds TestMain.
func addReaching(dir string, changed []string, found map[string]string) error {
	all, err := dirDecls(dir)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, name := range changed {
		names[keyOf(name)] = true
	}
	marked := spread(all, names, names["TestMain"])
	for i, d := range all {
		if key := dir + " " + d.name; marked[i] && isTest(d.name) && found[key] == "" {
			found[key] = "reaches"
		}
	}
	return nil
}

// dirDecls is every top-level declaration of dir's _test.go files at HEAD.
func dirDecls(dir string) ([]decl, error) {
	out, err := git("ls-tree", "-z", "--name-only", "HEAD", "--", strings.TrimPrefix(dir+"/", "./"))
	if err != nil {
		return nil, err
	}
	var all []decl
	for _, p := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if !strings.HasSuffix(p, "_test.go") {
			continue
		}
		found, err := decls{}.read("HEAD", p)
		if err != nil {
			return nil, err
		}
		all = append(all, found...)
	}
	return all, nil
}

// isTest reports whether go test runs a function of this name by name, by
// the rule of cmd/go's isTest: the prefix, then nothing or a character that
// is not lower case. TestMain runs the others and is no test.
func isTest(name string) bool {
	if name == "TestMain" {
		return false
	}
	for _, prefix := range []string{"Test", "Fuzz", "Example"} {
		if rest, ok := strings.CutPrefix(name, prefix); ok {
			r, _ := utf8.DecodeRuneInString(rest)
			return rest == "" || !unicode.IsLower(r)
		}
	}
	return false
}

// git runs git in the working directory and returns its standard output, or
// an error that quotes its standard error.
func git(args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(context.Background(), "git", args...) //nolint:gosec // git with the base the caller names, as ci/changed-tests.sh passes it
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
