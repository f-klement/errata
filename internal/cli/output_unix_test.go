//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/f-klement/errata/internal/check"
)

// withUmask runs f under umask mask and restores the old one. The umask is
// the process's, so no test of this package runs in parallel with it.
func withUmask(t *testing.T, mask int, f func()) {
	t.Helper()
	old := syscall.Umask(mask)
	defer syscall.Umask(old)
	f()
}

func perm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// A new file gets 0666 less the umask, as a shell redirect makes it, and a
// file that is there keeps its mode, so a report never widens who may read
// it. The planted breaks: the temporary file given 0644 by Chmod after it is
// opened, as the first version did, which a umask of 077 does not narrow; and
// keepMode's test made false, which makes the existing 0600 file 0644 under a
// umask of 022.
func TestOutputModeFollowsTheUmaskAndKeepsAnExistingFile(t *testing.T) {
	root := t.TempDir()
	checks := []check.Check{programCheck("good", "true")}
	fresh := filepath.Join(root, "fresh.xml")
	withUmask(t, 0o077, func() {
		if r := runCheck(t, root, checks, "-o", "junit="+fresh); r.code != ExitOK {
			t.Fatalf("exit %d; stderr %q", r.code, r.stderr)
		}
	})
	if got := perm(t, fresh); got != 0o600 {
		t.Errorf("a new file under umask 077 has mode %v, want 0600", got)
	}
	wide := filepath.Join(root, "wide.xml")
	withUmask(t, 0o022, func() {
		if r := runCheck(t, root, checks, "-o", "junit="+wide); r.code != ExitOK {
			t.Fatalf("exit %d; stderr %q", r.code, r.stderr)
		}
	})
	if got := perm(t, wide); got != 0o644 {
		t.Errorf("a new file under umask 022 has mode %v, want 0644", got)
	}

	existing := filepath.Join(root, "existing.xml")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	withUmask(t, 0o022, func() {
		if r := runCheck(t, root, checks, "-o", "junit="+existing); r.code != ExitOK {
			t.Fatalf("exit %d; stderr %q", r.code, r.stderr)
		}
	})
	if got := perm(t, existing); got != 0o600 {
		t.Errorf("an existing 0600 file has mode %v after the run, want 0600", got)
	}
	if readFile(t, existing) == "old" {
		t.Error("the existing file was not replaced")
	}
}

// A directory errata may not write to stops the run with exit 2 before any
// check runs. Root may write anywhere, so the test skips under it. The
// planted break: openOutput ignoring the OpenFile error, which runs the
// check.
func TestOutputInADirectoryWithoutPermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write to a directory of mode 0500")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	r := runCheck(t, root, []check.Check{programCheck("touch", "touch", "ran")}, "-o", "junit="+filepath.Join(locked, "errata.xml"))
	if r.code != ExitUsage {
		t.Fatalf("exit %d, want %d", r.code, ExitUsage)
	}
	if !strings.Contains(r.stderr, "permission denied") {
		t.Errorf("stderr %q, want permission denied", r.stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "ran")); err == nil {
		t.Error("the check ran")
	}
}

// A symbolic link at PATH is replaced by the report, whether it points at a
// file or a directory, and what it points at is left as it was: errata never
// writes through a link. The planted break: os.Stat in place of os.Lstat in
// openOutput, which follows the link to the directory and refuses it.
func TestOutputReplacesASymbolicLink(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(t *testing.T, root string) string
	}{
		{"to a file", func(t *testing.T, root string) string {
			t.Helper()
			target := filepath.Join(root, "target.xml")
			if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
				t.Fatal(err)
			}
			return target
		}},
		{"to a directory", func(t *testing.T, root string) string {
			t.Helper()
			target := filepath.Join(root, "dir")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			return target
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			target := tc.target(t, root)
			link := filepath.Join(root, "errata.xml")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			r := runCheck(t, root, []check.Check{programCheck("good", "true")}, "-o", "junit="+link)
			if r.code != ExitOK {
				t.Fatalf("exit %d; stderr %q", r.code, r.stderr)
			}
			info, err := os.Lstat(link)
			if err != nil {
				t.Fatal(err)
			}
			if !info.Mode().IsRegular() || !strings.HasPrefix(readFile(t, link), "<?xml") {
				t.Errorf("errata.xml is %v, want a regular file holding the report", info.Mode())
			}
			info, err = os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().IsRegular() && readFile(t, target) != "target" {
				t.Error("the report was written through the link")
			}
		})
	}
}
