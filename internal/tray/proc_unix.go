//go:build unix

package tray

import (
	"os/exec"
	"syscall"
)

// ownGroup starts cmd in a process group of its own, so that cancelling it
// kills what hpctun started too: ssh, and the password dialog ssh opened.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
