//go:build !unix

package main

import (
	"os/exec"
	"syscall"
)

// Elsewhere than unix, which make mutation does not run on, the command is
// killed alone and a signal is not passed on.

func ownGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func raise(syscall.Signal) {}
