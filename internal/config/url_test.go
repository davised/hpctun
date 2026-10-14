package config

import (
	"errors"
	"strings"
	"testing"
)

func TestTunnelURL(t *testing.T) {
	src := "gateway = \"gw\"\ndefault_host = \"node1\"\n" +
		"[tunnel.chat]\nsocks = 1080\nurl = \"https://chat.example.org\"\n" +
		"[tunnel.jup]\nport = 8888\nurl = \"http://localhost:8888/lab\"\n" +
		"[tunnel.dash]\nport = 8099\n"
	c, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"chat": "https://chat.example.org", "jup": "http://localhost:8888/lab", "dash": ""} {
		tun, _ := c.Lookup(name)
		if tun.URL != want {
			t.Errorf("%s url = %q, want %q", name, tun.URL, want)
		}
	}
	// Written and read back, the url survives; a tunnel without one writes none.
	var b strings.Builder
	for _, tun := range c.Tunnels {
		b.WriteString(FormatTunnel(tun, c.DefaultHost))
	}
	if strings.Count(b.String(), "url = ") != 2 {
		t.Fatalf("written:\n%s", b.String())
	}
	again, err := Parse([]byte("gateway = \"gw\"\ndefault_host = \"node1\"\n" + b.String()))
	if err != nil {
		t.Fatal(err)
	}
	for i := range c.Tunnels {
		if again.Tunnels[i] != c.Tunnels[i] {
			t.Errorf("round trip: %+v != %+v", again.Tunnels[i], c.Tunnels[i])
		}
	}
}

func TestTunnelURLRejected(t *testing.T) {
	for _, u := range []string{"ftp://x.org", "chat.example.org", "/lab", "http://", "not a url", "file:///etc/passwd", "http://:80"} {
		_, err := Parse([]byte("gateway = \"gw\"\n[tunnel.x]\nport = 1\nhost = \"h\"\nurl = \"" + u + "\"\n"))
		var ve *ValidationError
		if !errorsAs(err, &ve) || ve.Tunnel != "x" || ve.Field != "url" {
			t.Errorf("url %q: err = %v", u, err)
		}
	}
}

func errorsAs(err error, target **ValidationError) bool { return errors.As(err, target) }

func TestTunnelBrowseFalse(t *testing.T) {
	src := "gateway = \"gw\"\ndefault_host = \"node1\"\n[tunnel.llm]\nport = 8090\nbrowse = false\n[tunnel.dash]\nport = 8099\nbrowse = true\n"
	c, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	llm, _ := c.Lookup("llm")
	dash, _ := c.Lookup("dash")
	if !llm.NoBrowse || dash.NoBrowse {
		t.Fatalf("llm %+v dash %+v", llm, dash)
	}
	out := FormatTunnel(llm, c.DefaultHost) + FormatTunnel(dash, c.DefaultHost)
	if strings.Count(out, "browse = false") != 1 || strings.Contains(out, "browse = true") {
		t.Fatalf("written:\n%s", out)
	}
	again, err := Parse([]byte("gateway = \"gw\"\ndefault_host = \"node1\"\n" + out))
	if err != nil {
		t.Fatal(err)
	}
	if l, _ := again.Lookup("llm"); l != llm {
		t.Fatalf("round trip: %+v != %+v", l, llm)
	}
	_, err = Parse([]byte("gateway = \"gw\"\n[tunnel.x]\nsocks = 1080\nbrowse = false\nurl = \"https://x.org\"\n"))
	var ve *ValidationError
	if !errorsAs(err, &ve) || ve.Tunnel != "x" || ve.Field != "browse" {
		t.Fatalf("browse = false with url: err = %v", err)
	}
}
