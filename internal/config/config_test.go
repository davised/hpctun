package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `
gateway = "gw.example.org"
default_host = "node1"

[tunnel.jupyter]
port = 4001
route = "jump"

[tunnel.dash]
port = 8099

[tunnel.web]
port = 43723
host = "web1"
remote_port = 80

[tunnel.socks]
socks = 5005
`

func TestParseAppliesDefaults(t *testing.T) {
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if c.Gateway != "gw.example.org" || c.DefaultHost != "node1" {
		t.Fatalf("top-level = %q %q", c.Gateway, c.DefaultHost)
	}
	want := []Tunnel{
		{Name: "dash", Port: 8099, Host: "node1", RemotePort: 8099, Route: RouteMaster},
		{Name: "jupyter", Port: 4001, Host: "node1", RemotePort: 4001, Route: RouteJump},
		{Name: "socks", Port: 5005, Route: RouteSocks},
		{Name: "web", Port: 43723, Host: "web1", RemotePort: 80, Route: RouteMaster},
	}
	if len(c.Tunnels) != len(want) {
		t.Fatalf("got %d tunnels, want %d: %+v", len(c.Tunnels), len(want), c.Tunnels)
	}
	for i := range want {
		if c.Tunnels[i] != want[i] {
			t.Errorf("tunnel %d = %+v, want %+v", i, c.Tunnels[i], want[i])
		}
	}
}

func TestParseValidation(t *testing.T) {
	cases := []struct {
		name, toml, tunnel, field string
	}{
		{"port and socks", "[tunnel.a]\nport = 1\nsocks = 2\n", "a", "socks"},
		{"neither", "[tunnel.a]\nhost = \"h\"\n", "a", "port"},
		{"bad route", "[tunnel.a]\nport = 1\nhost = \"h\"\nroute = \"tcp\"\n", "a", "route"},
		{"duplicate port", "[tunnel.a]\nport = 1\nhost = \"h\"\n[tunnel.b]\nport = 1\nhost = \"h\"\n", "b", "port"},
		{"route on socks", "[tunnel.a]\nsocks = 1\nroute = \"jump\"\n", "a", "route"},
		{"host on socks", "[tunnel.a]\nsocks = 1\nhost = \"h\"\n", "a", "host"},
		{"remote_port on socks", "[tunnel.a]\nsocks = 1\nremote_port = 2\n", "a", "remote_port"},
		{"no host", "[tunnel.a]\nport = 1\n", "a", "host"},
		{"port too big", "[tunnel.a]\nport = 70000\nhost = \"h\"\n", "a", "port"},
		{"negative remote", "[tunnel.a]\nport = 1\nhost = \"h\"\nremote_port = -3\n", "a", "remote_port"},
		{"bad name", "[tunnel.Jupyter]\nport = 1\nhost = \"h\"\n", "Jupyter", "name"},
		{"gateway option", "gateway = \"-oProxyCommand=x\"\n", "", "gateway"},
		{"gateway newline", "gateway = \"gw\\nHost *\"\n", "", "gateway"},
		{"tunnel_gateway option", "gateway = \"gw\"\ntunnel_gateway = \"-oProxyCommand=x\"\n", "", "tunnel_gateway"},
		{"default_host option", "default_host = \"-oProxyCommand=x\"\n", "", "default_host"},
		{"host option", "[tunnel.a]\nport = 1\nhost = \"-oProxyCommand=x\"\n", "a", "host"},
		{"host with space", "[tunnel.a]\nport = 1\nhost = \"a b\"\n", "a", "host"},
		{"name starting with -", "[tunnel.-x]\nport = 1\nhost = \"h\"\n", "-x", "name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.toml))
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want *ValidationError", err)
			}
			if ve.Tunnel != c.tunnel || ve.Field != c.field {
				t.Fatalf("got tunnel %q field %q (%v), want %q %q", ve.Tunnel, ve.Field, err, c.tunnel, c.field)
			}
		})
	}
}

// Review Focus 5: a typo in a shared config must fail loudly.
func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte("default_host = \"h\"\n[tunnel.web]\nport = 1\nremote-port = 80\n"))
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Tunnel != "web" || ve.Field != "remote-port" {
		t.Fatalf("err = %v, want unknown field remote-port on tunnel web", err)
	}
	_, err = Parse([]byte("gatway = \"x\"\n"))
	if !errors.As(err, &ve) || ve.Field != "gatway" {
		t.Fatalf("err = %v, want unknown top-level field gatway", err)
	}
}

func TestLookupAndByPort(t *testing.T) {
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if tn, ok := c.Lookup("web"); !ok || tn.Port != 43723 {
		t.Fatalf("Lookup(web) = %+v %v", tn, ok)
	}
	if tn, ok := c.ByPort(5005); !ok || tn.Name != "socks" {
		t.Fatalf("ByPort(5005) = %+v %v", tn, ok)
	}
	if _, ok := c.Lookup("nope"); ok {
		t.Fatal("Lookup(nope) found something")
	}
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLocate(t *testing.T) {
	cases := []struct {
		name, flag string
		env        map[string]string
		want       string
	}{
		{"flag wins", "/f.toml", map[string]string{"HPCTUN_CONFIG": "/e.toml", "HOME": "/h"}, "/f.toml"},
		{"env", "", map[string]string{"HPCTUN_CONFIG": "/e.toml", "HOME": "/h"}, "/e.toml"},
		{"xdg", "", map[string]string{"XDG_CONFIG_HOME": "/x", "HOME": "/h"}, "/x/hpctun/config.toml"},
		{"home", "", map[string]string{"HOME": "/h"}, "/h/.config/hpctun/config.toml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Locate(c.flag, env(c.env), "darwin")
			if err != nil || got != c.want {
				t.Fatalf("Locate = %q, %v; want %q", got, err, c.want)
			}
		})
	}
	if _, err := Locate("", env(map[string]string{}), "darwin"); err == nil {
		t.Fatal("Locate with no HOME succeeded")
	}
}

