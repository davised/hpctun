package tunnel

import (
	"context"
	"strings"
	"testing"
	"time"

	"hpctun/internal/config"
	"hpctun/internal/gateway"
	"hpctun/internal/probe"
	"hpctun/internal/proc"
	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
	"hpctun/internal/state"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(`
gateway = "gw"
default_host = "node1"
[tunnel.jup]
port = 4001
route = "jump"
[tunnel.dash]
port = 8099
[tunnel.proxy]
socks = 5005
`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func listeners(ls ...probe.Listener) probe.Snapshot {
	s := probe.Snapshot{Listeners: map[int]probe.Listener{}, Conns: map[int]int{}}
	for _, l := range ls {
		s.Listeners[l.Port] = l
	}
	return s
}

func procs(m map[int]string) func(int) (string, bool) {
	return func(pid int) (string, bool) { c, ok := m[pid]; return c, ok }
}

func TestClassify(t *testing.T) {
	cfg := testConfig(t)
	jup, _ := cfg.Lookup("jup")
	dash, _ := cfg.Lookup("dash")
	master := gateway.Status{Running: true, PID: 100}
	jumpEntry := &state.State{Tunnels: []state.Entry{{Name: "jup", Port: 4001, Route: config.RouteJump, Spec: "-L4001:localhost:4001", PID: 200}}}
	cases := []struct {
		name   string
		spec   Spec
		snap   probe.Snapshot
		st     *state.State
		master gateway.Status
		procs  map[int]string
		want   State
		owner  string
	}{
		{"master down", FromConfig(dash), listeners(), &state.State{}, master, nil, StateDown, ""},
		{"master up", FromConfig(dash), listeners(probe.Listener{Port: 8099, PID: 100, Command: "ssh"}), &state.State{}, master, nil, StateUp, ""},
		{"master listener but master not running", FromConfig(dash), listeners(probe.Listener{Port: 8099, PID: 100, Command: "ssh"}), &state.State{}, gateway.Status{}, nil, StateConflict, "pid 100 (ssh)"},
		{"held by python", FromConfig(dash), listeners(probe.Listener{Port: 8099, PID: 9, Command: "python3"}), &state.State{}, master, nil, StateConflict, "pid 9 (python3)"},
		{"jump up", FromConfig(jup), listeners(probe.Listener{Port: 4001, PID: 200, Command: "ssh"}), jumpEntry, master,
			map[int]string{200: "ssh -Nf -o X -L4001:localhost:4001"}, StateUp, ""},
		{"jump pid reused", FromConfig(jup), listeners(probe.Listener{Port: 4001, PID: 200, Command: "ssh"}), jumpEntry, master,
			map[int]string{200: "ssh somewhere-else"}, StateConflict, "pid 200 (ssh)"},
		{"jump hand-started, no entry", FromConfig(jup), listeners(probe.Listener{Port: 4001, PID: 300, Command: "ssh"}), &state.State{}, master,
			map[int]string{300: "ssh -J gw node1 -Nf -L4001:localhost:4001"}, StateConflict, "pid 300 (ssh)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := View{Config: cfg, State: c.st, Snap: c.snap, Master: c.master, Gateway: "gw", ProcCmd: procs(c.procs)}
			got, owner := v.Classify(c.spec)
			if got != c.want || owner != c.owner {
				t.Fatalf("got %s %q, want %s %q", got, owner, c.want, c.owner)
			}
		})
	}
}

// A master-route tunnel whose entry records a control path the gateway no
// longer uses is judged against the master behind that path, not the current
// one.
func TestClassifyRecordedSocket(t *testing.T) {
	cfg := testConfig(t)
	dash, _ := cfg.Lookup("dash")
	snap := listeners(probe.Listener{Port: 8099, PID: 300, Command: "ssh"})
	onOld := state.Entry{Port: 8099, Route: config.RouteMaster, Spec: "-L8099:node1:8099", Gateway: "gw", ControlPath: "/st/cm-old"}
	onCurrent := onOld
	onCurrent.ControlPath = "/u/cm-gw"
	cases := []struct {
		name  string
		st    *state.State
		view  View
		want  State
		owner string
	}{
		{"live on the recorded socket", &state.State{Tunnels: []state.Entry{onOld}},
			View{Master: gateway.Status{Running: true, PID: 100}, Socket: "/u/cm-gw", Sockets: map[string]gateway.Status{"/st/cm-old": {Running: true, PID: 300}}},
			StateUp, ""},
		{"recorded master not running", &state.State{Tunnels: []state.Entry{onOld}},
			View{Master: gateway.Status{Running: true, PID: 100}, Socket: "/u/cm-gw"},
			StateConflict, "pid 300 (ssh)"},
		{"entry on the current socket", &state.State{Tunnels: []state.Entry{onCurrent}},
			View{Master: gateway.Status{Running: true, PID: 300}, Socket: "/u/cm-gw"},
			StateUp, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := c.view
			v.Config, v.State, v.Snap, v.Gateway, v.ProcCmd = cfg, c.st, snap, "gw", procs(nil)
			got, owner := v.Classify(FromConfig(dash))
			if got != c.want || owner != c.owner {
				t.Fatalf("got %s %q, want %s %q", got, owner, c.want, c.owner)
			}
		})
	}
}

