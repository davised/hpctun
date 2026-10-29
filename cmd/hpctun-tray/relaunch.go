package main

import (
	"errors"
	"os/exec"

	"hpctun/internal/detach"
	"hpctun/internal/selfupdate"
)

// relaunchArgv is the command that starts the updated tray. On macOS it
// opens the app, so the new tray is an app macOS launched, not our child.
// Elsewhere it is the path recorded at startup, with the same arguments:
// after the swap the system names the running file "… (deleted)".
func relaunchArgv(goos, exe string, args []string) ([]string, error) {
	if goos == "darwin" {
		inst, err := selfupdate.Detect(exe)
		if err != nil {
			return nil, err
		}
		if inst.Kind != selfupdate.App {
			return nil, errors.New("hpctun-tray is not running from hpctun-tray.app")
		}
		return []string{"open", "-n", inst.Bundle}, nil
	}
	return append([]string{exe}, args...), nil
}

// relaunch starts the new tray, detached, without the context Quit is
// about to cancel. It reports only that the program started.
func relaunch(goos, exe string, args []string) error {
	argv, err := relaunchArgv(goos, exe, args)
	if err != nil {
		return err
	}
	_, err = detach.Start(func() *exec.Cmd { return exec.Command(argv[0], argv[1:]...) })
	return err
}
