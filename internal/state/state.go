// Package state keeps the small record of what hpctun opened: labels, forward
// specs and jump PIDs that cannot be observed from the live system.
package state

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"hpctun/internal/config"
)

// Entry is one tunnel hpctun opened.
type Entry struct {
	Name       string       `json:"name"` // empty for ad-hoc
	Port       int          `json:"port"`
	Route      config.Route `json:"route"`
	Host       string       `json:"host"`
	RemotePort int          `json:"remote_port"`
	Spec       string       `json:"spec"`    // exact -L/-D argument, needed by ssh -O cancel
	Gateway    string       `json:"gateway"` // gateway it was opened through
	// ControlPath is the control socket of the master the tunnel was opened
	// through. Older state files lack it; empty means "use the gateway as
	// resolved now".
	ControlPath string `json:"control_path,omitempty"`
	PID         int    `json:"pid"` // jump route; every route in standalone mode
	// PIDStarted is when PID started, in standalone mode (Windows), where it
	// tells the tunnel's ssh from a later process given the same pid.
	PIDStarted time.Time `json:"pid_started,omitzero"`
	OpenedAt   time.Time `json:"opened_at"`
}

// State is the whole state file.
type State struct {
	Tunnels []Entry `json:"tunnels"`
}

// ByPort returns the entry for a local port.
func (st *State) ByPort(port int) (Entry, bool) {
	if st == nil {
		return Entry{}, false
	}
	for _, e := range st.Tunnels {
		if e.Port == port {
			return e, true
		}
	}
	return Entry{}, false
}

// Put adds e, replacing any entry on the same port.
func (st *State) Put(e Entry) {
	st.Remove(e.Port)
	st.Tunnels = append(st.Tunnels, e)
}

// Remove deletes the entry for port, if any.
func (st *State) Remove(port int) {
	st.Tunnels = slices.DeleteFunc(st.Tunnels, func(e Entry) bool { return e.Port == port })
}

// Dir is $XDG_STATE_HOME/hpctun, else ~/.local/state/hpctun. On Windows (goos
// "windows") it is always %LOCALAPPDATA%\hpctun: XDG_STATE_HOME is a Unix
// convention, and a directory it names is not necessarily private there,
// while %LOCALAPPDATA% is.
func Dir(getenv func(string) string, goos string) (string, error) {
	return dir(getenv, goos, "hpctun")
}

// LegacyDir is Dir under the name before the rename.
func LegacyDir(getenv func(string) string, goos string) (string, error) {
	return dir(getenv, goos, "tun")
}

// CheckDir refuses an existing state directory that another user owns or
// can write to; a missing one is fine (EnsureDir creates it private).
func CheckDir(dir string) error {
	if err := checkPrivate(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// EnsureDir creates dir (0700) if needed and refuses it, like CheckDir,
// when another user owns it or can write to it.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return checkPrivate(dir)
}

func dir(getenv func(string) string, goos, name string) (string, error) {
	// XDG_STATE_HOME is ignored on Windows: the state directory there must
	// stay under %LOCALAPPDATA%, which is per-user and private.
	if goos == "windows" {
		l := getenv("LOCALAPPDATA")
		if l == "" {
			return "", errors.New("cannot locate state directory: LOCALAPPDATA is not set")
		}
		return filepath.Join(l, name), nil
	}
	if x := getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, name), nil
	}
	home := getenv("HOME")
	if home == "" {
		return "", errors.New("cannot locate state directory: HOME is not set")
	}
	return filepath.Join(home, ".local", "state", name), nil
}

// Read loads the state file without locking, for read-only commands. The
// file is replaced atomically, so a reader never sees a partial write.
func Read(dir string) (*State, error) {
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return &State{}, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Store is the state directory held under an exclusive lock.
type Store struct {
	dir  string
	lock *os.File
}

// lockPoll is how long Open sleeps before retrying a busy lock. Waiting is
// interruptible by ctx, so the sleep has to be a timer, not a blocking flock.
const lockPoll = 50 * time.Millisecond

// Lock creates path's directory (0700) and the file (0600) if needed and
// takes an exclusive lock on it, waiting while another process holds it.
// waiting, if set, is called once when the lock is busy. Close the file to
// release the lock. Lock gives up with ctx.Err() when ctx ends.
func Lock(ctx context.Context, path string, waiting func()) (*os.File, error) {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for waitingDone := false; ; {
		busy, err := tryLock(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		if !busy {
			return f, nil
		}
		if !waitingDone && waiting != nil {
			waiting()
		}
		waitingDone = true
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

// Open creates dir if needed and takes the exclusive lock, waiting for any
// other hpctun command to finish. waiting, if set, is called once when the lock
// is busy. Open gives up with ctx.Err() when ctx ends.
func Open(ctx context.Context, dir string, waiting func()) (*Store, error) {
	f, err := Lock(ctx, filepath.Join(dir, "state.json.lock"), waiting)
	if err != nil {
		return nil, err
	}
	return &Store{dir: dir, lock: f}, nil
}

// Dir is the state directory.
func (s *Store) Dir() string { return s.dir }

// Close releases the lock.
func (s *Store) Close() error { return s.lock.Close() }

// Load reads the state file.
func (s *Store) Load() (*State, error) { return Read(s.dir) }

// Save writes the state file atomically, entries sorted by port.
func (s *Store) Save(st *State) error {
	slices.SortFunc(st.Tunnels, func(a, b Entry) int { return cmp.Compare(a.Port, b.Port) })
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(s.dir, "state.json"))
}
