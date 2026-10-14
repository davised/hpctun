package tray

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"hpctun/internal/jsondoc"
)

func ptr[T any](v T) *T { return &v }

// fakeCLI answers hpctun commands from a handler and records them.
type fakeCLI struct {
	mu      sync.Mutex
	calls   [][]string
	handler func(args []string) (stdout, stderr string, exit int)
}

func (f *fakeCLI) Run(_ context.Context, args ...string) ([]byte, []byte, int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	h := f.handler
	f.mu.Unlock()
	out, errb, exit := h(args)
	return []byte(out), []byte(errb), exit, nil
}

func (f *fakeCLI) called(prefix string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			out = append(out, c)
		}
	}
	return out
}

func doc(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// fangBox is how hpctun prints an error on stderr without a terminal.
const fangBox = "          \n   ERROR  \n          \n  Gateway gw: could not connect (ssh exited 255):   \n  ssh: Could not resolve hostname gw.   \n\n  Try --help for usage.\n"

func TestErrorOf(t *testing.T) {
	cases := []struct {
		stdout, stderr string
		exit           int
		want           string
	}{
		{`{"version":1,"error":{"message":"no tunnel on port 4555","exit":2}}`, fangBox, 2, "no tunnel on port 4555"},
		{"", fangBox, 1, "Gateway gw: could not connect (ssh exited 255): ssh: Could not resolve hostname gw."},
		{"", "", 3, "hpctun exited 3"},
		{`{"version":1}`, "", 0, ""},
	}
	for _, c := range cases {
		err := errorOf([]byte(c.stdout), []byte(c.stderr), c.exit)
		if got := fmtErr(err); got != c.want {
			t.Errorf("errorOf(%q, exit %d) = %q, want %q", c.stdout, c.exit, got, c.want)
		}
	}
}

func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestResultsPartialFailureIsNotAnError(t *testing.T) {
	res := jsondoc.Results{Version: 1, Results: []jsondoc.Result{{Kind: "named", Name: ptr("jup"), Port: 4001, Outcome: "failed", Detail: ptr("ssh exited 255:\nPermission denied")}}}
	cli := &fakeCLI{handler: func([]string) (string, string, int) { return doc(t, res), fangBox, 1 }}
	got, err := Results(context.Background(), cli, "up", "--json", "4001")
	if err != nil || len(got.Results) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if f := failures(got); f != "jup: ssh exited 255: Permission denied" {
		t.Fatalf("failures = %q", f)
	}
}

func TestFindCLI(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "hpctun-tray")
	noPath := func(string) (string, error) { return "", exec.ErrNotFound }
	if _, err := FindCLI(exe, noPath); err == nil {
		t.Fatal("found an hpctun that is not there")
	}
	onPath := func(n string) (string, error) { return "/usr/local/bin/" + n, nil }
	if p, _ := FindCLI(exe, onPath); p != "/usr/local/bin/hpctun" {
		t.Fatalf("PATH: %q", p)
	}
	next := filepath.Join(dir, "hpctun")
	if err := os.WriteFile(next, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if p, _ := FindCLI(exe, onPath); p != next {
		t.Fatalf("next to the tray: %q", p)
	}
}

func status(gwRunning bool, rows ...jsondoc.Row) *jsondoc.Status {
	st := &jsondoc.Status{Version: 1, Config: "/c.toml", Gateway: &jsondoc.Gateway{Dest: "hpc", Mode: ptr("managed"), Running: gwRunning, Hints: []string{}}, Tunnels: rows}
	if gwRunning {
		st.Gateway.PID = ptr(42)
	}
	if st.Tunnels == nil {
		st.Tunnels = []jsondoc.Row{}
	}
	return st
}

var (
	jupUp    = jsondoc.Row{Kind: "named", Name: ptr("jupyter"), Port: 4001, Route: ptr("jump"), Target: ptr("node1:4001"), State: "up", Conns: ptr(0)}
	dashDown = jsondoc.Row{Kind: "named", Name: ptr("dash"), Port: 8099, Route: ptr("master"), Target: ptr("node1:8099"), State: "down"}
	socksUp  = jsondoc.Row{Kind: "named", Name: ptr("proxy"), Port: 5005, Route: ptr("socks"), State: "up", Conns: ptr(1)}
	adhocUp  = jsondoc.Row{Kind: "adhoc", Port: 4003, Route: ptr("master"), Target: ptr("web1:80"), State: "up", Conns: ptr(0)}
	conflict = jsondoc.Row{Kind: "named", Name: ptr("web"), Port: 4002, Route: ptr("master"), Target: ptr("node1:4002"), State: "conflict", Conns: ptr(0), Owner: ptr("pid 5 (python3)")}
	external = jsondoc.Row{Kind: "external", Port: 43723, Target: ptr("ssh pid 500"), State: "external", Conns: ptr(0)}
)

func TestBuild(t *testing.T) {
	cases := []struct {
		name string
		s    state
		icon Icon
		head string
	}{
		{"checking", state{}, IconOff, "hpctun: checking…"},
		{"status failed", state{statusErr: errors.New("config: line 1")}, IconError, "hpctun: config: line 1"},
		{"no gateway", state{status: &jsondoc.Status{Version: 1, Tunnels: []jsondoc.Row{}}}, IconError, "No gateway configured: run hpctun init"},
		{"gateway error", state{status: &jsondoc.Status{Version: 1, Gateway: &jsondoc.Gateway{Dest: "hpc", Error: ptr("ssh -G hpc: exit 255")}}}, IconError, "hpc: ssh -G hpc: exit 255"},
		{"not connected", state{status: status(false, dashDown)}, IconOff, "hpc: not connected"},
		{"all up", state{status: status(true, jupUp, socksUp, adhocUp, external)}, IconOn, "hpc: connected"},
		{"one down", state{status: status(true, jupUp, dashDown)}, IconPartial, "hpc: connected"},
		{"conflict", state{status: status(true, jupUp, conflict)}, IconPartial, "hpc: connected"},
		{"no tunnels", state{status: status(true)}, IconOn, "hpc: connected"},
		{"logging in", state{status: status(false), loginBusy: true}, IconOff, "hpc: not connected …"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := build(c.s)
			if m.Icon != c.icon || m.Header != c.head {
				t.Fatalf("icon %d header %q; want %d %q", m.Icon, m.Header, c.icon, c.head)
			}
		})
	}
}

