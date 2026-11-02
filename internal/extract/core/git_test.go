package core

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core/gitindex"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// git runs git in dir with a fixed identity and no user or system config,
// and fails the test on an error.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// repo is a git repository of files, every one added.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := extracttest.Repo(t, files)
	git(t, root, "init", "-q")
	git(t, root, "add", "-A")
	return root
}

// writeIndex writes root's .git/index as a version 2 index of SHA-1
// names listing paths, each a regular file, in the order given, which git
// itself never writes out of its order.
func writeIndex(t *testing.T, root string, paths ...string) {
	t.Helper()
	b := []byte("DIRC\x00\x00\x00\x02\x00\x00\x00\x00")
	binary.BigEndian.PutUint32(b[8:], uint32(len(paths)))
	for _, p := range paths {
		e := make([]byte, 40+20+2)
		binary.BigEndian.PutUint32(e[24:], 0o100644)
		binary.BigEndian.PutUint16(e[60:], uint16(len(p)))
		e = append(e, p...)
		b = append(b, append(e, make([]byte, 8-len(e)%8)...)...)
	}
	b = append(b, make([]byte, 20)...)
	if err := os.WriteFile(filepath.Join(root, ".git", "index"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// memIndex is the Index of files, each a regular file, in their order.
func memIndex(t *testing.T, files ...string) gitindex.Index {
	t.Helper()
	var entries []gitindex.Entry
	for _, f := range files {
		entries = append(entries, gitindex.Entry{Path: f, Mode: 0o100644})
	}
	ix, err := gitindex.New(entries)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// globbedOf is the paths of ix a glob matches against, joined with
// spaces.
func globbedOf(t *testing.T, ix gitindex.Index) string {
	t.Helper()
	sp := ix.Paths().Span("")
	got, err := sp.Match(func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(got, " ")
}
