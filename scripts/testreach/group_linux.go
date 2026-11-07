//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

// ownGroup starts the command in a process group of its own, so that
// killGroup reaches the test binary go starts as well as go, and kills it when
// testreach dies, as by gremlins' SIGKILL at its limit.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
