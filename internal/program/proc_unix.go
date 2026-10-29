//go:build unix

package program

import (
	"os/exec"
	"syscall"
)

// isolate runs the program in its own process group, and makes cancelling it
// kill that group. Killing only the direct child would leave a shell's
// children running, holding its output pipes open past the timeout.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
