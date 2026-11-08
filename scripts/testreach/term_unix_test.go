//go:build unix

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// F3 of the review of #264: a TERM to testreach, as the deadline's timeout
// sends, takes the test binary it runs with it.
func TestRunTermLeavesNoChildBehind(t *testing.T) {
	if os.Getenv("TESTREACH_EXEC") == "term" {
		os.Exit(run(os.Getenv("FAKE_GO"), os.Getenv("ROOT"), os.Getenv("INDEX"), hangRun))
	}
	cmd, bin, child := startHanging(t, "^TestRunTermLeavesNoChildBehind$", "term")
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	// The planted break, the handler's kill of the group removed: the sleep
	// the binary started, outside Pdeathsig's reach, runs on.
	for _, pid := range []int{bin, child} {
		if !gone(pid) {
			_ = syscall.Kill(-bin, syscall.SIGKILL)
			t.Fatalf("process %d outlived a TERM to testreach (the binary %d, its child %d)", pid, bin, child)
		}
	}
}

// hangRun is a mutant run whose narrowed share is long, 39.5 s of the 120 s
// gremlins gives, so that only the signal a test sends ends the hanging
// binary while the test looks: with mutantRun's 1.5 s, testreach's own limit
// ended it first on a busy node, and the test passed whatever the signal did.
var hangRun = []string{"test", "-tags", "mutation", "-timeout", "122s", "-failfast", "example.com/m/internal/p"}

// startHanging runs testreach as a child process of the test, through the
// test named by run with TESTREACH_EXEC=mode, on a mutant whose narrowed
// binary hangs, and returns it with the pids of that binary and its child.
func startHanging(t *testing.T, run, mode string) (*exec.Cmd, int, int) {
	t.Helper()
	w := newWorld(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run", run)
	cmd.Env = append(os.Environ(), "TESTREACH_EXEC="+mode, "FAKE_GO="+w.goBin, "ROOT="+w.root, "INDEX="+w.index, "PID_FILE="+pidFile)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	readPid := func(f string) int {
		b, err := os.ReadFile(f)
		if err != nil {
			return 0
		}
		n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return n
	}
	var bin, child int
	// Up to 30 s, for a busy node; the loop ends once both are written.
	for i := 0; i < 1200 && (bin == 0 || child == 0); i++ {
		time.Sleep(25 * time.Millisecond)
		bin, child = readPid(pidFile), readPid(pidFile+".child")
	}
	if bin == 0 || child == 0 {
		_ = cmd.Process.Kill()
		t.Fatal("the fake binary never started")
	}
	return cmd, bin, child
}

// gone waits up to 30 s for the process to end, for a busy node; it returns
// as soon as it has.
func gone(pid int) bool {
	for range 600 {
		if !alive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// alive reports whether the process runs and is not a zombie.
func alive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return syscall.Kill(pid, 0) == nil
	}
	f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
	return len(f) > 0 && f[0] != "Z" && f[0] != "X"
}
