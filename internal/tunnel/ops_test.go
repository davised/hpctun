package tunnel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"hpctun/internal/config"
	"hpctun/internal/gateway"
	"hpctun/internal/probe"
	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
	"hpctun/internal/state"
)

func init() { gateway.RetryDelay = 0 }

// world is a fake machine: an ssh master, listening ports and processes,
// driven by the commands tun runs.
type world struct {
	masterPID int            // 0: not running
	socketPID map[string]int // ControlPath substring -> pid, answered ahead of masterPID on a check
	listeners map[int]probe.Listener
	procs     map[int]string
	nextPID   int
	fail      map[string]sshctl.Result // Call.Line() substring -> forced result
	onCall    func(c sshctltest.Call) error
	killed    []int
	fake      *sshctltest.Fake
}

func newWorld() *world {
	w := &world{listeners: map[int]probe.Listener{}, procs: map[int]string{}, nextPID: 1000, fail: map[string]sshctl.Result{}, socketPID: map[string]int{}}
	w.fake = &sshctltest.Fake{Handler: w.handle}
	return w
}

func (w *world) pid() int { w.nextPID++; return w.nextPID }

// specPort is the local port of a -L or -D spec: the last field of
// -D[bind:]port, and the field before host:hostport of -L[bind:]port:host:hostport.
func specPort(spec string) int {
	f := strings.Split(spec[2:], ":")
	i := 0
	switch {
	case strings.HasPrefix(spec, "-D"):
		i = len(f) - 1
	case len(f) == 4:
		i = 1
	}
	p, _ := strconv.Atoi(f[i])
	return p
}

func (w *world) handle(c sshctltest.Call) (sshctl.Result, error) {
	if w.onCall != nil {
		if err := w.onCall(c); err != nil {
			return sshctl.Result{}, err
		}
	}
	line := c.Line()
	for sub, res := range w.fail {
		if strings.Contains(line, sub) {
			return res, nil
		}
	}
	has := func(s string) bool { return slices.Contains(c.Args, s) }
	switch {
	case c.Name == "lsof" && has("-sTCP:LISTEN"):
		var b strings.Builder
		for _, l := range w.listeners {
			fmt.Fprintf(&b, "p%d\nc%s\nf3\nPTCP\nn127.0.0.1:%d\n", l.PID, l.Command, l.Port)
		}
		if b.Len() == 0 {
			return sshctl.Result{ExitCode: 1}, nil
		}
		return sshctl.Result{Stdout: []byte(b.String())}, nil
	case c.Name == "lsof":
		return sshctl.Result{ExitCode: 1}, nil
	case c.Name == "ps":
		pid, _ := strconv.Atoi(c.Args[len(c.Args)-1])
		if cmd, ok := w.procs[pid]; ok {
			return sshctl.Result{Stdout: []byte(cmd + "\n")}, nil
		}
		return sshctl.Result{ExitCode: 1}, nil
	case has("check"):
		for sub, pid := range w.socketPID {
			if strings.Contains(line, sub) {
				return sshctl.Result{Stderr: []byte(fmt.Sprintf("Master running (pid=%d)\n", pid))}, nil
			}
		}
		if w.masterPID != 0 {
			return sshctl.Result{Stderr: []byte(fmt.Sprintf("Master running (pid=%d)\n", w.masterPID))}, nil
		}
		return sshctl.Result{ExitCode: 255, Stderr: []byte("Control socket connect(x): No such file or directory\n")}, nil
	case has("-fN"):
		w.masterPID = w.pid()
		return sshctl.Result{}, nil
	case has("forward"):
		port := specPort(c.Args[slices.Index(c.Args, "forward")+1])
		w.listeners[port] = probe.Listener{Port: port, PID: w.masterPID, Command: "ssh"}
		return sshctl.Result{}, nil
	case has("cancel"):
		delete(w.listeners, specPort(c.Args[slices.Index(c.Args, "cancel")+1]))
		return sshctl.Result{}, nil
	case has("-Nf"):
		spec := c.Args[slices.IndexFunc(c.Args, func(a string) bool { return strings.HasPrefix(a, "-L") })]
		pid := w.pid()
		w.listeners[specPort(spec)] = probe.Listener{Port: specPort(spec), PID: pid, Command: "ssh"}
		w.procs[pid] = line
		return sshctl.Result{}, nil
	}
	return sshctl.Result{}, nil
}