func standalone(rows ...jsondoc.Row) *jsondoc.Status {
	st := status(false, rows...)
	st.Gateway.Mode = ptr("standalone")
	return st
}

func TestBuildStandalone(t *testing.T) {
	cases := []struct {
		name string
		st   *jsondoc.Status
		icon Icon
		head string
	}{
		{"all up", standalone(jupUp, socksUp), IconOn, "hpc: 2 of 2 tunnels up"},
		{"one down", standalone(jupUp, dashDown), IconPartial, "hpc: 1 of 2 tunnels up"},
		{"none up", standalone(dashDown), IconOff, "hpc: 0 of 1 tunnels up"},
		{"no tunnels", standalone(), IconOff, "hpc: no tunnels"},
		{"external ignored", standalone(jupUp, external), IconOn, "hpc: 1 of 1 tunnels up"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := build(state{status: c.st})
			if m.Icon != c.icon || m.Header != c.head {
				t.Fatalf("icon %d header %q; want %d %q", m.Icon, m.Header, c.icon, c.head)
			}
			if m.CanLogin || m.CanLogout {
				t.Fatalf("login items shown: %+v", m)
			}
		})
	}
}

func TestBuildTunnels(t *testing.T) {
	m := build(state{status: status(true, jupUp, conflict, socksUp, adhocUp, dashDown, external), busy: map[int]bool{8099: true}})
	want := []Item{
		{4001, "jupyter   :4001 → node1:4001", true, false},
		{4002, "web   :4002 → node1:4002 (port held by pid 5 (python3))", false, true},
		{5005, "proxy   :5005 SOCKS proxy", true, false},
		{4003, "(ad-hoc)   :4003 → web1:80", true, false},
		{8099, "dash   :8099 → node1:8099 …", false, true},
	}
	if len(m.Tunnels) != len(want) {
		t.Fatalf("got %+v", m.Tunnels)
	}
	for i := range want {
		if m.Tunnels[i] != want[i] {
			t.Errorf("item %d = %+v\nwant     %+v", i, m.Tunnels[i], want[i])
		}
	}
	if !m.CanOpenAll || m.CanLogin || !m.CanLogout || m.Config != "/c.toml" {
		t.Fatalf("model %+v", m)
	}
}

