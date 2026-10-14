package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
	"hpctun/internal/state"
)

// stub writes an executable shell script at path.
func stub(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestStatusCreatesNoStateDir pins that `tun status` is a pure read: with a
// configured gateway it reaches gateway resolution, yet the state directory
// must not exist afterwards. The write flows (up/down) create it themselves
// through state.Store.Open.
func TestStatusCreatesNoStateDir(t *testing.T) {
	home := t.TempDir()
	// lsof finds no matches (its exit 1 means "nothing"); ssh fails, so
	// gateway.Resolve errors out - only after any would-be MkdirAll.
	bin := t.TempDir()
	sshRan := filepath.Join(t.TempDir(), "ssh-was-run")
	stub(t, filepath.Join(bin, "lsof"), "exit 1")
	stub(t, filepath.Join(bin, "ssh"), ": > '"+sshRan+"'\nexit 255")
	t.Setenv("PATH", bin)

	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("gateway = \"gw.example.org\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}
	dir, err := state.Dir(getenv, "darwin")
	if err != nil {
		t.Fatal(err)
	}

	a := newApp()
	a.getenv = getenv
	a.configPath = cfgPath
	a.stdout, a.stderr = io.Discard, io.Discard
	if err := runStatus(context.Background(), a, false, false); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("status created the state directory %s (stat err = %v)", dir, err)
	}
	// Honesty check: the ssh stub must have run, proving status reached
	// gateway resolution instead of aborting earlier.
	if _, err := os.Stat(sshRan); err != nil {
		t.Fatalf("status never resolved the gateway (ssh stub did not run): %v", err)
	}
}

func TestStateDirRefusedWhenOthersCanWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	a, _, _ := testApp(t, "")
	dir, _ := state.Dir(a.getenv, a.goos)
	os.MkdirAll(dir, 0o700)
	os.Chmod(dir, 0o777)
	err := runCmd(a, "status")
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "can be changed by other users") {
		t.Fatalf("err = %v", err)
	}
}

// statusRunner answers lsof with no listeners, ssh -G with a gateway that
// has no ControlMaster (managed mode) and ssh -O check with no master.
func statusRunner() *sshctltest.Fake {
	return &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		switch {
		case c.Name == "lsof":
			return sshctl.Result{ExitCode: 1}, nil
		case strings.Contains(c.Line(), " -G "):
			return sshctl.Result{Stdout: []byte("hostname gw.example.org\n")}, nil
		}
		return sshctl.Result{ExitCode: 255, Stderr: []byte("Control socket connect(x): No such file or directory\n")}, nil
	}}
}

func TestStatusJSONCommand(t *testing.T) {
	a, out, errb := testApp(t, "gateway = \"gw\"\n")
	a.run = statusRunner()
	root := newRootCmd(a)
	root.SetArgs([]string{"status", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("status --json: %v\nstderr: %s", err, errb)
	}
	doc := mustOneJSON(t, out.Bytes())
	if doc["config"] != a.configPath {
		t.Fatalf("config = %v, want %s", doc["config"], a.configPath)
	}
	gw, ok := doc["gateway"].(map[string]any)
	if !ok || gw["dest"] != "gw" || gw["mode"] != "managed" || gw["running"] != false || gw["pid"] != nil {
		t.Fatalf("gateway = %v", doc["gateway"])
	}
	if tunnels, ok := doc["tunnels"].([]any); !ok || len(tunnels) != 0 {
		t.Fatalf("tunnels = %#v, want []", doc["tunnels"])
	}
}

// Without a gateway, status --json says so with null rather than failing.
func TestStatusJSONNoGateway(t *testing.T) {
	a, out, _ := testApp(t, "default_host = \"node1\"\n")
	a.run = statusRunner()
	if err := runStatus(context.Background(), a, true, false); err != nil {
		t.Fatal(err)
	}
	if doc := mustOneJSON(t, out.Bytes()); doc["gateway"] != nil {
		t.Fatalf("gateway = %v, want null", doc["gateway"])
	}
}

// --no-code leaves the code job out (null) instead of asking Slurm.
func TestStatusNoCode(t *testing.T) {
	for _, c := range []struct {
		args []string
		want any
	}{
		{[]string{"status", "--json"}, map[string]any{"state": "unknown", "job": nil, "error": nil}},
		{[]string{"status", "--json", "--no-code"}, nil},
	} {
		a, out, _ := testApp(t, codeCfg)
		a.run = statusRunner()
		if err := runCmd(a, c.args...); err != nil {
			t.Fatal(err)
		}
		if got := mustOneJSON(t, out.Bytes())["code"]; fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%q: code = %v, want %v", c.args, got, c.want)
		}
	}
}
