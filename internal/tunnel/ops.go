package tunnel

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"hpctun/internal/config"
	"hpctun/internal/gateway"
	"hpctun/internal/probe"
	"hpctun/internal/proc"
	"hpctun/internal/sshctl"
	"hpctun/internal/state"
)

// Outcome is what happened to one tunnel.
type Outcome string

const (
	Opened      Outcome = "opened"
	AlreadyUp   Outcome = "already up"
	Closed      Outcome = "closed"
	AlreadyDown Outcome = "already down"
	Skipped     Outcome = "skipped"
	Failed      Outcome = "failed"
)

// Result is the outcome for one tunnel, with detail for failures and skips.
type Result struct {
	Spec    Spec
	Outcome Outcome
	Detail  string
}

// Env is what Up and Down need. The caller holds Store's lock.
type Env struct {
	Runner  sshctl.Runner
	Gateway *gateway.Gateway
	// GatewayFor resolves a gateway other than the configured one, to close
	// tunnels opened through it.
	GatewayFor func(ctx context.Context, dest string) (*gateway.Gateway, error)
	Store      *state.Store
	State      *state.State
	Config     *config.Config
	LogDir     string
	Kill       func(pid int) error
	// Terminate ends a tunnel's ssh, verified on the same handle it ends it
	// through, so a pid Windows has reused is never killed (standalone down).
	Terminate func(pid int, started time.Time) error
	Now       func() time.Time
	Wait      time.Duration // between checks for a new listener
	// Standalone: no shared master; each tunnel is its own ssh process,
	// started with Runner.Start and owned by pid and start time (Windows).
	Standalone  bool
	Ident       func(pid int) (proc.Info, bool) // standalone: a pid's program and start time
	OpenTimeout time.Duration                   // standalone: how long a new ssh may take to listen
}

func (env *Env) procCmd(ctx context.Context) func(int) (string, bool) {
	return func(pid int) (string, bool) {
		cmd, ok, err := probe.ProcessCommand(ctx, env.Runner, pid)
		return cmd, ok && err == nil
	}
}

// view builds the View for one classification. socket says which control
// socket master belongs to; sockets, when set, holds the masters behind the
// other control paths recorded in state (see RecordedMasters).
func (env *Env) view(ctx context.Context, master gateway.Status, gw, socket string, sockets map[string]gateway.Status) (View, error) {
	snap, err := probe.Take(ctx, env.Runner)
	if err != nil {
		return View{}, err
	}
	return View{Config: env.Config, State: env.State, Snap: snap, Master: master, Gateway: gw, Socket: socket, Sockets: sockets, ProcCmd: env.procCmd(ctx),
		Standalone: env.Standalone, Ident: env.Ident}, nil
}

// Up opens each spec that is not already up. It never stops early: every
// spec gets a Result.
func Up(ctx context.Context, env *Env, specs []Spec) []Result {
	if env.Standalone {
		return env.upStandalone(ctx, specs)
	}
	results := make([]Result, 0, len(specs))
	master, err := env.Gateway.Check(ctx, env.Runner)
	// Entries upOne records always carry the gateway's current socket, which
	// RecordedMasters skips, so no other socket's master can change within one Up.
	var sockets map[string]gateway.Status
	if err == nil {
		sockets = RecordedMasters(ctx, env.Runner, env.State, env.Gateway.SocketPath)
	}
	for _, s := range specs {
		switch {
		case err != nil:
			results = append(results, Result{Spec: s, Outcome: Failed, Detail: err.Error()})
		case ctx.Err() != nil:
			results = append(results, Result{Spec: s, Outcome: Failed, Detail: ctx.Err().Error()})
		default:
			results = append(results, env.upOne(ctx, s, &master, sockets))
		}
	}
	return results
}

