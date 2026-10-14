// Package legacy moves files left under tun's names from before the
// rename to hpctun.
package legacy

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// MoveDir renames old to new when old exists and new does not. It reports
// whether it moved anything. When both exist, new wins and old is left
// alone.
func MoveDir(old, new string) (bool, error) {
	if _, err := os.Lstat(new); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if _, err := os.Lstat(old); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(new), 0o755); err != nil {
		return false, err
	}
	if err := os.Rename(old, new); err != nil {
		return false, err
	}
	return true, nil
}
