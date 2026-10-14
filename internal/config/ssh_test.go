package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseSSH(t *testing.T) {
	c, err := Parse([]byte(`gateway = "shell"
[ssh]
user = "me"
gateway_host = "shell.example.org"
transfer = "files"
transfer_host = "files.example.org"
nodes = ["node*", "gpu??"]
`))
	if err != nil {
		t.Fatal(err)
	}
	want := &SSH{User: "me", GatewayHost: "shell.example.org", Transfer: "files", TransferHost: "files.example.org", Nodes: []string{"node*", "gpu??"}}
	if !reflect.DeepEqual(c.SSH, want) {
		t.Fatalf("got %+v", c.SSH)
	}
	c, err = Parse([]byte(`gateway = "me@gw.example.org"`))
	if err != nil || c.SSH != nil {
		t.Fatalf("no [ssh]: %+v %v", c, err)
	}
}

func TestParseSSHErrors(t *testing.T) {
	cases := []struct{ name, text, field string }{
		{"gateway not an alias", "gateway = \"me@gw\"\n[ssh]\ntransfer = \"files\"\n", "gateway"},
		{"no gateway", "[ssh]\ntransfer = \"files\"\n", "gateway"},
		{"transfer_host alone", "gateway = \"g\"\n[ssh]\nuser = \"me\"\ntransfer_host = \"f.example.org\"\n", "ssh.transfer_host"},
		{"transfer is gateway", "gateway = \"g\"\n[ssh]\ntransfer = \"g\"\n", "ssh.transfer"},
		{"user required", "gateway = \"g\"\n[ssh]\nnodes = [\"n*\"]\n", "ssh.user"},
		{"bad pattern", "gateway = \"g\"\n[ssh]\nuser = \"me\"\nnodes = [\"!n*\"]\n", "ssh.nodes"},
		{"bad user", "gateway = \"g\"\n[ssh]\nuser = \"m e\"\nnodes = [\"n*\"]\n", "ssh.user"},
		{"bad host", "gateway = \"g\"\n[ssh]\nuser = \"me\"\ngateway_host = \"a b\"\n", "ssh.gateway_host"},
		{"unknown key", "gateway = \"g\"\n[ssh]\nfoo = 1\n", "ssh.foo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.text))
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != c.field {
				t.Fatalf("err = %v, want field %s", err, c.field)
			}
		})
	}
}

