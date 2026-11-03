//go:build !unix

package program

import "os/exec"

// isolate has no process groups to use here; Run's WaitDelay still stops a
// child that outlives the program from holding the run open.
func isolate(*exec.Cmd) {}

// reap has no process group to kill here: a child the program left behind
// runs on.
func reap(*exec.Cmd) {}
