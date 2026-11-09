package main

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os/exec"
	"path"
	"slices"
	"strings"
)

// gitRunner runs git with args and returns what it printed on stdout.
type gitRunner func(args ...string) ([]byte, error)

func execGit(args ...string) ([]byte, error) {
	cmd := exec.CommandContext(context.Background(), "git", args...) //nolint:gosec // the revisions ci/gate.sh names, and paths git listed
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// input is what check compares: the test files of each package the change
// touched a test file of, at each side, the lines the change added to them,
// the testdata files it edited or deleted, and the first .go file outside the
// tests it changed, or "".
type input struct {
	base, head map[string][]byte
	added      map[string]map[int]bool
	golden     []string
	code       string
}

// load reads the change from base to head through git. It reads every test
// file of a package the change touched a test file of, not only the touched
// ones, for the helpers a test calls and a test moved out of a touched file.
func load(git gitRunner, base, head string) (input, error) {
	in := input{base: map[string][]byte{}, head: map[string][]byte{}}
	// --no-renames lists a moved file at its old path and its new one, so
	// the packages of both are read.
	names, err := git("diff", "--no-renames", "--no-ext-diff", "--name-status", "-z", base, head, "--")
	if err != nil {
		return in, err
	}
	dirs := map[string]bool{}
	fields := strings.Split(strings.TrimSuffix(string(names), "\x00"), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		in.take(fields[i], fields[i+1], dirs)
	}
	for _, dir := range slices.Sorted(maps.Keys(dirs)) {
		if err := readPackage(git, base, dir, in.base); err != nil {
			return in, err
		}
		if err := readPackage(git, head, dir, in.head); err != nil {
			return in, err
		}
	}
	diff, err := git("-c", "core.quotePath=false", "diff", "--no-renames", "--no-ext-diff", "--no-color",
		"-U0", "--src-prefix=a/", "--dst-prefix=b/", base, head, "--", "*_test.go")
	in.added = addedLines(diff)
	return in, err
}

// take files one changed path, of git's status: a test file's package into
// dirs, a testdata file the change did not add into golden, the first other
// .go file into code.
func (in *input) take(status, file string, dirs map[string]bool) {
	switch {
	case isTestFile(file):
		dirs[path.Dir(file)] = true
	case inTestdata(file):
		if status != "A" {
			in.golden = append(in.golden, file)
		}
	case strings.HasSuffix(file, ".go") && in.code == "":
		in.code = file
	}
}

// readPackage puts each test file of the package in dir at rev into side.
func readPackage(git gitRunner, rev, dir string, side map[string][]byte) error {
	list, err := git("ls-tree", "--name-only", "-z", rev, "--", dir+"/")
	if err != nil {
		return err
	}
	for _, file := range strings.Split(string(list), "\x00") {
		if !isTestFile(file) || path.Dir(file) != dir {
			continue
		}
		src, err := git("show", rev+":"+file)
		if err != nil {
			return err
		}
		side[file] = src
	}
	return nil
}

func isTestFile(path string) bool {
	return strings.HasSuffix(path, "_test.go") && !inTestdata(path)
}

// inTestdata: go test builds nothing under a directory named testdata.
func inTestdata(path string) bool {
	return strings.Contains("/"+path, "/testdata/")
}

// addedLines reads a unified diff with no context and returns, for each file,
// the head's line numbers of the lines it added.
func addedLines(diff []byte) map[string]map[int]bool {
	added := map[string]map[int]bool{}
	var lines map[int]bool
	header, line := false, 0
	for _, l := range strings.Split(string(diff), "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			header, lines = true, nil
		case header && strings.HasPrefix(l, "+++ b/"):
			lines = map[int]bool{}
			added[strings.TrimPrefix(l, "+++ b/")] = lines
		case strings.HasPrefix(l, "@@ "):
			header, line = false, hunkStart(l)
		case !header && lines != nil && strings.HasPrefix(l, "+"):
			lines[line] = true
			line++
		}
	}
	return added
}

// hunkStart is the head's first line of the hunk a header such as
// "@@ -3,0 +4,2 @@" opens.
func hunkStart(header string) int {
	_, after, _ := strings.Cut(header, " +")
	var n int
	_, _ = fmt.Sscanf(after, "%d", &n)
	return n
}