func (w *world) env(t *testing.T, cfg *config.Config, st *state.State) *Env {
	t.Helper()
	store, err := state.Open(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	g := gateway.FromSSHG("gw", "/st", map[string]string{"controlmaster": "false"})
	return &Env{
		Runner: w.fake, Gateway: g, Store: store, State: st, Config: cfg, LogDir: t.TempDir(),
		GatewayFor: func(_ context.Context, dest string) (*gateway.Gateway, error) {
			return gateway.FromSSHG(dest, "/st", map[string]string{"controlmaster": "false"}), nil
		},
		Kill: func(pid int) error { w.killed = append(w.killed, pid); delete(w.procs, pid); return nil },
		Now:  func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) },
	}
}

func spec(t *testing.T, cfg *config.Config, name string) Spec {
	tn, ok := cfg.Lookup(name)
	if !ok {
		t.Fatalf("no tunnel %s", name)
	}
	return FromConfig(tn)
}

func TestUpMasterRouteStartsMasterAndRecords(t *testing.T) {
	w := newWorld()
	cfg := testConfig(t)
	env := w.env(t, cfg, &state.State{})
	res := Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})
	if res[0].Outcome != Opened {
		t.Fatalf("got %+v", res[0])
	}
	if len(w.fake.Find("-fN")) != 1 {
		t.Fatal("master not started")
	}
	e, ok := env.State.ByPort(8099)
	if !ok || e.Name != "dash" || e.Spec != "-Llocalhost:8099:node1:8099" || e.Gateway != "gw" || e.PID != 0 {
		t.Fatalf("entry = %+v %v", e, ok)
	}
	saved, _ := env.Store.Load()
	if _, ok := saved.ByPort(8099); !ok {
		t.Fatal("state not saved")
	}
}

func TestUpRecordsControlPath(t *testing.T) {
	w := newWorld()
	cfg := testConfig(t)
	env := w.env(t, cfg, &state.State{})
	res := Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})
	if res[0].Outcome != Opened {
		t.Fatalf("got %+v", res[0])
	}
	e, _ := env.State.ByPort(8099)
	if e.ControlPath == "" || e.ControlPath != env.Gateway.SocketPath {
		t.Fatalf("entry ControlPath %q, want %q", e.ControlPath, env.Gateway.SocketPath)
	}
}

// The tunnel is live on a control socket the gateway no longer uses: up sees
// it as already open, not as a conflict against the current master.
func TestUpAlreadyUpOnRecordedSocket(t *testing.T) {
	w := newWorld()
	w.masterPID = 999 // the master on the socket the gateway uses now
	w.socketPID["/st/cm-old"] = 700
	w.listeners[8099] = probe.Listener{Port: 8099, PID: 700, Command: "ssh"}
	cfg := testConfig(t)
	st := &state.State{Tunnels: []state.Entry{{Name: "dash", Port: 8099, Route: config.RouteMaster, Host: "node1", RemotePort: 8099, Spec: "-L8099:node1:8099", Gateway: "gw", ControlPath: "/st/cm-old"}}}
	env := w.env(t, cfg, st)
	env.Gateway = gateway.FromSSHG("gw", "/st", map[string]string{"controlmaster": "auto", "controlpath": "/home/u/.ssh/cm-gw"})
	res := Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})
	if res[0].Outcome != AlreadyUp {
		t.Fatalf("got %+v", res[0])
	}
	if len(w.fake.Find("forward")) != 0 {
		t.Fatal("forwarded again on a live tunnel")
	}
	e, _ := env.State.ByPort(8099)
	if e.ControlPath != "/st/cm-old" {
		t.Fatalf("entry ControlPath %q, want the recorded socket kept", e.ControlPath)
	}
}

