package prompt

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"hpctun/internal/config"
)

// Preset holds values already given on the command line. Zero values are
// unset; Route is "" when unset.
type Preset struct {
	Name       string
	Port       int
	Socks      bool
	Host       string
	RemotePort int
	Route      config.Route
	URL        string // page hpctun browse opens
}

// FormOpts configures TunnelForm.
type FormOpts struct {
	DefaultHost string
	NameTaken   func(string) bool
	PortTaken   func(int) error // non-nil error explains why the port is unusable
	// Full asks every unset field, including optional ones with defaults.
	// Otherwise only required fields that are missing are asked.
	Full bool
	// Context is shown above the questions; the form adds a line for the
	// tunnel being defined.
	Context Context
	// Hosts complete the target host question.
	Hosts []string
}

const loopbackHelp = "Answer yes if the service only accepts connections from its own host " +
	"(it listens on 127.0.0.1 or localhost there). Check on the remote host with: ss -ltn | grep <port>"

// TunnelForm asks for the tunnel fields not already in p. Esc on a question
// returns to the previous one; Esc on the first returns ErrBack.
func TunnelForm(a Asker, p Preset, o FormOpts) (config.Tunnel, error) {
	socks := p.Socks
	var name, host, remotePort string // answers as typed; blank takes the default
	port := p.Port
	loopback := p.Route == config.RouteJump
	page := p.URL

	show := func() {
		d := config.Tunnel{Name: name, Port: port, Host: host}
		if p.Name != "" {
			d.Name = p.Name
		}
		a.SetContext(o.Context.With(Line{"New tunnel", draft(d, socks)}))
	}
	err := runSteps(
		func() (bool, error) {
			if !o.Full || p.Socks || p.Port != 0 || p.Route != "" {
				return false, nil
			}
			show()
			def := "port"
			if socks {
				def = "socks"
			}
			kind, err := a.Select(SelectQ{
				Title: "What kind of tunnel?",
				Description: "A port forward makes one service on a remote host (Jupyter, a web dashboard) " +
					"reachable on a port on this machine.\nA SOCKS proxy sends a browser's traffic through the " +
					"gateway, so any internal site works.",
				Options: []Option{
					{Label: "Port forward (local port → host:port)", Value: "port"},
					{Label: "SOCKS proxy", Value: "socks"},
				},
				Default: def,
			})
			if err == nil {
				socks = kind == "socks"
			}
			return true, err
		},
		func() (bool, error) {
			if p.Name != "" {
				return false, nil
			}
			show()
			v, err := a.Input(InputQ{
				Title:       "Name",
				Description: "You open it with `hpctun up <name>`. Lowercase letters, digits, - and _.",
				Value:       name,
				Validate: func(s string) error {
					if err := config.ValidateName(s); err != nil {
						return err
					}
					if o.NameTaken(s) {
						return fmt.Errorf("a tunnel named %q already exists", s)
					}
					return nil
				},
			})
			if err == nil {
				name = v
			}
			return true, err
		},
		func() (bool, error) {
			if p.Port != 0 {
				return false, nil
			}
			show()
			q := InputQ{Title: "Local port", Description: "The port on this machine: you will browse to localhost:<port>. 1-65535."}
			if socks {
				q = InputQ{Title: "Local SOCKS port", Description: "Set your browser's SOCKS5 proxy to localhost:<port>. 1-65535."}
			}
			if port != 0 {
				q.Value = strconv.Itoa(port)
			}
			v, err := askPort(a, q, o.PortTaken)
			if err == nil {
				port = v
			}
			return true, err
		},
		func() (bool, error) {
			if socks || p.Host != "" || (!o.Full && o.DefaultHost != "") {
				return false, nil
			}
			show()
			desc := "The host running the service, as the gateway reaches it (for example a compute node)."
			if o.DefaultHost != "" {
				desc += " Blank means " + o.DefaultHost + "."
			}
			v, err := a.Input(InputQ{
				Title: "Target host", Description: desc, Default: o.DefaultHost, Value: host, Suggestions: o.Hosts,
				Validate: func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("a host is required")
					}
					return config.ValidateDest(strings.TrimSpace(s))
				},
			})
			if err == nil {
				host = v
			}
			return true, err
		},
		func() (bool, error) {
			if socks || p.RemotePort != 0 || !o.Full {
				return false, nil
			}
			show()
			v, err := a.Input(InputQ{
				Title:       "Remote port",
				Description: fmt.Sprintf("The port the service listens on there. Blank means %d, the same as the local port.", port),
				Default:     strconv.Itoa(port),
				Value:       remotePort,
				Validate:    validPort(nil),
			})
			if err == nil {
				if v == strconv.Itoa(port) {
					v = "" // follow the local port if the user goes back and changes it
				}
				remotePort = v
			}
			return true, err
		},
		func() (bool, error) {
			if socks || p.Route != "" || !o.Full {
				return false, nil
			}
			show()
			v, err := a.Confirm("Does the service listen only on localhost on the remote host?", loopbackHelp, loopback)
			if err == nil {
				loopback = v
			}
			return true, err
		},
		func() (bool, error) {
			if p.URL != "" || !o.Full {
				return false, nil
			}
			show()
			v, err := a.Input(InputQ{
				Title:       "Page to open (optional)",
				Description: "What `hpctun browse <name>` opens. Blank: localhost:<port> for a port forward, an empty window for a SOCKS proxy.",
				Value:       page,
				Validate: func(s string) error {
					if strings.TrimSpace(s) == "" {
						return nil
					}
					return config.ValidateURL(strings.TrimSpace(s))
				},
			})
			if err == nil {
				page = strings.TrimSpace(v)
			}
			return true, err
		},
	)
	if err != nil {
		return config.Tunnel{}, err
	}

	t := config.Tunnel{Name: p.Name, Port: port, Host: p.Host, RemotePort: p.RemotePort, Route: p.Route}
	if t.Name == "" {
		t.Name = name
	}
	if socks {
		return config.Tunnel{Name: t.Name, Port: t.Port, Route: config.RouteSocks, URL: page}, nil
	}
	if t.Host == "" {
		t.Host = host
		if t.Host == "" {
			t.Host = o.DefaultHost
		}
	}
	if t.RemotePort == 0 {
		t.RemotePort = t.Port
		if remotePort != "" {
			t.RemotePort, _ = strconv.Atoi(remotePort)
		}
	}
	if t.Route == "" {
		t.Route = config.RouteMaster
		if loopback {
			t.Route = config.RouteJump
		}
	}
	t.URL = page
	return t, nil
}

