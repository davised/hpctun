// Package tunnel works out the state of every tunnel from config, the state
// file and live probes, and opens and closes tunnels.
package tunnel

import (
	"fmt"

	"hpctun/internal/config"
	"hpctun/internal/sshctl"
	"hpctun/internal/state"
)

// Spec is one tunnel hpctun manages: named (from config) or ad-hoc (Name empty).
type Spec struct {
	Name       string
	Port       int
	Route      config.Route
	Host       string
	RemotePort int
}

func FromConfig(t config.Tunnel) Spec {
	return Spec{Name: t.Name, Port: t.Port, Route: t.Route, Host: t.Host, RemotePort: t.RemotePort}
}

func FromEntry(e state.Entry) Spec {
	return Spec{Name: e.Name, Port: e.Port, Route: e.Route, Host: e.Host, RemotePort: e.RemotePort}
}

// Tunnel converts s to a config tunnel (used when saving an ad-hoc tunnel).
func (s Spec) Tunnel() config.Tunnel {
	return config.Tunnel{Name: s.Name, Port: s.Port, Host: s.Host, RemotePort: s.RemotePort, Route: s.Route}
}

// ForwardSpec is the -L or -D argument that opens s.
func (s Spec) ForwardSpec() string {
	switch s.Route {
	case config.RouteSocks:
		return sshctl.SocksSpec(s.Port)
	case config.RouteJump:
		return sshctl.LocalSpec(s.Port, "localhost", s.RemotePort)
	default:
		return sshctl.LocalSpec(s.Port, s.Host, s.RemotePort)
	}
}

// Target is where s leads, for display.
func (s Spec) Target() string {
	if s.Route == config.RouteSocks {
		return "-"
	}
	return fmt.Sprintf("%s:%d", s.Host, s.RemotePort)
}

// Kind is KindNamed for a config tunnel, else KindAdhoc.
func (s Spec) Kind() Kind {
	if s.Name == "" {
		return KindAdhoc
	}
	return KindNamed
}

// Label is the name column: the tunnel name, or "(ad-hoc)".
func (s Spec) Label() string {
	if s.Name == "" {
		return "(ad-hoc)"
	}
	return s.Name
}
