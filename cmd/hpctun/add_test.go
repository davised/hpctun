package main

import (
	"slices"
	"strings"
	"testing"

	"hpctun/internal/config"
	"hpctun/internal/prompt"
)

func TestAddPreset(t *testing.T) {
	p, given := addPreset([]string{"jup"}, addFlags{port: 4005, loopback: true, loopbackSet: true})
	want := prompt.Preset{Name: "jup", Port: 4005, Route: config.RouteJump}
	if p != want || !given {
		t.Fatalf("got %+v %v", p, given)
	}
	if _, given := addPreset(nil, addFlags{}); given {
		t.Fatal("empty preset reported as given")
	}
	if p, _ := addPreset(nil, addFlags{socks: 5005}); !p.Socks || p.Port != 5005 {
		t.Fatalf("socks preset %+v", p)
	}
}

func TestMissingForAdd(t *testing.T) {
	cases := []struct {
		p    prompt.Preset
		dh   string
		want []string
	}{
		{prompt.Preset{Name: "a", Port: 1}, "h", nil},
		{prompt.Preset{Name: "a", Port: 1}, "", []string{"--host"}},
		{prompt.Preset{Port: 1, Host: "x"}, "", []string{"name"}},
		{prompt.Preset{Name: "a"}, "h", []string{"--port or --socks"}},
		{prompt.Preset{Name: "a", Port: 1, Socks: true}, "", nil},
	}
	for _, c := range cases {
		if got := missingForAdd(c.p, c.dh); !slices.Equal(got, c.want) {
			t.Errorf("%+v: got %q, want %q", c.p, got, c.want)
		}
	}
}

func TestAddURL(t *testing.T) {
	a, out, _ := testApp(t, "gateway = \"gw\"\n")
	if err := runCmd(a, "add", "chat", "--socks", "1080", "--url", "https://chat.example.org"); err != nil {
		t.Fatal(err)
	}
	cfg, err := a.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := cfg.Lookup("chat"); c.URL != "https://chat.example.org" {
		t.Fatalf("chat = %+v\n%s", c, out)
	}
	err = runCmd(a, "add", "x", "--socks", "1081", "--url", "ftp://x")
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("bad url: %v", err)
	}
}
