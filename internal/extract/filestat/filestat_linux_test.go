package filestat

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// A FIFO at the name, outright or through a link, is no regular file, as
// for a reader, so both kinds are unknown for it, at once and without
// opening it. Planted break: the regular file test dropped from exists,
// which read a FIFO as true.
func TestFIFOIsNoFile(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "p"), 0o600); err != nil {
		t.Fatal(err)
	}
	extracttest.Symlink(t, "p", filepath.Join(root, "link"))
	for _, file := range []string{"p", "link"} {
		for kind, build := range builds {
			got, err := extracttest.Run(t, build, Spec{File: file}, root)
			if err == nil || err.Error() != "stat "+file+": it is not a regular file" || got != nil {
				t.Errorf("%s %s: %+v, %v", kind, file, got, err)
			}
		}
	}
}

// A file below a directory that cannot be searched may or may not be there,
// so both kinds are unknown, and exists does not say false. A file that
// cannot be read but is there is there, and its size is known, since
// neither kind reads it. Root searches and reads everything, so the test
// needs another user and skips on the runner; TestExistsRefuses fails on
// its planted break, exists reading every Stat error as false, on any user.
func TestPermissions(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root searches a directory of mode 000")
	}
	root := extracttest.Repo(t, map[string]string{"a/f": "x", "locked": "abcd"})
	for _, name := range []string{"a", "locked"} {
		if err := os.Chmod(filepath.Join(root, name), 0); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "a"), 0o750) })
	for kind, build := range builds {
		got, err := extracttest.Run(t, build, Spec{File: "a/f"}, root)
		if err == nil || err.Error() != "stat a/f: permission denied" || got != nil {
			t.Errorf("%s a/f: %+v, %v; want the error stat a/f: permission denied", kind, got, err)
		}
	}
	if got := extracttest.MustRun(t, BuildExists, Spec{File: "locked"}, root); extracttest.Texts(got) != "true@0" {
		t.Errorf("exists locked: %s, want true", extracttest.Texts(got))
	}
	if got := extracttest.MustRun(t, BuildSize, Spec{File: "locked"}, root); extracttest.Texts(got) != "4@0" {
		t.Errorf("size locked: %s, want 4", extracttest.Texts(got))
	}
}
