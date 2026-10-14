// Package config locates, loads, validates and appends to hpctun's TOML config.
package config

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/BurntSushi/toml"
)

// Route is how a tunnel reaches its target.
type Route string

const (
	RouteMaster Route = "master" // forward added to the gateway master
	RouteJump   Route = "jump"   // own ssh process to the target, through the gateway
	RouteSocks  Route = "socks"  // dynamic SOCKS proxy on the gateway master
)

// Tunnel is one named tunnel with defaults applied.
type Tunnel struct {
	Name       string
	Port       int    // local port; for socks, the SOCKS port
	Host       string // target host; empty for socks
	RemotePort int    // port on Host; 0 for socks
	Route      Route
	URL        string // page `hpctun browse` opens; "" for none
	NoBrowse   bool   // browse = false: no page to open (an API for tools)
}

// Config is a loaded config file.
type Config struct {
	Path          string // file read, or the file that would be written
	Exists        bool
	Gateway       string
	TunnelGateway string // tunnel_gateway; "" means tunnels use Gateway
	DefaultHost   string
	Tunnels       []Tunnel // sorted by name
	SSH           *SSH     // nil when the file has no [ssh] table
	Code          *Code    // nil when the file has no [code] table
	VSCode        *VSCode  // nil when the file has no [vscode] table
}

// TunnelDest is the ssh destination tunnels go through: tunnel_gateway when
// set, else the gateway.
func (c *Config) TunnelDest() string { return cmp.Or(c.TunnelGateway, c.Gateway) }

// Lookup returns the tunnel with the given name.
func (c *Config) Lookup(name string) (Tunnel, bool) {
	for _, t := range c.Tunnels {
		if t.Name == name {
			return t, true
		}
	}
	return Tunnel{}, false
}

// ByPort returns the tunnel listening on the given local port.
func (c *Config) ByPort(port int) (Tunnel, bool) {
	for _, t := range c.Tunnels {
		if t.Port == port {
			return t, true
		}
	}
	return Tunnel{}, false
}

// ValidationError names the tunnel and field that make a config invalid.
type ValidationError struct {
	Tunnel string // empty for top-level fields
	Field  string
	Msg    string
}

func (e *ValidationError) Error() string {
	if e.Tunnel == "" {
		return fmt.Sprintf("%s: %s", e.Field, e.Msg)
	}
	return fmt.Sprintf("tunnel %q: %s: %s", e.Tunnel, e.Field, e.Msg)
}

type rawTunnel struct {
	Port       int    `toml:"port"`
	RemotePort int    `toml:"remote_port"`
	Host       string `toml:"host"`
	Route      string `toml:"route"`
	Socks      int    `toml:"socks"`
	URL        string `toml:"url"`
	Browse     *bool  `toml:"browse"`
}

type rawConfig struct {
	Gateway       string               `toml:"gateway"`
	TunnelGateway string               `toml:"tunnel_gateway"`
	DefaultHost   string               `toml:"default_host"`
	Tunnel        map[string]rawTunnel `toml:"tunnel"`
	SSH           *rawSSH              `toml:"ssh"`
	Code          *rawCode             `toml:"code"`
	VSCode        *rawVSCode           `toml:"vscode"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9_][a-z0-9_-]*$`)

// ValidateName reports whether name is usable as a tunnel name.
func ValidateName(name string) error {
	if !nameRE.MatchString(name) {
		return errors.New("must match [a-z0-9_-]+ and not start with -")
	}
	return nil
}

// ValidateURL accepts an absolute http or https URL with a host: a page
// hpctun browse can open.
func ValidateURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("%q is not an http or https URL", s)
	}
	return nil
}

// ValidPort reports whether p is a usable TCP port.
func ValidPort(p int) bool { return p >= 1 && p <= 65535 }

// Name is the tool's name, used for its config and state directories;
// LegacyName is the name before the rename, read only to migrate.
const (
	Name       = "hpctun"
	LegacyName = "tun"
)

