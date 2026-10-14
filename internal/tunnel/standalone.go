package tunnel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"hpctun/internal/config"
	"hpctun/internal/gateway"
	"hpctun/internal/probe"
	"hpctun/internal/sshctl"
	"hpctun/internal/state"
)

// ErrNoSSH is a missing ssh on Windows, where the OpenSSH client is an
// optional feature.
var ErrNoSSH = errors.New(`ssh is not installed: add the "OpenSSH Client" optional feature in Settings > Apps`)

// ownsStandalone reports whether pid, listening on port, is the ssh hpctun
// started for the tunnel recorded there: same pid, an ssh program, and the
// same start time (a reused pid starts later).
func (v View) ownsStandalone(port, pid int) bool {
	e, ok := v.State.ByPort(port)
	if !ok || e.PID != pid || v.Ident == nil {
		return false
	}
	info, ok := v.Ident(pid)
	return ok && info.Image == "ssh" && info.Started.Equal(e.PIDStarted)
}

// SignInHint is advice for an ssh log showing a sign-in that needed a
// prompt, which tunnels on Windows cannot show; "" for any other failure.
func SignInHint(log, dest string) string {
	l := strings.ToLower(log)
	if !strings.Contains(l, "permission denied") && !strings.Contains(l, "host key verification failed") && !strings.Contains(l, "batch mode") {
		return ""
	}
	return fmt.Sprintf("hpctun's tunnels on Windows sign in without prompting: load your key with `ssh-add` (or set one up with `hpctun ssh-config key`), and run `ssh %s` once to accept its host key.", dest)
}

// upStandalone opens each spec as its own ssh. It never stops early.
func (env *Env) upStandalone(ctx context.Context, specs []Spec) []Result {
	results := make([]Result, 0, len(specs))
	for _, s := range specs {
		if ctx.Err() != nil {
			results = append(results, Result{Spec: s, Outcome: Failed, Detail: ctx.Err().Error()})
			continue
		}
		results = append(results, env.upOneStandalone(ctx, s))
	}
	return results
}

func (env *Env) dest() string {
	if env.Gateway == nil {
		return ""
	}
	return env.Gateway.Dest
}

func (env *Env) upOneStandalone(ctx context.Context, s Spec) Result {
	fail := func(format string, a ...any) Result {
		return Result{Spec: s, Outcome: Failed, Detail: fmt.Sprintf(format, a...)}
	}
	dest := env.dest()
	if dest == "" {
		return fail("no gateway configured")
	}
	v, err := env.view(ctx, gateway.Status{}, dest, "", nil)
	if err != nil {
		return fail("%v", err)
	}
	switch st, owner := v.Classify(s); st {
	case StateUp:
		return Result{Spec: s, Outcome: AlreadyUp}
	case StateConflict:
		return fail("port %d is held by %s", s.Port, owner)
	}
	spec, jumpHost := s.ForwardSpec(), ""
	if s.Route == config.RouteJump {
		jumpHost = s.Host
	}
	logPath := filepath.Join(env.LogDir, fmt.Sprintf("tunnel-%d.log", s.Port))
	pid, err := env.Runner.Start(ctx, logPath, "ssh", sshctl.StandaloneArgs(dest, spec, jumpHost)...)
	if errors.Is(err, exec.ErrNotFound) {
		return fail("%v", ErrNoSSH)
	}
	if err != nil {
		return fail("%v", err)
	}
	started, err := env.awaitStandalone(ctx, s.Port, pid, logPath, dest)
	if err != nil {
		return fail("%v", err)
	}
	prev, hadPrev := env.State.ByPort(s.Port)
	env.State.Put(state.Entry{
		Name: s.Name, Port: s.Port, Route: s.Route, Host: s.Host, RemotePort: s.RemotePort,
		Spec: spec, Gateway: dest, PID: pid, PIDStarted: started, OpenedAt: env.Now().UTC(),
	})
	if err := env.Store.Save(env.State); err != nil {
		// The ssh is already listening and nothing would record it: put the
		// state back the way it was and stop it.
		if hadPrev {
			env.State.Put(prev)
		} else {
			env.State.Remove(s.Port)
		}
		return fail("tunnel opened but the state file could not be saved: %v%s", err, env.stopNote(pid))
	}
	return Result{Spec: s, Outcome: Opened}
}

