package prompt

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"hpctun/internal/config"
	"hpctun/internal/gateway"
)

func deps(s *script, out *bytes.Buffer) WizardDeps {
	return WizardDeps{
		Asker: s, Out: out,
		Hosts: []string{"hpc-tunnel", "web"},
		ModeOf: func(alias string) (gateway.Mode, error) {
			if alias == "hpc-tunnel" {
				return gateway.ModeSSHConfig, nil
			}
			return gateway.ModeManaged, nil
		},
		TestGateway: func(string) error { return nil },
		PortInUse:   func(p int) bool { return p == 631 },
		LocalUser:   "me",
		HostName:    func(alias string) (string, error) { return alias + ".example.org", nil },
		Preview: func(nc config.NewConfig) SSHPreview {
			return SSHPreview{Text: "# preview via " + nc.Gateway + " for " + nc.SSH.User}
		},
	}
}

func TestWizardHappyPath(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{
		"hpc-tunnel", // gateway
		"y",          // test connection
		"", "",       // no transfer host, no node patterns
		"y", "", // set up tunnels, sharing the gateway's connection
		"node1",                                       // default host
		"add",                                         // next action
		"port", "jup", "631", "4001", "", "", "y", "", // 631 in use -> re-asked
		"add",
		"socks", "proxy", "5005", "",
		"done",
		"y", // write
	}}
	nc, _, err := Wizard(deps(s, &out), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.Gateway != "hpc-tunnel" || nc.DefaultHost != "node1" || len(nc.Tunnels) != 2 {
		t.Fatalf("got %+v", nc)
	}
	if nc.Tunnels[0] != (config.Tunnel{Name: "jup", Port: 4001, Host: "node1", RemotePort: 4001, Route: config.RouteJump}) {
		t.Fatalf("tunnel 0 = %+v", nc.Tunnels[0])
	}
	if gw := s.ctx.Recap[0]; gw.Label != "Gateway" || !strings.Contains(gw.Value, "ControlMaster") || !strings.Contains(gw.Value, "connected") {
		t.Fatalf("gateway recap = %+v", gw)
	}
	if n := len(s.ctx.Recap); n != 6 || s.ctx.Recap[2] != (Line{"VS Code", "not set up (add a [code] table later)"}) || s.ctx.Recap[5].Label != "" || !strings.Contains(s.ctx.Recap[5].Value, "SOCKS") {
		t.Fatalf("recap = %+v", s.ctx.Recap)
	}
}

func TestWizardTypedHostnameAndFailedTestRetries(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{
		"", "bad.example.org", "y", // enter a hostname; test fails
		"gw.example.org", "y", // back at the hostname; second attempt succeeds
		"",     // short name: gw
		"", "", // no transfer host, no node patterns
		"",  // no tunnels
		"",  // username: me
		"y", // apply the ssh changes (the question defaults to no)
		"y",
	}}
	d := deps(s, &out)
	d.TestGateway = func(dest string) error {
		if dest == "bad.example.org" {
			return errors.New("Permission denied")
		}
		return nil
	}
	nc, _, err := Wizard(d, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.Gateway != "gw" || !reflect.DeepEqual(nc.SSH, &config.SSH{User: "me", GatewayHost: "gw.example.org"}) {
		t.Fatalf("got %+v", nc)
	}
	if s.asked[3] != "Gateway hostname" || !strings.Contains(s.shown[3].Notice, "Permission denied") {
		t.Fatalf("test failure not shown with the hostname question: %q %+v", s.asked, s.shown[3])
	}
	if s.shown[4].Notice != "" {
		t.Fatalf("notice shown twice: %+v", s.shown[4])
	}
}

func TestWizardSeedProvidesDefaultsAndPortsAreTaken(t *testing.T) {
	var out bytes.Buffer
	seed := &config.NewConfig{Gateway: "team-gw", DefaultHost: "node1", Tunnels: []config.Tunnel{
		{Name: "dash", Port: 8099, Host: "node1", RemotePort: 8099, Route: config.RouteMaster},
	}}
	s := &script{t: t, answers: []string{
		"", "", // gateway select: "" = enter a hostname; input pre-filled with team-gw
		"n",        // skip test
		"", "", "", // short name, no transfer, no nodes
		"y", "", // set up tunnels, sharing the gateway's connection
		"", "n", // username, decline the ssh setup
		"", // default host = node1 (seed)
		"add", "port", "dash", "web", "8099", "8100", "", "", "n", "",
		"done",
		"y",
	}}
	nc, _, err := Wizard(deps(s, &out), seed)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.Gateway != "team-gw" || len(nc.Tunnels) != 2 || nc.Tunnels[1].Name != "web" || nc.Tunnels[1].Port != 8100 {
		t.Fatalf("got %+v", nc)
	}
}

