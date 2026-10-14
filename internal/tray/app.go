package tray

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"hpctun/internal/jsondoc"
)

// View draws a Model. Render may be called from any goroutine, never
// concurrently.
type View interface {
	Render(Model)
}

// App keeps the menu in step with hpctun: it polls status and runs what
// the user clicks.
type App struct {
	CLI  CLI
	View View
	// Poll is the time between status runs; CodeEvery is how many polls
	// pass between ones that also ask Slurm about the code job.
	Poll      time.Duration
	CodeEvery int
	// StatusTimeout and ActionTimeout bound one CLI run. An action may
	// wait on a password dialog or a phone push, so it gets longer.
	StatusTimeout, ActionTimeout time.Duration

	once    sync.Once
	mu      sync.Mutex
	render  sync.Mutex
	s       state
	kick    chan struct{}
	actions sync.WaitGroup
}

// init fills in defaults. Every entry point calls it, since clicks may
// arrive before Run starts.
func (a *App) init() { a.once.Do(a.defaults) }

func (a *App) defaults() {
	a.kick = make(chan struct{}, 1)
	a.s.busy = map[int]bool{}
	if a.Poll == 0 {
		a.Poll = 5 * time.Second
	}
	if a.CodeEvery == 0 {
		a.CodeEvery = 12
	}
	if a.StatusTimeout == 0 {
		a.StatusTimeout = 30 * time.Second
	}
	if a.ActionTimeout == 0 {
		a.ActionTimeout = 3 * time.Minute
	}
}

// Run polls until ctx ends. Polls never overlap: the next starts after the
// previous finished, at the next tick or when an action asks for one.
func (a *App) Run(ctx context.Context) {
	a.init()
	a.show()
	a.poll(ctx, true)
	t := time.NewTicker(a.Poll)
	defer t.Stop()
	for n := 1; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.kick:
		}
		a.poll(ctx, n%a.CodeEvery == 0)
	}
}

// Wait waits for actions still running; tests use it.
func (a *App) Wait() { a.actions.Wait() }

func (a *App) poll(ctx context.Context, code bool) {
	ctx, cancel := context.WithTimeout(ctx, a.StatusTimeout)
	defer cancel()
	st, err := Status(ctx, a.CLI, code)
	if ctx.Err() == context.Canceled {
		return // shutting down
	}
	a.mu.Lock()
	a.s.status, a.s.statusErr = st, err
	switch {
	case st == nil:
	case code:
		a.s.code = st.Code
	case a.s.code != nil && (st.Gateway == nil || !st.Gateway.Running):
		// Polls without the code query keep the last answer, but
		// disconnected the job cannot be known.
		a.s.code = &jsondoc.Code{State: "unknown"}
	}
	a.mu.Unlock()
	a.show()
}

