package state

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hpctun/internal/config"
)

func TestDir(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if d, _ := Dir(env(map[string]string{"XDG_STATE_HOME": "/x", "HOME": "/h"}), "linux"); d != "/x/hpctun" {
		t.Fatal(d)
	}
	if d, _ := Dir(env(map[string]string{"HOME": "/h"}), "linux"); d != "/h/.local/state/hpctun" {
		t.Fatal(d)
	}
	if _, err := Dir(env(map[string]string{}), "linux"); err == nil {
		t.Fatal("Dir with no HOME succeeded")
	}
	if d, err := LegacyDir(env(map[string]string{"HOME": "/h"}), "linux"); err != nil || d != "/h/.local/state/tun" {
		t.Fatalf("LegacyDir = %q, %v", d, err)
	}
}

func TestDirWindows(t *testing.T) {
	env := map[string]string{"LOCALAPPDATA": `C:\Users\me\AppData\Local`, "HOME": "/ignored"}
	got, err := Dir(func(k string) string { return env[k] }, "windows")
	if err != nil || got != filepath.Join(`C:\Users\me\AppData\Local`, "hpctun") {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := LegacyDir(func(k string) string { return env[k] }, "windows"); err != nil || got != filepath.Join(`C:\Users\me\AppData\Local`, "tun") {
		t.Fatalf("LegacyDir got %q, %v", got, err)
	}
	// XDG_STATE_HOME is a Unix convention, and a directory it names is not
	// necessarily private on Windows; %LOCALAPPDATA% is, so it wins there.
	env["XDG_STATE_HOME"] = "/x"
	if got, _ := Dir(func(k string) string { return env[k] }, "windows"); got != filepath.Join(`C:\Users\me\AppData\Local`, "hpctun") {
		t.Fatalf("XDG_STATE_HOME not ignored on Windows: %q", got)
	}
	if _, err := Dir(func(string) string { return "" }, "windows"); err == nil || !strings.Contains(err.Error(), "LOCALAPPDATA") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureDirPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	dir := filepath.Join(t.TempDir(), "hpctun")
	if err := CheckDir(dir); err != nil {
		t.Fatalf("missing dir: %v", err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", fi.Mode())
	}
	os.Chmod(dir, 0o777)
	if err := EnsureDir(dir); err == nil || !strings.Contains(err.Error(), "can be changed by other users") {
		t.Fatalf("world-writable: %v", err)
	}
	if _, err := Lock(context.Background(), filepath.Join(dir, "x.lock"), nil); err == nil {
		t.Fatal("Lock used a world-writable dir")
	}
	if os.Geteuid() != 0 {
		if err := CheckDir("/usr"); err == nil || !strings.Contains(err.Error(), "belongs to another user") {
			t.Fatalf("root's dir: %v", err)
		}
	}
}

// A private directory another user can rename away and replace is not safe,
// even though it is 0700 and ours: askpass.sh would be written and run from
// storage they control.
func TestEnsureDirRefusesUnsafeParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	// Mkdir is subject to umask, so chmod to make the parent really 0777.
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "hpctun")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{CheckDir(dir), EnsureDir(dir)} {
		if err == nil || !strings.Contains(err.Error(), real) {
			t.Fatalf("unsafe parent = %v", err)
		}
	}
}

// The sticky bit makes a world-writable parent safe: another user cannot
// rename our directory out of it.
func TestEnsureDirStickyParentOK(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(filepath.Join(parent, "hpctun")); err != nil {
		t.Fatal(err)
	}
}

// A state directory under the usual private parents is accepted.
func TestEnsureDirNormalParentOK(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	if err := EnsureDir(filepath.Join(t.TempDir(), "sub", "hpctun")); err != nil {
		t.Fatal(err)
	}
}

// A symbolic link another user owns leads hpctun to whatever directory they
// like, so a path that goes through one is refused even while it currently
// names a private directory of ours: they can point it elsewhere at any time,
// and callers keep using the path as given.
func TestEnsureDirRefusesForeignSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	target := filepath.Join(t.TempDir(), "hpctun")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	// Pretend the link belongs to another user: the test cannot chown it
	// without root, so route the ownership lookup through ownerOf.
	orig := ownerOf
	t.Cleanup(func() { ownerOf = orig })
	ownerOf = func(fi fs.FileInfo) uint32 {
		if fi.Mode()&fs.ModeSymlink != 0 && fi.Name() == "state" {
			return uint32(os.Getuid()) + 1
		}
		return orig(fi)
	}
	for _, err := range []error{CheckDir(link), EnsureDir(link)} {
		if err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("foreign symlink = %v", err)
		}
	}
}

