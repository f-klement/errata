package core

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A file that grows past the limit while it is read is refused, at the size
// it had reached: one byte past the limit. A FIFO makes that case
// repeatable. Its size reads 0 and a writer has put more than the limit
// into it, so read's check of the size passes and its read finds a byte
// after the limit. The planted break is returning the bytes read, which
// hands back the file cut at the limit.
func TestReadRefusesAFileThatGrowsPastTheLimit(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "f")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// On Linux a FIFO opened for reading and writing opens at once, and as
	// its writer it lets read's own open go ahead without waiting.
	w, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.WriteString(strings.Repeat("x", 16)); err != nil {
		t.Fatal(err)
	}
	data, err := Source{File: "f", Limit: 8}.Read(root)
	want := "read f: the file is 9 bytes, over the limit of 8 bytes"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("read %q, %v; want an error starting %q", data, err, want)
	}
}
