package selfupdate

import (
	"fmt"
	"os"
)

// rename is os.Rename; tests make it fail.
var rename = os.Rename

// Replace puts newPath (on the same filesystem) in old's place. A file on
// Unix is one atomic rename; the running process keeps its old inode. A
// directory (an app bundle) cannot be renamed over, and a running .exe on
// Windows cannot be overwritten, so old is first renamed to old.old and
// put back if newPath cannot take its place. A directory's .old is then
// removed; a Windows .exe.old is still in use, and CleanOld removes it on
// the next update.
func Replace(old, newPath string, windows bool) error {
	fi, err := os.Stat(old)
	if err != nil {
		return fmt.Errorf("can't replace %s: %w", old, err)
	}
	if !fi.IsDir() && !windows {
		if err := rename(newPath, old); err != nil {
			return fmt.Errorf("can't replace %s: %w", old, err)
		}
		return nil
	}
	bak := old + ".old"
	os.RemoveAll(bak)
	if err := rename(old, bak); err != nil {
		return fmt.Errorf("can't replace %s: %w", old, err)
	}
	if err := rename(newPath, old); err != nil {
		if rerr := os.Rename(bak, old); rerr != nil {
			return fmt.Errorf("can't replace %s: %w (and putting it back from %s failed: %v)", old, err, bak, rerr)
		}
		return fmt.Errorf("can't replace %s: %w", old, err)
	}
	if fi.IsDir() {
		os.RemoveAll(bak)
	}
	return nil
}

// CleanOld removes what an earlier Replace left at path.old.
func CleanOld(path string) { os.RemoveAll(path + ".old") }
