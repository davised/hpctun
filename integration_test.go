package tun_test

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"hpctun/internal/config"
	"hpctun/internal/gateway"
	"hpctun/internal/sshctl"
	"hpctun/internal/state"
	"hpctun/internal/tunnel"
)

// TestIntegrationManagedMasterRoute opens, checks and closes a master-route
// tunnel through a real gateway in managed mode. It needs
// TUN_INTEGRATION=1 and TUN_TEST_GATEWAY set to an ssh destination that has
// no ControlMaster configured and accepts a login without prompting.
func TestIntegrationManagedMasterRoute(t *testing.T) {
	dest := os.Getenv("TUN_TEST_GATEWAY")
	if os.Getenv("TUN_INTEGRATION") != "1" || dest == "" {
		t.Skip("set TUN_INTEGRATION=1 and TUN_TEST_GATEWAY to run")
	}
	ctx := context.Background()
	// Short dir: t.TempDir() on macOS can exceed the 104-byte socket limit.
	dir, err := os.MkdirTemp("/tmp", "tun-it")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	r := sshctl.ExecRunner{}
	g, err := gateway.Resolve(ctx, r, dest, dir)
	if err != nil {
		t.Fatal(err)
	}
	if g.Mode != gateway.ModeManaged {
		t.Skipf("%s has ControlMaster configured; need a gateway in managed mode", dest)
	}
	defer r.Run(ctx, "ssh", sshctl.ExitArgs(g.GW)...)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	store, err := state.Open(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	env := &tunnel.Env{
		Runner: r, Gateway: g, Store: store, State: &state.State{}, Config: &config.Config{},
		LogDir: dir, Kill: tunnel.KillTERM, Now: time.Now, Wait: 100 * time.Millisecond,
		GatewayFor: func(ctx context.Context, d string) (*gateway.Gateway, error) { return gateway.Resolve(ctx, r, d, dir) },
	}
	spec := tunnel.Spec{Port: port, Route: config.RouteMaster, Host: "localhost", RemotePort: 22}
	if res := tunnel.Up(ctx, env, []tunnel.Spec{spec}); res[0].Outcome != tunnel.Opened {
		t.Fatalf("up: %+v", res[0])
	}
	conn, err := net.DialTimeout("tcp", l.Addr().String(), 10*time.Second)
	if err != nil {
		t.Fatalf("dial through tunnel: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 64)
	n, _ := conn.Read(buf)
	conn.Close()
	if !strings.HasPrefix(string(buf[:n]), "SSH-") {
		t.Fatalf("expected an SSH banner through the tunnel, got %q", buf[:n])
	}
	if res := tunnel.Down(ctx, env, []tunnel.Spec{spec}); res[0].Outcome != tunnel.Closed {
		t.Fatalf("down: %+v", res[0])
	}
}
