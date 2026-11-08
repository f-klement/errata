//go:build linux

package main

import (
	"os"
	"syscall"
	"testing"
)

// F3 of the review of #264: gremlins kills testreach at its limit with
// SIGKILL, which no handler sees; Pdeathsig takes the binary testreach runs
// with it, as gremlins' kill of go did. The planted break: Pdeathsig removed.
func TestRunKillTakesTheBinaryWithIt(t *testing.T) {
	if os.Getenv("TESTREACH_EXEC") == "kill" {
		os.Exit(run(os.Getenv("FAKE_GO"), os.Getenv("ROOT"), os.Getenv("INDEX"), hangRun))
	}
	cmd, bin, _ := startHanging(t, "^TestRunKillTakesTheBinaryWithIt$", "kill")
	t.Cleanup(func() { _ = syscall.Kill(-bin, syscall.SIGKILL) })
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if !gone(bin) {
		t.Fatalf("the binary %d outlived a SIGKILL to testreach", bin)
	}
}
