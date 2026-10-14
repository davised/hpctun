//go:build !windows

package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ownerOf returns the uid that owns fi. It is a variable so tests can present
// a file as belonging to another user without running as root. On the rare
// platform where the uid cannot be read it reports the current user's, which
// skips the ownership test rather than refusing every path.
var ownerOf = func(fi fs.FileInfo) uint32 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Uid
	}
	return uint32(os.Getuid())
}

// maxLinks caps how many symbolic links one checked path may go through. It is
// the usual kernel limit, and it turns a link loop into an error the user can
// act on instead of a walk that never ends.
const maxLinks = 40

// checkPrivate refuses a state directory another user owns or can write to,
// and one that sits inside, or is reached through, a directory or symbolic
// link another user could use to replace it. hpctun keeps its control sockets
// and askpass.sh there, so whoever could replace them could take over the
// user's connections or read the passwords typed into the dialog.
//
// dir is made absolute and then resolved one component at a time from "/",
// following links exactly as the kernel would when a caller opens the path.
// Every link met must belong to the current user or to root (whoever owns a
// link can point it elsewhere at any time), every directory above the last
// must be one no other user can write to or replace, and the directory the
// path ends at must be the user's own and not writable by others. Resolving
// the path first is what catches a link that leads through storage nobody
// checked, and an absolute path is what brings a relative XDG_STATE_HOME's
// working directory and its parents into the walk.
//
// A component that does not exist yet ends the walk: only the nearest existing
// ancestor can be inspected, and EnsureDir creates the rest under it.
func checkPrivate(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	return checkAbsolute(dir, abs)
}

// checkAbsolute walks abs, the absolute form of dir, from "/" to its end,
// resolving one component at a time as described on checkPrivate.
func checkAbsolute(dir, abs string) error {
	root := string(filepath.Separator)
	parts := pathComponents(abs)
	// "/" is on the way to every path, so it is checked like any other
	// directory on the way: root owns it on every ordinary system, but nothing
	// guarantees that inside a container.
	if len(parts) > 0 {
		fi, err := os.Lstat(root)
		if err != nil {
			return err
		}
		if err := ancestorSafe(dir, root, fi); err != nil {
			return err
		}
	}
	resolved := root
	links := 0
	for len(parts) > 0 {
		name := parts[0]
		rest := parts[1:]
		p := filepath.Join(resolved, name)
		fi, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// The rest of the path is not there yet; the directories walked so
			// far are the whole story.
			return nil
		case err != nil:
			return err
		case fi.Mode()&fs.ModeSymlink != 0:
			links++
			if links > maxLinks {
				return fmt.Errorf("state directory %s goes through too many links (more than %d symbolic links)", dir, maxLinks)
			}
			if err := linkSafe(dir, p, fi); err != nil {
				return err
			}
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			// An absolute target restarts at "/"; a relative one is resolved
			// against the link's own directory, which resolved still holds.
			if filepath.IsAbs(target) {
				resolved = string(filepath.Separator)
			}
			parts = append(pathComponents(target), rest...)
			continue
		}
		if len(rest) == 0 {
			return privateDir(dir, fi)
		}
		if !fi.IsDir() {
			return fmt.Errorf("state directory %s cannot be used: %s is not a directory", dir, p)
		}
		if err := ancestorSafe(dir, p, fi); err != nil {
			return err
		}
		resolved = p
		parts = rest
	}
	// The path ran out of components without a last one to inspect, which
	// happens when a link points at "/" itself; check what it resolved to.
	fi, err := os.Lstat(resolved)
	if err != nil {
		return err
	}
	return privateDir(dir, fi)
}

// pathComponents splits an absolute path into the names between its
// separators, dropping the empty names and the "." parts that name no step of
// their own.
func pathComponents(p string) []string {
	var out []string
	for _, part := range strings.Split(p, string(filepath.Separator)) {
		if part != "" && part != "." {
			out = append(out, part)
		}
	}
	return out
}

// privateDir refuses the directory the state path ends at when another user
// owns it or can write to it: it holds the control sockets and the askpass
// helper hpctun writes and runs.
func privateDir(dir string, fi fs.FileInfo) error {
	if !fi.IsDir() {
		return fmt.Errorf("state directory %s is not a directory", dir)
	}
	if uid := ownerOf(fi); uid != uint32(os.Getuid()) {
		return fmt.Errorf("state directory %s belongs to another user (uid %d); hpctun keeps its control sockets and askpass helper there, so it must be yours: set XDG_STATE_HOME to a private directory in your home, such as ~/.hst", dir, uid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("state directory %s can be changed by other users (mode %04o); hpctun keeps its control sockets and askpass helper there, so it must be yours alone: run `chmod 700 %s`, or set XDG_STATE_HOME to a private directory in your home", dir, fi.Mode().Perm(), dir)
	}
	return nil
}

// linkSafe refuses one symbolic link met on the way to the state directory
// when it belongs to neither the current user nor root: whoever owns the link
// can point it somewhere else after hpctun has approved the path.
func linkSafe(dir, link string, fi fs.FileInfo) error {
	uid := ownerOf(fi)
	if uid == uint32(os.Getuid()) || uid == 0 {
		return nil
	}
	return fmt.Errorf("state directory %s goes through the symbolic link %s, which belongs to another user (uid %d); that link can be pointed elsewhere at any time, so set XDG_STATE_HOME to a private directory in your home, such as ~/.hst", dir, link, uid)
}

// ancestorSafe refuses one directory p on the way to the state directory.
func ancestorSafe(dir, p string, fi fs.FileInfo) error {
	if uid := ownerOf(fi); uid != uint32(os.Getuid()) && uid != 0 {
		return fmt.Errorf("state directory %s is inside %s, which belongs to another user (uid %d); hpctun keeps its control sockets and askpass helper there, so it must be yours alone: set XDG_STATE_HOME to a private directory in your home, such as ~/.hst", dir, p, uid)
	}
	if fi.Mode().Perm()&0o022 != 0 && fi.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("state directory %s is inside %s, which can be changed by other users (mode %04o); hpctun keeps its control sockets and askpass helper there, so it must be yours alone: set XDG_STATE_HOME to a private directory in your home, such as ~/.hst", dir, p, fi.Mode().Perm())
	}
	return nil
}