// Locate returns the config path: flagPath, then $HPCTUN_CONFIG, then
// $TUN_CONFIG (the name before the rename), then config.toml in Dir. The
// file need not exist.
func Locate(flagPath string, getenv func(string) string, goos string) (string, error) {
	if flagPath != "" {
		return flagPath, nil
	}
	if p := cmp.Or(getenv("HPCTUN_CONFIG"), getenv("TUN_CONFIG")); p != "" {
		return p, nil
	}
	d, err := Dir(getenv, goos)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.toml"), nil
}

// Dir is $XDG_CONFIG_HOME/hpctun, else %APPDATA%\hpctun on Windows (goos
// "windows"), else ~/.config/hpctun.
func Dir(getenv func(string) string, goos string) (string, error) { return dir(getenv, goos, Name) }

// LegacyDir is Dir under the name before the rename.
func LegacyDir(getenv func(string) string, goos string) (string, error) {
	return dir(getenv, goos, LegacyName)
}

func dir(getenv func(string) string, goos, name string) (string, error) {
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, name), nil
	}
	if goos == "windows" {
		appdata := getenv("APPDATA")
		if appdata == "" {
			return "", errors.New("cannot locate config: APPDATA is not set")
		}
		return filepath.Join(appdata, name), nil
	}
	home := getenv("HOME")
	if home == "" {
		return "", errors.New("cannot locate config: HOME is not set")
	}
	return filepath.Join(home, ".config", name), nil
}

// Load reads and validates the config at path. A missing file is not an
// error: it yields an empty config with Exists false.
func Load(path string) (*Config, error) { return load(path, false) }

// LoadSeed is Load for a file that only seeds hpctun init --from, such as a
// site file shipped with the release: its [ssh] table may leave out user,
// even with [code], since the wizard asks for it. Every other check still
// applies.
func LoadSeed(path string) (*Config, error) { return load(path, true) }

func load(path string, seed bool) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{Path: path}, nil
	}
	if err != nil {
		return nil, err
	}
	c, err := parse(data, seed)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Path, c.Exists = path, true
	return c, nil
}

// Parse decodes and validates config TOML.
func Parse(data []byte) (*Config, error) { return parse(data, false) }

func parse(data []byte, seed bool) (*Config, error) {
	var raw rawConfig
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return nil, err
	}
	if und := md.Undecoded(); len(und) > 0 {
		key := und[0]
		if len(key) == 3 && key[0] == "tunnel" {
			return nil, &ValidationError{Tunnel: key[1], Field: key[2], Msg: "unknown field"}
		}
		return nil, &ValidationError{Field: key.String(), Msg: "unknown field"}
	}
	// Checked whether or not there is an [ssh] table: these values reach
	// ssh's command line either way.
	for _, f := range []struct{ field, v string }{{"gateway", raw.Gateway}, {"tunnel_gateway", raw.TunnelGateway}, {"default_host", raw.DefaultHost}} {
		if f.v == "" {
			continue
		}
		if err := ValidateDest(f.v); err != nil {
			return nil, &ValidationError{Field: f.field, Msg: err.Error()}
		}
	}
	s, err := sshFromRaw(raw.Gateway, raw.SSH, seed)
	if err != nil {
		return nil, err
	}
	if err := validateTunnelGateway(raw.Gateway, raw.TunnelGateway, s); err != nil {
		return nil, err
	}
	code, err := codeFromRaw(raw.Code, md.IsDefined("code"), md.IsDefined("code", "server_dirs"), s, seed)
	if err != nil {
		return nil, err
	}
	vs, err := vscodeFromRaw(raw.VSCode, md.IsDefined("vscode"))
	if err != nil {
		return nil, err
	}
	c := &Config{Gateway: raw.Gateway, TunnelGateway: raw.TunnelGateway, DefaultHost: raw.DefaultHost, SSH: s, Code: code, VSCode: vs}
	byPort := map[int]string{}
	for _, name := range slices.Sorted(maps.Keys(raw.Tunnel)) {
		t, err := resolve(name, raw.Tunnel[name], raw.DefaultHost)
		if err != nil {
			return nil, err
		}
		if other, dup := byPort[t.Port]; dup {
			field := "port"
			if t.Route == RouteSocks {
				field = "socks"
			}
			return nil, &ValidationError{Tunnel: name, Field: field, Msg: fmt.Sprintf("local port %d is also used by tunnel %q", t.Port, other)}
		}
		byPort[t.Port] = name
		c.Tunnels = append(c.Tunnels, t)
	}
	return c, nil
}

