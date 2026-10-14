//go:build !windows

package detach

import (
	"os/exec"
	"syscall"
)

// start puts the program in a session of its own.
func start(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	cmd := newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd, cmd.Start()
}
