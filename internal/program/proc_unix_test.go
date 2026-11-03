//go:build unix

package program

import (
	"context"
	"errors"
	"os"
	"os/exec"
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
	awaitGone(t, childPid(t, dir), "the program timed out")
}

// A program that exits 0 and leaves a background child does not leave it
// running once Run returns: Run kills the group after the program exits.
// The first child writes elsewhere, so the program's exit ends the run at
// once; the second holds stdout, so the run waits WaitDelay for it and ends
// with exec.ErrWaitDelay, as it did before, and the child is killed after.
// A program that exits 1 with such a child ends with its exit error, not
// ErrWaitDelay, which exec reports only after an exit of 0, and its group
// is killed as well. Each sleeps 4 s, past the second the test waits.
// Planted break: reap made a no-op, which left each sleep running.
func TestRunKillsTheGroupAfterTheProgramExits(t *testing.T) {
	for _, tc := range []struct {
		script string
		want   func(error) bool
	}{
		{"sleep 4 >/dev/null 2>&1 & echo $! > child.pid", func(err error) bool { return err == nil }},
		{"sleep 4 & echo $! > child.pid", func(err error) bool { return errors.Is(err, exec.ErrWaitDelay) }},
		{"sleep 4 & echo $! > child.pid; exit 1", func(err error) bool {
			var exit *exec.ExitError
			return errors.As(err, &exit) && exit.ExitCode() == 1
		}},
	} {
		dir := t.TempDir()
		s := sh(tc.script)
		s.Dir = dir
		if e := Run(context.Background(), s); !tc.want(e.Err) {
			t.Fatalf("%s: ended with %v", tc.script, e.Err)
		}
		awaitGone(t, childPid(t, dir), "the program exited")
	}
}

// childPid is the pid a test's program wrote to child.pid in dir.
func childPid(t *testing.T, dir string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "child.pid"))
	if err != nil {
		t.Fatalf("the program did not record its child: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// awaitGone fails t unless pid is gone within a second, and kills it then
// so a failing test leaves nothing behind.
func awaitGone(t *testing.T, pid int, after string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child %d still running after %s", pid, after)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