func TestValidatorsRefuseLeadingDash(t *testing.T) {
	if ValidateHostName("-oProxyCommand=x") == nil {
		t.Error("host name starting with -")
	}
	if ValidatePattern("-x*") == nil {
		t.Error("pattern starting with -")
	}
	for _, ok := range []string{"gw", "me@gw.example.org", "ssh://me@gw:2222", "node1"} {
		if err := ValidateDest(ok); err != nil {
			t.Errorf("ValidateDest(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-oProxyCommand=x", "a b", "gw\n", "gw\x00"} {
		if ValidateDest(bad) == nil {
			t.Errorf("ValidateDest(%q) accepted", bad)
		}
	}
}

func TestRenderIncludesSSH(t *testing.T) {
	nc := NewConfig{Gateway: "shell", SSH: &SSH{User: "me", GatewayHost: "shell.example.org", Nodes: []string{"node*"}},
		Tunnels: []Tunnel{{Name: "jup", Port: 4001, Host: "node1", RemotePort: 4001, Route: RouteMaster}}}
	c, err := Parse([]byte(Render(nc)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.SSH, nc.SSH) || len(c.Tunnels) != 1 {
		t.Fatalf("round trip: %+v", c)
	}
	if !reflect.DeepEqual(FromConfig(c).SSH, nc.SSH) {
		t.Fatal("FromConfig dropped SSH")
	}
}

func TestReplaceSSHTextInsertsAndKeepsTheRest(t *testing.T) {
	old := "# my config\ngateway = \"me@gw.example.org\"\ndefault_host = \"node1\"\n\n# tunnels\n[tunnel.jup]\nport = 4001\n"
	got, err := ReplaceSSHText([]byte(old), &SSH{User: "me", GatewayHost: "gw.example.org", Nodes: []string{"node*"}}, "gw", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "# my config\ngateway = \"gw\"\ndefault_host = \"node1\"\n\n[ssh]\nuser = \"me\"\ngateway_host = \"gw.example.org\"\nnodes = [\"node*\"]\n\n# tunnels\n[tunnel.jup]\nport = 4001\n"
	if string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestReplaceSSHTextKeepsUnchangedGatewayLine(t *testing.T) {
	old := "gateway = \"gw\"  # my gateway\ndefault_host = \"node1\"\n"
	got, err := ReplaceSSHText([]byte(old), &SSH{User: "me", GatewayHost: "gw.example.org"}, "gw", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "gateway = \"gw\"  # my gateway\ndefault_host = \"node1\"\n\n[ssh]\nuser = \"me\"\ngateway_host = \"gw.example.org\"\n"
	if string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestReplaceSSHTextRewritesChangedGatewayLine(t *testing.T) {
	old := "gateway = \"old\"  # my gateway\ndefault_host = \"node1\"\n"
	got, err := ReplaceSSHText([]byte(old), &SSH{User: "me", GatewayHost: "gw.example.org"}, "gw", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "gateway = \"gw\"\ndefault_host = \"node1\"\n\n[ssh]\nuser = \"me\"\ngateway_host = \"gw.example.org\"\n"
	if string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

const betweenTunnels = "gateway = \"gw\"\ndefault_host = \"node\"\n[tunnel.a]\nport = 1\n\n[ssh]\nuser = \"old\"\n# old comment\nnodes = [\"x*\"]\n\n# next\n[tunnel.b]\nport = 2\n"

func TestReplaceSSHTextBetweenTunnelTables(t *testing.T) {
	got, err := ReplaceSSHText([]byte(betweenTunnels), &SSH{User: "me", Nodes: []string{"n*"}}, "gw", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "gateway = \"gw\"\ndefault_host = \"node\"\n[tunnel.a]\nport = 1\n\n[ssh]\nuser = \"me\"\nnodes = [\"n*\"]\n\n# next\n[tunnel.b]\nport = 2\n"
	if string(got) != want {
		t.Fatalf("got\n%s", got)
	}
}

func TestReplaceSSHTextRemoves(t *testing.T) {
	got, err := ReplaceSSHText([]byte(betweenTunnels), nil, "gw", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "gateway = \"gw\"\ndefault_host = \"node\"\n[tunnel.a]\nport = 1\n\n\n# next\n[tunnel.b]\nport = 2\n"
	if string(got) != want {
		t.Fatalf("got\n%s", got)
	}
}

func TestReplaceSSHTextInvalid(t *testing.T) {
	_, err := ReplaceSSHText([]byte(betweenTunnels), &SSH{Nodes: []string{"n*"}}, "gw", "") // no user
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "ssh.user" {
		t.Fatalf("err = %v", err)
	}
}

func TestParseTunnelGateway(t *testing.T) {
	c, err := Parse([]byte(`gateway = "shell"
tunnel_gateway = "shell-tunnels"
[ssh]
user = "me"
gateway_host = "shell.example.org"
tunnel_gateway_host = "shell.example.org"
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.TunnelGateway != "shell-tunnels" || c.TunnelDest() != "shell-tunnels" || c.SSH.TunnelGatewayHost != "shell.example.org" {
		t.Fatalf("got %+v %+v", c, c.SSH)
	}
	c, err = Parse([]byte(`gateway = "me@gw.example.org"` + "\n" + `tunnel_gateway = "me@tun.example.org"`))
	if err != nil || c.TunnelDest() != "me@tun.example.org" {
		t.Fatalf("any destination without [ssh]: %+v %v", c, err)
	}
	c, err = Parse([]byte(`gateway = "shell"`))
	if err != nil || c.TunnelGateway != "" || c.TunnelDest() != "shell" {
		t.Fatalf("default: %+v %v", c, err)
	}
}

func TestParseTunnelGatewayErrors(t *testing.T) {
	cases := []struct{ name, text, field string }{
		{"same as gateway", "gateway = \"g\"\ntunnel_gateway = \"g\"\n", "tunnel_gateway"},
		{"no gateway", "tunnel_gateway = \"t\"\n", "tunnel_gateway"},
		{"same as transfer", "gateway = \"g\"\ntunnel_gateway = \"files\"\n[ssh]\ntransfer = \"files\"\n", "tunnel_gateway"},
		{"host without key", "gateway = \"g\"\n[ssh]\nuser = \"me\"\ntunnel_gateway_host = \"t.example.org\"\n", "ssh.tunnel_gateway_host"},
		{"host needs alias", "gateway = \"g\"\ntunnel_gateway = \"me@t\"\n[ssh]\nuser = \"me\"\ntunnel_gateway_host = \"t.example.org\"\n", "tunnel_gateway"},
		{"bad host", "gateway = \"g\"\ntunnel_gateway = \"t\"\n[ssh]\nuser = \"me\"\ntunnel_gateway_host = \"a b\"\n", "ssh.tunnel_gateway_host"},
		{"user required", "gateway = \"g\"\ntunnel_gateway = \"t\"\n[ssh]\ntunnel_gateway_host = \"t.example.org\"\n", "ssh.user"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.text))
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != c.field {
				t.Fatalf("err = %v, want field %s", err, c.field)
			}
		})
	}
}

func TestFormatSSHTunnelGatewayHost(t *testing.T) {
	got := FormatSSH(&SSH{User: "me", GatewayHost: "g.example.org", TunnelGatewayHost: "g.example.org"})
	want := "[ssh]\nuser = \"me\"\ngateway_host = \"g.example.org\"\ntunnel_gateway_host = \"g.example.org\"\n"
	if got != want {
		t.Fatalf("got\n%s", got)
	}
}

func TestReplaceSSHTextTunnelGateway(t *testing.T) {
	s := &SSH{User: "me", GatewayHost: "gw.example.org", TunnelGatewayHost: "gw.example.org"}
	// inserted after the gateway line
	old := "gateway = \"gw\"\ndefault_host = \"node1\"\n"
	got, err := ReplaceSSHText([]byte(old), s, "gw", "gw-tunnels")
	if err != nil {
		t.Fatal(err)
	}
	want := "gateway = \"gw\"\ntunnel_gateway = \"gw-tunnels\"\ndefault_host = \"node1\"\n\n[ssh]\nuser = \"me\"\ngateway_host = \"gw.example.org\"\ntunnel_gateway_host = \"gw.example.org\"\n"
	if string(got) != want {
		t.Fatalf("insert: got\n%s\nwant\n%s", got, want)
	}
	// an unchanged line keeps its comment
	old = "gateway = \"gw\"\ntunnel_gateway = \"gw-tunnels\"  # mine\n"
	got, err = ReplaceSSHText([]byte(old), s, "gw", "gw-tunnels")
	if err != nil || !strings.Contains(string(got), "tunnel_gateway = \"gw-tunnels\"  # mine\n") {
		t.Fatalf("keep: %v\n%s", err, got)
	}
	// a changed line is rewritten
	got, err = ReplaceSSHText([]byte(old), &SSH{User: "me", GatewayHost: "gw.example.org", TunnelGatewayHost: "t.example.org"}, "gw", "tun")
	if err != nil || !strings.Contains(string(got), "\ntunnel_gateway = \"tun\"\n") {
		t.Fatalf("rewrite: %v\n%s", err, got)
	}
	// removed when "" and the [ssh] table no longer defines it
	got, err = ReplaceSSHText([]byte(old), &SSH{User: "me", GatewayHost: "gw.example.org"}, "gw", "")
	if err != nil || strings.Contains(string(got), "tunnel_gateway") {
		t.Fatalf("remove: %v\n%s", err, got)
	}
}
