//go:build unix

package core

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// An index that is a FIFO, which git never writes, is refused at once,
// not waited on until a writer comes. Planted break: the regular file
// check and the non-blocking open dropped, which waited on the FIFO.
func TestTrackedFilesRefusesAFIFO(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{".git/HEAD": "ref: refs/heads/main\n", ".git/objects/x": "", ".git/refs/x": ""})
	if err := syscall.Mkfifo(filepath.Join(root, ".git", "index"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := TrackedFiles(root); done <- err }()
	select {
	case err := <-done:
		extracttest.WantError(t, err, "read git's index: it is not a regular file")
	case <-time.After(5 * time.Second):
		t.Fatal("TrackedFiles waits on a FIFO")
	}
}

// An index that is a symlink to a device is refused before it is opened,
// since opening a device such as a watchdog acts on it. /dev/null stands
// for one, which needs no root. Planted break: the check before the open
// dropped, which opened /dev/null and refused it as no regular file.
func TestTrackedFilesRefusesADeviceUnopened(t *testing.T) {
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("no /dev/null")
	}
	root := extracttest.Repo(t, map[string]string{".git/HEAD": "ref: refs/heads/main\n", ".git/objects/x": "", ".git/refs/x": ""})
	extracttest.Symlink(t, "/dev/null", filepath.Join(root, ".git", "index"))
	_, err := TrackedFiles(root)
	extracttest.WantError(t, err, "read git's index: it is a device, which errata does not open")
}
