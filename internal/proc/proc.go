// Package proc reports what hpctun needs to know about a running process:
// its program and when it started, which together tell a tunnel's ssh from
// a later process that got the same pid (Windows).
package proc

import (
	"strings"
	"time"
)

// Info is a running process.
type Info struct {
	Image   string    // program base name, lowercased, without .exe: "ssh"
	Started time.Time // creation time, UTC
}

// Lookup reports pid's Info; ok is false when no such process runs or it
// cannot be inspected. It is only implemented on Windows.
func Lookup(pid int) (Info, bool) { return lookup(pid) }

// Terminate ends pid, which must still be the process that started at
// started. On Windows the verification and the termination share one
// OpenProcess handle, so a pid the system reused in between is left alone.
// It is only implemented on Windows.
func Terminate(pid int, started time.Time) error { return terminate(pid, started) }

// ImageName turns a program path into Info.Image.
func ImageName(path string) string {
	if i := strings.LastIndexAny(path, `\/`); i >= 0 {
		path = path[i+1:]
	}
	path = strings.ToLower(path)
	return strings.TrimSuffix(path, ".exe")
}
