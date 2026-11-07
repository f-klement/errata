//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// killGroup kills the command's process group.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// raise ends this process with the signal, so its parent reads the status it
// would have read from the process that got it.
func raise(sig syscall.Signal) { _ = syscall.Kill(os.Getpid(), sig) }
