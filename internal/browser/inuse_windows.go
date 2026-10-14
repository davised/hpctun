package browser

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// FileInUse reports whether another process holds path open without
// sharing it, as Chromium holds its profile's lockfile while it runs.
func FileInUse(path string) bool {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err == nil {
		f.Close()
		return false
	}
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
