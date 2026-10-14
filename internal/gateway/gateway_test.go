package gateway

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
)

func init() { RetryDelay = 0 }

func TestParseSSHG(t *testing.T) {
	m := ParseSSHG([]byte("user me\nhostname gw.example.org\nControlMaster true\ncontrolpath /s/sock\nlocalforward a\nlocalforward b\n"))
	if m["controlmaster"] != "true" || m["controlpath"] != "/s/sock" || m["localforward"] != "a" {
		t.Fatalf("got %v", m)
	}
}

func TestFromSSHGModes(t *testing.T) {
	g := FromSSHG("hpc-alias", "/st", map[string]string{"controlmaster": "true", "controlpath": "/home/.ssh/s/sock"})
	if g.Mode != ModeSSHConfig || g.SocketPath != "/home/.ssh/s/sock" {
		t.Fatalf("ssh-config: %+v", g)
	}
	if !slices.Equal(g.GW.ClientOpts, []string{"-o", "ControlMaster=no"}) || g.GW.MasterOpts != nil {
		t.Fatalf("ssh-config opts: %+v", g.GW)
	}

	for _, sshG := range []map[string]string{
		{"controlmaster": "false"},
		{"controlmaster": "auto"},                        // no controlpath
		{"controlmaster": "auto", "controlpath": "none"}, // disabled
	} {
		g := FromSSHG("gw.example.org", "/st", sshG)
		sock := ManagedSocketPath("/st", "gw.example.org")
		if g.Mode != ModeManaged || g.SocketPath != sock {
			t.Fatalf("%v: %+v", sshG, g)
		}
		wantClient := []string{"-o", "ControlMaster=no", "-o", "ControlPath=" + sock}
		if !slices.Equal(g.GW.ClientOpts, wantClient) {
			t.Fatalf("client opts %q", g.GW.ClientOpts)
		}
		if !slices.Contains(g.GW.MasterOpts, "ControlPersist=yes") || !slices.Contains(g.GW.MasterOpts, "ServerAliveInterval=60") {
			t.Fatalf("master opts %q", g.GW.MasterOpts)
		}
	}
}

func TestHints(t *testing.T) {
	cases := []struct {
		name string
		sshG map[string]string
		want []string // substrings, one per expected hint
	}{
		{"good", map[string]string{"controlmaster": "auto", "controlpath": "/s", "serveraliveinterval": "60", "serveralivecountmax": "10"}, nil},
		{"yes", map[string]string{"controlmaster": "true", "controlpath": "/s", "serveraliveinterval": "60", "serveralivecountmax": "10"}, []string{"ControlMaster auto"}},
		{"no keepalive", map[string]string{"controlmaster": "auto", "controlpath": "/s", "serveraliveinterval": "0", "serveralivecountmax": "3"}, []string{"ServerAliveInterval"}},
		{"long window", map[string]string{"controlmaster": "true", "controlpath": "/s", "serveraliveinterval": "60", "serveralivecountmax": "120"}, []string{"ControlMaster auto", "2h0m0s"}},
		{"managed mode never hints", map[string]string{"controlmaster": "false", "serveraliveinterval": "0"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FromSSHG("gw", "/st", c.sshG).Hints
			if len(got) != len(c.want) {
				t.Fatalf("got %q, want %d hints", got, len(c.want))
			}
			for i, sub := range c.want {
				if !strings.Contains(got[i], sub) {
					t.Errorf("hint %d = %q, want it to contain %q", i, got[i], sub)
				}
			}
		})
	}
}

func TestManagedSocketPathQuotedWhenItHasSpaces(t *testing.T) {
	g := FromSSHG("gw", "/Users/A B/state", map[string]string{"controlmaster": "false"})
	want := `ControlPath="` + ManagedSocketPath("/Users/A B/state", "gw") + `"`
	if !slices.Contains(g.GW.ClientOpts, want) {
		t.Fatalf("client opts %q, want %q", g.GW.ClientOpts, want)
	}
}