// --all re-ran the master check for every stale socket once per tunnel; Up
// checks each recorded socket a single time for the whole call.
func TestUpChecksRecordedSocketsOnce(t *testing.T) {
	w := newWorld()
	w.masterPID = 999 // the master on the socket the gateway uses now
	w.socketPID["/st/cm-old"] = 700
	w.listeners[8099] = probe.Listener{Port: 8099, PID: 700, Command: "ssh"}
	w.listeners[5005] = probe.Listener{Port: 5005, PID: 700, Command: "ssh"}
	cfg := testConfig(t)
	st := &state.State{Tunnels: []state.Entry{
		{Name: "dash", Port: 8099, Route: config.RouteMaster, Host: "node1", RemotePort: 8099, Spec: "-L8099:node1:8099", Gateway: "gw", ControlPath: "/st/cm-old"},
		{Name: "proxy", Port: 5005, Route: config.RouteSocks, Spec: "-Dlocalhost:5005", Gateway: "gw", ControlPath: "/st/cm-old"},
	}}
	env := w.env(t, cfg, st)
	env.Gateway = gateway.FromSSHG("gw", "/st", map[string]string{"controlmaster": "auto", "controlpath": "/home/u/.ssh/cm-gw"})
	res := Up(context.Background(), env, []Spec{spec(t, cfg, "dash"), spec(t, cfg, "proxy")})
	if res[0].Outcome != AlreadyUp || res[1].Outcome != AlreadyUp {
		t.Fatalf("got %+v", res)
	}
	var checks int
	for _, c := range w.fake.Find("check") {
		if strings.Contains(c.Line(), "-O check") && strings.Contains(c.Line(), "ControlPath=/st/cm-old") {
			checks++
		}
	}
	if checks != 1 {
		t.Fatalf("recorded socket checked %d times, want once: %+v", checks, w.fake.Find("check"))
	}
}

func TestUpIsIdempotent(t *testing.T) {
	w := newWorld()
	cfg := testConfig(t)
	env := w.env(t, cfg, &state.State{})
	Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})
	before := len(w.fake.Find("forward"))
	res := Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})
	if res[0].Outcome != AlreadyUp || len(w.fake.Find("forward")) != before {
		t.Fatalf("second up: %+v, forwards %d -> %d", res[0], before, len(w.fake.Find("forward")))
	}
}

func TestUpConflict(t *testing.T) {
	w := newWorld()
	w.listeners[8099] = probe.Listener{Port: 8099, PID: 9, Command: "python3"}
	cfg := testConfig(t)
	env := w.env(t, cfg, &state.State{})
	res := Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})
	if res[0].Outcome != Failed || !strings.Contains(res[0].Detail, "pid 9 (python3)") {
		t.Fatalf("got %+v", res[0])
	}
	if len(w.fake.Find("forward")) != 0 || len(w.fake.Find("-fN")) != 0 {
		t.Fatal("opened something despite the conflict")
	}
}

func TestUpJumpRecordsPID(t *testing.T) {
	w := newWorld()
	cfg := testConfig(t)
	env := w.env(t, cfg, &state.State{})
	res := Up(context.Background(), env, []Spec{spec(t, cfg, "jup")})
	if res[0].Outcome != Opened {
		t.Fatalf("got %+v", res[0])
	}
	jumps := w.fake.Find("-Nf")
	if len(jumps) != 1 || !jumps[0].Background || !strings.HasSuffix(jumps[0].LogPath, "jump-4001.log") {
		t.Fatalf("jump calls %+v", jumps)
	}
	if !strings.Contains(jumps[0].Line(), "ProxyCommand=ssh -o ControlMaster=no") {
		t.Fatalf("jump not routed through the master: %s", jumps[0].Line())
	}
	e, _ := env.State.ByPort(4001)
	if e.PID != w.listeners[4001].PID || e.PID == 0 {
		t.Fatalf("entry PID %d, listener PID %d", e.PID, w.listeners[4001].PID)
	}
}

func TestUpForwardFailureRecordsNothing(t *testing.T) {
	w := newWorld()
	w.fail["forward"] = sshctl.Result{ExitCode: 255, Stderr: []byte("Port forwarding failed.\n")}
	cfg := testConfig(t)
	env := w.env(t, cfg, &state.State{})
	res := Up(context.Background(), env, []Spec{spec(t, cfg, "dash"), spec(t, cfg, "proxy")})
	for _, r := range res {
		if r.Outcome != Failed || !strings.Contains(r.Detail, "Port forwarding failed") {
			t.Fatalf("got %+v", r)
		}
	}
	if len(env.State.Tunnels) != 0 {
		t.Fatalf("recorded %+v", env.State.Tunnels)
	}
}

