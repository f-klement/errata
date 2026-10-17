//go:build unix

package protocol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A check that starts a background child and times out must not leave that
// child running: errata's timeout bounds the whole check.
func TestRunTimeoutKillsChildren(t *testing.T) {
	dir := t.TempDir()
	c := sh("sleep 30 & echo $! > child.pid; wait")
	c.Timeout = 200 * time.Millisecond
	Run(context.Background(), dir, c)

	raw, err := os.ReadFile(filepath.Join(dir, "child.pid"))
	if err != nil {
		t.Fatalf("the check did not record its child: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child %d still running after the check timed out", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
