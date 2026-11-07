//go:build unix && !linux

package main

import (
	"os/exec"
	"syscall"
)

// ownGroup starts the command in a process group of its own, so that
// killGroup reaches the test binary go starts as well as go.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
