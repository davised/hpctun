package main

import (
	"os"
	"strings"
	"testing"

	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
	"hpctun/internal/state"
)

// masters fakes ssh for aliases whose ssh config has ControlMaster set,
// except those listed in managed.
func masters(managed ...string) (*sshctltest.Fake, map[string]bool) {
	up := map[string]bool{}
	f := &sshctltest.Fake{}
	f.Handler = func(c sshctltest.Call) (sshctl.Result, error) {
		dest := c.Args[len(c.Args)-1]
		line := c.Line()
		switch {
		case strings.Contains(line, " -G "):
			out := "hostname " + dest + ".example.org\n"
			if !slicesContains(managed, dest) {
				out += "controlmaster auto\ncontrolpath /tmp/cm-" + dest + "\n"
			}
			return sshctl.Result{Stdout: []byte(out)}, nil
		case strings.Contains(line, "-O check"):
			if up[dest] {
				return sshctl.Result{Stderr: []byte("Master running (pid=42)\n")}, nil
			}
			return sshctl.Result{ExitCode: 255, Stderr: []byte("Control socket connect(/tmp/cm): No such file or directory\n")}, nil
		case strings.Contains(line, "-O exit"):
			up[dest] = false
			return sshctl.Result{Stderr: []byte("Exit request sent.\n")}, nil
		case c.Background:
			up[dest] = true
		}
		return sshctl.Result{}, nil
	}
	return f, up
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestLoginBothThenAlready(t *testing.T) {
	a, out, _ := testApp(t, sshCfg)
	f, _ := masters()
	a.run = f
	if err := runCmd(a, "login"); err != nil {
		t.Fatal(err)
	}
	want := "shell  logging in...\nshell  connected (pid 42)\nfiles  logging in...\nfiles  connected (pid 42)\n"
	if out.String() != want {
		t.Fatalf("out %q", out.String())
	}
	if bg := f.Find("-fN -- files"); len(bg) != 1 || !strings.HasSuffix(bg[0].LogPath, "login-files.log") {
		t.Fatalf("background calls %+v", bg)
	}
	out.Reset()
	if err := runCmd(a, "login", "files"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "files  already connected (pid 42)\n" {
		t.Fatalf("out %q", out.String())
	}
}

func TestLoginErrors(t *testing.T) {
	a, _, _ := testApp(t, sshCfg)
	a.run, _ = masters()
	if err := runCmd(a, "login", "web"); err == nil || exitCode(err) != 2 || !strings.Contains(err.Error(), "neither the gateway") {
		t.Fatalf("unknown alias: %v", err)
	}
	b, _, _ := testApp(t, "gateway = \"shell\"\n")
	b.run, _ = masters()
	if err := runCmd(b, "login", "files"); err == nil || !strings.Contains(err.Error(), "no transfer host") {
		t.Fatalf("no transfer: %v", err)
	}
	c, _, _ := testApp(t, "gateway = \"shell\"\n[ssh]\ntransfer = \"files\"\n")
	c.run, _ = masters("files")
	if err := runCmd(c, "login", "files"); err == nil || exitCode(err) != 1 || !strings.Contains(err.Error(), "no ControlMaster") {
		t.Fatalf("managed transfer: %v", err)
	}
}

func TestLogoutRefusesGatewayWithTunnels(t *testing.T) {
	a, out, _ := testApp(t, sshCfg)
	f, up := masters()
	a.run = f
	up["shell"], up["files"] = true, true
	dir, _ := a.stateDir()
	os.MkdirAll(dir, 0o700)
	store, err := state.Open(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&state.State{Tunnels: []state.Entry{{Name: "jup", Port: 4001}}}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	err = runCmd(a, "logout")
	if err == nil || !strings.Contains(err.Error(), "tunnels are using shell (jup)") {
		t.Fatalf("err = %v", err)
	}
	if out.String() != "files  logged out\n" || up["files"] || !up["shell"] {
		t.Fatalf("out %q up %v", out.String(), up)
	}
}