func TestWizardAbort(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{"", "<abort>"}}
	if _, _, err := Wizard(deps(s, &out), nil); !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v", err)
	}
}

func TestWizardEscGoesBack(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{
		"web", "<back>", // Esc at the test question returns to the gateway list
		"hpc-tunnel", "n",
		"", "", // transfer, nodes
		"y", "", // set up tunnels, sharing the gateway's connection
		"node1",
		"<back>", "node2", // Esc at the tunnel menu returns to the default host
		"add", "<back>", // Esc on a tunnel's first question returns to the menu
		"add", "socks", "proxy", "5005", "",
		"done",
		"<back>", "done", // Esc at the review returns to the menu
		"y",
	}}
	nc, _, err := Wizard(deps(s, &out), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.Gateway != "hpc-tunnel" || nc.DefaultHost != "node2" || len(nc.Tunnels) != 1 || nc.Tunnels[0].Name != "proxy" {
		t.Fatalf("got %+v", nc)
	}
}

func TestWizardEscOnFirstQuestionOrDecliningAborts(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{"<back>"}}
	if _, _, err := Wizard(deps(s, &out), nil); !errors.Is(err, ErrAborted) {
		t.Fatalf("esc: err = %v", err)
	}
	s = &script{t: t, answers: []string{"web", "n", "", "", "", "n"}}
	if _, _, err := Wizard(deps(s, &out), nil); !errors.Is(err, ErrAborted) {
		t.Fatalf("decline: err = %v", err)
	}
	s.done()
}

func TestWizardWithoutSSHConfigHostsAsksForHostname(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{"gw", "n", "", "", "", "", "", "n", "y"}}
	d := deps(s, &out)
	d.Hosts = nil
	nc, _, err := Wizard(d, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.Gateway != "gw" || s.asked[0] != "Gateway hostname" {
		t.Fatalf("got %+v, asked %q", nc, s.asked)
	}
}

// TestWizardNoControlMaster: on Windows the connection test is a sign-in
// check, the connection mode is never looked up, and the tunnel questions
// are asked.
func TestWizardNoControlMaster(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{"hpc-tunnel", "y", "", "node*", "n", "", "y", "y"}}
	d := deps(s, &out)
	d.NoControlMaster, d.Path = true, "/cfg.toml"
	d.ModeOf = func(string) (gateway.Mode, error) { t.Fatal("ModeOf called"); return "", nil }
	tested := ""
	d.TestGateway = func(gw string) error { tested = gw; return nil }
	nc, _, err := Wizard(d, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	want := []string{"Gateway", "Test signing in without a prompt?", "Transfer host (optional)", "Hosts you log in to through the gateway (optional)",
		"Set up tunnels? (advanced)", CodeQuestion, "Your username on these hosts", "Apply these changes?", "Write this config to /cfg.toml?"}
	if !reflect.DeepEqual(s.asked, want) {
		t.Fatalf("asked %q", s.asked)
	}
	if tested != "hpc-tunnel" || nc.Gateway != "hpc-tunnel" || nc.SSH == nil || nc.SSH.User != "me" {
		t.Fatalf("tested %q, got %+v", tested, nc)
	}
	if g := s.shown[len(s.shown)-1].Recap[0]; g.Value != "hpc-tunnel  (connected ✓)" {
		t.Fatalf("gateway recap %q", g.Value)
	}
}

// A failed sign-in check on Windows is a notice, not a step back: a key
// can be set up later.
func TestWizardNoControlMasterSignInFails(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{"hpc-tunnel", "y", "", "", "n", "y"}}
	d := deps(s, &out)
	d.NoControlMaster, d.Path = true, "/cfg.toml"
	d.ModeOf = func(string) (gateway.Mode, error) { t.Fatal("ModeOf called"); return "", nil }
	d.TestGateway = func(string) error { return errors.New("Permission denied (publickey)") }
	if _, _, err := Wizard(d, nil); err != nil {
		t.Fatal(err)
	}
	s.done()
	if !strings.Contains(s.shown[2].Notice, "Permission denied") || s.asked[2] != "Transfer host (optional)" {
		t.Fatalf("asked %q, notice %q", s.asked, s.shown[2].Notice)
	}
}

