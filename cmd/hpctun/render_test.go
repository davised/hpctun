package main

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"hpctun/internal/gateway"
	"hpctun/internal/tunnel"
)

var update = flag.Bool("update", false, "rewrite golden files")

func plain(s string) string {
	lines := strings.Split(ansi.Strip(s), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

func TestRenderStatusGolden(t *testing.T) {
	h := header{Config: "/home/me/.config/hpctun/config.toml", Dest: "gw.example.org", Mode: gateway.ModeManaged, Status: gateway.Status{Running: true, PID: 4242}}
	rows := []tunnel.Row{
		{Kind: tunnel.KindNamed, Name: "jupyter", Port: 4001, Route: "jump", Target: "aspen1:4001", State: tunnel.StateUp, Conns: 0},
		{Kind: tunnel.KindAdhoc, Name: "(ad-hoc)", Port: 4002, Route: "master", Target: "aspen1:4002", State: tunnel.StateConflict, Conns: 1, Owner: "pid 999 (python3)"},
		{Kind: tunnel.KindNamed, Name: "socks", Port: 5005, Route: "socks", Target: "-", State: tunnel.StateDown, Conns: -1},
		{Kind: tunnel.KindExternal, Name: "-", Port: 43723, Route: "?", Target: "ssh pid 25301", State: tunnel.StateExternal, Conns: 0, Warning: "opened through old-gw, not the configured gateway"},
	}
	checkGolden(t, "testdata/status.golden", plain(renderStatus(h, rows)))
	// The same status as --json prints it, indented here for review.
	js, err := json.MarshalIndent(statusDoc(h, rows), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "testdata/status.json.golden", string(js)+"\n")
}

func checkGolden(t *testing.T, golden, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s: got\n%s\nwant\n%s", golden, got, want)
	}
}

func TestHeaderVariants(t *testing.T) {
	cases := []struct {
		h    header
		want string
	}{
		{header{}, "gateway: not configured"},
		{header{Dest: "gw", Mode: gateway.ModeSSHConfig}, "gateway gw: not running (ssh-config)"},
		{header{Dest: "gw", Err: errors.New("boom")}, "gateway gw: error: boom"},
	}
	for _, c := range cases {
		if got := c.h.String(); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

func TestRenderHints(t *testing.T) {
	h := header{Dest: "gw", Mode: gateway.ModeSSHConfig, Status: gateway.Status{Running: true, PID: 7},
		Hints: []string{"ControlMaster yes: use ControlMaster auto"}}
	got := plain(renderStatus(h, nil))
	want := "gateway gw: running (pid 7, ssh-config)\n  hint: ControlMaster yes: use ControlMaster auto\n\nno tunnels\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestRenderNoTunnels(t *testing.T) {
	got := plain(renderStatus(header{}, nil))
	if got != "gateway: not configured\n\nno tunnels\n" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderStatusCodeLine(t *testing.T) {
	h := header{Dest: "shell", Code: &codeLine{None: true}}
	if got := renderStatus(h, nil); !strings.Contains(got, "\ncode job: none\n") {
		t.Fatalf("got\n%s", got)
	}
}

func TestHeaderTransferLine(t *testing.T) {
	h := header{Dest: "shell", Mode: gateway.ModeSSHConfig, Status: gateway.Status{Running: true, PID: 7},
		Transfer: &conn{Role: "transfer", Alias: "files", Mode: gateway.ModeSSHConfig}}
	got := renderStatus(h, nil)
	if !strings.Contains(got, "gateway shell: running (pid 7, ssh-config)\ntransfer files: not connected (`hpctun login files`)\n") {
		t.Fatalf("got\n%s", got)
	}
}

func TestHeaderGatewayConnected(t *testing.T) {
	up := gateway.Status{Running: true, PID: 7}
	if !(header{Dest: "shell", Status: up}).gatewayConnected() {
		t.Fatal("gateway running, no tunnel gateway: want connected")
	}
	// With a tunnel gateway the header line is the tunnel gateway's; tun
	// code needs the gateway's own connection, on the Login line.
	h := header{Dest: "shell-tunnels", Tunnels: true, Status: up, Login: &conn{Role: "gateway", Alias: "shell"}}
	if h.gatewayConnected() {
		t.Fatal("tunnel gateway running, gateway not: want not connected")
	}
	h = header{Dest: "shell-tunnels", Tunnels: true, Login: &conn{Role: "gateway", Alias: "shell", Status: up}}
	if !h.gatewayConnected() {
		t.Fatal("gateway running, tunnel gateway not: want connected")
	}
}

func TestHeaderWithTunnelGateway(t *testing.T) {
	h := header{Dest: "shell-tunnels", Tunnels: true, Mode: gateway.ModeSSHConfig, Status: gateway.Status{Running: true, PID: 7},
		Login:    &conn{Role: "gateway", Alias: "shell", Mode: gateway.ModeSSHConfig, Status: gateway.Status{Running: true, PID: 9}},
		Transfer: &conn{Role: "transfer", Alias: "files", Mode: gateway.ModeSSHConfig}}
	got := plain(renderStatus(h, nil))
	want := "tunnels shell-tunnels: running (pid 7, ssh-config)\n" +
		"gateway shell: connected (pid 9)\n" +
		"transfer files: not connected (`hpctun login files`)\n\nno tunnels\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	managed := conn{Role: "gateway", Alias: "me@gw", Mode: gateway.ModeManaged}
	if managed.String() != "gateway me@gw: not connected (`hpctun login me@gw`)" {
		t.Fatalf("managed gateway line %q", managed.String())
	}
}
