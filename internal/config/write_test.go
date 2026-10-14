package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFormatTunnelOmitsDefaults(t *testing.T) {
	cases := []struct {
		name string
		t    Tunnel
		dh   string
		want string
	}{
		{"master default host", Tunnel{Name: "dash", Port: 8099, Host: "node1", RemotePort: 8099, Route: RouteMaster}, "node1",
			"[tunnel.dash]\nport = 8099\n"},
		{"jump other host other port", Tunnel{Name: "web", Port: 8080, Host: "web1", RemotePort: 80, Route: RouteJump}, "node1",
			"[tunnel.web]\nport = 8080\nhost = \"web1\"\nremote_port = 80\nroute = \"jump\"\n"},
		{"socks", Tunnel{Name: "proxy", Port: 5005, Route: RouteSocks}, "node1",
			"[tunnel.proxy]\nsocks = 5005\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FormatTunnel(c.t, c.dh); got != c.want {
				t.Fatalf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

func TestAppendTunnelPreservesExistingText(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	orig := "# my comment\ngateway = \"gw\"   # trailing\ndefault_host = \"node1\"\n\n[tunnel.dash]\nport = 8099"
	if err := os.WriteFile(p, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	err := AppendTunnel(p, Tunnel{Name: "jup", Port: 4001, Host: "node1", RemotePort: 4001, Route: RouteJump})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(got), orig) {
		t.Fatalf("original text not preserved:\n%s", got)
	}
	want := orig + "\n\n[tunnel.jup]\nport = 4001\nroute = \"jump\"\n"
	if string(got) != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Lookup("jup"); !ok {
		t.Fatal("appended tunnel not found after reload")
	}
}

func TestAppendTunnelRejectsAndLeavesFileUnchanged(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	orig := "gateway = \"gw\"\ndefault_host = \"node1\"\n\n[tunnel.dash]\nport = 8099\n"
	if err := os.WriteFile(p, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tn := range []Tunnel{
		{Name: "dash", Port: 9000, Host: "node1", RemotePort: 9000, Route: RouteMaster},  // duplicate name
		{Name: "other", Port: 8099, Host: "node1", RemotePort: 8099, Route: RouteMaster}, // duplicate port
	} {
		err := AppendTunnel(p, tn)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("AppendTunnel(%+v) err = %v, want ValidationError", tn, err)
		}
		got, _ := os.ReadFile(p)
		if string(got) != orig {
			t.Fatalf("file changed after rejected append:\n%s", got)
		}
	}
}

func TestAppendTunnelCreatesFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.toml")
	if err := AppendTunnel(p, Tunnel{Name: "proxy", Port: 5005, Route: RouteSocks}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "[tunnel.proxy]\nsocks = 5005\n" {
		t.Fatalf("got %q", got)
	}
}

// TestAppendTunnelThroughSymlink covers configs stored elsewhere and linked into
// the config dir: the append has to land in the link's target.
func TestAppendTunnelThroughSymlink(t *testing.T) {
	keep := t.TempDir()
	linkDir := t.TempDir()
	target := filepath.Join(keep, "config.toml")
	orig := "# keep me\ngateway = \"gw\"\ndefault_host = \"node1\"\n"
	if err := os.WriteFile(target, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linkDir, "config.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	err := AppendTunnel(link, Tunnel{Name: "proxy", Port: 5005, Route: RouteSocks})
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("link replaced by a regular file: %v", fi.Mode())
	}
	if got, err := os.Readlink(link); err != nil || got != target {
		t.Fatalf("Readlink = %q, %v, want %q", got, err, target)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	want := orig + "\n[tunnel.proxy]\nsocks = 5005\n"
	if string(got) != want {
		t.Fatalf("target\n%q\nwant\n%q", got, want)
	}
	fi, err = os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("target perms = %v, want 0600", fi.Mode().Perm())
	}
	for _, dir := range []string{keep, linkDir} {
		left, err := filepath.Glob(filepath.Join(dir, ".tun-config-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(left) > 0 {
			t.Fatalf("temp files left in %s: %v", dir, left)
		}
	}
}

// TestAppendTunnelDanglingSymlinkRefuses pins that an unresolvable symlink
// (dangling target here) is never silently replaced by a regular file.
func TestAppendTunnelDanglingSymlinkRefuses(t *testing.T) {
	linkDir := t.TempDir()
	missingDir := t.TempDir()
	target := filepath.Join(missingDir, "config.toml")
	link := filepath.Join(linkDir, "config.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	err := AppendTunnel(link, Tunnel{Name: "proxy", Port: 5005, Route: RouteSocks})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("AppendTunnel through dangling symlink err = %v, want it to mention \"symlink\"", err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("link replaced by a regular file: %v", fi.Mode())
	}
	if _, err := os.Stat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dangling target was created (stat err = %v)", err)
	}
}

func TestNewConfigIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	dir := filepath.Join(t.TempDir(), "new", "hpctun")
	path := filepath.Join(dir, "config.toml")
	if err := WriteNew(path, NewConfig{Gateway: "gw"}, false); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("file %v", fi.Mode())
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v", fi.Mode())
	}
	other := filepath.Join(t.TempDir(), "a", "config.toml")
	if err := AppendTunnel(other, Tunnel{Name: "x", Port: 1, Host: "h", RemotePort: 1, Route: RouteMaster}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(other); fi.Mode().Perm() != 0o600 {
		t.Fatalf("appended new file %v", fi.Mode())
	}
}

func TestRenderRoundTrips(t *testing.T) {
	nc := NewConfig{
		Gateway:     "gw.example.org",
		DefaultHost: "node1",
		Tunnels: []Tunnel{
			{Name: "dash", Port: 8099, Host: "node1", RemotePort: 8099, Route: RouteMaster},
			{Name: "proxy", Port: 5005, Route: RouteSocks},
		},
	}
	out := Render(nc)
	if !strings.Contains(out, "# Any ssh destination") {
		t.Fatalf("template comments missing:\n%s", out)
	}
	c, err := Parse([]byte(out))
	if err != nil {
		t.Fatalf("rendered config does not parse: %v\n%s", err, out)
	}
	if got := FromConfig(c); got.Gateway != nc.Gateway || got.DefaultHost != nc.DefaultHost || len(got.Tunnels) != 2 {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestRenderWithoutDefaultHostParses(t *testing.T) {
	out := Render(NewConfig{Gateway: "gw"})
	if _, err := Parse([]byte(out)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestWriteNewRefusesExistingUnlessForced(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("gateway = \"old\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nc := NewConfig{Gateway: "new"}
	if err := WriteNew(p, nc, false); err == nil {
		t.Fatal("WriteNew overwrote without force")
	}
	if err := WriteNew(p, nc, true); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil || c.Gateway != "new" {
		t.Fatalf("after forced write: %+v %v", c, err)
	}
}

func TestWriteNewForceThroughSymlink(t *testing.T) {
	keep := t.TempDir()
	linkDir := t.TempDir()
	target := filepath.Join(keep, "config.toml")
	if err := os.WriteFile(target, []byte("gateway = \"old\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linkDir, "config.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteNew(link, NewConfig{Gateway: "new"}, true); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("link replaced by a regular file: %v", fi.Mode())
	}
	c, err := Load(target)
	if err != nil || c.Gateway != "new" {
		t.Fatalf("target after forced write: %+v %v", c, err)
	}
}

func TestRenderTunnelGateway(t *testing.T) {
	nc := NewConfig{Gateway: "shell", TunnelGateway: "shell-tunnels",
		SSH: &SSH{User: "me", GatewayHost: "s.example.org", TunnelGatewayHost: "s.example.org"}}
	out := Render(nc)
	if !strings.Contains(out, "gateway = \"shell\"\n\n# What tunnels go through, on a connection of their own.\ntunnel_gateway = \"shell-tunnels\"\n") {
		t.Fatalf("got\n%s", out)
	}
	c, err := Parse([]byte(out))
	if err != nil || c.TunnelGateway != "shell-tunnels" {
		t.Fatalf("round trip: %+v %v", c, err)
	}
	if FromConfig(c).TunnelGateway != "shell-tunnels" {
		t.Fatal("FromConfig dropped tunnel_gateway")
	}
	if strings.Contains(Render(NewConfig{Gateway: "shell"}), "tunnel_gateway") {
		t.Fatal("rendered tunnel_gateway when unset")
	}
}
