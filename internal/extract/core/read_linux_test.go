package core

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A file that grows past the limit while it is read is refused, at the size
// it had reached: one byte past the limit. A reader of more bytes than the
// size Read saw stands for it, since a FIFO, which made the case
// repeatable, is now refused before it is read. The planted break is
// returning the bytes read, which hands back the file cut at the limit.
func TestReadRefusesAFileThatGrowsPastTheLimit(t *testing.T) {
	data, err := Source{File: "f", Limit: 8}.readLimited(strings.NewReader(strings.Repeat("x", 16)))
	want := "read f: the file is 9 bytes, over the limit of 8 bytes"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("read %q, %v; want an error starting %q", data, err, want)
	}
	if data, err := (Source{File: "f", Limit: 8}).readLimited(strings.NewReader("12345678")); err != nil || string(data) != "12345678" {
		t.Errorf("a file of the limit: %q, %v", data, err)
	}
}

// A FIFO in the working tree, named outright or through a symlink, is
// refused at once, before any read, so a FIFO with no writer does not hang
// the run, and a device is no regular file either. Planted breaks: the type
// check dropped, which read the FIFO as an empty file; the open made
// blocking, which waited on it.
func TestReadRefusesAFIFOAndADevice(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "f"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("f", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"f", "link"} {
		done := make(chan error, 1)
		go func() { _, err := (Source{File: name, Limit: 8}).Read(root); done <- err }()
		select {
		case err := <-done:
			if err == nil || err.Error() != "read "+name+": it is not a regular file" {
				t.Errorf("%s: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: Read waits on a FIFO", name)
		}
	}
	info, err := os.Lstat("/dev/null")
	if err != nil {
		t.Skip("no /dev/null")
	}
	if err := regular(info); err == nil || err.Error() != "it is not a regular file" {
		t.Errorf("/dev/null: %v", err)
	}
}

// A device is refused by its type before it is opened, since opening one
// can act on it: a watchdog arms, a serial port raises DTR. /dev/null, read
// with /dev as the root, stands for one and needs no root to make. Planted
// break: the check before the open dropped, which opened /dev/null and
// refused it as no regular file.
func TestReadRefusesADeviceUnopened(t *testing.T) {
	if _, err := os.Lstat("/dev/null"); err != nil {
		t.Skip("no /dev/null")
	}
	_, err := Source{File: "null", Limit: 8}.Read("/dev")
	if err == nil || err.Error() != "read null: it is a device, which errata does not open" {
		t.Errorf("/dev/null: %v", err)
	}
}