func (env *Env) upOne(ctx context.Context, s Spec, master *gateway.Status, sockets map[string]gateway.Status) Result {
	fail := func(format string, a ...any) Result {
		return Result{Spec: s, Outcome: Failed, Detail: fmt.Sprintf(format, a...)}
	}
	socket := env.Gateway.SocketPath
	v, err := env.view(ctx, *master, env.Gateway.Dest, socket, sockets)
	if err != nil {
		return fail("%v", err)
	}
	switch st, owner := v.Classify(s); st {
	case StateUp:
		return Result{Spec: s, Outcome: AlreadyUp}
	case StateConflict:
		return fail("port %d is held by %s", s.Port, owner)
	}
	if !master.Running {
		st, err := env.Gateway.EnsureStarted(ctx, env.Runner, filepath.Join(env.LogDir, "master.log"))
		if err != nil {
			return fail("%v", err)
		}
		*master = st
	}
	spec := s.ForwardSpec()
	if s.Route == config.RouteJump {
		logPath := filepath.Join(env.LogDir, fmt.Sprintf("jump-%d.log", s.Port))
		res, err := env.Runner.Background(ctx, logPath, "ssh", sshctl.JumpArgs(env.Gateway.GW, s.Host, spec)...)
		if err != nil {
			return fail("%v", err)
		}
		if res.ExitCode != 0 {
			return fail("ssh exited %d:\n%s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
		}
	} else {
		res, err := env.Runner.Run(ctx, "ssh", sshctl.ForwardArgs(env.Gateway.GW, spec)...)
		if err != nil {
			return fail("%v", err)
		}
		if res.ExitCode != 0 {
			return fail("ssh -O forward exited %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
		}
	}
	l, ok, err := env.awaitListener(ctx, s.Port)
	if err != nil {
		return fail("ssh reported success but port %d could not be checked: %v", s.Port, err)
	}
	if !ok {
		return fail("ssh reported success but nothing is listening on port %d", s.Port)
	}
	if s.Route != config.RouteJump && l.PID != master.PID {
		return fail("port %d was taken by pid %d (%s)", s.Port, l.PID, l.Command)
	}
	e := state.Entry{
		Name: s.Name, Port: s.Port, Route: s.Route, Host: s.Host, RemotePort: s.RemotePort,
		Spec: spec, Gateway: env.Gateway.Dest, ControlPath: env.Gateway.SocketPath, OpenedAt: env.Now().UTC(),
	}
	if s.Route == config.RouteJump {
		e.PID = l.PID
	}
	env.State.Put(e)
	if err := env.Store.Save(env.State); err != nil {
		return fail("tunnel opened but the state file could not be saved: %v", err)
	}
	return Result{Spec: s, Outcome: Opened}
}

// awaitListener waits for port to start listening; a failed probe is an
// error, not an empty port.
func (env *Env) awaitListener(ctx context.Context, port int) (probe.Listener, bool, error) {
	for range 10 {
		snap, err := probe.Take(ctx, env.Runner)
		if err != nil {
			return probe.Listener{}, false, err
		}
		if l, ok := snap.Listeners[port]; ok {
			return l, true, nil
		}
		time.Sleep(env.Wait)
	}
	return probe.Listener{}, false, nil
}

// Down closes each spec hpctun opened. Ports held by anything hpctun did not open
// are never touched.
func Down(ctx context.Context, env *Env, specs []Spec) []Result {
	results := make([]Result, 0, len(specs))
	// masters caches the check of each control socket, keyed by destination
	// and socket path, so two sockets for one destination are checked apart.
	masters := map[string]gateway.Status{}
	for _, s := range specs {
		var r Result
		if env.Standalone {
			r = env.downStandalone(ctx, s)
		} else {
			r = env.downOne(ctx, s, masters)
		}
		if r.Outcome != Failed {
			env.State.Remove(s.Port)
		}
		results = append(results, r)
	}
	if err := env.Store.Save(env.State); err != nil {
		for i := range results {
			if results[i].Outcome != Failed {
				results[i] = Result{Spec: results[i].Spec, Outcome: Failed, Detail: "state file could not be saved: " + err.Error()}
			}
		}
	}
	return results
}

func (env *Env) downOne(ctx context.Context, s Spec, masters map[string]gateway.Status) Result {
	fail := func(format string, a ...any) Result {
		return Result{Spec: s, Outcome: Failed, Detail: fmt.Sprintf(format, a...)}
	}
	entry, hasEntry := env.State.ByPort(s.Port)
	gw := env.Gateway
	if hasEntry && entry.Gateway != "" && (gw == nil || entry.Gateway != gw.Dest) {
		g, err := env.GatewayFor(ctx, entry.Gateway)
		if err != nil {
			return fail("%v", err)
		}
		gw = g
	}
	// Close through the socket the tunnel was opened on, which the gateway as
	// resolved now may no longer use.
	if gw != nil && hasEntry && entry.ControlPath != "" && entry.ControlPath != gw.SocketPath {
		gw = gw.AtSocket(entry.ControlPath)
	}
	var master gateway.Status
	if gw != nil && s.Route != config.RouteJump {
		key := gw.Dest + "\x00" + gw.SocketPath
		st, ok := masters[key]
		if !ok {
			var err error
			if st, err = gw.Check(ctx, env.Runner); err != nil {
				return fail("%v", err)
			}
			masters[key] = st
		}
		master = st
	}
	dest, socket := "", ""
	if gw != nil {
		dest, socket = gw.Dest, gw.SocketPath
	}
	// gw is the recorded socket after the AtSocket swap above, so master
	// already belongs to it and no other recorded socket needs checking.
	v, err := env.view(ctx, master, dest, socket, nil)
	if err != nil {
		return fail("%v", err)
	}
	st, owner := v.Classify(s)
	switch st {
	case StateDown:
		return Result{Spec: s, Outcome: AlreadyDown}
	case StateConflict:
		if s.Route == config.RouteJump && hasEntry && v.Snap.Listeners[s.Port].PID == entry.PID {
			return Result{Spec: s, Outcome: Skipped, Detail: fmt.Sprintf("pid %d no longer runs this tunnel; not killed", entry.PID)}
		}
		return Result{Spec: s, Outcome: Skipped, Detail: fmt.Sprintf("port %d is held by %s, which hpctun did not open; not touched", s.Port, owner)}
	}
	if s.Route == config.RouteJump {
		if err := env.Kill(v.Snap.Listeners[s.Port].PID); err != nil {
			return fail("%v", err)
		}
		return Result{Spec: s, Outcome: Closed}
	}
	spec := s.ForwardSpec()
	if hasEntry && entry.Spec != "" {
		spec = entry.Spec
	}
	// StateUp for a master or socks route needs a running master, which needs
	// a gateway, so gw cannot normally be nil here. Guard it anyway.
	if gw == nil {
		return fail("no gateway to cancel port %d through", s.Port)
	}
	res, err := env.Runner.Run(ctx, "ssh", sshctl.CancelArgs(gw.GW, spec)...)
	if err != nil {
		return fail("%v", err)
	}
	if res.ExitCode != 0 {
		return fail("ssh -O cancel exited %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return Result{Spec: s, Outcome: Closed}
}
