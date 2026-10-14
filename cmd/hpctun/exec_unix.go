//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// execProcess replaces hpctun with argv[0], found on PATH.
func execProcess(argv []string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ())
}
