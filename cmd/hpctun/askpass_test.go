package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"charm.land/fang/v2"

	"hpctun/internal/sshctl"
)

func TestAskpassPrintsAnswer(t *testing.T) {
	a, out, _ := testApp(t, "")
	a.goos = "linux"
	a.lookPath = func(n string) (string, error) {
		if n == "zenity" {
			return "/usr/bin/zenity", nil
		}
		return "", exec.ErrNotFound
	}
	var argv []string
	a.dialog = func(v []string) (string, int, error) { argv = v; return "hunter2\n", 0, nil }
	if err := runCmd(a, "askpass", "(me@shell.example.org) Password: "); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hunter2\n" {
		t.Fatalf("stdout %q", out)
	}
	want := []string{"zenity", "--entry", "--hide-text", "--title", "hpctun: me@shell.example.org", "--text", "(me@shell.example.org) Password:", "--no-markup"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv %q", argv)
	}
}

// A second-factor prompt shows the menu ssh wrote to its log.
func TestAskpassShowsDuoMenu(t *testing.T) {
	a, _, _ := testApp(t, "")
	a.goos = "darwin"
	log := filepath.Join(t.TempDir(), "master.log")
	writeFile(t, log, "Duo two-factor login for me\n\n 1. Duo Push to XXX-XXX-1234\n")
	base := a.getenv
	a.getenv = func(k string) string {
		if k == "HPCTUN_ASKPASS_LOG" {
			return log
		}
		return base(k)
	}
	var argv []string
	a.dialog = func(v []string) (string, int, error) { argv = v; return "1\n", 0, nil }
	if err := runCmd(a, "askpass", "Passcode or option (1-1): "); err != nil {
		t.Fatal(err)
	}
	if text := argv[len(argv)-2]; !strings.Contains(text, "1. Duo Push") || !strings.HasSuffix(text, "Passcode or option (1-1):") {
		t.Fatalf("dialog text %q", text)
	}
}

func TestAskpassCancelled(t *testing.T) {
	a, out, _ := testApp(t, "")
	a.goos = "darwin"
	a.dialog = func([]string) (string, int, error) { return "", 1, nil }
	err := runCmd(a, "askpass", "(me@gw) Password: ")
	if exitCode(err) != 1 || out.Len() != 0 {
		t.Fatalf("err %v, stdout %q", err, out)
	}
	var box bytes.Buffer
	errorHandler(&box, fang.Styles{}, err)
	if box.Len() != 0 {
		t.Fatalf("error box for a cancelled dialog: %q", box.String())
	}
}

// Without zenity or kdialog, askpass says so in one plain line.
func TestAskpassNoDialog(t *testing.T) {
	a, out, errb := testApp(t, "")
	a.goos = "linux"
	a.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	err := runCmd(a, "askpass", "(me@gw) Password: ")
	if exitCode(err) != 1 || out.Len() != 0 || errb.String() != "hpctun askpass: install zenity or kdialog so hpctun can ask for your password in a window\n" {
		t.Fatalf("err %v, stdout %q, stderr %q", err, out, errb)
	}
}

// --askpass (or HPCTUN_ASKPASS=1) gives the real runner the askpass
// environment and writes the wrapper script; without it, nothing changes.
func TestAskpassEnv(t *testing.T) {
	a, _, _ := testApp(t, "")
	a.run, a.goos = nil, "linux"
	if r := a.runner().(sshctl.ExecRunner); r.Env != nil {
		t.Fatalf("Env without --askpass: %q", r.Env)
	}
	a.askpass = true
	env := a.runner().(sshctl.ExecRunner).Env
	if len(env) != 2 || env[1] != "SSH_ASKPASS_REQUIRE=force" || !strings.HasPrefix(env[0], "SSH_ASKPASS=") {
		t.Fatalf("Env %q", env)
	}
	script := strings.TrimPrefix(env[0], "SSH_ASKPASS=")
	b, err := os.ReadFile(script)
	if err != nil || !strings.Contains(string(b), "'/opt/tun/bin/tun' askpass") {
		t.Fatalf("script %q, %v", b, err)
	}
	if fi, _ := os.Stat(script); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", fi.Mode())
	}
	a.goos = "windows"
	if a.askpassOn() {
		t.Fatal("askpass on Windows")
	}
}

// askpass.sh is never written through a link someone planted.
func TestAskpassScriptReplacesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	a, _, _ := testApp(t, "")
	a.run, a.goos, a.askpass = nil, "linux", true
	dir, _ := a.stateDir()
	os.MkdirAll(dir, 0o700)
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("precious"), 0o600)
	os.Symlink(victim, filepath.Join(dir, "askpass.sh"))
	a.runner()
	if b, _ := os.ReadFile(victim); string(b) != "precious" {
		t.Fatalf("wrote through the link: %q", b)
	}
	fi, err := os.Lstat(filepath.Join(dir, "askpass.sh"))
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("askpass.sh %v %v", fi, err)
	}
}

func TestAskpassFlagRegistered(t *testing.T) {
	root := newRootCmd(&app{getenv: func(string) string { return "" }, goos: "linux"})
	if root.PersistentFlags().Lookup("askpass") == nil {
		t.Fatal("no --askpass")
	}
	for _, c := range root.Commands() {
		if c.Name() == "askpass" && !c.Hidden {
			t.Fatal("askpass is listed in help")
		}
	}
}