// Refresh asks for a poll now.
func (a *App) Refresh() {
	a.init()
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// show renders the current state.
func (a *App) show() {
	a.mu.Lock()
	m := build(a.s)
	a.mu.Unlock()
	a.render.Lock()
	defer a.render.Unlock()
	a.View.Render(m)
}

// Toggle opens the tunnel on port if it is not up, and closes it if it is.
// A click on a busy tunnel is ignored.
func (a *App) Toggle(ctx context.Context, port int) {
	a.init()
	a.mu.Lock()
	row, ok := a.row(port)
	if !ok || a.s.busy[port] || row.State == "conflict" {
		a.mu.Unlock()
		return
	}
	args := []string{"--askpass", "up", "--json", strconv.Itoa(port)}
	if row.State == "up" {
		args = []string{"down", "--json", strconv.Itoa(port)}
	}
	a.tunnels(ctx, []int{port}, args)
}

// Browse opens the tunnel on port in a browser (hpctun browse), opening the
// tunnel first if needed. A click on a busy tunnel is ignored.
func (a *App) Browse(ctx context.Context, port int) {
	a.init()
	a.mu.Lock()
	row, ok := a.row(port)
	if !ok || a.s.busy[port] || row.State == "conflict" {
		a.mu.Unlock()
		return
	}
	arg := strconv.Itoa(port)
	if row.Name != nil {
		arg = *row.Name
	}
	a.s.busy[port] = true
	a.s.lastErr = ""
	a.mu.Unlock()
	a.show()
	a.background(ctx, func(ctx context.Context) string {
		// "--": a name is never read as a flag.
		if err := Plain(ctx, a.CLI, "--askpass", "browse", "--", arg); err != nil {
			return err.Error()
		}
		return ""
	}, func() { delete(a.s.busy, port) })
}

// OpenAll opens every tunnel; CloseAll closes them.
func (a *App) OpenAll(ctx context.Context) {
	a.all(ctx, "--askpass", "up", "--json", "--all")
}

func (a *App) CloseAll(ctx context.Context) {
	a.all(ctx, "down", "--json", "--all")
}

func (a *App) all(ctx context.Context, args ...string) {
	a.init()
	a.mu.Lock()
	var ports []int
	if a.s.status != nil {
		for _, r := range a.s.status.Tunnels {
			if r.Kind != "external" {
				if a.s.busy[r.Port] {
					a.mu.Unlock()
					return // finish what is running first
				}
				ports = append(ports, r.Port)
			}
		}
	}
	a.tunnels(ctx, ports, args)
}

// tunnels runs args in the background with ports marked busy. Called with
// a.mu held; it unlocks.
func (a *App) tunnels(ctx context.Context, ports []int, args []string) {
	for _, p := range ports {
		a.s.busy[p] = true
	}
	a.s.lastErr = ""
	a.mu.Unlock()
	a.show()
	a.background(ctx, func(ctx context.Context) string {
		res, err := Results(ctx, a.CLI, args...)
		if err != nil {
			return err.Error()
		}
		return failures(res)
	}, func() {
		for _, p := range ports {
			delete(a.s.busy, p)
		}
	})
}

// Login opens the gateway connection (and the transfer host's), asking
// through dialogs; Logout closes them.
func (a *App) Login(ctx context.Context)  { a.login(ctx, "login") }
func (a *App) Logout(ctx context.Context) { a.login(ctx, "logout") }

func (a *App) login(ctx context.Context, verb string) {
	a.init()
	a.mu.Lock()
	if a.s.loginBusy {
		a.mu.Unlock()
		return
	}
	args := []string{"--askpass", verb}
	// With a separate tunnel gateway, the menu's connection is that one,
	// which login opens only by name; naming it means naming the others
	// too.
	if st := a.s.status; st != nil && st.Gateway != nil && st.Gateway.TunnelsOnly {
		args = append(args, st.Gateway.Dest)
		for _, c := range []*jsondoc.Conn{st.Login, st.Transfer} {
			if c != nil {
				args = append(args, c.Alias)
			}
		}
	}
	a.s.loginBusy, a.s.lastErr = true, ""
	a.mu.Unlock()
	a.show()
	a.background(ctx, func(ctx context.Context) string {
		if err := Plain(ctx, a.CLI, args...); err != nil {
			return err.Error()
		}
		return ""
	}, func() { a.s.loginBusy = false })
}

// ShowError shows msg as the last error, as a failed action would, until
// it is dismissed or the next action starts.
func (a *App) ShowError(msg string) {
	a.init()
	a.mu.Lock()
	a.s.lastErr = msg
	a.mu.Unlock()
	a.show()
}

// ClearError hides the last error.
func (a *App) ClearError() {
	a.init()
	a.mu.Lock()
	a.s.lastErr = ""
	a.mu.Unlock()
	a.show()
}

// background runs do with the action timeout, then, under the lock, done
// and the error do returned, and asks for a poll.
func (a *App) background(ctx context.Context, do func(context.Context) string, done func()) {
	a.actions.Add(1)
	go func() {
		defer a.actions.Done()
		ctx, cancel := context.WithTimeout(ctx, a.ActionTimeout)
		msg := do(ctx)
		cancel()
		a.mu.Lock()
		done()
		a.s.lastErr = msg
		a.mu.Unlock()
		a.show()
		a.Refresh()
	}()
}

// row finds port's row in the last status. Called with a.mu held.
func (a *App) row(port int) (jsondoc.Row, bool) {
	if a.s.status == nil {
		return jsondoc.Row{}, false
	}
	for _, r := range a.s.status.Tunnels {
		if r.Port == port && r.Kind != "external" {
			return r, true
		}
	}
	return jsondoc.Row{}, false
}

// failures sums up the failed results: the first one's name and why, on
// one line, and how many others failed.
func failures(res *jsondoc.Results) string {
	var first string
	n := 0
	for _, r := range res.Results {
		if r.Outcome != "failed" {
			continue
		}
		n++
		if first != "" {
			continue
		}
		name := "port " + strconv.Itoa(r.Port)
		if r.Name != nil {
			name = *r.Name
		}
		why := "failed"
		if r.Detail != nil {
			why = strings.Join(strings.Fields(*r.Detail), " ")
		}
		first = name + ": " + why
	}
	if n > 1 {
		first += " (and " + strconv.Itoa(n-1) + " more)"
	}
	return first
}
