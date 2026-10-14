//go:build !windows

package state

import (
	"errors"
	"os"
	"syscall"
)

// tryLock takes the exclusive lock on f without blocking. busy reports that
// another process holds it.
func tryLock(f *os.File) (busy bool, err error) {
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
