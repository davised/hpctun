package tray

import (
	"fmt"
	"strings"

	"hpctun/internal/jsondoc"
)

// Icon is the state the menu-bar icon shows.
type Icon int

const (
	IconOff     Icon = iota // the gateway is not connected
	IconPartial             // connected, but a named tunnel is down or held by another program
	IconOn                  // connected, every named tunnel up
	IconError               // no gateway, or status failed
)

// Item is one tunnel in the menu.
type Item struct {
	Port     int
	Label    string
	Checked  bool // the tunnel is up
	Disabled bool // busy, or its port is held by another program
}

// Page is an "Open … in browser" line: a tunnel with a page to open.
type Page struct {
	Port     int
	Label    string
	Disabled bool // busy, or its port is held by another program
}

// Model is everything the menu shows.
type Model struct {
	Icon       Icon
	Header     string   // first line: the gateway's state
	Conns      []string // a line per other shared connection: login gateway, transfer host
	Tooltip    string
	CanLogin   bool
	CanLogout  bool
	Tunnels    []Item
	CanOpenAll bool
	Pages      []Page // tunnels hpctun browse can open
	Code       string // the code job line; "" hides it
	LastError  string // "" hides it
	Config     string // config file to open; "" disables Open config
}

// state is what the controller knows, from which Build makes a Model.
type state struct {
	status    *jsondoc.Status
	statusErr error
	code      *jsondoc.Code
	busy      map[int]bool
	loginBusy bool
	lastErr   string
}

// maxLine keeps menu lines from stretching across the screen.
const maxLine = 90

// clip makes s one line of at most maxLine characters.
func clip(s string) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "   ", "\x00")), " ")
	s = strings.ReplaceAll(s, "\x00", "   ")
	if r := []rune(s); len(r) > maxLine {
		return string(r[:maxLine-1]) + "…"
	}
	return s
}

func build(s state) Model {
	m := Model{LastError: clip(s.lastErr)}
	st := s.status
	switch {
	case s.statusErr != nil:
		m.Icon, m.Header = IconError, clip("hpctun: "+s.statusErr.Error())
	case st == nil:
		m.Icon, m.Header = IconOff, "hpctun: checking…"
	case st.Gateway == nil:
		m.Icon, m.Header = IconError, "No gateway configured: run hpctun init"
	case st.Gateway.Error != nil:
		m.Icon, m.Header = IconError, clip(st.Gateway.Dest+": "+*st.Gateway.Error)
	case st.Gateway.Mode != nil && *st.Gateway.Mode == "standalone":
		// No connection to log in to: each tunnel signs in by itself.
		m.Icon, m.Header = standaloneSummary(st)
	case !st.Gateway.Running:
		m.Icon, m.Header = IconOff, st.Gateway.Dest+": not connected"
		m.CanLogin = !s.loginBusy
	default:
		m.Icon, m.Header = IconOn, st.Gateway.Dest+": connected"
		m.CanLogout = !s.loginBusy
	}
	if s.loginBusy {
		m.Header += " …"
	}
	m.Tooltip = "hpctun — " + m.Header
	if st == nil {
		return m
	}
	if st.Gateway != nil && st.Gateway.Error == nil {
		for _, c := range []*jsondoc.Conn{st.Login, st.Transfer} {
			if c == nil {
				continue
			}
			m.Conns = append(m.Conns, connLine(c))
			if c.Shared && !c.Connected && c.Error == nil && !s.loginBusy {
				m.CanLogin = true
			}
		}
	}
	m.Config = st.Config
	for _, r := range st.Tunnels {
		if r.Kind == "external" {
			continue // not ours to open or close
		}
		it := Item{Port: r.Port, Label: label(r), Checked: r.State == "up"}
		if r.Kind == "named" && r.State != "up" && m.Icon == IconOn {
			m.Icon = IconPartial
		}
		if r.State == "conflict" {
			it.Disabled = true
			if m.Icon == IconOn {
				m.Icon = IconPartial
			}
		}
		if s.busy[r.Port] {
			it.Label += " …"
			it.Disabled = true
		}
		m.Tunnels = append(m.Tunnels, it)
		browsable := r.Browse == nil || *r.Browse
		if browsable && (r.Route == nil || *r.Route != "socks" || r.URL != nil) {
			name := fmt.Sprintf(":%d", r.Port)
			if r.Name != nil {
				name = *r.Name
			}
			m.Pages = append(m.Pages, Page{Port: r.Port, Label: "Open " + name + " in browser", Disabled: it.Disabled})
		}
		m.CanOpenAll = true
	}
	m.Code = codeLine(s.code)
	return m
}

// standaloneSummary is the icon and header when each tunnel is its own
// ssh (Windows): how many of hpctun's tunnels are up.
func standaloneSummary(st *jsondoc.Status) (Icon, string) {
	total, up := 0, 0
	for _, r := range st.Tunnels {
		if r.Kind == "external" {
			continue
		}
		total++
		if r.State == "up" {
			up++
		}
	}
	dest := st.Gateway.Dest
	switch {
	case total == 0:
		return IconOff, dest + ": no tunnels"
	case up == total:
		return IconOn, fmt.Sprintf("%s: %d of %d tunnels up", dest, up, total)
	case up > 0:
		return IconPartial, fmt.Sprintf("%s: %d of %d tunnels up", dest, up, total)
	}
	return IconOff, fmt.Sprintf("%s: %d of %d tunnels up", dest, up, total)
}

func label(r jsondoc.Row) string {
	name := "(ad-hoc)"
	if r.Name != nil {
		name = *r.Name
	}
	var where string
	switch {
	case r.Route != nil && *r.Route == "socks":
		where = fmt.Sprintf(":%d SOCKS proxy", r.Port)
	case r.Target != nil:
		where = fmt.Sprintf(":%d → %s", r.Port, *r.Target)
	default:
		where = fmt.Sprintf(":%d", r.Port)
	}
	l := name + "   " + where
	if r.State == "conflict" && r.Owner != nil {
		l += " (port held by " + *r.Owner + ")"
	}
	return clip(l)
}

// connLine is a shared connection's line, as hpctun status words it.
func connLine(c *jsondoc.Conn) string {
	switch {
	case c.Error != nil:
		return clip(c.Alias + ": error: " + *c.Error)
	case !c.Shared:
		return c.Alias + ": logins not shared (no ControlMaster)"
	case c.Connected:
		return c.Alias + ": connected"
	}
	return c.Alias + ": not connected"
}

func codeLine(c *jsondoc.Code) string {
	if c == nil {
		return ""
	}
	switch c.State {
	case "none":
		return "Code job: none"
	case "unknown":
		return "Code job: unknown (not connected)"
	case "error":
		msg := "unknown error"
		if c.Error != nil {
			msg = *c.Error
		}
		return clip("Code job: error: " + msg)
	}
	if c.Job == nil {
		return ""
	}
	j := c.Job
	if j.State == "RUNNING" {
		return clip(fmt.Sprintf("Code job: running on %s, %s left", j.Node, j.TimeLeft))
	}
	l := "Code job: " + strings.ToLower(j.State)
	if j.Reason != "" {
		l += " (" + j.Reason + ")"
	}
	return clip(l)
}
