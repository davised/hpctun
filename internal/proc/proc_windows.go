package proc

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

const stillActive = 259 // STILL_ACTIVE: GetExitCodeProcess of a running process

func lookup(pid int) (Info, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return Info{}, false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil || code != stillActive {
		return Info{}, false
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return Info{}, false
	}
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) != nil {
		return Info{}, false
	}
	return Info{Image: ImageName(windows.UTF16ToString(buf[:n])), Started: time.Unix(0, created.Nanoseconds()).UTC()}, true
}

// terminate ends pid only if it still is the ssh that started at started.
// Everything is checked on the one handle it also terminates through, so a
// pid Windows reused after the caller's own check is never touched.
func terminate(pid int, started time.Time) error {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("pid %d no longer runs this tunnel", pid)
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil || code != stillActive {
		return fmt.Errorf("pid %d no longer runs this tunnel", pid)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil || ImageName(windows.UTF16ToString(buf[:n])) != "ssh" {
		return fmt.Errorf("pid %d no longer runs this tunnel", pid)
	}
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) != nil ||
		!time.Unix(0, created.Nanoseconds()).UTC().Equal(started) {
		return fmt.Errorf("pid %d no longer runs this tunnel", pid)
	}
	if err := windows.TerminateProcess(h, 1); err != nil {
		return fmt.Errorf("stopping ssh (pid %d) failed: %w", pid, err)
	}
	return nil
}