// draft describes a tunnel that is still being defined, for the recap.
func draft(t config.Tunnel, socks bool) string {
	var parts []string
	if t.Name != "" {
		parts = append(parts, t.Name)
	}
	if socks {
		parts = append(parts, "SOCKS proxy")
	} else {
		parts = append(parts, "port forward")
	}
	if t.Port != 0 {
		p := fmt.Sprintf("localhost:%d", t.Port)
		if !socks && t.Host != "" {
			p += " → " + t.Host
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "  ")
}

func validPort(taken func(int) error) func(string) error {
	return func(s string) error {
		p, err := strconv.Atoi(s)
		if err != nil || !config.ValidPort(p) {
			return errors.New("enter a port between 1 and 65535")
		}
		if taken != nil {
			return taken(p)
		}
		return nil
	}
}

func askPort(a Asker, q InputQ, taken func(int) error) (int, error) {
	q.Validate = validPort(taken)
	s, err := a.Input(q)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}

// SaveName asks whether to keep an ad-hoc tunnel. Blank means no.
func SaveName(a Asker, nameTaken func(string) bool) (string, error) {
	return a.Input(InputQ{
		Title:       "Save as a named tunnel? Name (blank to skip):",
		Description: "",
		Validate: func(s string) error {
			if s == "" {
				return nil
			}
			if err := config.ValidateName(s); err != nil {
				return err
			}
			if nameTaken(s) {
				return fmt.Errorf("a tunnel named %q already exists", s)
			}
			return nil
		},
	})
}