// A probe that fails after ssh's forward is a check failure, not a port that
// nothing listens on.
func TestUpProbeFailureAfterForward(t *testing.T) {
	w := newWorld()
	w.masterPID = 700
	cfg := testConfig(t)
	env := w.env(t, cfg, &state.State{})
	forwarded := false
	w.onCall = func(c sshctltest.Call) error {
		switch {
		case slices.Contains(c.Args, "forward"):
			forwarded = true
		case forwarded && c.Name == "lsof":
			return errors.New("lsof broke")
		}
		return nil
	}
	res := Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})
	if res[0].Outcome != Failed || !strings.Contains(res[0].Detail, "lsof broke") || strings.Contains(res[0].Detail, "nothing is listening") {
		t.Fatalf("got %+v", res[0])
	}
	if _, ok := env.State.ByPort(8099); ok {
		t.Fatal("recorded a tunnel whose port was never checked")
	}
}

// Review Focus 4: Ctrl-C during a prompt records nothing.
func TestUpCancelledRecordsNothing(t *testing.T) {
	w := newWorld()
	cfg := testConfig(t)
	env := w.env(t, cfg, &state.State{})
	ctx, cancel := context.WithCancel(context.Background())
	w.onCall = func(c sshctltest.Call) error {
		if c.Background {
			cancel()
			return context.Canceled
		}
		return nil
	}
	res := Up(ctx, env, []Spec{spec(t, cfg, "jup"), spec(t, cfg, "dash")})
	for _, r := range res {
		if r.Outcome != Failed {
			t.Fatalf("got %+v", r)
		}
	}
	saved, _ := env.Store.Load()
	if len(env.State.Tunnels) != 0 || len(saved.Tunnels) != 0 {
		t.Fatalf("recorded %+v / saved %+v", env.State.Tunnels, saved.Tunnels)
	}
}

// Review Focus 3: master restarted with a new PID; up re-adds its forwards.
func TestUpRestoresAfterMasterRestart(t *testing.T) {
	w := newWorld()
	w.masterPID = 555 // new master, no forwards on it
	cfg := testConfig(t)
	st := &state.State{Tunnels: []state.Entry{{Name: "dash", Port: 8099, Route: config.RouteMaster, Host: "node1", RemotePort: 8099, Spec: "-L8099:node1:8099", Gateway: "gw"}}}
	env := w.env(t, cfg, st)
	v := View{Config: cfg, State: st, Snap: probe.Snapshot{Listeners: w.listeners}, Master: gateway.Status{Running: true, PID: 555}}
	if got, _ := v.Classify(spec(t, cfg, "dash")); got != StateDown {
		t.Fatalf("classified %s after restart, want down", got)
	}
	res := Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})
	if res[0].Outcome != Opened || w.listeners[8099].PID != 555 {
		t.Fatalf("got %+v, listener %+v", res[0], w.listeners[8099])
	}
	if len(w.fake.Find("-fN")) != 0 {
		t.Fatal("started a second master")
	}
}

func TestDownMasterUsesRecordedSpec(t *testing.T) {
	w := newWorld()
	cfg := testConfig(t)
	env := w.env(t, cfg, &state.State{})
	Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})
	res := Down(context.Background(), env, []Spec{spec(t, cfg, "dash")})
	if res[0].Outcome != Closed {
		t.Fatalf("got %+v", res[0])
	}
	cancels := w.fake.Find("cancel")
	if len(cancels) != 1 || !slices.Contains(cancels[0].Args, "-Llocalhost:8099:node1:8099") {
		t.Fatalf("cancel calls %+v", cancels)
	}
	if _, ok := env.State.ByPort(8099); ok {
		t.Fatal("entry not removed")
	}
}

// A tunnel opened before forwards were bound to localhost is recorded with
// the old spec. down must cancel it with exactly that spec: ssh matches a
// cancel by its listen address, so a regenerated spec would not find it.
func TestDownCancelsWithRecordedUnboundSpec(t *testing.T) {
	w := newWorld()
	w.masterPID = 700
	w.listeners[8099] = probe.Listener{Port: 8099, PID: 700, Command: "ssh"}
	cfg := testConfig(t)
	st := &state.State{Tunnels: []state.Entry{{Name: "dash", Port: 8099, Route: config.RouteMaster, Host: "node1", RemotePort: 8099, Spec: "-L8099:node1:8099", Gateway: "gw"}}}
	env := w.env(t, cfg, st)
	res := Down(context.Background(), env, []Spec{spec(t, cfg, "dash")})
	if res[0].Outcome != Closed {
		t.Fatalf("got %+v", res[0])
	}
	cancels := w.fake.Find("cancel")
	if len(cancels) != 1 || !slices.Contains(cancels[0].Args, "-L8099:node1:8099") || slices.Contains(cancels[0].Args, "-Llocalhost:8099:node1:8099") {
		t.Fatalf("cancel calls %+v", cancels)
	}
}

