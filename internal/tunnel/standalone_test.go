package tunnel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"hpctun/internal/config"
	"hpctun/internal/gateway"
	"hpctun/internal/probe"
	"hpctun/internal/proc"
	"hpctun/internal/sshctl/sshctltest"
	"hpctun/internal/state"
)

func TestSignInHint(t *testing.T) {
	for _, log := range []string{
		"shell: Permission denied (publickey,keyboard-interactive).",
		"Host key verification failed.",
		"Enter passphrase for key: read_passphrase: can't open /dev/tty: batch mode",
	} {
		h := SignInHint(log, "shell")
		if !strings.Contains(h, "ssh-add") || !strings.Contains(h, "`ssh shell`") || !strings.Contains(h, "hpctun ssh-config key") {
			t.Errorf("%q: hint %q", log, h)
		}
	}
	if h := SignInHint("ssh: connect to host shell port 22: Connection timed out", "shell"); h != "" {
		t.Errorf("network error got a hint: %q", h)
	}
}

// standalone turns w into a Windows machine: Start runs a tunnel's own ssh,
// which listens on its port unless w.hang, or exits at once writing
// w.exitLog when that is set.
type standaloneWorld struct {
	*world
	started    map[int]time.Time
	hang       bool
	exitLog    string
	terminated map[int]time.Time // pids down ended through Terminate, by the start time it verified
}

func newStandalone(t *testing.T, cfg *config.Config, st *state.State) (*standaloneWorld, *Env) {
	w := &standaloneWorld{world: newWorld(), started: map[int]time.Time{}, terminated: map[int]time.Time{}}
	env := w.env(t, cfg, st)
	env.Standalone, env.Ident, env.OpenTimeout, env.Wait = true, w.ident, 300*time.Millisecond, time.Millisecond
	env.Gateway = &gateway.Gateway{Dest: "gw", Mode: gateway.ModeStandalone}
	env.GatewayFor = func(context.Context, string) (*gateway.Gateway, error) {
		t.Fatal("GatewayFor called in standalone mode")
		return nil, nil
	}
	env.Terminate = func(pid int, started time.Time) error {
		w.terminated[pid] = started
		delete(w.procs, pid)
		for port, l := range w.listeners {
			if l.PID == pid {
				delete(w.listeners, port)
			}
		}
		return nil
	}
	w.fake.OnStart = w.start
	return w, env
}

func (w *standaloneWorld) start(c sshctltest.Call) (int, error) {
	pid := w.pid()
	if w.exitLog != "" {
		os.WriteFile(c.LogPath, []byte(w.exitLog), 0o600)
		return pid, nil // gone at once: never in procs
	}
	w.procs[pid] = c.Line()
	w.started[pid] = time.Unix(1_800_000_000, int64(pid)).UTC()
	if !w.hang {
		for _, a := range c.Args {
			if strings.HasPrefix(a, "-L") || strings.HasPrefix(a, "-D") {
				w.listeners[specPort(a)] = probe.Listener{Port: specPort(a), PID: pid, Command: "ssh"}
			}
		}
	}
	return pid, nil
}

func (w *standaloneWorld) ident(pid int) (proc.Info, bool) {
	if _, ok := w.procs[pid]; !ok {
		return proc.Info{}, false
	}
	return proc.Info{Image: "ssh", Started: w.started[pid]}, true
}

func TestStandaloneUpEachRoute(t *testing.T) {
	cfg := testConfig(t)
	w, env := newStandalone(t, cfg, &state.State{})
	res := Up(context.Background(), env, []Spec{spec(t, cfg, "dash"), spec(t, cfg, "jup"), spec(t, cfg, "proxy")})
	for _, r := range res {
		if r.Outcome != Opened {
			t.Fatalf("%s: %+v", r.Spec.Name, r)
		}
	}
	starts := w.fake.Find("BatchMode=yes")
	if len(starts) != 3 || !starts[0].Start || !strings.HasSuffix(starts[0].LogPath, "tunnel-8099.log") {
		t.Fatalf("starts %+v", starts)
	}
	wantTails := []string{"-Llocalhost:8099:node1:8099 -- gw", "-Llocalhost:4001:localhost:4001 -o ProxyCommand=ssh -o BatchMode=yes -W %h:%p -- gw -- node1", "-Dlocalhost:5005 -- gw"}
	for i, want := range wantTails {
		if !strings.HasSuffix(starts[i].Line(), want) {
			t.Errorf("start %d: %s\nwant suffix %s", i, starts[i].Line(), want)
		}
	}
	if len(w.fake.Find("-O")) != 0 || len(w.fake.Find("-fN")) != 0 {
		t.Fatalf("touched a master: %+v", w.fake.Calls)
	}
	e, _ := env.State.ByPort(8099)
	if e.PID == 0 || !e.PIDStarted.Equal(w.started[e.PID]) || e.Gateway != "gw" || e.ControlPath != "" {
		t.Fatalf("entry %+v", e)
	}
	// Again: already up, and no new ssh starts.
	if r := Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})[0]; r.Outcome != AlreadyUp {
		t.Fatalf("second up: %+v", r)
	}
	if n := len(w.fake.Find("BatchMode=yes")); n != 3 {
		t.Fatalf("second up started ssh: %d starts", n)
	}
}

