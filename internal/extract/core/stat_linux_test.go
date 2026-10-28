package core

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// Stat only looks at a FIFO, named outright or through a link, so a FIFO
// with no writer gives its type at once. The planted break: Stat opening
// the file, which waits on the FIFO.
func TestStatLooksAtAFIFO(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "p"), 0o600); err != nil {
		t.Fatal(err)
	}
	extracttest.Symlink(t, "p", filepath.Join(root, "link"))
	type result struct {
		info fs.FileInfo
		err  error
	}
	for _, name := range []string{"p", "link"} {
		done := make(chan result, 1)
		go func() { info, err := (Source{File: name}).Stat(root); done <- result{info, err} }()
		select {
		case r := <-done:
			if r.err != nil || r.info.Mode().Type() != fs.ModeNamedPipe {
				t.Errorf("%s: %v, %v; want a named pipe", name, r.info, r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: Stat waits on a FIFO", name)
		}
	}
}

// A directory on the way that cannot be searched is refused, and not read
// as a missing file: whether the file is there is not known. Root searches
// every directory, so the test needs another user and skips on the runner;
// TestNotThere fails on its planted break, notThere reading every error as
// fs.ErrNotExist, on any user.
func TestStatRefusesAnUnsearchableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root searches a directory of mode 000")
	}
	root := extracttest.Repo(t, map[string]string{"a/f": "x"})
	if err := os.Chmod(filepath.Join(root, "a"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "a"), 0o750) })
	_, err := (Source{File: "a/f"}).Stat(root)
	if err == nil || err.Error() != "stat a/f: permission denied" || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%v; want the refusal stat a/f: permission denied", err)
	}
}