func TestControlPathOpt(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/st/cm-abc", "ControlPath=/st/cm-abc"},
		{"/Users/A B/state/cm-x", `ControlPath="/Users/A B/state/cm-x"`},
		{"/tmp/50%/cm", "ControlPath=/tmp/50%%/cm"},
		{`/tmp/q"uote`, `ControlPath="/tmp/q\"uote"`},
		{`/tmp/back\slash`, `ControlPath="/tmp/back\\slash"`},
		{"/tmp/it's", `ControlPath="/tmp/it's"`},
	}
	for _, c := range cases {
		if got := controlPathOpt(c.path); got != c.want {
			t.Errorf("controlPathOpt(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// TestControlPathOptRoundTripsThroughSSH pins the escaping against the real
// ssh: -G prints the option back after splitting and percent-expanding it, so
// every path must come out exactly as it went in. -G makes no network
// connection; -F /dev/null ignores the user's config.
func TestControlPathOptRoundTripsThroughSSH(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh not installed")
	}
	for _, p := range []string{
		"/tmp/plain/cm", "/tmp/sp ace/cm", "/tmp/50%/cm-%h",
		`/tmp/q"uote/cm`, `/tmp/back\slash/cm`, "/tmp/it's/cm",
	} {
		out, err := exec.Command("ssh", "-F", "/dev/null", "-G",
			"-o", "ControlMaster=auto", "-o", controlPathOpt(p), "host.example").Output()
		if err != nil {
			t.Fatalf("ssh -G with %q: %v", p, err)
		}
		got := ""
		for line := range strings.SplitSeq(string(out), "\n") {
			if v, ok := strings.CutPrefix(line, "controlpath "); ok {
				got = v
				break
			}
		}
		if got != p {
			t.Errorf("ssh round trip: %q came back as %q", p, got)
		}
	}
}

func TestManagedSocketPathIsStable(t *testing.T) {
	a, b := ManagedSocketPath("/st", "gw"), ManagedSocketPath("/st", "gw")
	if a != b || !strings.HasPrefix(a, "/st/cm-") || len(filepath.Base(a)) != len("cm-")+16 {
		t.Fatalf("got %q %q", a, b)
	}
	if ManagedSocketPath("/st", "other") == a {
		t.Fatal("different gateways share a socket path")
	}
}

func TestAtSocket(t *testing.T) {
	g := FromSSHG("gw", "/st", map[string]string{"controlmaster": "false"})
	a := g.AtSocket("/home/u/.ssh/cm gw")
	if a.SocketPath != "/home/u/.ssh/cm gw" || a.GW.Dest != "gw" {
		t.Fatalf("copy: %+v", a)
	}
	want := []string{"-o", "ControlMaster=no", "-o", `ControlPath="/home/u/.ssh/cm gw"`}
	if !slices.Equal(a.GW.ClientOpts, want) {
		t.Fatalf("client opts %q, want %q", a.GW.ClientOpts, want)
	}
	if a.GW.MasterOpts != nil {
		t.Fatalf("copy could start a master: %q", a.GW.MasterOpts)
	}
	sock := ManagedSocketPath("/st", "gw")
	if g.SocketPath != sock || !slices.Equal(g.GW.ClientOpts, []string{"-o", "ControlMaster=no", "-o", "ControlPath=" + sock}) {
		t.Fatalf("original changed: %+v", g)
	}
}

func sshG(out string) func(sshctltest.Call) (sshctl.Result, error) {
	return func(c sshctltest.Call) (sshctl.Result, error) { return sshctl.Result{Stdout: []byte(out)}, nil }
}

func TestResolveRejectsLongSocketPath(t *testing.T) {
	f := &sshctltest.Fake{Handler: sshG("controlmaster false\n")}
	long := "/" + strings.Repeat("d", 100)
	_, err := Resolve(context.Background(), f, "gw", long)
	if !errors.Is(err, ErrSocketPathTooLong) || !strings.Contains(err.Error(), "XDG_STATE_HOME") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveReportsSSHGFailure(t *testing.T) {
	f := &sshctltest.Fake{Handler: func(sshctltest.Call) (sshctl.Result, error) {
		return sshctl.Result{ExitCode: 255, Stderr: []byte("bad option\n")}, nil
	}}
	if _, err := Resolve(context.Background(), f, "gw", "/st"); err == nil || !strings.Contains(err.Error(), "bad option") {
		t.Fatalf("err = %v", err)
	}
}

func checkResult(stderr string, code int) func(sshctltest.Call) (sshctl.Result, error) {
	return func(sshctltest.Call) (sshctl.Result, error) {
		return sshctl.Result{Stderr: []byte(stderr), ExitCode: code}, nil
	}
}

func TestCheck(t *testing.T) {
	g := FromSSHG("gw", "/st", map[string]string{"controlmaster": "false"})
	cases := []struct {
		name    string
		stderr  string
		code    int
		want    Status
		wantErr bool
	}{
		{"running", "Master running (pid=4242)\r\n", 0, Status{Running: true, PID: 4242}, false},
		{"no socket", "Control socket connect(/st/cm-x): No such file or directory\n", 255, Status{}, false},
		{"stale", "Control socket connect(/st/cm-x): Connection refused\n", 255, Status{Stale: true}, false},
		{"other failure", "something else\n", 255, Status{}, true},
		{"zero exit without pid", "banner text\n", 0, Status{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &sshctltest.Fake{Handler: checkResult(c.stderr, c.code)}
			got, err := g.Check(context.Background(), f)
			if (err != nil) != c.wantErr || got != c.want {
				t.Fatalf("got %+v, %v", got, err)
			}
			if !slices.Contains(f.Calls[0].Args, "ControlMaster=no") {
				t.Fatalf("check without ControlMaster=no: %q", f.Calls[0].Args)
			}
		})
	}
}

// master is a fake gateway: -O check reports running once -fN has been called.
type master struct {
	running  bool
	startErr string
}

func (m *master) handle(c sshctltest.Call) (sshctl.Result, error) {
	switch {
	case slices.Contains(c.Args, "check"):
		if m.running {
			return sshctl.Result{Stderr: []byte("Master running (pid=77)\n")}, nil
		}
		return sshctl.Result{ExitCode: 255, Stderr: []byte("Control socket connect(x): No such file or directory\n")}, nil
	case slices.Contains(c.Args, "-fN"):
		if m.startErr != "" {
			return sshctl.Result{ExitCode: 255, Stderr: []byte(m.startErr)}, nil
		}
		m.running = true
		return sshctl.Result{}, nil
	}
	return sshctl.Result{}, nil
}

func TestEnsureStartedAlreadyRunning(t *testing.T) {
	g := FromSSHG("gw", "/st", map[string]string{"controlmaster": "false"})
	m := &master{running: true}
	f := &sshctltest.Fake{Handler: m.handle}
	st, err := g.EnsureStarted(context.Background(), f, "/tmp/log")
	if err != nil || st.PID != 77 {
		t.Fatalf("got %+v, %v", st, err)
	}
	if len(f.Find("-fN")) != 0 {
		t.Fatal("started a master that was already running")
	}
}

func TestEnsureStartedStartsInBackground(t *testing.T) {
	g := FromSSHG("gw", "/st", map[string]string{"controlmaster": "false"})
	m := &master{}
	f := &sshctltest.Fake{Handler: m.handle}
	st, err := g.EnsureStarted(context.Background(), f, "/tmp/master.log")
	if err != nil || !st.Running || st.PID != 77 {
		t.Fatalf("got %+v, %v", st, err)
	}
	starts := f.Find("-fN")
	if len(starts) != 1 || !starts[0].Background || starts[0].LogPath != "/tmp/master.log" {
		t.Fatalf("start calls: %+v", starts)
	}
}

func TestEnsureStartedReportsLog(t *testing.T) {
	g := FromSSHG("gw", "/st", map[string]string{"controlmaster": "false"})
	f := &sshctltest.Fake{Handler: (&master{startErr: "Permission denied (publickey).\n"}).handle}
	_, err := g.EnsureStarted(context.Background(), f, "/tmp/log")
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureStartedRemovesStaleSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "tun-gw")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close() // leaves a socket file nobody listens on

	g := FromSSHG("gw", dir, map[string]string{"controlmaster": "true", "controlpath": sock})
	started := false
	f := &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		switch {
		case slices.Contains(c.Args, "check") && !started:
			return sshctl.Result{ExitCode: 255, Stderr: []byte("Control socket connect(" + sock + "): Connection refused\n")}, nil
		case slices.Contains(c.Args, "check"):
			return sshctl.Result{Stderr: []byte("Master running (pid=9)\n")}, nil
		case slices.Contains(c.Args, "-fN"):
			if _, err := os.Lstat(sock); err == nil {
				t.Fatal("master started while the stale socket still exists")
			}
			started = true
		}
		return sshctl.Result{}, nil
	}}
	if _, err := g.EnsureStarted(context.Background(), f, filepath.Join(dir, "log")); err != nil {
		t.Fatal(err)
	}
}

// TestEnsureStartedNamesMissingSocketDir covers a deleted ControlPath
// directory: ssh logs in but cannot bind the control socket, so no master
// ever listens. The error names the directory rather than only the socket.
func TestEnsureStartedNamesMissingSocketDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockets")
	g := FromSSHG("gw", "/st", map[string]string{"controlmaster": "auto", "controlpath": filepath.Join(dir, "abc")})
	f := &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		if slices.Contains(c.Args, "check") {
			return sshctl.Result{ExitCode: 255, Stderr: []byte("Control socket connect(x): No such file or directory\n")}, nil
		}
		return sshctl.Result{}, nil
	}}
	_, err := g.EnsureStarted(context.Background(), f, "/tmp/log")
	if err == nil || !strings.Contains(err.Error(), dir+" does not exist") {
		t.Fatalf("err = %v", err)
	}
}
