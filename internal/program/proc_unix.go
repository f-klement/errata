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

// reap kills what is left of the program's process group once Run's Wait
// has returned, so a child the program left behind does not run on after
// errata. ESRCH, a group with nothing left in it, is the common case and
// is no error. The group's id is the program's pid, which Wait has freed;
// the kernel hands that pid to no new process while a member of the group
// lives, so the kill reaches no stranger's group unless the group is
// already empty and the pid comes round again in the moment between Wait
// and the kill.
func reap(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
