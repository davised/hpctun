package main

import (
	"os"
	"strings"
	"testing"

	"hpctun/internal/state"
)

const tunnelGWCfg = `gateway = "shell"
tunnel_gateway = "shell-tunnels"
[ssh]
user = "me"
gateway_host = "shell.example.org"
tunnel_gateway_host = "shell.example.org"
transfer = "files"
transfer_host = "files.example.org"
`

func TestOpenEnvUsesTunnelGateway(t *testing.T) {
	a, _, _ := testApp(t, tunnelGWCfg)
	a.run, _ = masters()
	cfg, err := a.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	env, release, err := a.openEnv(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if env.Gateway.Dest != "shell-tunnels" || env.Gateway.SocketPath != "/tmp/cm-shell-tunnels" {
		t.Fatalf("env gateway %+v", env.Gateway)
	}
	// Entries opened through the old gateway still resolve through GatewayFor.
	g, err := env.GatewayFor(t.Context(), "shell")
	if err != nil || g.Dest != "shell" {
		t.Fatalf("GatewayFor(shell) = %+v, %v", g, err)
	}
}

func TestLoginTunnelGatewayByName(t *testing.T) {
	a, out, _ := testApp(t, tunnelGWCfg)
	f, _ := masters()
	a.run = f
	if err := runCmd(a, "login", "shell-tunnels"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "shell-tunnels  logging in...\nshell-tunnels  connected (pid 42)\n" {
		t.Fatalf("out %q", out.String())
	}
	if bg := f.Find("-fN -- shell-tunnels"); len(bg) != 1 || !strings.HasSuffix(bg[0].LogPath, "master.log") {
		t.Fatalf("background calls %+v", bg)
	}
	out.Reset()
	if err := runCmd(a, "login"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "shell-tunnels") {
		t.Fatalf("login with no arguments opened the tunnel gateway: %q", out.String())
	}
	// "-fN -- shell" is also a substring of "-fN -- shell-tunnels", and the dest is
	// the last word in the ssh args (sshctl.StartArgs), so there is no
	// trailing space to match on; filter on the exact last argument instead.
	var gwLogins []string
	for _, c := range f.Find("-fN -- shell") {
		if c.Args[len(c.Args)-1] == "shell" {
			gwLogins = append(gwLogins, c.LogPath)
		}
	}
	if len(gwLogins) != 1 || !strings.HasSuffix(gwLogins[0], "login-shell.log") {
		t.Fatalf("gateway log: %+v", gwLogins)
	}
}

func TestLogoutWithTunnelGateway(t *testing.T) {
	a, out, _ := testApp(t, tunnelGWCfg)
	f, up := masters()
	a.run = f
	up["shell"], up["shell-tunnels"] = true, true
	dir, _ := a.stateDir()
	os.MkdirAll(dir, 0o700)
	store, err := state.Open(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&state.State{Tunnels: []state.Entry{{Name: "jup", Port: 4001}}}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err := runCmd(a, "logout", "shell"); err != nil {
		t.Fatalf("logout of the login gateway: %v", err)
	}
	if up["shell"] || !up["shell-tunnels"] || !strings.Contains(out.String(), "shell  logged out") {
		t.Fatalf("out %q up %v", out.String(), up)
	}
	err = runCmd(a, "logout", "shell-tunnels")
	if err == nil || !strings.Contains(err.Error(), "tunnels are using shell-tunnels (jup)") {
		t.Fatalf("err = %v", err)
	}
}

// TestLogoutGatewayWithOlderTunnels covers tunnels opened through the gateway
// before tunnel_gateway was set: logging out of the gateway would close them,
// so it is refused, naming only those tunnels.
func TestLogoutGatewayWithOlderTunnels(t *testing.T) {
	a, _, _ := testApp(t, tunnelGWCfg)
	f, up := masters()
	a.run = f
	up["shell"], up["shell-tunnels"] = true, true
	dir, _ := a.stateDir()
	os.MkdirAll(dir, 0o700)
	store, err := state.Open(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Save(&state.State{Tunnels: []state.Entry{
		{Name: "jup", Port: 4001, Gateway: "shell-tunnels"},
		{Name: "old", Port: 4002, Gateway: "shell"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	err = runCmd(a, "logout", "shell")
	if err == nil || !strings.Contains(err.Error(), "tunnels are using shell (old)") {
		t.Fatalf("err = %v", err)
	}
	if !up["shell"] {
		t.Fatal("logged out of shell")
	}
	err = runCmd(a, "logout", "shell-tunnels")
	if err == nil || !strings.Contains(err.Error(), "tunnels are using shell-tunnels (jup)") {
		t.Fatalf("err = %v", err)
	}
}
