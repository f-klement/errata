//go:build !unix

package protocol

import "os/exec"

// isolate has no process groups to use here; Run's WaitDelay still stops a
// child that outlives the check from holding the run open.
func isolate(*exec.Cmd) {}
