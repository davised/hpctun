//go:build !windows

// Package filelock takes an exclusive, non-blocking lock on an open file.
// The system releases it when the file is closed or the process exits,
// crash included, so a lock can never be left behind.
package filelock

import (
	"errors"
	"os"
	"syscall"
)

// TryLock takes the exclusive lock on f without blocking. busy reports that
// another process holds it.
func TryLock(f *os.File) (busy bool, err error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return false, nil
		case errors.Is(err, syscall.EINTR):
			continue // async preemption signal; the call never started, retry
		case errors.Is(err, syscall.EWOULDBLOCK):
			return true, nil
		default:
			return false, err
		}
	}
}