// The transfer host, and the login gateway when tunnels have their own,
// get a line each under the header, and Log in shows while one is down.
func TestBuildConns(t *testing.T) {
	files := func(connected bool) *jsondoc.Conn {
		c := &jsondoc.Conn{Alias: "files", Mode: ptr("ssh-config"), Shared: true, Connected: connected}
		if connected {
			c.PID = ptr(7)
		}
		return c
	}
	cases := []struct {
		name     string
		gw       bool
		login    *jsondoc.Conn
		transfer *jsondoc.Conn
		lines    []string
		canLogin bool
	}{
		{"none", true, nil, nil, nil, false},
		{"files up", true, nil, files(true), []string{"files: connected"}, false},
		{"files down", true, nil, files(false), []string{"files: not connected"}, true},
		{"gateway down too", false, nil, files(false), []string{"files: not connected"}, true},
		{"not shared", true, nil, &jsondoc.Conn{Alias: "files", Mode: ptr("managed")}, []string{"files: logins not shared (no ControlMaster)"}, false},
		{"error", true, nil, &jsondoc.Conn{Alias: "files", Shared: true, Error: ptr("ssh -G files: exit 255")}, []string{"files: error: ssh -G files: exit 255"}, false},
		{"login gateway", true, &jsondoc.Conn{Alias: "shell", Shared: true, Connected: true, PID: ptr(8)}, files(true), []string{"shell: connected", "files: connected"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := status(c.gw)
			st.Login, st.Transfer = c.login, c.transfer
			m := build(state{status: st})
			if !reflect.DeepEqual(m.Conns, c.lines) || m.CanLogin != c.canLogin {
				t.Fatalf("conns %q canLogin %v; want %q %v", m.Conns, m.CanLogin, c.lines, c.canLogin)
			}
		})
	}
	if m := build(state{status: &jsondoc.Status{Version: 1, Transfer: files(false), Login: files(false)}}); m.Conns != nil || m.CanLogin {
		t.Fatalf("no gateway: %+v", m)
	}
}

// With a separate tunnel gateway, Log in names every connection: login
// with no names opens only the gateway and the transfer host.
func TestAppLoginTunnelsOnly(t *testing.T) {
	var mu sync.Mutex
	st := status(false)
	st.Gateway.TunnelsOnly = true
	st.Login = &jsondoc.Conn{Alias: "shell", Shared: true}
	st.Transfer = &jsondoc.Conn{Alias: "files", Shared: true}
	cli := statusCLI(t, st, &mu)
	view := &recorder{}
	a := &App{CLI: cli, View: view, Poll: time.Hour}
	runApp(t, a)
	eventually(t, "first status", func() bool { return view.last().CanLogin })
	a.Login(context.Background())
	a.Wait()
	got := cli.called("--askpass login")
	if len(got) != 1 || strings.Join(got[0], " ") != "--askpass login hpc shell files" {
		t.Fatalf("login calls %q", got)
	}
}

func TestCodeLine(t *testing.T) {
	cases := map[string]*jsondoc.Code{
		"":                                  nil,
		"Code job: none":                    {State: "none"},
		"Code job: unknown (not connected)": {State: "unknown"},
		"Code job: error: squeue: timeout":  {State: "error", Error: ptr("squeue: timeout")},
		"Code job: running on node7, 1:00:00 left": {State: "running", Job: &jsondoc.Job{ID: "1", State: "RUNNING", Node: "node7", TimeLeft: "1:00:00"}},
		"Code job: pending (Priority)":             {State: "running", Job: &jsondoc.Job{ID: "1", State: "PENDING", Reason: "Priority"}},
	}
	for want, c := range cases {
		if got := codeLine(c); got != want {
			t.Errorf("codeLine(%+v) = %q, want %q", c, got, want)
		}
	}
}

// recorder keeps every model rendered.
type recorder struct {
	mu     sync.Mutex
	models []Model
}

func (r *recorder) Render(m Model) {
	r.mu.Lock()
	r.models = append(r.models, m)
	r.mu.Unlock()
}

func (r *recorder) last() Model {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.models) == 0 {
		return Model{}
	}
	return r.models[len(r.models)-1]
}

