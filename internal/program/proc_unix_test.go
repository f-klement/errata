//go:build unix

package program

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

// A program that starts a background child and times out does not leave
// that child running: the timeout kills the process group. The child sleeps
// 4 s, past the 1.2 s that WaitDelay and the timeout take and the 1 s the
// test waits after, so a mutant that leaves it running fails, and under a
// mutant that drops the timeout this test passes after 4 s, and
// TestRunTimeout fails 3 s later, about 7 s in all, under mutation.sh's
// 10 s floor. Planted break: isolate's Cancel killing the program alone,
// which left the sleep.
func TestRunTimeoutKillsTheGroup(t *testing.T) {
	dir := t.TempDir()
	s := sh("sleep 4 & echo $! > child.pid; wait")
	s.Dir, s.Timeout = dir, 200*time.Millisecond
	Run(context.Background(), s)

	raw, err := os.ReadFile(filepath.Join(dir, "child.pid"))
	if err != nil {
		t.Fatalf("the program did not record its child: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child %d still running after the program timed out", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
