package main

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"hpctun/internal/gateway"
	"hpctun/internal/slurmjob"
	"hpctun/internal/tunnel"
)

// header is the tunnel gateway's line above the status table, and the
// lines for the other shared connections.
type header struct {
	Config   string // the config file's path (--json only)
	Dest     string
	Tunnels  bool // Dest is a tunnel_gateway apart from the gateway
	Mode     gateway.Mode
	Status   gateway.Status
	Err      error
	Hints    []string
	Login    *conn     // the gateway, when Tunnels
	Transfer *conn     // nil when no transfer host is configured
	Code     *codeLine // nil when [code] is not set
}

// gatewayConnected reports whether the gateway itself (not a tunnel gateway)
// is connected; hpctun code runs its commands there.
func (h header) gatewayConnected() bool {
	if h.Login != nil {
		return h.Login.Err == nil && h.Login.Status.Running
	}
	return h.Err == nil && h.Status.Running
}

func (h header) String() string {
	label := "gateway"
	if h.Tunnels {
		label = "tunnels"
	}
	switch {
	case h.Dest == "":
		return "gateway: not configured"
	case h.Err != nil:
		return fmt.Sprintf("%s %s: error: %v", label, h.Dest, h.Err)
	case h.Mode == gateway.ModeStandalone:
		return fmt.Sprintf("%s %s: no shared connection; each tunnel signs in by itself", label, h.Dest)
	case h.Status.Running:
		return fmt.Sprintf("%s %s: running (pid %d, %s)", label, h.Dest, h.Status.PID, h.Mode)
	default:
		return fmt.Sprintf("%s %s: not running (%s)", label, h.Dest, h.Mode)
	}
}

// conn is a shared connection's line under the header: the transfer host,
// or the gateway when tunnels have their own.
type conn struct {
	Role   string // "transfer" or "gateway"
	Alias  string
	Mode   gateway.Mode
	Status gateway.Status
	Err    error
}

func (c conn) String() string {
	switch {
	case c.Err != nil:
		return fmt.Sprintf("%s %s: error: %v", c.Role, c.Alias, c.Err)
	case c.Mode == gateway.ModeStandalone:
		return fmt.Sprintf("%s %s: no shared connection; each connection signs in by itself", c.Role, c.Alias)
	case c.Mode == gateway.ModeManaged && c.Role == "transfer":
		return fmt.Sprintf("transfer %s: no ControlMaster in your ssh config, so logins are not shared", c.Alias)
	case c.Status.Running:
		return fmt.Sprintf("%s %s: connected (pid %d)", c.Role, c.Alias, c.Status.PID)
	default:
		return fmt.Sprintf("%s %s: not connected (`hpctun login %s`)", c.Role, c.Alias, c.Alias)
	}
}

// codeLine is hpctun code's job, under the gateway line.
type codeLine struct {
	Job          *slurmjob.Job
	None         bool
	NotConnected bool
	Err          error
}

func (c codeLine) String() string {
	switch {
	case c.NotConnected:
		return "code job: unknown (gateway not connected)"
	case c.Err != nil:
		return fmt.Sprintf("code job: error: %v", c.Err)
	case c.None || c.Job == nil:
		return "code job: none"
	}
	return "code " + jobLine(*c.Job)
}

var stateColor = map[tunnel.State]string{
	tunnel.StateUp:       "2", // green
	tunnel.StateDown:     "8", // grey
	tunnel.StateConflict: "1", // red
	tunnel.StateExternal: "3", // yellow
}

// renderStatus draws the header, the table and notes for conflicts and
// warnings. Colours are dropped by lipgloss.Fprint when output is not a
// terminal.
func renderStatus(h header, rows []tunnel.Row) string {
	var b strings.Builder
	b.WriteString(h.String())
	b.WriteString("\n")
	if h.Login != nil {
		b.WriteString(h.Login.String() + "\n")
	}
	if h.Transfer != nil {
		b.WriteString(h.Transfer.String() + "\n")
	}
	if h.Code != nil {
		b.WriteString(h.Code.String() + "\n")
	}
	for _, hint := range h.Hints {
		fmt.Fprintf(&b, "  hint: %s\n", hint)
	}
	b.WriteString("\n")
	if len(rows) == 0 {
		b.WriteString("no tunnels\n")
		return b.String()
	}
	cell := lipgloss.NewStyle().PaddingRight(2)
	t := table.New().
		Border(lipgloss.HiddenBorder()).
		BorderTop(false).BorderBottom(false).BorderLeft(false).BorderRight(false).
		BorderColumn(false).BorderHeader(false).
		Headers("NAME", "PORT", "ROUTE", "TARGET", "STATE", "CONNS")
	for _, r := range rows {
		conns := "-"
		if r.Conns >= 0 {
			conns = strconv.Itoa(r.Conns)
		}
		t.Row(r.Name, strconv.Itoa(r.Port), r.Route, r.Target, string(r.State), conns)
	}
	t.StyleFunc(func(row, col int) lipgloss.Style {
		if row == table.HeaderRow {
			return cell.Bold(true)
		}
		if col == 4 {
			return cell.Foreground(lipgloss.Color(stateColor[rows[row].State]))
		}
		return cell
	})
	b.WriteString(t.Render())
	b.WriteString("\n")
	var notes []string
	for _, r := range rows {
		if r.State == tunnel.StateConflict {
			notes = append(notes, fmt.Sprintf("  %d: held by %s", r.Port, r.Owner))
		}
		if r.Warning != "" {
			notes = append(notes, fmt.Sprintf("  %d: %s", r.Port, r.Warning))
		}
	}
	if len(notes) > 0 {
		b.WriteString("\n" + strings.Join(notes, "\n") + "\n")
	}
	return b.String()
}