// A link the user owns, reaching a private directory, is accepted: the user
// could re-point it, but only at storage that is already theirs.
func TestEnsureDirOwnSymlinkOK(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	target := filepath.Join(t.TempDir(), "hpctun")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(link); err != nil {
		t.Fatalf("own symlink: %v", err)
	}
	if err := EnsureDir(link); err != nil {
		t.Fatalf("own symlink: %v", err)
	}
}

// A link the user owns is safe only if everything it leads through is safe:
// here a user-owned link points at a link another user owns, and that foreign
// link can be pointed somewhere else at any time, so the private directory at
// the end says nothing about where the path goes tomorrow.
func TestEnsureDirRefusesForeignLinkInChain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	target := filepath.Join(t.TempDir(), "hpctun")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	mid := filepath.Join(t.TempDir(), "mid")
	if err := os.Symlink(target, mid); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(mid, first); err != nil {
		t.Fatal(err)
	}
	// Pretend mid belongs to another user: the test cannot chown it without
	// root, so route the ownership lookup through ownerOf.
	orig := ownerOf
	t.Cleanup(func() { ownerOf = orig })
	ownerOf = func(fi fs.FileInfo) uint32 {
		if fi.Mode()&fs.ModeSymlink != 0 && fi.Name() == "mid" {
			return uint32(os.Getuid()) + 1
		}
		return orig(fi)
	}
	for _, err := range []error{CheckDir(first), EnsureDir(first)} {
		if err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("foreign link in chain = %v", err)
		}
	}
}

// The mirror of TestEnsureDirRefusesForeignLinkInChain: a chain of links the
// user owns, ending at a private directory, is accepted. It stops the chain
// check from being tightened into "no links at all".
func TestEnsureDirOwnLinkChainOK(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	target := filepath.Join(t.TempDir(), "hpctun")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	mid := filepath.Join(t.TempDir(), "mid")
	if err := os.Symlink(target, mid); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(mid, first); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(first); err != nil {
		t.Fatalf("own link chain: %v", err)
	}
	if err := EnsureDir(first); err != nil {
		t.Fatalf("own link chain: %v", err)
	}
}

// A relative XDG_STATE_HOME is resolved against the working directory, and
// that directory's own parents can be replaced by others too. Resolving the
// path first is what lets the walk see them.
func TestEnsureDirRelativePathChecksAncestors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	root := t.TempDir()
	unsafe := filepath.Join(root, "unsafe")
	wd := filepath.Join(unsafe, "wd")
	if err := os.MkdirAll(wd, 0o700); err != nil {
		t.Fatal(err)
	}
	// Mkdir is subject to umask, so chmod to make the parent really 0777.
	if err := os.Chmod(unsafe, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Chdir(wd)
	real, err := filepath.EvalSymlinks(unsafe)
	if err != nil {
		t.Fatal(err)
	}
	// "hpctun" is missing and its working directory is 0700, but one level up
	// is world-writable, so another user could replace what is created.
	if err := EnsureDir("hpctun"); err == nil || !strings.Contains(err.Error(), real) {
		t.Fatalf("relative path under an unsafe ancestor = %v", err)
	}
	if err := CheckDir("hpctun"); err == nil {
		t.Fatal("CheckDir accepted a relative path under an unsafe ancestor")
	}
}

// The default state directory of a real home must still be accepted: the
// checks refuse storage other users control, not the user's own
// ~/.local/state/hpctun. Skipped where that directory does not exist, as on a
// fresh build machine.
func TestEnsureDirRealHomeAccepted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	d := filepath.Join(home, ".local", "state", "hpctun")
	if _, err := os.Lstat(d); err != nil {
		t.Skipf("%s does not exist", d)
	}
	if err := CheckDir(d); err != nil {
		t.Fatalf("CheckDir(%s) = %v", d, err)
	}
}

// A path that goes through a loop of links is refused instead of being walked
// forever.
func TestEnsureDirRefusesLinkLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(a); err == nil || !strings.Contains(err.Error(), "too many links") {
		t.Fatalf("link loop = %v", err)
	}
	// EnsureDir has to create the directory too, which a loop prevents first.
	if err := EnsureDir(a); err == nil {
		t.Fatal("EnsureDir accepted a link loop")
	}
}

func TestLockWaitsForHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "code.lock")
	held, err := Lock(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		di, _ := os.Stat(filepath.Dir(path))
		if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
			t.Fatalf("modes %v %v", fi.Mode().Perm(), di.Mode().Perm())
		}
	}
	var waited atomic.Bool
	done := make(chan error, 1)
	go func() {
		f, err := Lock(context.Background(), path, func() { waited.Store(true) })
		if err == nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("second Lock returned while the first was held: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	held.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !waited.Load() {
		t.Fatal("waiting was not called")
	}
}

func TestReadMissingIsEmpty(t *testing.T) {
	st, err := Read(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(st.Tunnels) != 0 {
		t.Fatalf("got %+v, %v", st, err)
	}
}

func TestPutRemoveByPort(t *testing.T) {
	var st State
	st.Put(Entry{Port: 4002, Host: "a"})
	st.Put(Entry{Port: 4001, Host: "b"})
	st.Put(Entry{Port: 4002, Host: "c"}) // replaces
	if len(st.Tunnels) != 2 {
		t.Fatalf("got %+v", st.Tunnels)
	}
	if e, ok := st.ByPort(4002); !ok || e.Host != "c" {
		t.Fatalf("ByPort(4002) = %+v %v", e, ok)
	}
	st.Remove(4002)
	if _, ok := st.ByPort(4002); ok {
		t.Fatal("Remove did not remove")
	}
	var nilState *State
	if _, ok := nilState.ByPort(1); ok {
		t.Fatal("nil state found an entry")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tun")
	s, err := Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	when := time.Date(2026, 9, 23, 14, 2, 11, 0, time.UTC)
	socks := Entry{Port: 5005, Route: config.RouteSocks, Spec: "-Dlocalhost:5005", Gateway: "gw", OpenedAt: when}
	jup := Entry{Name: "jup", Port: 4001, Route: config.RouteJump, Host: "n1", RemotePort: 4001, Spec: "-L4001:localhost:4001", Gateway: "gw", PID: 77, OpenedAt: when}
	if err := s.Save(&State{Tunnels: []Entry{socks, jup}}); err != nil {
		t.Fatal(err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Tunnels) != 2 || out.Tunnels[0] != jup || out.Tunnels[1] != socks {
		t.Fatalf("round trip (sorted by port) = %+v", out.Tunnels)
	}
	fi, err := os.Stat(filepath.Join(dir, "state.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state.json mode = %v, %v", fi.Mode().Perm(), err)
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v", di.Mode().Perm())
	}
}

func TestOpenWaitsForLock(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var waited atomic.Bool
	done := make(chan *Store)
	go func() {
		s, err := Open(context.Background(), dir, func() { waited.Store(true) })
		if err != nil {
			t.Error(err)
		}
		done <- s
	}()
	select {
	case <-done:
		t.Fatal("second Open did not wait for the lock")
	case <-time.After(200 * time.Millisecond):
	}
	if !waited.Load() {
		t.Fatal("waiting callback not called")
	}
	first.Close()
	select {
	case s := <-done:
		s.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("second Open still blocked after the lock was released")
	}
}

func TestOpenCancelledWhileWaiting(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var waits atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	type attempt struct {
		s   *Store
		err error
	}
	done := make(chan attempt, 1)
	go func() {
		s, err := Open(ctx, dir, func() { waits.Add(1) })
		done <- attempt{s, err}
	}()
	select {
	case a := <-done:
		t.Fatalf("waiting Open returned before cancel: %+v", a)
	case <-time.After(150 * time.Millisecond):
	}
	if n := waits.Load(); n != 1 {
		t.Fatalf("waiting callback called %d times, want exactly 1", n)
	}
	cancel()
	select {
	case a := <-done:
		if !errors.Is(a.err, context.Canceled) || a.s != nil {
			t.Fatalf("cancelled Open = %+v", a)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting Open did not return after cancel")
	}
	first.Close()
	fresh := make(chan *Store, 1)
	go func() {
		s, err := Open(context.Background(), dir, nil)
		if err != nil {
			t.Error(err)
		}
		fresh <- s
	}()
	select {
	case s := <-fresh:
		s.Close()
	case <-time.After(time.Second):
		t.Fatal("Open after cancel blocked: the cancelled attempt kept the lock")
	}
}