// validateTunnelGateway checks tunnel_gateway against the gateway and the
// [ssh] table s (nil when absent).
func validateTunnelGateway(gateway, tg string, s *SSH) error {
	bad := func(field, msg string) error { return &ValidationError{Field: field, Msg: msg} }
	if tg == "" {
		if s != nil && s.TunnelGatewayHost != "" {
			return bad("ssh.tunnel_gateway_host", "requires tunnel_gateway")
		}
		return nil
	}
	switch {
	case gateway == "":
		return bad("tunnel_gateway", "requires gateway")
	case tg == gateway:
		return bad("tunnel_gateway", "must differ from gateway; leave it out for tunnels to use the gateway")
	case s == nil:
		return nil
	case s.Transfer != "" && tg == s.Transfer:
		return bad("tunnel_gateway", "must differ from ssh.transfer")
	case s.TunnelGatewayHost != "" && ValidateAlias(tg) != nil:
		return bad("tunnel_gateway", "must be an ssh alias (no user@, spaces or patterns) when ssh.tunnel_gateway_host is set")
	}
	return nil
}

func resolve(name string, r rawTunnel, defaultHost string) (Tunnel, error) {
	bad := func(field, msg string) (Tunnel, error) {
		return Tunnel{}, &ValidationError{Tunnel: name, Field: field, Msg: msg}
	}
	if err := ValidateName(name); err != nil {
		return bad("name", err.Error())
	}
	if r.URL != "" {
		if err := ValidateURL(r.URL); err != nil {
			return bad("url", err.Error())
		}
	}
	noBrowse := r.Browse != nil && !*r.Browse
	if noBrowse && r.URL != "" {
		return bad("browse", "cannot be false on a tunnel with a url")
	}
	switch {
	case r.Port != 0 && r.Socks != 0:
		return bad("socks", "cannot be combined with port")
	case r.Port == 0 && r.Socks == 0:
		return bad("port", "one of port or socks is required")
	}
	if r.Socks != 0 {
		switch {
		case r.Route != "":
			return bad("route", "not allowed on a socks tunnel")
		case r.Host != "":
			return bad("host", "not allowed on a socks tunnel")
		case r.RemotePort != 0:
			return bad("remote_port", "not allowed on a socks tunnel")
		case !ValidPort(r.Socks):
			return bad("socks", "must be between 1 and 65535")
		}
		return Tunnel{Name: name, Port: r.Socks, Route: RouteSocks, URL: r.URL, NoBrowse: noBrowse}, nil
	}
	if !ValidPort(r.Port) {
		return bad("port", "must be between 1 and 65535")
	}
	if r.RemotePort != 0 && !ValidPort(r.RemotePort) {
		return bad("remote_port", "must be between 1 and 65535")
	}
	t := Tunnel{
		Name:       name,
		Port:       r.Port,
		Host:       cmp.Or(r.Host, defaultHost),
		RemotePort: cmp.Or(r.RemotePort, r.Port),
		Route:      RouteMaster,
		URL:        r.URL,
		NoBrowse:   noBrowse,
	}
	switch r.Route {
	case "", string(RouteMaster):
	case string(RouteJump):
		t.Route = RouteJump
	default:
		return bad("route", fmt.Sprintf("must be master or jump, not %q", r.Route))
	}
	if t.Host == "" {
		return bad("host", "required when default_host is not set")
	}
	if err := ValidateDest(t.Host); err != nil {
		return bad("host", err.Error())
	}
	return t, nil
}