func TestStandaloneUpPortHeld(t *testing.T) {
	cfg := testConfig(t)
	w, env := newStandalone(t, cfg, &state.State{})
	w.listeners[8099] = probe.Listener{Port: 8099, PID: 5, Command: "python"}
	r := Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})[0]
	if r.Outcome != Failed || r.Detail != "port 8099 is held by pid 5 (python)" || len(w.fake.Find("BatchMode")) != 0 {
		t.Fatalf("got %+v; starts %d", r, len(w.fake.Find("BatchMode")))
	}
}

func TestStandaloneUpSignInFails(t *testing.T) {
	cfg := testConfig(t)
	w, env := newStandalone(t, cfg, &state.State{})
	w.exitLog = "gw: Permission denied (publickey,keyboard-interactive).\n"
	r := Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})[0]
	if r.Outcome != Failed || !strings.Contains(r.Detail, "ssh exited before port 8099 opened") ||
		!strings.Contains(r.Detail, "Permission denied") || !strings.Contains(r.Detail, "ssh-add") {
		t.Fatalf("got %+v", r)
	}
	if _, ok := env.State.ByPort(8099); ok {
		t.Fatal("recorded a failed tunnel")
	}
}

func TestStandaloneUpTimeoutKills(t *testing.T) {
	cfg := testConfig(t)
	w, env := newStandalone(t, cfg, &state.State{})
	w.hang = true
	r := Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})[0]
	if r.Outcome != Failed || !strings.Contains(r.Detail, "did not open port 8099") || len(w.killed) != 1 {
		t.Fatalf("got %+v, killed %v", r, w.killed)
	}
}

func TestStandaloneUpCancelledKills(t *testing.T) {
	cfg := testConfig(t)
	w, env := newStandalone(t, cfg, &state.State{})
	w.hang = true
	ctx, cancel := context.WithCancel(context.Background())
	w.onCall = func(c sshctltest.Call) error {
		if c.Name == "lsof" && len(w.procs) > 0 {
			cancel() // Ctrl-C while waiting for the listener
		}
		return nil
	}
	r := Up(ctx, env, []Spec{spec(t, cfg, "dash")})[0]
	if r.Outcome != Failed || len(w.killed) != 1 {
		t.Fatalf("got %+v, killed %v", r, w.killed)
	}
	if _, ok := env.State.ByPort(8099); ok {
		t.Fatal("recorded a cancelled tunnel")
	}
}

func TestStandaloneUpNoSSH(t *testing.T) {
	cfg := testConfig(t)
	w, env := newStandalone(t, cfg, &state.State{})
	w.fake.OnStart = func(sshctltest.Call) (int, error) { return 0, fmt.Errorf("exec: %q: %w", "ssh", exec.ErrNotFound) }
	r := Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})[0]
	if r.Outcome != Failed || r.Detail != ErrNoSSH.Error() {
		t.Fatalf("got %+v", r)
	}
}

