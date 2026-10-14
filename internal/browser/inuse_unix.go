//go:build !windows

package browser

// FileInUse is only needed on Windows (see Env.InUse).
func FileInUse(string) bool { return false }
