package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"hpctun/internal/proc"
	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
)

// windowsApp is testApp on Windows: USERPROFILE is the temp home, HOME
// points somewhere else (as Git Bash may set it), APPDATA and USERNAME are
// set, and USER is not. LOCALAPPDATA is where the state directory comes from
// there: XDG_STATE_HOME, which testApp sets, is a Unix convention and is
// ignored on Windows. Like testApp's, its runner fails the test on any
// command.
func windowsApp(t *testing.T, cfg string) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	a, out, errb := testApp(t, cfg)
	profile := a.getenv("HOME")
	env := map[string]string{
		"USERPROFILE":  profile,
		"HOME":         filepath.Join(profile, "elsewhere"),
		"APPDATA":      filepath.Join(profile, "AppData", "Roaming"),
		"LOCALAPPDATA": filepath.Join(profile, "AppData", "Local"),
		"USERNAME":     "winme",
		"USER":         "",
	}
	base := a.getenv
	a.getenv = func(k string) string {
		if v, ok := env[k]; ok {
			return v
		}
		return base(k)
	}
	a.goos = "windows"
	return a, out, errb
}

func TestWindowsHomeIsUserProfile(t *testing.T) {
	a, _, _ := windowsApp(t, "")
	profile := a.getenv("USERPROFILE")
	if got := a.sshPaths().UserConfig; got != filepath.Join(profile, ".ssh", "config") {
		t.Fatalf("UserConfig = %s", got)
	}
	if got := a.tilde(filepath.Join(profile, ".ssh", "config")); got != "~"+string(filepath.Separator)+filepath.Join(".ssh", "config") {
		t.Fatalf("tilde = %s", got)
	}
	if got := a.localUser(); got != "winme" {
		t.Fatalf("localUser = %q", got)
	}
}

// winRunner answers ssh -G like sshG, and ssh-add -l with agentCode.
func winRunner(agentCode int) *sshctltest.Fake {
	g := sshG()
	return &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		if c.Name == "ssh-add" {
			return sshctl.Result{ExitCode: agentCode}, nil
		}
		return g.Handler(c)
	}}
}

func TestWindowsSyncWritesNoControlMaster(t *testing.T) {
	a, out, _ := windowsApp(t, sshCfg)
	a.run = winRunner(2)
	if err := runCmd(a, "ssh-config", "sync", "--yes"); err != nil {
		t.Fatalf("err = %v, out %s", err, out.String())
	}
	p := a.sshPaths()
	b, _ := os.ReadFile(p.TunConfig)
	if strings.Contains(string(b), "    Control") || !strings.Contains(string(b), "AddKeysToAgent yes") {
		t.Fatalf("hpctun_config:\n%s", b)
	}
	if exists(p.Sockets) {
		t.Fatal("created a sockets directory")
	}
	if !strings.Contains(out.String(), "note: the ssh-agent service is not running") {
		t.Fatalf("out:\n%s", out.String())
	}
}

// TestWindowsInit: init asks the gateway and ssh questions, declines the
// sign-in check, defines no tunnels, starts no connection, and ends by
// suggesting ssh and mentioning `hpctun add`.
func TestWindowsInit(t *testing.T) {
	a, out, _ := windowsApp(t, "")
	yes := true
	s := &initScript{t: t, answers: []string{
		"gw.example.org", // typed gateway (no ~/.ssh/config, so no list)
		"n",              // no sign-in test
		"",               // short name: gw
		"",               // transfer: skip
		"node*",          // node patterns
		"",               // set up tunnels: no (the default)
		"",               // username: winme, from USERNAME
		"y",              // apply the ssh changes
		"y",              // write the config
		"n",              // decline the ssh key offer
	}}
	f := winRunner(0)
	a.run, a.tty, a.ask = f, &yes, s
	if err := runCmd(a, "init"); err != nil {
		t.Fatalf("err = %v, out %s", err, out.String())
	}
	s.done()
	for _, c := range f.Calls {
		if slices.Contains(c.Args, "-O") || slices.Contains(c.Args, "-fN") {
			t.Fatalf("unexpected call %s", c.Line())
		}
	}
	p := a.sshPaths()
	b, _ := os.ReadFile(p.TunConfig)
	if !strings.Contains(string(b), "User winme") || strings.Contains(string(b), "ControlMaster auto") {
		t.Fatalf("hpctun_config:\n%s", b)
	}
	if exists(p.Sockets) || !exists(a.configPath) || !strings.Contains(out.String(), "Try `ssh gw`.\n") {
		t.Fatalf("sockets %v config %v out:\n%s", exists(p.Sockets), exists(a.configPath), out.String())
	}
	if !strings.Contains(out.String(), "Add tunnels with `hpctun add`") {
		t.Fatalf("out:\n%s", out.String())
	}
}