// statusCLI answers status with *st, up/down by flipping the port's state,
// and login by marking the gateway running.
func statusCLI(t *testing.T, st *jsondoc.Status, mu *sync.Mutex) *fakeCLI {
	return &fakeCLI{handler: func(args []string) (string, string, int) {
		mu.Lock()
		defer mu.Unlock()
		a := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(a, "status --json"):
			return doc(t, st), "", 0
		case strings.Contains(a, "browse"):
			return "", "", 0
		case strings.Contains(a, "login"):
			st.Gateway.Running, st.Gateway.PID = true, ptr(42)
			return "hpc  connected (pid 42)\n", "", 0
		case strings.Contains(a, "up --json") || strings.Contains(a, "down --json"):
			up := strings.Contains(a, "up --json")
			var res jsondoc.Results
			res.Version = 1
			for i, r := range st.Tunnels {
				if !strings.HasSuffix(a, "--all") && !strings.HasSuffix(a, " "+strconv.Itoa(r.Port)) {
					continue
				}
				st.Tunnels[i].State = map[bool]string{true: "up", false: "down"}[up]
				res.Results = append(res.Results, jsondoc.Result{Kind: r.Kind, Name: r.Name, Port: r.Port, Outcome: map[bool]string{true: "opened", false: "closed"}[up]})
			}
			return doc(t, res), "", 0
		}
		return "", "unexpected", 2
	}}
}

func runApp(t *testing.T, a *App) (context.CancelFunc, chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel, done
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAppToggleAndLogin(t *testing.T) {
	var mu sync.Mutex
	st := status(false, dashDown)
	cli := statusCLI(t, st, &mu)
	view := &recorder{}
	a := &App{CLI: cli, View: view, Poll: time.Hour}
	runApp(t, a)
	eventually(t, "first status", func() bool { return view.last().Header == "hpc: not connected" })
	if !view.last().CanLogin {
		t.Fatal("cannot log in")
	}

	a.Login(context.Background())
	a.Wait()
	eventually(t, "connected", func() bool { return view.last().Header == "hpc: connected" })
	if got := cli.called("--askpass login"); len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("login calls %q", got)
	}

	a.Toggle(context.Background(), 8099)
	a.Wait()
	eventually(t, "dash up", func() bool { m := view.last(); return len(m.Tunnels) == 1 && m.Tunnels[0].Checked })
	if got := cli.called("--askpass up --json 8099"); len(got) != 1 {
		t.Fatalf("up calls %q", cli.calls)
	}
	if view.last().Icon != IconOn {
		t.Fatalf("icon %d", view.last().Icon)
	}

	a.Toggle(context.Background(), 8099)
	a.Wait()
	eventually(t, "dash down", func() bool { return !view.last().Tunnels[0].Checked })
	if got := cli.called("down --json 8099"); len(got) != 1 {
		t.Fatalf("down calls %q", cli.calls)
	}
	// Polls use --no-code; the first also asks about the code job.
	if n := len(cli.called("status --json --no-code")); n == 0 {
		t.Fatal("no --no-code polls")
	}
	if first := cli.calls[0]; strings.Join(first, " ") != "status --json" {
		t.Fatalf("first call %q", first)
	}
}

// A click on a tunnel that is busy, or held by another program, does nothing.
func TestAppIgnoresBusyAndConflict(t *testing.T) {
	var mu sync.Mutex
	st := status(true, dashDown, conflict)
	release := make(chan struct{})
	base := statusCLI(t, st, &mu)
	cli := &fakeCLI{handler: func(args []string) (string, string, int) {
		if args[0] == "--askpass" {
			<-release
		}
		return base.handler(args)
	}}
	view := &recorder{}
	a := &App{CLI: cli, View: view, Poll: time.Hour}
	runApp(t, a)
	eventually(t, "first status", func() bool { return len(view.last().Tunnels) == 2 })

	a.Toggle(context.Background(), 8099)
	eventually(t, "busy", func() bool { return view.last().Tunnels[0].Disabled })
	a.Toggle(context.Background(), 8099)
	a.Toggle(context.Background(), 4002)
	a.OpenAll(context.Background())
	close(release)
	a.Wait()
	if got := cli.called("--askpass"); len(got) != 1 {
		t.Fatalf("ran %q", got)
	}
}

func TestAppLastError(t *testing.T) {
	var mu sync.Mutex
	st := status(true, dashDown)
	base := statusCLI(t, st, &mu)
	cli := &fakeCLI{handler: func(args []string) (string, string, int) {
		if args[0] == "--askpass" {
			res := jsondoc.Results{Version: 1, Results: []jsondoc.Result{{Kind: "named", Name: ptr("dash"), Port: 8099, Outcome: "failed", Detail: ptr("ssh -O forward exited 255: bind: Address already in use")}}}
			return doc(t, res), fangBox, 1
		}
		return base.handler(args)
	}}
	view := &recorder{}
	a := &App{CLI: cli, View: view, Poll: time.Hour}
	runApp(t, a)
	eventually(t, "first status", func() bool { return len(view.last().Tunnels) == 1 })
	a.Toggle(context.Background(), 8099)
	a.Wait()
	eventually(t, "error shown", func() bool {
		return view.last().LastError == "dash: ssh -O forward exited 255: bind: Address already in use"
	})
	a.ClearError()
	if view.last().LastError != "" {
		t.Fatal("not cleared")
	}
}

