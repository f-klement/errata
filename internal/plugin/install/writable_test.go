package install

import (
	"os"
	"path/filepath"
	"testing"
)

// writable runs before a rename only on Windows, so it is tested here
// directly: a read only file becomes writable, a link is not followed and
// its target keeps its mode, and a missing file is no error. Planted breaks:
// writable that follows a link (os.Stat), which made the target writable;
// a chmod that keeps the file's mode, which left it read only.
func TestWritableMakesOnlyARegularFileWritable(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "build")
	if err := os.WriteFile(file, []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := writable(file); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm()&0o200 == 0 {
		t.Fatalf("mode %v, err %v: want the file writable by its owner", info.Mode(), err)
	}

	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("y"), 0o400); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("this system makes no symlink: %v", err)
	}
	if err := writable(link); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("target mode %v, err %v: want 0400, the link not followed", info.Mode(), err)
	}

	if err := writable(filepath.Join(dir, "missing")); err != nil {
		t.Fatalf("a missing file: %v", err)
	}
}
