package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hpctun/internal/state"
)

func TestMigrateDirs(t *testing.T) {
	a, stdout, stderr := testApp(t, "")
	home := a.home()
	old := filepath.Join(home, ".config", "tun")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	a.migrate(false)
	if _, err := os.Stat(filepath.Join(home, ".config", "hpctun", "config.toml")); err != nil {
		t.Fatalf("not moved: %v", err)
	}
	if !strings.Contains(stderr.String(), "moved ~/.config/tun to ~/.config/hpctun (tun is now hpctun)") {
		t.Errorf("stderr: %q", stderr.String())
	}
	if stdout.String() != "" {
		t.Errorf("stdout must stay empty: %q", stdout.String())
	}
}

func TestMigrateSSHHint(t *testing.T) {
	a, _, stderr := testApp(t, "")
	home := a.home()
	os.MkdirAll(filepath.Join(a.getenv("XDG_STATE_HOME"), "tun"), 0o755)
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "tun_config"), []byte("Host x\n"), 0o600)
	a.migrate(true)
	if !strings.Contains(stderr.String(), "run `hpctun ssh-config sync`") {
		t.Errorf("stderr: %q", stderr.String())
	}
}

// TestMigrateRewritesControlPaths covers tunnels left open across the
// upgrade: their state.json entries name a control socket under the old
// state dir, which the move leaves behind at the new one under the same
// suffix. A socket outside the state dir (e.g. ~/.ssh/sockets, from a
// ssh-config-managed gateway) is untouched.
func TestMigrateRewritesControlPaths(t *testing.T) {
	a, _, _ := testApp(t, "")
	old := filepath.Join(a.getenv("XDG_STATE_HOME"), "tun")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(a.home(), ".ssh", "sockets", "cm-xyz")
	st := state.State{Tunnels: []state.Entry{
		{Port: 4001, ControlPath: filepath.Join(old, "cm-abc")},
		{Port: 4002, ControlPath: outside},
	}}
	data, err := json.MarshalIndent(&st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "state.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	a.migrate(false)

	nu := filepath.Join(a.getenv("XDG_STATE_HOME"), "hpctun")
	got, err := state.Read(nu)
	if err != nil {
		t.Fatal(err)
	}
	e1, ok := got.ByPort(4001)
	if want := filepath.Join(nu, "cm-abc"); !ok || e1.ControlPath != want {
		t.Fatalf("port 4001 ControlPath = %q, want %q", e1.ControlPath, want)
	}
	e2, ok := got.ByPort(4002)
	if !ok || e2.ControlPath != outside {
		t.Fatalf("port 4002 ControlPath = %q, want unchanged %q", e2.ControlPath, outside)
	}
}

// TestMigrateSkipsConfigWhenEnvOverrides covers the spec's "--config and the
// environment variables bypass the directory lookup, so they bypass
// migration too": with HPCTUN_CONFIG (or the legacy TUN_CONFIG) set,
// ~/.config/tun is left alone. The state pair still migrates.
func TestMigrateSkipsConfigWhenEnvOverrides(t *testing.T) {
	for _, envVar := range []string{"HPCTUN_CONFIG", "TUN_CONFIG"} {
		t.Run(envVar, func(t *testing.T) {
			a, _, _ := testApp(t, "")
			home := a.home()
			oldConfig := filepath.Join(home, ".config", "tun")
			if err := os.MkdirAll(oldConfig, 0o755); err != nil {
				t.Fatal(err)
			}
			oldState := filepath.Join(a.getenv("XDG_STATE_HOME"), "tun")
			if err := os.MkdirAll(oldState, 0o755); err != nil {
				t.Fatal(err)
			}
			base := a.getenv
			a.getenv = func(k string) string {
				if k == envVar {
					return filepath.Join(home, "my.toml")
				}
				return base(k)
			}

			a.migrate(false)

			if _, err := os.Stat(oldConfig); err != nil {
				t.Fatalf("config dir migrated despite %s: %v", envVar, err)
			}
			if _, err := os.Stat(filepath.Join(home, ".config", "hpctun")); !os.IsNotExist(err) {
				t.Fatalf("new config dir created despite %s: %v", envVar, err)
			}
			// The state pair is unaffected by $*_CONFIG and still migrates.
			if _, err := os.Stat(filepath.Join(a.getenv("XDG_STATE_HOME"), "hpctun")); err != nil {
				t.Fatalf("state dir not migrated: %v", err)
			}
		})
	}
}

// TestMigrateHintSkippedForSSHConfigSync covers running `hpctun ssh-config
// sync` itself: printing "run `hpctun ssh-config sync`" while that command
// is the one already running is confusing, so PersistentPreRunE tells
// migrate to skip the hint for it.
func TestMigrateHintSkippedForSSHConfigSync(t *testing.T) {
	a, _, stderr := testApp(t, sshCfg)
	a.run = sshG()
	home := a.home()
	if err := os.MkdirAll(filepath.Join(home, ".config", "tun"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "tun_config"), []byte("Host old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runCmd(a, "ssh-config", "sync", "--yes"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "run `hpctun ssh-config sync`") {
		t.Fatalf("stderr still hints at the command already running: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "moved ~/.config/tun to ~/.config/hpctun") {
		t.Fatalf("migration itself did not run: %q", stderr.String())
	}
}

// TestMigrateFailedMoveWarnsAndContinues covers a move that cannot complete
// (here, a read-only parent of the new directory): migrate warns naming both
// paths and goes on rather than failing the command.
func TestMigrateFailedMoveWarnsAndContinues(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; directory permissions are not enforced")
	}
	a, stdout, stderr := testApp(t, "")
	home := a.home()
	parent := filepath.Join(home, ".config")
	old := filepath.Join(parent, "tun")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil { // read-only: rename cannot add or remove entries
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) })

	a.migrate(false)

	if !strings.Contains(stderr.String(), "warning: could not move ~/.config/tun to ~/.config/hpctun") {
		t.Errorf("stderr: %q", stderr.String())
	}
	if stdout.String() != "" {
		t.Errorf("stdout must stay empty: %q", stdout.String())
	}
}

func TestCompletionSkipsMigration(t *testing.T) {
	a, _, _ := testApp(t, "")
	old := filepath.Join(a.home(), ".config", "tun")
	os.MkdirAll(old, 0o755)
	root := newRootCmd(a)
	root.SetArgs([]string{"__complete", ""})
	_ = root.Execute()
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("completion migrated: %v", err)
	}
}

// ssh runs hpctun askpass mid-login; moving files then is not its job,
// and its stderr goes to ssh's log, not to the user.
func TestAskpassSkipsMigration(t *testing.T) {
	a, _, _ := testApp(t, "")
	a.goos = "linux"
	a.lookPath = func(string) (string, error) { return "/usr/bin/zenity", nil }
	a.dialog = func([]string) (string, int, error) { return "pw\n", 0, nil }
	old := filepath.Join(a.home(), ".config", "tun")
	os.MkdirAll(old, 0o755)
	if err := runCmd(a, "askpass", "Password: "); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("askpass migrated: %v", err)
	}
}
