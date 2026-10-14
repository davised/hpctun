//go:build !windows

package tunnel

import "syscall"

// KillTERM sends SIGTERM; it is the real Env.Kill.
func KillTERM(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
