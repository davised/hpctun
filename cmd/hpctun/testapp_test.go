package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
)

// testApp is an app whose HOME, config and state live in temp directories.
// The state directory is short so managed-mode socket paths fit. Its runner
// fails the test on any command: the real one would use the developer's own
// ~/.ssh/config and reach real hosts. Tests that expect commands set a.run.
func testApp(t *testing.T, cfg string) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	stateHome, err := os.MkdirTemp("/tmp", "tun-t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(stateHome) })
	cfgPath := filepath.Join(home, "tun.toml")
	if cfg != "" {
		if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{"HOME": home, "XDG_STATE_HOME": stateHome, "USER": "me"}
	var out, errb bytes.Buffer
	no := false
	a := &app{configPath: cfgPath, getenv: func(k string) string { return env[k] }, stdout: &out, stderr: &errb, tty: &no}
	a.executable = func() (string, error) { return "/opt/tun/bin/tun", nil }
	a.exec = func([]string) error { t.Fatal("unexpected exec"); return nil }
	a.interact = func(argv []string, _ io.Reader) error {
		t.Errorf("unexpected interactive command: %q (set a.interact to a fake)", argv)
		return errors.New("no interactive commands in tests")
	}
	a.run = &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		t.Errorf("unexpected command: %s (set a.run to a fake)", c.Line())
		return sshctl.Result{ExitCode: 255}, nil
	}}
	a.start = func(argv []string) error {
		t.Errorf("unexpected browser start: %q (set a.start to a fake)", argv)
		return errors.New("no browsers in tests")
	}
	a.stream = func(_ context.Context, argv []string, _ string, _ io.Writer) (string, int, error) {
		t.Errorf("unexpected remote command: %s (set a.stream to a fake)", strings.Join(argv, " "))
		return "", 255, nil
	}
	a.kill = func(pid int) error {
		t.Errorf("unexpected kill of pid %d (set a.kill)", pid)
		return nil
	}
	a.terminate = func(pid int, _ time.Time) error {
		t.Errorf("unexpected terminate of pid %d (set a.terminate)", pid)
		return nil
	}
	return a, &out, &errb
}

const sshCfg = `gateway = "shell"
[ssh]
user = "me"
gateway_host = "shell.example.org"
transfer = "files"
transfer_host = "files.example.org"
nodes = ["node*"]
`

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestTestAppNeverRunsRealCommands pins testApp's safety: a test that forgets
// to set a.run must not reach the real ssh, which would use the developer's
// own ~/.ssh/config and real hosts.
func TestTestAppNeverRunsRealCommands(t *testing.T) {
	a, _, _ := testApp(t, "")
	if _, real := a.runner().(sshctl.ExecRunner); real {
		t.Fatal("testApp hands out the real runner")
	}
}