func TestStandaloneDown(t *testing.T) {
	cfg := testConfig(t)
	w, env := newStandalone(t, cfg, &state.State{})
	Up(context.Background(), env, []Spec{spec(t, cfg, "dash"), spec(t, cfg, "jup")})
	dash, _ := env.State.ByPort(8099)
	jup, _ := env.State.ByPort(4001)
	// jup's ssh died and Windows gave its pid to a later ssh, listening on
	// the same port: same pid, later start time.
	w.procs[jup.PID], w.started[jup.PID] = "ssh me@elsewhere", jup.PIDStarted.Add(time.Hour)
	res := Down(context.Background(), env, []Spec{spec(t, cfg, "dash"), spec(t, cfg, "jup"), spec(t, cfg, "proxy")})
	if res[0].Outcome != Closed || len(w.terminated) != 1 {
		t.Fatalf("dash %+v terminated %v", res[0], w.terminated)
	}
	if got := w.terminated[dash.PID]; !got.Equal(dash.PIDStarted) {
		t.Fatalf("terminate verified start %v, want %v", got, dash.PIDStarted)
	}
	if res[1].Outcome != Skipped || !strings.Contains(res[1].Detail, "no longer runs this tunnel") {
		t.Fatalf("jup %+v", res[1])
	}
	if res[2].Outcome != AlreadyDown {
		t.Fatalf("proxy %+v", res[2])
	}
	if len(env.State.Tunnels) != 0 {
		t.Fatalf("entries left: %+v", env.State.Tunnels)
	}
}

// TestStandaloneUpSaveFailsStopsSSH: a tunnel whose record cannot be saved
// is stopped at once, not left running with nothing pointing at it.
func TestStandaloneUpSaveFailsStopsSSH(t *testing.T) {
	cfg := testConfig(t)
	w, env := newStandalone(t, cfg, &state.State{})
	if err := os.RemoveAll(env.Store.Dir()); err != nil {
		t.Fatal(err)
	}
	r := Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})[0]
	if r.Outcome != Failed || !strings.Contains(r.Detail, "state file could not be saved") || !strings.Contains(r.Detail, "stopped it") {
		t.Fatalf("got %+v", r)
	}
	if len(w.killed) != 1 || w.killed[0] != w.listeners[8099].PID {
		t.Fatalf("killed %v, want the new ssh %d", w.killed, w.listeners[8099].PID)
	}
	if _, ok := env.State.ByPort(8099); ok {
		t.Fatalf("entry left: %+v", env.State.Tunnels)
	}
}

func TestStandaloneDeadSSHReopens(t *testing.T) {
	cfg := testConfig(t)
	w, env := newStandalone(t, cfg, &state.State{})
	Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})
	old, _ := env.State.ByPort(8099)
	delete(w.procs, old.PID) // laptop slept; ssh gave up
	delete(w.listeners, 8099)
	r := Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})[0]
	e, _ := env.State.ByPort(8099)
	if r.Outcome != Opened || e.PID == old.PID {
		t.Fatalf("got %+v, entry %+v", r, e)
	}
}

// failKill makes stopping the tunnel's ssh fail, and returns a function
// reporting the pid it was asked to stop.
func failKill(env *Env) func() int {
	var pid int
	env.Kill = func(p int) error {
		pid = p
		return errors.New("access denied")
	}
	return func() int { return pid }
}

func TestStandaloneUpTimeoutKillFails(t *testing.T) {
	cfg := testConfig(t)
	w, env := newStandalone(t, cfg, &state.State{})
	w.hang = true
	stopped := failKill(env)
	r := Up(context.Background(), env, []Spec{spec(t, cfg, "dash")})[0]
	want := fmt.Sprintf("stopping ssh (pid %d) failed: access denied", stopped())
	if r.Outcome != Failed || !strings.Contains(r.Detail, "did not open port 8099") ||
		!strings.Contains(r.Detail, want) || strings.Contains(r.Detail, "stopped it") {
		t.Fatalf("got %+v, want detail containing %q", r, want)
	}
}

func TestStandaloneUpCancelledKillFails(t *testing.T) {
	cfg := testConfig(t)
	w, env := newStandalone(t, cfg, &state.State{})
	w.hang = true
	ctx, cancel := context.WithCancel(context.Background())
	w.onCall = func(c sshctltest.Call) error {
		if c.Name == "lsof" && len(w.procs) > 0 {
			cancel() // Ctrl-C while waiting for the listener
		}
		return nil
	}
	stopped := failKill(env)
	r := Up(ctx, env, []Spec{spec(t, cfg, "dash")})[0]
	want := fmt.Sprintf("stopping ssh (pid %d) failed: access denied", stopped())
	if r.Outcome != Failed || !strings.Contains(r.Detail, "context canceled") ||
		!strings.Contains(r.Detail, want) || strings.Contains(r.Detail, "stopped it") {
		t.Fatalf("got %+v, want detail containing %q", r, want)
	}
}