func TestRows(t *testing.T) {
	cfg := testConfig(t)
	st := &state.State{Tunnels: []state.Entry{
		{Port: 4002, Route: config.RouteMaster, Host: "node1", RemotePort: 4002, Spec: "-L4002:node1:4002", Gateway: "old-gw"},
	}}
	snap := listeners(
		probe.Listener{Port: 8099, PID: 100, Command: "ssh"},
		probe.Listener{Port: 4002, PID: 100, Command: "ssh"},
		probe.Listener{Port: 43723, PID: 500, Command: "ssh"},
		probe.Listener{Port: 631, PID: 1, Command: "cupsd"},
	)
	snap.Conns[8099] = 2
	v := View{Config: cfg, State: st, Snap: snap, Master: gateway.Status{Running: true, PID: 100}, Gateway: "gw", ProcCmd: procs(nil)}
	rows := v.Rows()
	want := []Row{
		{Kind: KindNamed, Name: "jup", Port: 4001, Route: "jump", Target: "node1:4001", State: StateDown, Conns: -1},
		{Kind: KindAdhoc, Name: "(ad-hoc)", Port: 4002, Route: "master", Target: "node1:4002", State: StateUp, Conns: 0, Warning: "opened through old-gw, not the configured gateway"},
		{Kind: KindNamed, Name: "proxy", Port: 5005, Route: "socks", Target: "-", State: StateDown, Conns: -1},
		{Kind: KindNamed, Name: "dash", Port: 8099, Route: "master", Target: "node1:8099", State: StateUp, Conns: 2},
		{Kind: KindExternal, Name: "-", Port: 43723, Route: "?", Target: "ssh pid 500", State: StateExternal, Conns: 0},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows: %+v", len(rows), rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v\nwant     %+v", i, rows[i], want[i])
		}
	}
}

// A live tunnel on a control socket the gateway no longer uses reads as up
// with a warning naming that socket, not as a conflict.
func TestRowsWarnRecordedSocket(t *testing.T) {
	cfg := testConfig(t)
	st := &state.State{Tunnels: []state.Entry{{Port: 8099, Route: config.RouteMaster, Spec: "-L8099:node1:8099", Gateway: "gw", ControlPath: "/st/cm-old"}}}
	snap := listeners(probe.Listener{Port: 8099, PID: 300, Command: "ssh"})
	v := View{Config: cfg, State: st, Snap: snap, Master: gateway.Status{Running: true, PID: 100}, Gateway: "gw",
		Socket: "/u/cm-gw", Sockets: map[string]gateway.Status{"/st/cm-old": {Running: true, PID: 300}}, ProcCmd: procs(nil)}
	byPort := map[int]Row{}
	for _, r := range v.Rows() {
		byPort[r.Port] = r
	}
	r := byPort[8099]
	if r.State != StateUp {
		t.Fatalf("dash classified %s, want up", r.State)
	}
	if !strings.Contains(r.Warning, "/st/cm-old") || !strings.Contains(r.Warning, "hpctun down 8099") {
		t.Fatalf("warning %q, want the recorded socket and the down hint", r.Warning)
	}
	// On the socket the gateway uses now there is nothing to warn about.
	st.Tunnels[0].ControlPath = "/u/cm-gw"
	v.Master, v.Sockets = gateway.Status{Running: true, PID: 300}, nil
	for _, r := range v.Rows() {
		if r.Port == 8099 && r.Warning != "" {
			t.Fatalf("warning %q for a tunnel on the current socket", r.Warning)
		}
	}
}

func TestRecordedMasters(t *testing.T) {
	st := &state.State{Tunnels: []state.Entry{
		{Port: 8001, Route: config.RouteMaster, Gateway: "gw", ControlPath: "/a"},
		{Port: 5006, Route: config.RouteSocks, Gateway: "gw", ControlPath: "/a"},
		{Port: 8002, Route: config.RouteMaster, Gateway: "gw", ControlPath: "/b"},
		{Port: 8003, Route: config.RouteMaster, Gateway: "gw", ControlPath: "/cur"},
		{Port: 8004, Route: config.RouteMaster, Gateway: "gw"},
		{Port: 4001, Route: config.RouteJump, Gateway: "gw", ControlPath: "/j", PID: 300},
	}}
	fake := &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		if strings.Contains(c.Line(), "-O check") && strings.Contains(c.Line(), "ControlPath=/a") {
			return sshctl.Result{Stderr: []byte("Master running (pid=11)\n")}, nil
		}
		return sshctl.Result{ExitCode: 255, Stderr: []byte("Control socket connect(x): No such file or directory\n")}, nil
	}}
	got := RecordedMasters(context.Background(), fake, st, "/cur")
	if checks := fake.Find("-O check"); len(checks) != 2 {
		t.Fatalf("check calls %+v, want one per distinct recorded socket", checks)
	}
	// Only /a (two tunnels, one check) and /b are looked at: /cur is the
	// caller's own socket, "" has no recorded socket and /j is a jump.
	want := map[string]gateway.Status{"/a": {Running: true, PID: 11}, "/b": {}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for path, status := range want {
		if got[path] != status {
			t.Fatalf("socket %s = %v, want %v", path, got[path], status)
		}
	}
}