func TestWizardConnectionTestRunsOutside(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{"hpc-tunnel", "y", "", "", "", "y"}}
	d := deps(s, &out)
	d.TestGateway = func(string) error {
		if s.outside != 1 {
			t.Errorf("TestGateway ran with the terminal still held (outside=%d)", s.outside)
		}
		return nil
	}
	if _, _, err := Wizard(d, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Connecting to hpc-tunnel...") {
		t.Fatalf("out %q", out.String())
	}
}

// TestWizardSeedTunnelsDefaultNo: a seed with tunnels (a site file such as
// osu.toml) still defaults the tunnel question to No, since most people only
// need logins and copies; Enter there leaves the seed's tunnels out.
func TestWizardSeedTunnelsDefaultNo(t *testing.T) {
	var out bytes.Buffer
	seed := &config.NewConfig{Gateway: "hpc-tunnel", Tunnels: []config.Tunnel{
		{Name: "chat", Port: 1080, Route: config.RouteSocks},
	}}
	s := &script{t: t, answers: []string{"", "n", "", "", "", "y"}} // gateway, no test, transfer, nodes, tunnels (default), write
	nc, _, err := Wizard(deps(s, &out), seed)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if len(nc.Tunnels) != 0 {
		t.Fatalf("got %+v", nc.Tunnels)
	}
}

// TestWizardNoToTunnelsDropsSeedTunnels: answering No to the tunnel
// question leaves a seed's tunnels and default host out of the config and
// the recap; going back and answering Yes offers them again.
func TestWizardNoToTunnelsDropsSeedTunnels(t *testing.T) {
	var out bytes.Buffer
	seed := &config.NewConfig{Gateway: "hpc-tunnel", DefaultHost: "node1", Tunnels: []config.Tunnel{
		{Name: "dash", Port: 8099, Host: "node1", RemotePort: 8099, Route: config.RouteMaster},
	}}
	s := &script{t: t, answers: []string{"", "n", "", "", "n", "y"}} // gateway, no test, transfer, nodes, no tunnels, write
	nc, _, err := Wizard(deps(s, &out), seed)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if len(nc.Tunnels) != 0 || nc.DefaultHost != "" {
		t.Fatalf("got %+v", nc)
	}
	for _, l := range s.ctx.Recap {
		if l.Label == "Default host" || strings.Contains(l.Value, "dash") {
			t.Fatalf("recap %+v", s.ctx.Recap)
		}
	}

	s = &script{t: t, answers: []string{
		"", "n", "", "", "n",
		"<back>", "y", "", // Esc at the write question returns to the tunnel question; Yes, share
		"", "done", "y", // default host node1 (seed), no more tunnels, write
	}}
	nc, _, err = Wizard(deps(s, &out), seed)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if len(nc.Tunnels) != 1 || nc.DefaultHost != "node1" {
		t.Fatalf("after Yes: got %+v", nc)
	}
}

// TestSeededSiteRefusesExistingAliases covers a user whose ~/.ssh/config
// already has shell and files: the seeded short names are refused and
// re-asked, never defined a second time.
func TestSeededSiteRefusesExistingAliases(t *testing.T) {
	seed := &config.NewConfig{Gateway: "shell", SSH: &config.SSH{
		GatewayHost: "shell.hpc.oregonstate.edu",
		Transfer:    "files", TransferHost: "files.hpc.oregonstate.edu",
	}}
	s := &script{t: t, answers: []string{
		"",        // gateway: Enter a hostname (seeded)
		"",        // gateway hostname (seeded)
		"n",       // skip the connection test
		"", "osu", // short name: shell refused, then osu
		"",              // transfer: Enter a hostname (seeded)
		"",              // transfer hostname (seeded)
		"", "osu-files", // short name: files refused, then osu-files
		"",  // node patterns
		"",  // no tunnels
		"",  // username: me
		"y", // apply the ssh changes
		"y", // write
	}}
	d := deps(s, &bytes.Buffer{})
	d.Hosts = []string{"shell", "files"}
	nc, _, err := Wizard(d, seed)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.Gateway != "osu" || nc.SSH.GatewayHost != "shell.hpc.oregonstate.edu" ||
		nc.SSH.Transfer != "osu-files" || nc.SSH.TransferHost != "files.hpc.oregonstate.edu" {
		t.Fatalf("nc = %+v %+v", nc, nc.SSH)
	}
}

