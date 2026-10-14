package tunnel

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"hpctun/internal/config"
	"hpctun/internal/gateway"
	"hpctun/internal/probe"
	"hpctun/internal/proc"
	"hpctun/internal/sshctl"
	"hpctun/internal/state"
)

// State is a tunnel's observed state.
type State string

const (
	StateUp       State = "up"
	StateDown     State = "down"
	StateConflict State = "conflict" // port held by someone other than the expected owner
	StateExternal State = "external" // ssh listener hpctun did not open
)

// Kind says where a row's tunnel comes from.
type Kind string

const (
	KindNamed    Kind = "named"    // a tunnel in the config
	KindAdhoc    Kind = "adhoc"    // recorded by up, not in the config
	KindExternal Kind = "external" // an ssh listener hpctun did not open
)

// Row is one line of `hpctun status`.
type Row struct {
	Kind     Kind
	Name     string // tunnel name, "(ad-hoc)", or "-" for external
	Port     int
	Route    string // master, jump, socks, or "?" for external
	Target   string
	State    State
	Conns    int    // established connections; -1 when not listening
	Owner    string // for conflict: "pid N (command)"
	Warning  string
	URL      string // the config tunnel's url; "" for none
	NoBrowse bool   // the config tunnel has browse = false
}

// View is everything needed to classify tunnels at one moment.
type View struct {
	Config  *config.Config
	State   *state.State
	Snap    probe.Snapshot
	Master  gateway.Status
	Gateway string // configured gateway; "" if none
	Socket  string // control socket Master belongs to; "" when there is no gateway
	// Sockets holds the status of the masters behind the other control
	// sockets recorded in state, keyed by path; nil when none were checked.
	Sockets map[string]gateway.Status
	ProcCmd func(pid int) (string, bool) // full command line of a pid
	// Standalone: each tunnel is its own ssh, owned by pid and start time
	// (Windows); Master and Sockets are not used.
	Standalone bool
	Ident      func(pid int) (proc.Info, bool) // standalone: a pid's program and start time
}

// Specs lists every managed tunnel: config tunnels in name order, then ad-hoc
// state entries on ports no config tunnel uses.
func Specs(cfg *config.Config, st *state.State) []Spec {
	var specs []Spec
	for _, t := range cfg.Tunnels {
		specs = append(specs, FromConfig(t))
	}
	for _, e := range st.Tunnels {
		if _, named := cfg.ByPort(e.Port); !named {
			specs = append(specs, FromEntry(e))
		}
	}
	return specs
}

// Classify reports the state of one managed tunnel and, for a conflict, who
// holds its port.
func (v View) Classify(s Spec) (State, string) {
	l, ok := v.Snap.Listeners[s.Port]
	if !ok {
		return StateDown, ""
	}
	if v.Standalone {
		if v.ownsStandalone(s.Port, l.PID) {
			return StateUp, ""
		}
		return StateConflict, fmt.Sprintf("pid %d (%s)", l.PID, l.Command)
	}
	switch s.Route {
	case config.RouteMaster, config.RouteSocks:
		m := v.Master
		// A tunnel stays up on the control socket it was opened through,
		// even after the gateway moves to a different one.
		if e, ok := v.State.ByPort(s.Port); ok && e.ControlPath != "" && e.ControlPath != v.Socket {
			m = v.Sockets[e.ControlPath] // a socket not checked is not running
		}
		if m.Running && l.PID == m.PID {
			return StateUp, ""
		}
	case config.RouteJump:
		if e, ok := v.State.ByPort(s.Port); ok && e.PID == l.PID && v.ProcCmd != nil {
			if cmd, ok := v.ProcCmd(l.PID); ok && strings.Contains(cmd, e.Spec) {
				return StateUp, ""
			}
		}
	}
	return StateConflict, fmt.Sprintf("pid %d (%s)", l.PID, l.Command)
}

// Rows builds the status table: managed tunnels plus ssh listeners hpctun did
// not open, sorted by port.
func (v View) Rows() []Row {
	managed := map[int]bool{}
	var rows []Row
	for _, s := range Specs(v.Config, v.State) {
		managed[s.Port] = true
		st, owner := v.Classify(s)
		r := Row{Kind: s.Kind(), Name: s.Label(), Port: s.Port, Route: string(s.Route), Target: s.Target(), State: st, Conns: -1, Owner: owner}
		if t, ok := v.Config.Lookup(s.Name); ok && s.Name != "" {
			r.URL, r.NoBrowse = t.URL, t.NoBrowse
		}
		if st != StateDown {
			r.Conns = v.Snap.Conns[s.Port]
		}
		if e, ok := v.State.ByPort(s.Port); ok {
			switch {
			case v.Gateway != "" && e.Gateway != "" && e.Gateway != v.Gateway:
				r.Warning = fmt.Sprintf("opened through %s, not the configured gateway", e.Gateway)
			case e.Route != config.RouteJump && e.ControlPath != "" && v.Socket != "" && e.ControlPath != v.Socket:
				r.Warning = fmt.Sprintf("opened through control socket %s, which the gateway no longer uses; `hpctun down %d` closes it", e.ControlPath, s.Port)
			}
		}
		rows = append(rows, r)
	}
	for port, l := range v.Snap.Listeners {
		if managed[port] || l.Command != "ssh" {
			continue
		}
		rows = append(rows, Row{Kind: KindExternal, Name: "-", Port: port, Route: "?", Target: fmt.Sprintf("ssh pid %d", l.PID), State: StateExternal, Conns: v.Snap.Conns[port]})
	}
	slices.SortFunc(rows, func(a, b Row) int { return cmp.Compare(a.Port, b.Port) })
	return rows
}

// RecordedMasters checks the master behind every control socket that
// master and socks entries were opened through, except current, which
// the caller has already checked. A failed check counts as not running.
func RecordedMasters(ctx context.Context, r sshctl.Runner, st *state.State, current string) map[string]gateway.Status {
	checked := map[string]gateway.Status{}
	if st == nil {
		return checked
	}
	for _, e := range st.Tunnels {
		// Jump tunnels carry a pid instead of a master, and an entry with no
		// recorded socket lives on the gateway as resolved now.
		if e.Route == config.RouteJump || e.Gateway == "" || e.ControlPath == "" || e.ControlPath == current {
			continue
		}
		if _, done := checked[e.ControlPath]; done {
			continue
		}
		master, err := (&gateway.Gateway{Dest: e.Gateway}).AtSocket(e.ControlPath).Check(ctx, r)
		if err != nil {
			master = gateway.Status{}
		}
		checked[e.ControlPath] = master
	}
	return checked
}