func TestDownJumpKillsVerifiedPID(t *testing.T) {
	w := newWorld()
	cfg := testConfig(t)
	env := w.env(t, cfg, &state.State{})
	Up(context.Background(), env, []Spec{spec(t, cfg, "jup")})
	pid := w.listeners[4001].PID
	res := Down(context.Background(), env, []Spec{spec(t, cfg, "jup")})
	if res[0].Outcome != Closed || !slices.Equal(w.killed, []int{pid}) {
		t.Fatalf("got %+v, killed %v", res[0], w.killed)
	}
}

func TestDownJumpPIDReusedNotKilled(t *testing.T) {
	w := newWorld()
	cfg := testConfig(t)
	w.listeners[4001] = probe.Listener{Port: 4001, PID: 42, Command: "ssh"}
	w.procs[42] = "ssh some-other-host"
	st := &state.State{Tunnels: []state.Entry{{Name: "jup", Port: 4001, Route: config.RouteJump, Spec: "-L4001:localhost:4001", PID: 42}}}
	env := w.env(t, cfg, st)
	res := Down(context.Background(), env, []Spec{spec(t, cfg, "jup")})
	if res[0].Outcome != Skipped || len(w.killed) != 0 {
		t.Fatalf("got %+v, killed %v", res[0], w.killed)
	}
	if _, ok := env.State.ByPort(4001); ok {
		t.Fatal("stale entry kept")
	}
}

// Review Focus 2: a hand-started forward on a named port is never killed.
func TestDownLeavesForeignProcessAlone(t *testing.T) {
	w := newWorld()
	cfg := testConfig(t)
	w.listeners[4001] = probe.Listener{Port: 4001, PID: 52216, Command: "ssh"}
	w.procs[52216] = "ssh -J gw node1 -Nf -L4001:localhost:4001"
	env := w.env(t, cfg, &state.State{})
	res := Down(context.Background(), env, []Spec{spec(t, cfg, "jup")})
	if res[0].Outcome != Skipped || !strings.Contains(res[0].Detail, "pid 52216") || len(w.killed) != 0 {
		t.Fatalf("got %+v, killed %v", res[0], w.killed)
	}
	if len(w.fake.Find("cancel")) != 0 {
		t.Fatal("cancelled a forward tun did not open")
	}
}

func TestDownAlreadyDownRemovesEntry(t *testing.T) {
	w := newWorld()
	cfg := testConfig(t)
	st := &state.State{Tunnels: []state.Entry{{Port: 4002, Route: config.RouteMaster, Host: "node1", RemotePort: 4002, Spec: "-L4002:node1:4002", Gateway: "gw"}}}
	env := w.env(t, cfg, st)
	res := Down(context.Background(), env, []Spec{FromEntry(st.Tunnels[0])})
	if res[0].Outcome != AlreadyDown || len(env.State.Tunnels) != 0 {
		t.Fatalf("got %+v, state %+v", res[0], env.State.Tunnels)
	}
}

func TestDownThroughOldGateway(t *testing.T) {
	w := newWorld()
	w.masterPID = 700
	w.listeners[4002] = probe.Listener{Port: 4002, PID: 700, Command: "ssh"}
	cfg := testConfig(t)
	st := &state.State{Tunnels: []state.Entry{{Port: 4002, Route: config.RouteMaster, Host: "node1", RemotePort: 4002, Spec: "-L4002:node1:4002", Gateway: "old-gw"}}}
	env := w.env(t, cfg, st)
	res := Down(context.Background(), env, []Spec{FromEntry(st.Tunnels[0])})
	if res[0].Outcome != Closed {
		t.Fatalf("got %+v", res[0])
	}
	cancels := w.fake.Find("cancel")
	if len(cancels) != 1 || cancels[0].Args[len(cancels[0].Args)-1] != "old-gw" {
		t.Fatalf("cancel not sent to old-gw: %+v", cancels)
	}
}

