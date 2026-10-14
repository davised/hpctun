package detach

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// start gives a console program (ssh) a console with no window, so nothing
// shows on screen and closing hpctun's terminal does not reach it; GUI
// programs (browsers) ignore CREATE_NO_WINDOW. A new process group keeps
// Ctrl-C away. Breaking away from the job keeps terminals that kill their
// job on close (Windows Terminal, VS Code) from taking the program along;
// when the job forbids breakaway, it starts inside the job instead.
func start(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	const flags = windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP
	cmd := newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags | windows.CREATE_BREAKAWAY_FROM_JOB}
	err := cmd.Start()
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return cmd, err
	}
	cmd = newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
	return cmd, cmd.Start()
}