// TestWindowsSSHConfigShow: the check runs on Windows (USERPROFILE) and
// reports the missing Include. Bare hpctun is status now, as everywhere.
func TestWindowsSSHConfigShow(t *testing.T) {
	a, out, _ := windowsApp(t, sshCfg)
	a.run = winRunner(0)
	runCmd(a, "ssh-config") // the check reports the missing Include as a problem
	if !strings.Contains(out.String(), "hpctun_config, generated from") || strings.Contains(out.String(), "ControlMaster auto") {
		t.Fatalf("out:\n%s", out.String())
	}
}

// TestWindowsCopyToDrivePath: the copy route knows drive letters on Windows.
func TestWindowsCopyToDrivePath(t *testing.T) {
	a, _, _ := windowsApp(t, sshCfg)
	var got []string
	a.exec = func(argv []string) error { got = argv; return nil }
	a.run = winRunner(0)
	if err := runCmd(a, "cp", "node1:/x", `C:\Users\me\file`); err != nil {
		t.Fatal(err)
	}
	if want := []string{"scp", "-J", "files", "node1:/x", `C:\Users\me\file`}; !slices.Equal(got, want) {
		t.Fatalf("argv %q", got)
	}
}

// withEnv overrides environment variables for a.
func withEnv(a *app, kv ...string) {
	base := a.getenv
	a.getenv = func(k string) string {
		for i := 0; i+1 < len(kv); i += 2 {
			if kv[i] == k {
				return kv[i+1]
			}
		}
		return base(k)
	}
}

// TestNoHomeRefuses: without a home directory, ~/.ssh would resolve against
// the current directory; tun refuses instead of writing there.
func TestNoHomeRefuses(t *testing.T) {
	t.Chdir(t.TempDir()) // where a relative ~/.ssh would land
	a, _, _ := windowsApp(t, sshCfg)
	withEnv(a, "USERPROFILE", "")
	if err := runCmd(a, "ssh-config", "sync", "--yes"); exitCode(err) != 2 || !strings.Contains(err.Error(), "USERPROFILE is not set") {
		t.Fatalf("windows: err = %v", err)
	}
	a, _, _ = testApp(t, sshCfg)
	a.run = winRunner(0)
	withEnv(a, "HOME", "")
	if err := runCmd(a, "ssh-config", "sync", "--yes"); exitCode(err) != 2 || !strings.Contains(err.Error(), "HOME is not set") {
		t.Fatalf("unix: err = %v", err)
	}
	if exists(".ssh") {
		t.Fatal("wrote .ssh into the current directory")
	}
}

// TestWindowsInitFromOSUSite covers onboarding on Windows from the site
// file: the sign-in check is declined and the tunnel question defaults to
// No, User from USERNAME.
func TestWindowsInitFromOSUSite(t *testing.T) {
	a, out, _ := windowsApp(t, "")
	yes := true
	s := &initScript{t: t, answers: []string{
		"",  // gateway hostname (seeded)
		"n", // test signing in without a prompt: no
		"",  // short name: shell
		"",  // transfer: Enter a hostname
		"",  // transfer hostname (seeded)
		"",  // short name: files
		"",  // node patterns: none
		"",  // set up tunnels: no (the default)
		"",  // username: winme, from USERNAME
		"y", // apply the ssh changes
		"y", // write the config
		"n", // decline the ssh key offer
	}}
	a.run, a.tty, a.ask = winRunner(0), &yes, s
	if err := runCmd(a, "init", "--from", osuSite); err != nil {
		t.Fatalf("err = %v, out %s", err, out.String())
	}
	s.done()
	b, _ := os.ReadFile(a.sshPaths().TunConfig)
	for _, want := range []string{"Host shell\n", "HostName shell.hpc.oregonstate.edu", "Host files\n", "User winme"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("hpctun_config lacks %q:\n%s", want, b)
		}
	}
}

