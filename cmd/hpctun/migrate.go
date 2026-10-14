package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"hpctun/internal/config"
	"hpctun/internal/legacy"
	"hpctun/internal/state"
)

// migrate moves the config and state directories from their names before
// the rename, telling the user on stderr. With ssh, a move also mentions an
// old ~/.ssh/tun_config, which only `hpctun ssh-config sync` migrates; that
// hint is itself skipped when a.migrateSkipHint says the command running is
// that very sync (root's PersistentPreRunE sets it). Errors are warnings:
// the command goes on with whatever the new names hold.
//
// --config and $HPCTUN_CONFIG/$TUN_CONFIG bypass the config directory
// lookup (config.Locate), so they bypass its migration too: the config pair
// is skipped when a.configFlagChanged (set by root's PersistentPreRunE from
// the --config flag's Changed, since a.configPath itself cannot tell a
// user-set value from testApp's own default in tests) or either env var is
// set.
func (a *app) migrate(ssh bool) {
	configSkipped := a.configFlagChanged || a.getenv("HPCTUN_CONFIG") != "" || a.getenv("TUN_CONFIG") != ""
	pairs := []struct {
		dir, legacy func(func(string) string, string) (string, error)
		skip        bool
		after       func(old, nu string) error
	}{
		{dir: config.Dir, legacy: config.LegacyDir, skip: configSkipped},
		{dir: state.Dir, legacy: state.LegacyDir, after: rewriteStateControlPaths},
	}
	moved := false
	for _, p := range pairs {
		if p.skip {
			continue
		}
		nu, err1 := p.dir(a.getenv, a.goos)
		old, err2 := p.legacy(a.getenv, a.goos)
		if err1 != nil || err2 != nil {
			continue
		}
		ok, err := legacy.MoveDir(old, nu)
		switch {
		case err != nil:
			fmt.Fprintf(a.stderr, "warning: could not move %s to %s: %v\n", a.tilde(old), a.tilde(nu), err)
		case ok:
			moved = true
			fmt.Fprintf(a.stderr, "moved %s to %s (tun is now hpctun)\n", a.tilde(old), a.tilde(nu))
			if p.after != nil {
				if err := p.after(old, nu); err != nil {
					fmt.Fprintf(a.stderr, "warning: could not update %s for the new location: %v\n", a.tilde(filepath.Join(nu, "state.json")), err)
				}
			}
		}
	}
	if moved && ssh && !a.migrateSkipHint {
		if _, err := os.Lstat(a.sshPaths().LegacyTunConfig); err == nil {
			fmt.Fprintln(a.stderr, "~/.ssh/tun_config is from before the rename; run `hpctun ssh-config sync`")
		}
	}
}

// rewriteStateControlPaths updates every state.json entry's ControlPath
// that names a managed-mode socket (internal/gateway ManagedSocketPath)
// under the state directory that just moved from old to nu, so tunnels
// opened before the move stay manageable: otherwise state.json still names
// the old directory although the socket now lives under the new one, and
// internal/tunnel/view.go Classify reports a conflict. Paths outside old
// (e.g. ~/.ssh/sockets/..., an ssh-config-managed gateway's own control
// path) are left alone. It uses the state package's own locking and atomic
// save, the same as any other writer of state.json.
func rewriteStateControlPaths(old, nu string) error {
	store, err := state.Open(context.Background(), nu, nil)
	if err != nil {
		return err
	}
	defer store.Close()
	st, err := store.Load()
	if err != nil {
		return err
	}
	prefix := old + string(filepath.Separator)
	changed := false
	for i, e := range st.Tunnels {
		if rel, ok := strings.CutPrefix(e.ControlPath, prefix); ok {
			st.Tunnels[i].ControlPath = filepath.Join(nu, rel)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return store.Save(st)
}