// The gateway switched to the user's own master after tun opened the forward;
// down still closes it through the control socket recorded at up.
func TestDownThroughRecordedControlPath(t *testing.T) {
	w := newWorld()
	w.masterPID = 700
	w.listeners[4002] = probe.Listener{Port: 4002, PID: 700, Command: "ssh"}
	cfg := testConfig(t)
	st := &state.State{Tunnels: []state.Entry{{Port: 4002, Route: config.RouteMaster, Host: "node1", RemotePort: 4002, Spec: "-L4002:node1:4002", Gateway: "gw", ControlPath: "/st/cm-managed"}}}
	env := w.env(t, cfg, st)
	env.Gateway = gateway.FromSSHG("gw", "/st", map[string]string{"controlmaster": "auto", "controlpath": "/home/u/.ssh/cm-gw"})
	res := Down(context.Background(), env, []Spec{FromEntry(st.Tunnels[0])})
	if res[0].Outcome != Closed {
		t.Fatalf("got %+v", res[0])
	}
	checks := w.fake.Find("check")
	if len(checks) != 1 {
		t.Fatalf("check calls %+v", checks)
	}
	cancels := w.fake.Find("cancel")
	if len(cancels) != 1 {
		t.Fatalf("cancel calls %+v", cancels)
	}
	for _, c := range append(checks, cancels...) {
		if !strings.Contains(c.Line(), "ControlPath=/st/cm-managed") {
			t.Fatalf("not through the recorded socket: %s", c.Line())
		}
	}
}

// One gateway, two sockets: the entry with a recorded control path and the
// one without are checked against their own masters.
func TestDownChecksEachSocketSeparately(t *testing.T) {
	w := newWorld()
	w.masterPID = 700
	w.listeners[4002] = probe.Listener{Port: 4002, PID: 700, Command: "ssh"}
	w.listeners[4003] = probe.Listener{Port: 4003, PID: 700, Command: "ssh"}
	cfg := testConfig(t)
	st := &state.State{Tunnels: []state.Entry{
		{Port: 4002, Route: config.RouteMaster, Host: "node1", RemotePort: 4002, Spec: "-L4002:node1:4002", Gateway: "gw", ControlPath: "/st/cm-managed"},
		{Port: 4003, Route: config.RouteMaster, Host: "node2", RemotePort: 4003, Spec: "-L4003:node2:4003", Gateway: "gw"},
	}}
	env := w.env(t, cfg, st)
	env.Gateway = gateway.FromSSHG("gw", "/st", map[string]string{"controlmaster": "auto", "controlpath": "/home/u/.ssh/cm-gw"})
	res := Down(context.Background(), env, []Spec{FromEntry(st.Tunnels[0]), FromEntry(st.Tunnels[1])})
	if res[0].Outcome != Closed || res[1].Outcome != Closed {
		t.Fatalf("got %+v", res)
	}
	checks := w.fake.Find("check")
	if len(checks) != 2 {
		t.Fatalf("check calls %+v", checks)
	}
	var managed, resolved int
	for _, c := range checks {
		if strings.Contains(c.Line(), "ControlPath=/st/cm-managed") {
			managed++
		} else {
			resolved++
		}
	}
	if managed != 1 || resolved != 1 {
		t.Fatalf("checks not one per socket: %+v", checks)
	}
}

// Down needs no configured gateway: every entry closes through the gateway it
// was opened through.
func TestDownWithoutConfiguredGateway(t *testing.T) {
	w := newWorld()
	w.masterPID = 700
	w.listeners[4002] = probe.Listener{Port: 4002, PID: 700, Command: "ssh"}
	cfg := testConfig(t)
	st := &state.State{Tunnels: []state.Entry{{Port: 4002, Route: config.RouteMaster, Host: "node1", RemotePort: 4002, Spec: "-L4002:node1:4002", Gateway: "old-gw"}}}
	env := w.env(t, cfg, st)
	env.Gateway = nil
	res := Down(context.Background(), env, []Spec{FromEntry(st.Tunnels[0]), spec(t, cfg, "dash")})
	if res[0].Outcome != Closed || res[1].Outcome != AlreadyDown {
		t.Fatalf("got %+v", res)
	}
	cancels := w.fake.Find("cancel")
	if len(cancels) != 1 || cancels[0].Args[len(cancels[0].Args)-1] != "old-gw" {
		t.Fatalf("cancel not sent to old-gw: %+v", cancels)
	}
	if _, ok := env.State.ByPort(4002); ok {
		t.Fatal("entry not removed")
	}
}
