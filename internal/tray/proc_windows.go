package tray

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// ownGroup runs hpctun with no console window -- hpctun-tray has none, and
// each status poll's hpctun would otherwise flash one -- and makes
// cancelling cmd end only hpctun itself. A tunnel's ssh outlives hpctun by
// design, so walking the process tree with taskkill /T would kill tunnels
// an `up --all` already recorded; an ssh still opening when the tray gives
// up may instead be left running unrecorded, which status then lists as an
// external ssh.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	cmd.Cancel = func() error { return cmd.Process.Kill() }
}