// awaitStandalone waits until pid listens on port, which OpenSSH does only
// after signing in, and returns pid's start time. If ssh exits first, the
// error carries its log and any sign-in advice; on timeout or
// cancellation, pid is killed.
func (env *Env) awaitStandalone(ctx context.Context, port, pid int, logPath, dest string) (time.Time, error) {
	deadline := time.Now().Add(env.OpenTimeout)
	for {
		snap, err := probe.Take(ctx, env.Runner)
		if err != nil && ctx.Err() == nil {
			return time.Time{}, fmt.Errorf("port %d could not be checked: %v%s", port, err, env.stopNote(pid))
		}
		info, alive := env.Ident(pid)
		switch {
		case ctx.Err() != nil:
			return time.Time{}, fmt.Errorf("%w%s", ctx.Err(), env.stopNote(pid))
		case alive && snap.Listeners[port].PID == pid:
			return info.Started, nil
		case !alive:
			return time.Time{}, logError(fmt.Sprintf("ssh exited before port %d opened", port), logPath, dest)
		case time.Now().After(deadline):
			msg := fmt.Sprintf("ssh did not open port %d within %s", port, env.OpenTimeout) + env.stopNote(pid)
			return time.Time{}, logError(msg, logPath, dest)
		}
		select {
		case <-ctx.Done():
		case <-time.After(env.Wait):
		}
	}
}

// stopNote kills the ssh of a tunnel that never opened, and returns a note
// for the error saying whether it could be stopped.
func (env *Env) stopNote(pid int) string {
	if err := env.Kill(pid); err != nil {
		return fmt.Sprintf("; stopping ssh (pid %d) failed: %v", pid, err)
	}
	return "; stopped it"
}

// logError is msg, the log's last lines, and sign-in advice when they
// call for it.
func logError(msg, logPath, dest string) error {
	b, _ := os.ReadFile(logPath)
	log := strings.TrimSpace(string(b))
	if lines := strings.Split(log, "\n"); len(lines) > 5 {
		log = strings.Join(lines[len(lines)-5:], "\n")
	}
	if log != "" {
		msg += ":\n" + log
	}
	if h := SignInHint(log, dest); h != "" {
		msg += "\n" + h
	}
	return errors.New(msg)
}

// downStandalone ends the ssh of a tunnel hpctun opened. The ssh is
// verified on the very handle it is ended through, so a later process that
// was given the recorded pid is left alone; anything else on the port is
// left alone as well.
func (env *Env) downStandalone(ctx context.Context, s Spec) Result {
	entry, hasEntry := env.State.ByPort(s.Port)
	v, err := env.view(ctx, gateway.Status{}, env.Config.TunnelDest(), "", nil)
	if err != nil {
		return Result{Spec: s, Outcome: Failed, Detail: err.Error()}
	}
	switch st, owner := v.Classify(s); st {
	case StateDown:
		return Result{Spec: s, Outcome: AlreadyDown}
	case StateConflict:
		if hasEntry && v.Snap.Listeners[s.Port].PID == entry.PID {
			return Result{Spec: s, Outcome: Skipped, Detail: fmt.Sprintf("pid %d no longer runs this tunnel; not killed", entry.PID)}
		}
		return Result{Spec: s, Outcome: Skipped, Detail: fmt.Sprintf("port %d is held by %s, which hpctun did not open; not touched", s.Port, owner)}
	}
	if err := env.Terminate(entry.PID, entry.PIDStarted); err != nil {
		return Result{Spec: s, Outcome: Failed, Detail: err.Error()}
	}
	return Result{Spec: s, Outcome: Closed}
}
