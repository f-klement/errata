package core

import (
	"errors"
	"io/fs"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// Stat follows the links on the way to a file as Read does and gives what is
// there: a file, the file a link inside the repository leads to, a directory.
// Nothing at the name, a link that dangles inside the repository and a name
// below a file read as fs.ErrNotExist; a link that leads out is refused
// whether its target exists or not, and so are a loop of links and a root
// that is not there or is a file, none of them as fs.ErrNotExist. Planted
// breaks: resolve before the root is opened, which read a file as the root
// as a name below a file, missing; the
// ENOTDIR test in notThere dropped, which made f/x a refusal and not a
// missing file; noDirectory.Is false; the resolve error passed over, which
// statted the link that leads out.
func TestStat(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "a: 1\n", "real/g.yaml": "abc"})
	extracttest.Symlink(t, "real/g.yaml", filepath.Join(root, "link"))
	extracttest.Symlink(t, "nowhere", filepath.Join(root, "dangling"))
	extracttest.Symlink(t, "/etc/hostname", filepath.Join(root, "out"))
	extracttest.Symlink(t, "/errata-no-such-file", filepath.Join(root, "outgone"))
	extracttest.Symlink(t, "loop", filepath.Join(root, "loop"))
	for _, c := range []struct {
		file string
		dir  bool
		size int64
	}{
		{"f", false, 5},
		{"link", false, 3},
		{"real/../f", false, 5},
		{"dangling/../f", false, 5},
		{"real", true, 0},
	} {
		info, err := (Source{File: c.file}).Stat(root)
		if err != nil {
			t.Errorf("%s: %v", c.file, err)
			continue
		}
		if info.IsDir() != c.dir || (!c.dir && (!info.Mode().IsRegular() || info.Size() != c.size)) {
			t.Errorf("%s: mode %v, size %d; want a directory %v of %d bytes", c.file, info.Mode(), info.Size(), c.dir, c.size)
		}
	}
	for _, c := range []struct {
		root, file, want string
		missing          bool
	}{
		{root, "gone", "stat gone: no such file or directory", true},
		{root, "real/gone", "stat real/gone: no such file or directory", true},
		{root, "dangling", "stat dangling: no such file or directory", true},
		{root, "f/x", "stat f/x: not a directory", true},
		{root, "out", "stat out: a symlink leads outside the repository", false},
		{root, "outgone", "stat outgone: a symlink leads outside the repository", false},
		{root, "loop", "stat loop: too many levels of symbolic links", false},
		{filepath.Join(t.TempDir(), "gone"), "f", "open the repository root: no such file or directory", false},
		{filepath.Join(root, "f"), "legacy/old.conf", "open the repository root: not a directory", false},
	} {
		info, err := (Source{File: c.file}).Stat(c.root)
		if err == nil || err.Error() != c.want || info != nil {
			t.Errorf("%s: %v, %v; want the error %q", c.file, info, err, c.want)
			continue
		}
		if errors.Is(err, fs.ErrNotExist) != c.missing {
			t.Errorf("%s: errors.Is(err, fs.ErrNotExist) is %v, want %v", c.file, !c.missing, c.missing)
		}
	}
}

// notThere reads ENOTDIR as fs.ErrNotExist, with its own text, and leaves
// any other error as it is: a directory errata may not search says nothing
// of whether the file is there. The permission tests skip as root, which
// the runner is, so this holds the line for them. Planted break: notThere
// reading every error as missing.
func TestNotThere(t *testing.T) {
	if err := notThere(syscall.ENOTDIR); !errors.Is(err, fs.ErrNotExist) || err.Error() != "not a directory" {
		t.Errorf("ENOTDIR: %v, want not a directory read as fs.ErrNotExist", err)
	}
	for _, e := range []error{syscall.EACCES, syscall.ELOOP, errOutside} {
		if err := notThere(e); errors.Is(err, fs.ErrNotExist) || err.Error() != e.Error() {
			t.Errorf("%v: %v, want it as it is", e, err)
		}
	}
}