// TestSeededCodeDeclinedIsLeftOut covers a seed with [code] whose ssh
// changes are declined: there is no [ssh] username, so [code] is left out of
// the config (which would not validate without [ssh]) and the review says
// why.
func TestSeededCodeDeclinedIsLeftOut(t *testing.T) {
	seed := &config.NewConfig{Gateway: "hpc-tunnel", SSH: &config.SSH{User: "alice"},
		Code: &config.Code{CPUs: config.DefaultCodeCPUs, Time: config.DefaultCodeTime, StartWithin: config.DefaultStartWithin, ServerDirs: config.DefaultServerDirs}}
	s := &script{t: t, answers: []string{
		"",  // gateway: hpc-tunnel (seeded, from ~/.ssh/config)
		"n", // skip the connection test
		"",  // transfer: skip
		"",  // node patterns
		"",  // no tunnels
		"",  // username: me
		"n", // decline the ssh changes
		"y", // write
	}}
	nc, _, err := Wizard(deps(s, &bytes.Buffer{}), seed)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.Code != nil || nc.SSH != nil {
		t.Fatalf("code %+v ssh %+v", nc.Code, nc.SSH)
	}
	if !strings.Contains(s.ctx.Notice, "hpctun has no username for the cluster, so the [code] table is left out.") {
		t.Fatalf("review notice %q", s.ctx.Notice)
	}
}

// TestWizardCodeQuestion: hpctun init asks about hpctun code. Without a
// seed [code] the default is No and the config gets none; Yes writes an
// empty [code] (every default) and asks for the username even when nothing
// else needs it.
func TestWizardCodeQuestion(t *testing.T) {
	for _, c := range []struct {
		answer   string
		wantCode bool
	}{{"", false}, {"y", true}} {
		answers := []string{"hpc-tunnel", "n", "", "", "", c.answer}
		if c.wantCode {
			answers = append(answers, "", "y") // username, apply the ssh changes
		}
		answers = append(answers, "y") // write
		s := &script{t: t, codeQ: true, answers: answers}
		nc, _, err := Wizard(deps(s, &bytes.Buffer{}), nil)
		if err != nil {
			t.Fatalf("%q: %v", c.answer, err)
		}
		s.done()
		if (nc.Code != nil) != c.wantCode || (c.wantCode && !reflect.DeepEqual(nc.Code, config.DefaultCode())) {
			t.Errorf("%q: code %+v", c.answer, nc.Code)
		}
		if asked := slicesIndex(s.asked, "Your username on these hosts") >= 0; asked != c.wantCode {
			t.Errorf("%q: username asked %v: %q", c.answer, asked, s.asked)
		}
		if c.wantCode && (nc.SSH == nil || nc.SSH.User != "me") {
			t.Errorf("%q: ssh %+v", c.answer, nc.SSH)
		}
	}
}

// TestWizardSeedCodeQuestion: with a seed [code] the default is Yes and the
// seed's values are kept; No leaves it out (and the username with it), and
// going back to answer Yes offers the seed's [code] again.
func TestWizardSeedCodeQuestion(t *testing.T) {
	seedCode := func() *config.Code {
		c := config.DefaultCode()
		c.Partition = "lab"
		return c
	}
	seed := func() *config.NewConfig { return &config.NewConfig{Gateway: "hpc-tunnel", Code: seedCode()} }
	for _, c := range []struct {
		name     string
		answers  []string
		wantCode bool
	}{
		{"default yes", []string{"", "n", "", "", "", "", "", "y", "y"}, true},
		{"no", []string{"", "n", "", "", "", "n", "y"}, false},
		{"no, back, yes", []string{"", "n", "", "", "", "n", "<back>", "y", "", "y", "y"}, true},
	} {
		s := &script{t: t, codeQ: true, answers: c.answers}
		nc, _, err := Wizard(deps(s, &bytes.Buffer{}), seed())
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		s.done()
		if c.wantCode && !reflect.DeepEqual(nc.Code, seedCode()) || !c.wantCode && nc.Code != nil {
			t.Errorf("%s: code %+v", c.name, nc.Code)
		}
	}
}
