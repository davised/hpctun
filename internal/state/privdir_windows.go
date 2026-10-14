package state

import "io/fs"

// checkPrivate does nothing on Windows: the state directory is always
// %LOCALAPPDATA%\hpctun, which Windows keeps private to the user. state.Dir
// ignores XDG_STATE_HOME here, so a shared directory cannot be chosen from the
// environment, and Go's permission bits do not describe who may write there in
// any case.
func checkPrivate(dir string) error { return nil }

// ownerOf exists so the package's tests compile on Windows; with checkPrivate
// a no-op, nothing calls it there.
var ownerOf = func(fs.FileInfo) uint32 { return 0 }