// An error the view reports (Start at login) stays through later polls
// until it is dismissed.
func TestAppShowErrorSurvivesPolls(t *testing.T) {
	var mu sync.Mutex
	cli := statusCLI(t, status(true, dashDown), &mu)
	view := &recorder{}
	a := &App{CLI: cli, View: view, Poll: time.Hour}
	runApp(t, a)
	eventually(t, "first status", func() bool { return len(view.last().Tunnels) == 1 })
	a.ShowError("Start at login: permission denied")
	n := len(cli.called("status"))
	a.Refresh()
	eventually(t, "another poll", func() bool { return len(cli.called("status")) > n })
	// Polls do not overlap, so once a third has started the second has
	// rendered.
	a.Refresh()
	eventually(t, "a third poll", func() bool { return len(cli.called("status")) > n+1 })
	if got := view.last().LastError; got != "Start at login: permission denied" {
		t.Fatalf("LastError %q", got)
	}
	a.ClearError()
	if view.last().LastError != "" {
		t.Fatal("not cleared")
	}
}

// Polls never overlap, however slow status is.
func TestAppPollsDoNotOverlap(t *testing.T) {
	var running, most int
	var mu sync.Mutex
	st := status(true)
	cli := &fakeCLI{handler: func([]string) (string, string, int) {
		mu.Lock()
		running++
		most = max(most, running)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return doc(t, st), "", 0
	}}
	a := &App{CLI: cli, View: &recorder{}, Poll: time.Millisecond}
	runApp(t, a)
	for range 20 {
		a.Refresh()
		time.Sleep(2 * time.Millisecond)
	}
	eventually(t, "several polls", func() bool { return len(cli.called("status")) > 3 })
	mu.Lock()
	defer mu.Unlock()
	if most != 1 {
		t.Fatalf("%d polls ran at once", most)
	}
}

// Losing the connection turns a known code job into "unknown".
func TestAppCodeAfterDisconnect(t *testing.T) {
	var mu sync.Mutex
	st := status(true)
	st.Code = &jsondoc.Code{State: "none"}
	cli := &fakeCLI{handler: func(args []string) (string, string, int) {
		mu.Lock()
		defer mu.Unlock()
		d := *st
		if strings.Contains(strings.Join(args, " "), "--no-code") {
			d.Code = nil
		}
		return doc(t, &d), "", 0
	}}
	view := &recorder{}
	a := &App{CLI: cli, View: view, Poll: time.Hour}
	runApp(t, a)
	eventually(t, "code none", func() bool { return view.last().Code == "Code job: none" })
	a.Refresh()
	eventually(t, "second poll", func() bool { return len(cli.called("status --json --no-code")) >= 1 })
	if view.last().Code != "Code job: none" {
		t.Fatalf("code line lost on a --no-code poll: %q", view.last().Code)
	}
	mu.Lock()
	st.Gateway.Running = false
	mu.Unlock()
	a.Refresh()
	eventually(t, "unknown", func() bool { return view.last().Code == "Code job: unknown (not connected)" })
}

func TestAutostart(t *testing.T) {
	home := t.TempDir()
	for _, goos := range []string{"darwin", "linux"} {
		s := Autostart{GOOS: goos, Home: home, Exe: `/Apps/My "Tools"/$hpctun-tray 100%`}
		if s.Enabled() {
			t.Fatalf("%s: enabled before Set", goos)
		}
		if err := s.Set(true); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(s.Path())
		if err != nil || !s.Enabled() {
			t.Fatalf("%s: %v", goos, err)
		}
		want := map[string]string{
			"darwin": `<string>/Apps/My &#34;Tools&#34;/$hpctun-tray 100%</string>`,
			"linux":  `Exec="/Apps/My \"Tools\"/\$hpctun-tray 100%%"`,
		}[goos]
		if !strings.Contains(string(b), want) {
			t.Fatalf("%s: %s\nwant it to contain %s", goos, b, want)
		}
		if err := s.Set(false); err != nil || s.Enabled() {
			t.Fatalf("%s: still enabled (%v)", goos, err)
		}
		if err := s.Set(false); err != nil {
			t.Fatalf("%s: removing twice: %v", goos, err)
		}
	}
	if p := (Autostart{GOOS: "linux", Home: "/h", ConfigHome: "/x"}).Path(); p != "/x/autostart/hpctun-tray.desktop" {
		t.Fatalf("XDG_CONFIG_HOME: %s", p)
	}
	if p := (Autostart{GOOS: "darwin", Home: "/h"}).Path(); p != "/h/Library/LaunchAgents/edu.oregonstate.hpctun-tray.plist" {
		t.Fatalf("darwin: %s", p)
	}
}