// TestWindowsInitTunnelsNeedKey: with tunnels set up, init's sign-in check
// fails, so it prints how to fix it instead of offering to open them.
func TestWindowsInitTunnelsNeedKey(t *testing.T) {
	a, out, _ := windowsApp(t, "")
	yes := true
	f := &winTunnels{signIn: 255}
	fake := f.runner(t)
	g := winRunner(0)
	handler := fake.Handler
	fake.Handler = func(c sshctltest.Call) (sshctl.Result, error) {
		if c.Name == "ssh-add" || strings.Contains(c.Line(), " -G ") {
			return g.Handler(c)
		}
		return handler(c)
	}
	s := &initScript{t: t, answers: []string{
		"",  // gateway hostname (seeded)
		"n", // test signing in without a prompt: no
		"",  // short name: shell
		"",  // transfer: Enter a hostname
		"",  // transfer hostname (seeded)
		"",  // short name: files
		"",  // node patterns: none
		"y", // set up tunnels
		"",  // username: winme, from USERNAME
		"y", // apply the ssh changes
		"",  // default host (a seed has none)
		"",  // tunnels: done (the site's are kept)
		"y", // write the config
		"n", // decline the ssh key offer
	}}
	a.run, a.tty, a.ask, a.ident = fake, &yes, s, f.ident
	if err := runCmd(a, "init", "--from", osuSite); err != nil {
		t.Fatalf("err = %v, out %s", err, out.String())
	}
	s.done()
	if !strings.Contains(out.String(), "Tunnels cannot open yet") || !strings.Contains(out.String(), "ssh-add") {
		t.Fatalf("out:\n%s", out)
	}
}

// winTunnels fakes a Windows machine's tunnels for cmd tests: Start opens
// the forward in a new ssh, the probe (lsof in tests, the TCP table on real
// Windows) lists it, and ident knows each ssh's start time.
type winTunnels struct {
	pids      int
	listening map[int]int // port -> pid
	started   map[int]time.Time
	signIn    int // exit code of `ssh -o BatchMode=yes <alias> true`
}

func (f *winTunnels) runner(t *testing.T) *sshctltest.Fake {
	f.listening, f.started = map[int]int{}, map[int]time.Time{}
	fake := &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		switch {
		case c.Name == "lsof" && slices.Contains(c.Args, "-sTCP:LISTEN"):
			var b strings.Builder
			for port, pid := range f.listening {
				fmt.Fprintf(&b, "p%d\ncssh\nf6\nPTCP\nn127.0.0.1:%d\n", pid, port)
			}
			if b.Len() == 0 {
				return sshctl.Result{ExitCode: 1}, nil
			}
			return sshctl.Result{Stdout: []byte(b.String())}, nil
		case c.Name == "lsof":
			return sshctl.Result{ExitCode: 1}, nil
		case c.Start:
			return sshctl.Result{}, nil
		case c.Name == "ssh" && slices.Contains(c.Args, "BatchMode=yes") && c.Args[len(c.Args)-1] == "true":
			if f.signIn != 0 {
				return sshctl.Result{ExitCode: f.signIn, Stderr: []byte("shell: Permission denied (publickey,keyboard-interactive).\n")}, nil
			}
			return sshctl.Result{}, nil
		}
		t.Errorf("unexpected command on Windows: %s", c.Line())
		return sshctl.Result{ExitCode: 255}, nil
	}}
	fake.OnStart = func(c sshctltest.Call) (int, error) {
		f.pids++
		pid := 1000 + f.pids
		for _, a := range c.Args {
			if m := regexp.MustCompile(`^-[LD](?:localhost:)?(\d+)`).FindStringSubmatch(a); m != nil {
				port, _ := strconv.Atoi(m[1])
				f.listening[port] = pid
			}
		}
		f.started[pid] = time.Unix(1_800_000_000, int64(pid)).UTC()
		return pid, nil
	}
	return fake
}

func (f *winTunnels) ident(pid int) (proc.Info, bool) {
	t, ok := f.started[pid]
	return proc.Info{Image: "ssh", Started: t}, ok
}

const winTunnelCfg = "default_host = \"node1\"\n" + sshCfg + "[tunnel.dash]\nport = 8099\n"