func TestLocateNames(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		name string
		env  map[string]string
		goos string
		want string
	}{
		{"new env wins", map[string]string{"HPCTUN_CONFIG": "/a", "TUN_CONFIG": "/b", "HOME": "/h"}, "linux", "/a"},
		{"old env fallback", map[string]string{"TUN_CONFIG": "/b", "HOME": "/h"}, "linux", "/b"},
		{"xdg", map[string]string{"XDG_CONFIG_HOME": "/x", "HOME": "/h"}, "linux", filepath.Join("/x", "hpctun", "config.toml")},
		{"home", map[string]string{"HOME": "/h"}, "darwin", filepath.Join("/h", ".config", "hpctun", "config.toml")},
		{"appdata", map[string]string{"APPDATA": `C:\A`}, "windows", filepath.Join(`C:\A`, "hpctun", "config.toml")},
	} {
		got, err := Locate("", env(c.env), c.goos)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	old, err := LegacyDir(env(map[string]string{"HOME": "/h"}), "linux")
	if err != nil || old != filepath.Join("/h", ".config", "tun") {
		t.Errorf("LegacyDir = %q, %v", old, err)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "none.toml")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Exists || c.Path != p || len(c.Tunnels) != 0 {
		t.Fatalf("Load(missing) = %+v", c)
	}
}

func TestLoadPrefixesPathOnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(p, []byte("[tunnel.a]\nport = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("err = %v, want it to mention %s", err, p)
	}
}

func TestLocateWindows(t *testing.T) {
	appdata := filepath.Join("C:", "Users", "me", "AppData", "Roaming")
	got, err := Locate("", env(map[string]string{"APPDATA": appdata, "HOME": "/ignored"}), "windows")
	if err != nil || got != filepath.Join(appdata, "hpctun", "config.toml") {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = Locate("", env(map[string]string{"APPDATA": appdata, "XDG_CONFIG_HOME": "/x"}), "windows")
	if err != nil || got != filepath.Join("/x", "hpctun", "config.toml") {
		t.Fatalf("XDG: got %q, %v", got, err)
	}
	if _, err := Locate("", env(map[string]string{"HOME": "/h"}), "windows"); err == nil || !strings.Contains(err.Error(), "APPDATA") {
		t.Fatalf("no APPDATA: %v", err)
	}
	if got, err := LegacyDir(env(map[string]string{"APPDATA": appdata}), "windows"); err != nil || got != filepath.Join(appdata, "tun") {
		t.Fatalf("LegacyDir got %q, %v", got, err)
	}
}

const seedNoUser = `gateway = "shell"

[ssh]
gateway_host  = "shell.example.org"
transfer      = "files"
transfer_host = "files.example.org"
`

func writeTemp(t *testing.T, name, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadSeedAllowsMissingUser covers a site file: hosts but no user,
// since hpctun init --from asks for the username.
func TestLoadSeedAllowsMissingUser(t *testing.T) {
	c, err := LoadSeed(writeTemp(t, "site.toml", seedNoUser))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Exists || c.Gateway != "shell" || c.SSH == nil || c.SSH.User != "" ||
		c.SSH.GatewayHost != "shell.example.org" || c.SSH.Transfer != "files" || c.SSH.TransferHost != "files.example.org" {
		t.Fatalf("LoadSeed = %+v %+v", c, c.SSH)
	}
}

// TestLoadSeedAcceptsUser covers a team config that does carry a user:
// LoadSeed still reads it (init ignores it).
func TestLoadSeedAcceptsUser(t *testing.T) {
	c, err := LoadSeed(writeTemp(t, "team.toml", seedNoUser+"user = \"alice\"\n"))
	if err != nil || c.SSH.User != "alice" {
		t.Fatalf("LoadSeed = %+v, %v", c, err)
	}
}

func TestLoadSeedMissingFileIsEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "none.toml")
	c, err := LoadSeed(p)
	if err != nil || c.Exists || c.Path != p {
		t.Fatalf("LoadSeed(missing) = %+v, %v", c, err)
	}
}

// TestLoadSeedKeepsOtherChecks: only the user rule is relaxed.
func TestLoadSeedKeepsOtherChecks(t *testing.T) {
	for name, s := range map[string]string{
		"unknown key":          seedNoUser + "colour = \"red\"\n",
		"bad host":             "gateway = \"shell\"\n[ssh]\ngateway_host = \"not a host\"\n",
		"transfer_host alone":  "gateway = \"shell\"\n[ssh]\ntransfer_host = \"files.example.org\"\n",
		"gateway not an alias": "gateway = \"me@shell\"\n[ssh]\ngateway_host = \"shell.example.org\"\n",
	} {
		p := writeTemp(t, "seed.toml", s)
		if _, err := LoadSeed(p); err == nil || !strings.Contains(err.Error(), p) {
			t.Errorf("%s: err = %v, want an error naming %s", name, err, p)
		}
	}
}

// TestLoadStillRequiresSSHUser: a real config.toml without ssh.user is
// still refused, so sync never writes hosts with no User.
func TestLoadStillRequiresSSHUser(t *testing.T) {
	_, err := Load(writeTemp(t, "config.toml", seedNoUser))
	if err == nil || !strings.Contains(err.Error(), "ssh.user") {
		t.Fatalf("Load err = %v, want ssh.user required", err)
	}
	if _, err := Parse([]byte(seedNoUser)); err == nil {
		t.Fatal("Parse accepted [ssh] hosts without user")
	}
}