func TestAutostartWindowsValue(t *testing.T) {
	s := Autostart{GOOS: "windows", Exe: `C:\Program Files\hpctun\hpctun-tray.exe`}
	if s.Path() != `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\hpctun-tray` {
		t.Fatalf("path %s", s.Path())
	}
	if s.Content() != `"C:\Program Files\hpctun\hpctun-tray.exe"` {
		t.Fatalf("content %s", s.Content())
	}
	if got := OpenFileCommand("windows", `C:\c.toml`); !slices.Equal(got, []string{"notepad.exe", `C:\c.toml`}) {
		t.Fatalf("open %q", got)
	}
}

func TestICOWrapsPNG(t *testing.T) {
	_, png := Icons(IconOn)
	ico := ICO(png)
	if len(ico) != 22+len(png) || !bytes.Equal(ico[22:], png) {
		t.Fatalf("len %d, png at 22: %v", len(ico), bytes.Equal(ico[22:], png))
	}
	h := binary.LittleEndian
	if h.Uint16(ico[0:]) != 0 || h.Uint16(ico[2:]) != 1 || h.Uint16(ico[4:]) != 1 {
		t.Fatalf("header % x", ico[:6])
	}
	if ico[6] != iconSize || ico[7] != iconSize || h.Uint16(ico[10:]) != 1 || h.Uint16(ico[12:]) != 32 ||
		h.Uint32(ico[14:]) != uint32(len(png)) || h.Uint32(ico[18:]) != 22 {
		t.Fatalf("entry % x", ico[6:22])
	}
}

func TestIconsArePNG(t *testing.T) {
	for _, ic := range []Icon{IconOff, IconPartial, IconOn, IconError} {
		tpl, reg := Icons(ic)
		for _, b := range [][]byte{tpl, reg} {
			if !strings.HasPrefix(string(b), "\x89PNG") {
				t.Fatalf("icon %d is not a PNG", ic)
			}
		}
	}
}

func TestBuildPages(t *testing.T) {
	chat := jsondoc.Row{Kind: "named", Name: ptr("chat"), Port: 1080, Route: ptr("socks"), State: "down", URL: ptr("https://chat.example.org")}
	api := jsondoc.Row{Kind: "named", Name: ptr("llm"), Port: 8090, Route: ptr("master"), Target: ptr("gw:8090"), State: "up", Browse: ptr(false)}
	m := build(state{status: status(true, jupUp, conflict, socksUp, adhocUp, dashDown, external, chat, api), busy: map[int]bool{8099: true}})
	want := []Page{
		{4001, "Open jupyter in browser", false},
		{4002, "Open web in browser", true},
		{4003, "Open :4003 in browser", false},
		{8099, "Open dash in browser", true},
		{1080, "Open chat in browser", false},
	}
	if len(m.Pages) != len(want) {
		t.Fatalf("got %+v", m.Pages)
	}
	for i := range want {
		if m.Pages[i] != want[i] {
			t.Errorf("page %d = %+v, want %+v", i, m.Pages[i], want[i])
		}
	}
}

func TestAppBrowse(t *testing.T) {
	var mu sync.Mutex
	st := status(true, dashDown)
	cli := statusCLI(t, st, &mu)
	view := &recorder{}
	a := &App{CLI: cli, View: view, Poll: time.Hour}
	runApp(t, a)
	eventually(t, "first status", func() bool { return len(view.last().Pages) == 1 })
	a.Browse(context.Background(), 8099)
	a.Wait()
	if got := cli.called("--askpass browse -- dash"); len(got) != 1 {
		t.Fatalf("calls %q", cli.calls)
	}
}