func TestWindowsUpStatusDown(t *testing.T) {
	a, out, errb := windowsApp(t, winTunnelCfg)
	f := &winTunnels{}
	a.run, a.ident = f.runner(t), f.ident
	a.terminate = func(int, time.Time) error { delete(f.listening, 8099); return nil }
	if err := runCmd(a, "up", "dash"); err != nil {
		t.Fatalf("up: %v\n%s%s", err, out, errb)
	}
	starts := a.run.(*sshctltest.Fake).Find("-Llocalhost:8099:node1:8099 -- shell")
	if len(starts) != 1 || !starts[0].Start {
		t.Fatalf("starts %+v", starts)
	}
	out.Reset()
	if err := runCmd(a, "status", "--json"); err != nil {
		t.Fatalf("status: %v\n%s", err, errb)
	}
	doc := mustOneJSON(t, out.Bytes())
	gw := doc["gateway"].(map[string]any)
	if gw["mode"] != "standalone" || gw["running"] != false || gw["pid"] != nil {
		t.Fatalf("gateway %v", gw)
	}
	tr := doc["transfer"].(map[string]any)
	if tr["shared"] != false || tr["mode"] != "standalone" {
		t.Fatalf("transfer %v", tr)
	}
	if rows := doc["tunnels"].([]any); len(rows) != 1 || rows[0].(map[string]any)["state"] != "up" {
		t.Fatalf("tunnels %v", doc["tunnels"])
	}
	out.Reset()
	if err := runCmd(a, "status"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "gateway shell: no shared connection; each tunnel signs in by itself") {
		t.Fatalf("status:\n%s", out)
	}
	out.Reset()
	if err := runCmd(a, "down", "dash"); err != nil {
		t.Fatalf("down: %v\n%s", err, errb)
	}
	if !strings.Contains(out.String(), "closed") {
		t.Fatalf("down:\n%s", out)
	}
}

// Bare hpctun on Windows is status, as elsewhere, and the tunnel commands
// show in help.
func TestWindowsBareIsStatus(t *testing.T) {
	a, out, _ := windowsApp(t, winTunnelCfg)
	f := &winTunnels{}
	a.run, a.ident = f.runner(t), f.ident
	if err := runCmd(a); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "dash") {
		t.Fatalf("bare hpctun:\n%s", out)
	}
	for _, c := range newRootCmd(a).Commands() {
		if c.Hidden && slices.Contains([]string{"up", "down", "status", "browse", "save", "add", "login", "logout"}, c.Name()) {
			t.Errorf("%s is hidden on Windows", c.Name())
		}
	}
}

func TestWindowsLoginChecksSignIn(t *testing.T) {
	a, out, _ := windowsApp(t, winTunnelCfg)
	f := &winTunnels{}
	a.run, a.ident = f.runner(t), f.ident
	if err := runCmd(a, "login"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	want := "shell  signs in without prompting: tunnels will work\nfiles  signs in without prompting\n"
	if out.String() != want {
		t.Fatalf("login:\n%s\nwant\n%s", out, want)
	}
	if calls := a.run.(*sshctltest.Fake).Find("-o BatchMode=yes -o ConnectTimeout=15 -- shell true"); len(calls) != 1 {
		t.Fatalf("calls %+v", a.run.(*sshctltest.Fake).Calls)
	}

	out.Reset()
	f.signIn = 255
	err := runCmd(a, "login", "shell")
	if exitCode(err) != 1 || !strings.Contains(out.String(), "shell  needs a prompt to sign in") ||
		!strings.Contains(out.String(), "Permission denied") || !strings.Contains(out.String(), "ssh-add") {
		t.Fatalf("err %v, out:\n%s", err, out)
	}
}

func TestWindowsLoginNoSSH(t *testing.T) {
	a, out, _ := windowsApp(t, winTunnelCfg)
	a.run = &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		return sshctl.Result{}, fmt.Errorf("exec: \"ssh\": %w", exec.ErrNotFound)
	}}
	err := runCmd(a, "login", "shell")
	if exitCode(err) != 1 || !strings.Contains(out.String(), "OpenSSH Client") {
		t.Fatalf("err %v, out:\n%s", err, out)
	}
}

func TestWindowsLogoutClosesTunnels(t *testing.T) {
	a, out, errb := windowsApp(t, winTunnelCfg)
	f := &winTunnels{}
	a.run, a.ident = f.runner(t), f.ident
	a.terminate = func(pid int, _ time.Time) error {
		for port, p := range f.listening {
			if p == pid {
				delete(f.listening, port)
			}
		}
		return nil
	}
	if err := runCmd(a, "up", "dash"); err != nil {
		t.Fatalf("up: %v\n%s", err, errb)
	}
	out.Reset()
	if err := runCmd(a, "logout"); err != nil {
		t.Fatalf("logout: %v\n%s", err, errb)
	}
	if !strings.Contains(out.String(), "files  no shared connection to close") || !strings.Contains(out.String(), "shell  closed 1 tunnel") {
		t.Fatalf("logout:\n%s", out)
	}
	if len(f.listening) != 0 {
		t.Fatalf("still listening: %v", f.listening)
	}
	out.Reset()
	if err := runCmd(a, "logout", "shell"); err != nil || out.String() != "shell  no tunnels open\n" {
		t.Fatalf("second logout: %v\n%s", err, out)
	}
}
