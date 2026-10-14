package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestDownWithoutGateway pins that `tun down` runs with no gateway in the
// config: state entries carry the gateway they were opened through, so down
// never resolves one. The ssh stub marks that it ran, and nothing may run it.
func TestDownWithoutGateway(t *testing.T) {
	home := t.TempDir()
	stateHome := t.TempDir()
	// lsof finds no listeners at all, and ssh would fail if it were called.
	bin := t.TempDir()
	sshRan := filepath.Join(t.TempDir(), "ssh-was-run")
	stub(t, filepath.Join(bin, "lsof"), "exit 1")
	stub(t, filepath.Join(bin, "ssh"), ": > '"+sshRan+"'\nexit 255")
	t.Setenv("PATH", bin)

	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("default_host = \"node1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		switch k {
		case "HOME":
			return home
		case "XDG_STATE_HOME":
			return stateHome
		}
		return ""
	}

	a := newApp()
	a.getenv = getenv
	a.configPath = cfgPath
	a.stdout, a.stderr = io.Discard, io.Discard
	root := newRootCmd(a)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"down", "--all"})
	err := root.ExecuteContext(context.Background())
	if got := exitCode(err); got != 0 {
		t.Fatalf("`tun down --all`: exitCode(%v) = %d, want 0", err, got)
	}
	if _, err := os.Stat(sshRan); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("down reached ssh without a configured gateway (stat err = %v)", err)
	}
}