func TestSpecForwardSpecAndTarget(t *testing.T) {
	cases := []struct {
		s           Spec
		fwd, target string
	}{
		{Spec{Port: 8099, Route: config.RouteMaster, Host: "n1", RemotePort: 80}, "-Llocalhost:8099:n1:80", "n1:80"},
		{Spec{Port: 4001, Route: config.RouteJump, Host: "n1", RemotePort: 4001}, "-Llocalhost:4001:localhost:4001", "n1:4001"},
		{Spec{Port: 5005, Route: config.RouteSocks}, "-Dlocalhost:5005", "-"},
	}
	for _, c := range cases {
		if c.s.ForwardSpec() != c.fwd || c.s.Target() != c.target {
			t.Errorf("%+v: got %q %q", c.s, c.s.ForwardSpec(), c.s.Target())
		}
	}
}

func TestRowsURL(t *testing.T) {
	cfg, err := config.Parse([]byte(`
gateway = "gw"
default_host = "node1"
[tunnel.jup]
port = 4001
url = "http://localhost:4001/lab"
[tunnel.dash]
port = 8099
[tunnel.api]
port = 8090
browse = false
`))
	if err != nil {
		t.Fatal(err)
	}
	st := &state.State{Tunnels: []state.Entry{
		{Port: 4002, Route: config.RouteMaster, Host: "node1", RemotePort: 4002, Spec: "-L4002:node1:4002", Gateway: "gw"},
	}}
	v := View{Config: cfg, State: st, Snap: listeners(), Master: gateway.Status{Running: true, PID: 100}, Gateway: "gw", ProcCmd: procs(nil)}
	got := map[string]string{}
	for _, r := range v.Rows() {
		got[r.Name] = r.URL
	}
	want := map[string]string{"jup": "http://localhost:4001/lab", "dash": "", "api": "", "(ad-hoc)": ""}
	for k, w := range want {
		if g, ok := got[k]; !ok || g != w {
			t.Errorf("row %q URL = %q (present %v), want %q", k, g, ok, w)
		}
	}
	for _, r := range v.Rows() {
		if r.NoBrowse != (r.Name == "api") {
			t.Errorf("row %q NoBrowse = %v", r.Name, r.NoBrowse)
		}
	}
}

func TestClassifyStandalone(t *testing.T) {
	cfg := testConfig(t)
	t0 := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	st := &state.State{Tunnels: []state.Entry{
		{Name: "dash", Port: 8099, Route: config.RouteMaster, PID: 10, PIDStarted: t0},
		{Name: "jup", Port: 4001, Route: config.RouteJump, PID: 11, PIDStarted: t0},
		{Name: "proxy", Port: 5005, Route: config.RouteSocks, PID: 12, PIDStarted: t0},
	}}
	ident := func(m map[int]proc.Info) func(int) (proc.Info, bool) {
		return func(pid int) (proc.Info, bool) { i, ok := m[pid]; return i, ok }
	}
	snap := listeners(
		probe.Listener{Port: 8099, PID: 10, Command: "ssh"},
		probe.Listener{Port: 4001, PID: 11, Command: "ssh"},
		probe.Listener{Port: 5005, PID: 12, Command: "ssh"},
	)
	v := View{Config: cfg, State: st, Snap: snap, Standalone: true, Ident: ident(map[int]proc.Info{
		10: {Image: "ssh", Started: t0},
		11: {Image: "ssh", Started: t0.Add(time.Hour)}, // pid reused by a later ssh
		12: {Image: "python", Started: t0},             // pid reused by something else
	})}
	cases := map[string]State{"dash": StateUp, "jup": StateConflict, "proxy": StateConflict}
	for name, want := range cases {
		if got, _ := v.Classify(spec(t, cfg, name)); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	// No listener: down, whatever state says.
	v.Snap = listeners()
	if got, _ := v.Classify(spec(t, cfg, "dash")); got != StateDown {
		t.Errorf("no listener: %s", got)
	}
	// Standalone never consults a master, even one with the listener's pid.
	v.Snap, v.Master = listeners(probe.Listener{Port: 8099, PID: 99, Command: "ssh"}), gateway.Status{Running: true, PID: 99}
	if got, _ := v.Classify(spec(t, cfg, "dash")); got != StateConflict {
		t.Errorf("master's pid: %s", got)
	}
}
