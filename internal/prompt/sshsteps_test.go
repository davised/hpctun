package prompt

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"hpctun/internal/change"
	"hpctun/internal/config"
)

func TestWizardTransferAndNodes(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{
		"hpc-tunnel", "n",
		transferType, "files.example.org", "", // short name: files
		"node* gpu??",
		"",  // no tunnels
		"",  // username: me
		"y", // apply
		"y",
	}}
	nc, _, err := Wizard(deps(s, &out), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	want := &config.SSH{User: "me", Transfer: "files", TransferHost: "files.example.org", Nodes: []string{"node*", "gpu??"}}
	if nc.Gateway != "hpc-tunnel" || !reflect.DeepEqual(nc.SSH, want) {
		t.Fatalf("got %q %+v", nc.Gateway, nc.SSH)
	}
	review := s.shown[slicesIndex(s.asked, "Apply these changes?")]
	if review.Body != "# preview via hpc-tunnel for me" {
		t.Fatalf("review body %q", review.Body)
	}
}

func slicesIndex(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func TestWizardExistingTransferAliasNeedsNoReview(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{"hpc-tunnel", "n", "web", "", "", "y"}}
	nc, _, err := Wizard(deps(s, &out), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if !reflect.DeepEqual(nc.SSH, &config.SSH{Transfer: "web"}) {
		t.Fatalf("got %+v", nc.SSH)
	}
}

func TestWizardDeclineKeepsTypedGateway(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{"", "you@gw.example.org", "n", "", "", "", "", "", "n", "y"}}
	nc, _, err := Wizard(deps(s, &out), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.Gateway != "you@gw.example.org" || nc.SSH != nil {
		t.Fatalf("got %q %+v", nc.Gateway, nc.SSH)
	}
	if i := slicesIndex(s.asked, "Your username on these hosts"); i < 0 {
		t.Fatalf("asked %q", s.asked)
	}
}

func TestWizardSeedSSHAsksUserAgain(t *testing.T) {
	var out bytes.Buffer
	seed := &config.NewConfig{Gateway: "shell", SSH: &config.SSH{User: "them", GatewayHost: "gw.example.org",
		Transfer: "files", TransferHost: "files.example.org", Nodes: []string{"node*"}}}
	s := &script{t: t, answers: []string{
		"", "", "n", "", // hostname list, hostname (pre-filled), no test, short name (pre-filled shell)
		"", "", "", "", // transfer (type), host, short name, nodes: all pre-filled
		"",      // no tunnels
		"", "y", // username: me (not them), apply
		"y",
	}}
	nc, _, err := Wizard(deps(s, &out), seed)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	want := &config.SSH{User: "me", GatewayHost: "gw.example.org", Transfer: "files", TransferHost: "files.example.org", Nodes: []string{"node*"}}
	if nc.Gateway != "shell" || !reflect.DeepEqual(nc.SSH, want) {
		t.Fatalf("got %q %+v", nc.Gateway, nc.SSH)
	}
}

func TestWizardNewAliasMustBeNew(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{"", "gw.example.org", "n", "web", "gw", "", "", "", "", "y", "y"}}
	if _, _, err := Wizard(deps(s, &out), nil); err != nil {
		t.Fatal(err)
	}
	s.done() // "web" was rejected (already a host), "gw" accepted
}

func TestSSHSetupKeepsUserAndDeclineAborts(t *testing.T) {
	cur := config.NewConfig{Gateway: "shell", SSH: &config.SSH{User: "them", GatewayHost: "gw.example.org", Nodes: []string{"node*"}}}
	var out bytes.Buffer
	s := &script{t: t, answers: []string{"", "", "", "", "web*", "", "", "y"}}
	nc, _, err := SSHSetup(deps(s, &out), cur)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.SSH.User != "them" || !reflect.DeepEqual(nc.SSH.Nodes, []string{"web*"}) || nc.Gateway != "shell" {
		t.Fatalf("got %q %+v", nc.Gateway, nc.SSH)
	}
	s = &script{t: t, answers: []string{"", "", "", "", "", "", "", "n"}}
	if _, _, err := SSHSetup(deps(s, &out), cur); !errors.Is(err, ErrAborted) {
		t.Fatalf("decline: %v", err)
	}
}

// TestSSHSetupManagedConfigDefinesAlias covers converting a managed-mode
// gateway (gateway = "user@host", no [ssh]): SSHSetup lets the user give it
// an alias, and the finished config uses that alias with the typed user and
// host recorded in [ssh].
func TestSSHSetupManagedConfigDefinesAlias(t *testing.T) {
	cur := config.NewConfig{Gateway: "you@gw.example.org"}
	var out bytes.Buffer
	s := &script{t: t, answers: []string{
		"",      // gateway list: "" = enter a hostname
		"",      // hostname pre-filled with you@gw.example.org
		"shell", // short name for gw.example.org
		"",      // transfer: skip (default)
		"",      // node patterns: none
		"",      // no tunnels
		"",      // username: pre-filled with "you"
		"y",     // apply
	}}
	nc, _, err := SSHSetup(deps(s, &out), cur)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	want := &config.SSH{User: "you", GatewayHost: "gw.example.org"}
	if nc.Gateway != "shell" || !reflect.DeepEqual(nc.SSH, want) {
		t.Fatalf("got %q %+v", nc.Gateway, nc.SSH)
	}
}

func TestWizardEscThroughSSHSteps(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{
		"hpc-tunnel", "n",
		transferType, "<back>", // Esc at the transfer hostname returns to the transfer list
		transferSkip, "<back>", // Esc at node patterns returns to the transfer list
		transferSkip, "node*", "", "<back>", // Esc at username returns to "Set up tunnels?"
		"<back>",        // Esc there returns to node patterns
		"", "", "", "y", // nodes kept, no tunnels, username, apply
		"y",
	}}
	nc, _, err := Wizard(deps(s, &out), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.SSH == nil || strings.Join(nc.SSH.Nodes, " ") != "node*" || nc.SSH.Transfer != "" {
		t.Fatalf("got %+v", nc.SSH)
	}
}

// TestWizardUserAtExistingAlias covers typing user@alias for an alias in
// ~/.ssh/config. tun would define a new alias whose HostName is the other
// alias, which ssh looks up in DNS; and [ssh] needs a plain alias as the
// gateway. The question refuses it and says to choose the alias instead.
func TestWizardUserAtExistingAlias(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{
		"", "you@hpc-tunnel", "hpc-tunnel", "n", // refused, then the alias; no test
		"", "node*", "", "", "y", // no transfer host, node patterns, no tunnels, username, apply
		"y",
	}}
	nc, _, err := Wizard(deps(s, &out), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	want := &config.SSH{User: "me", Nodes: []string{"node*"}}
	if nc.Gateway != "hpc-tunnel" || !reflect.DeepEqual(nc.SSH, want) {
		t.Fatalf("got %q %+v", nc.Gateway, nc.SSH)
	}
}

func TestValidateTypedGateway(t *testing.T) {
	w := newWizard(deps(&script{t: t}, &bytes.Buffer{}), nil, false)
	err := w.validateTyped("you@hpc-tunnel")
	want := "hpc-tunnel is already a host in your ssh config; enter just hpc-tunnel, " +
		"and to log in there as you, add `User you` under `Host hpc-tunnel` in ~/.ssh/config"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v", err)
	}
	for _, ok := range []string{"hpc-tunnel", "you@gw.example.org", "gw.example.org"} {
		if err := w.validateTyped(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	if w.validateTyped("") == nil {
		t.Error("empty accepted")
	}
}

// TestWizardReturnsApprovedChanges checks that the changes carried out of
// the wizard are those of the review the user accepted last: going back to
// the review and declining it leaves none.
func TestWizardReturnsApprovedChanges(t *testing.T) {
	d := func(s *script) WizardDeps {
		d := deps(s, &bytes.Buffer{})
		d.Preview = func(nc config.NewConfig) SSHPreview {
			return SSHPreview{Text: "diff", Changes: []change.Change{{Path: nc.Gateway + ":" + strings.Join(nc.SSH.Nodes, ",")}}}
		}
		return d
	}
	s := &script{t: t, answers: []string{"hpc-tunnel", "n", "", "node*", "", "", "y", "y"}}
	_, approved, err := Wizard(d(s), nil)
	s.done()
	if err != nil || len(approved) != 1 || approved[0].Path != "hpc-tunnel:node*" {
		t.Fatalf("approved %+v, err %v", approved, err)
	}

	s = &script{t: t, answers: []string{"hpc-tunnel", "n", "", "node*", "", "", "y", "<back>", "n", "y"}}
	nc, approved, err := Wizard(d(s), nil)
	s.done()
	if err != nil || approved != nil || nc.SSH != nil {
		t.Fatalf("approved %+v, ssh %+v, err %v", approved, nc.SSH, err)
	}
}

func TestWizardSkipsTunnelsByDefault(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{"hpc-tunnel", "n", "", "", "", "y"}}
	nc, _, err := Wizard(deps(s, &out), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.TunnelGateway != "" || nc.DefaultHost != "" || len(nc.Tunnels) != 0 {
		t.Fatalf("got %+v", nc)
	}
	if slicesIndex(s.asked, "Add a tunnel?") >= 0 || slicesIndex(s.asked, "Default target host (optional)") >= 0 {
		t.Fatalf("asked %q", s.asked)
	}
	last := s.ctx.Recap[len(s.ctx.Recap)-2] // then the VS Code line
	if last.Label != "Tunnels" || !strings.Contains(last.Value, "skipped") {
		t.Fatalf("recap %+v", s.ctx.Recap)
	}
}

func TestWizardSeparateTunnelConnection(t *testing.T) {
	var out bytes.Buffer
	var previewed config.NewConfig
	s := &script{t: t, answers: []string{
		"", "gw.example.org", "n", "", // typed gateway, no test, short name gw
		"", "", // no transfer, no nodes
		"y", tunnelSeparate, "", "", // tunnels: separate, host (gateway's), name (gw-tunnels)
		"", "y", // username, apply
		"", "done", "y",
	}}
	d := deps(s, &out)
	d.Preview = func(nc config.NewConfig) SSHPreview { previewed = nc; return SSHPreview{Text: "diff"} }
	nc, _, err := Wizard(d, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	want := &config.SSH{User: "me", GatewayHost: "gw.example.org", TunnelGatewayHost: "gw.example.org"}
	if nc.Gateway != "gw" || nc.TunnelGateway != "gw-tunnels" || !reflect.DeepEqual(nc.SSH, want) {
		t.Fatalf("got %q %q %+v", nc.Gateway, nc.TunnelGateway, nc.SSH)
	}
	if previewed.TunnelGateway != "gw-tunnels" || previewed.SSH.TunnelGatewayHost != "gw.example.org" {
		t.Fatalf("preview got %+v", previewed)
	}
}

func TestWizardSeparateHostDefaultsFromSSHConfig(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{
		"hpc-tunnel", "n", "", "", // existing alias, no test, no transfer, no nodes
		"y", tunnelSeparate, "", "", "", "y", // host from ssh -G, name, username, apply
		"", "done", "y",
	}}
	nc, _, err := Wizard(deps(s, &out), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.TunnelGateway != "hpc-tunnel-tunnels" || nc.SSH.TunnelGatewayHost != "hpc-tunnel.example.org" {
		t.Fatalf("got %q %+v", nc.TunnelGateway, nc.SSH)
	}
}

func TestWizardSeparateDeclinedDropsTunnelGateway(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{
		"hpc-tunnel", "n", "", "",
		"y", tunnelSeparate, "", "", "", "n", // decline the review
		"", "done", "y",
	}}
	nc, _, err := Wizard(deps(s, &out), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.TunnelGateway != "" || nc.SSH != nil {
		t.Fatalf("got %q %+v", nc.TunnelGateway, nc.SSH)
	}
	var line Line
	for _, l := range s.ctx.Recap {
		if l.Label == "Tunnel connection" {
			line = l
		}
	}
	if !strings.Contains(line.Value, "needs the ssh changes") {
		t.Fatalf("recap %+v", s.ctx.Recap)
	}
}

func TestWizardTunnelAliasMustBeNew(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{
		"", "gw.example.org", "n", "", "", "",
		"y", tunnelSeparate, "", "gw", "web", "gwt", // gw is the gateway, web is in ~/.ssh/config
		"", "y", "", "done", "y",
	}}
	nc, _, err := Wizard(deps(s, &out), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.TunnelGateway != "gwt" {
		t.Fatalf("got %q", nc.TunnelGateway)
	}
}

func TestWizardEscThroughTunnelSteps(t *testing.T) {
	var out bytes.Buffer
	s := &script{t: t, answers: []string{
		"hpc-tunnel", "n", "", "",
		"y", tunnelSeparate, "<back>", // Esc at the tunnel host returns to the connection choice
		"<back>",         // Esc at the connection choice returns to "Set up tunnels?"
		"y", tunnelShare, // yes again; the choice now defaults to separate, so pick share
		"", "done", "y",
	}}
	nc, _, err := Wizard(deps(s, &out), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.TunnelGateway != "" {
		t.Fatalf("got %q", nc.TunnelGateway)
	}
}

func TestSSHSetupTunnelGateway(t *testing.T) {
	cur := config.NewConfig{Gateway: "shell", TunnelGateway: "shell-tunnels",
		SSH: &config.SSH{User: "me", GatewayHost: "gw.example.org", TunnelGatewayHost: "gw.example.org"}}
	var out bytes.Buffer
	// Pre-filled: tunnels Yes, separate, host, name; keep everything.
	s := &script{t: t, answers: []string{"", "", "", "", "", "", "", "", "", "", "y"}}
	nc, _, err := SSHSetup(deps(s, &out), cur)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.TunnelGateway != "shell-tunnels" || nc.SSH.TunnelGatewayHost != "gw.example.org" {
		t.Fatalf("got %q %+v", nc.TunnelGateway, nc.SSH)
	}
	// Answering No removes it.
	s = &script{t: t, answers: []string{"", "", "", "", "", "n", "", "y"}}
	nc, _, err = SSHSetup(deps(s, &out), cur)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.TunnelGateway != "" || nc.SSH.TunnelGatewayHost != "" {
		t.Fatalf("got %q %+v", nc.TunnelGateway, nc.SSH)
	}
}

func TestSSHSetupKeepsHandEditedTunnelGateway(t *testing.T) {
	cur := config.NewConfig{Gateway: "shell", TunnelGateway: "mytun",
		SSH: &config.SSH{User: "me", GatewayHost: "gw.example.org"}}
	var out bytes.Buffer
	s := &script{t: t, answers: []string{"", "", "", "", "", "", "", "", "y"}} // tunnels Yes, connection Keep
	nc, _, err := SSHSetup(deps(s, &out), cur)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.TunnelGateway != "mytun" || nc.SSH.TunnelGatewayHost != "" {
		t.Fatalf("got %q %+v", nc.TunnelGateway, nc.SSH)
	}
}

// On Windows the tunnel questions are asked and a seed's tunnels are kept;
// the share-or-separate connection questions are not, and a seed's tunnel
// gateway is kept as it is.
func TestWizardNoControlMasterKeepsSeedTunnels(t *testing.T) {
	var out bytes.Buffer
	seed := &config.NewConfig{Gateway: "shell", TunnelGateway: "shell-tunnels", DefaultHost: "node1",
		Tunnels: []config.Tunnel{{Name: "chat", Port: 1080, Route: config.RouteSocks}},
		SSH:     &config.SSH{User: "them", GatewayHost: "gw.example.org", TunnelGatewayHost: "gw.example.org"}}
	// list, host, sign-in test (no), name, transfer, nodes, tunnels (yes), user, apply, default host, tunnels done, write
	s := &script{t: t, answers: []string{"", "", "n", "", "", "", "y", "", "y", "", "", "y"}}
	d := deps(s, &out)
	d.NoControlMaster = true
	nc, _, err := Wizard(d, seed)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	for _, q := range []string{"Tunnel connection", "Tunnel host", "Short name for the tunnel connection"} {
		if slicesIndex(s.asked, q) >= 0 {
			t.Fatalf("asked %q: %q", q, s.asked)
		}
	}
	if nc.TunnelGateway != "shell-tunnels" || nc.SSH.TunnelGatewayHost != "gw.example.org" {
		t.Fatalf("seed tunnel settings not kept: %q %+v", nc.TunnelGateway, nc.SSH)
	}
	if len(nc.Tunnels) != 1 || nc.DefaultHost != "node1" {
		t.Fatalf("seed tunnels dropped: %+v %q", nc.Tunnels, nc.DefaultHost)
	}
}

// TestSSHSetupKeepMatchingGateway covers a hand-edited tunnel_gateway that
// the user then picks as the gateway: Keep is not offered, since the config
// would fail validation after the ssh changes were written, and tunnels
// share the gateway's connection instead.
func TestSSHSetupKeepMatchingGateway(t *testing.T) {
	cur := config.NewConfig{Gateway: "shell", TunnelGateway: "hpc-tunnel",
		SSH: &config.SSH{User: "me", GatewayHost: "gw.example.org"}}
	// Gateway hpc-tunnel, no transfer, no nodes, tunnels Yes, connection default.
	s := &script{t: t, answers: []string{"hpc-tunnel", "", "", "", ""}}
	nc, _, err := SSHSetup(deps(s, &bytes.Buffer{}), cur)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.Gateway != "hpc-tunnel" || nc.TunnelGateway != "" {
		t.Fatalf("got %q %q", nc.Gateway, nc.TunnelGateway)
	}
	s = &script{t: t, answers: []string{"hpc-tunnel", "", "", "", tunnelKeep}}
	if _, _, err := SSHSetup(deps(s, &bytes.Buffer{}), cur); err == nil || !strings.Contains(err.Error(), "not an option") {
		t.Fatalf("Keep offered: %v", err)
	}
}

func TestSSHSetupKeepMatchingTransfer(t *testing.T) {
	cur := config.NewConfig{Gateway: "shell", TunnelGateway: "web",
		SSH: &config.SSH{User: "me", GatewayHost: "gw.example.org"}}
	s := &script{t: t, answers: []string{
		"", "", "", "web", "", // gateway kept, transfer web, no nodes
		"", "", // tunnels Yes, connection default
		"", "y", // username, apply
	}}
	nc, _, err := SSHSetup(deps(s, &bytes.Buffer{}), cur)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if nc.SSH.Transfer != "web" || nc.TunnelGateway != "" {
		t.Fatalf("got %q %+v", nc.TunnelGateway, nc.SSH)
	}
}

// TestKeepChoiceGoesStale covers Keep chosen while it was valid, then the
// gateway changed to the kept alias: the answers no longer give it.
func TestKeepChoiceGoesStale(t *testing.T) {
	seed := &config.NewConfig{Gateway: "shell", TunnelGateway: "hpc-tunnel",
		SSH: &config.SSH{User: "me", GatewayHost: "gw.example.org"}}
	w := newWizard(deps(&script{t: t}, &bytes.Buffer{}), seed, true)
	if w.tunChoice != tunnelKeep || w.tunnelGateway() != "hpc-tunnel" {
		t.Fatalf("seed: %q %q", w.tunChoice, w.tunnelGateway())
	}
	w.choice, w.gwHost = "hpc-tunnel", ""
	if got := w.tunnelGateway(); got != "" {
		t.Fatalf("stale keep gives %q", got)
	}
	if got := w.tunnelRecap(); got != "shared with the gateway" {
		t.Fatalf("recap %q", got)
	}
}

// TestTunnelHostWarnsForAliasGateway covers a separate tunnel connection for
// a gateway from ~/.ssh/config: tun defines it with only HostName and User,
// so the question says the alias's other settings are not copied.
func TestTunnelHostWarnsForAliasGateway(t *testing.T) {
	s := &script{t: t, answers: []string{
		"hpc-tunnel", "n", "", "",
		"y", tunnelSeparate, "", "", "", "y",
		"", "done", "y",
	}}
	if _, _, err := Wizard(deps(s, &bytes.Buffer{}), nil); err != nil {
		t.Fatal(err)
	}
	s.done()
	desc := s.inputs["Tunnel host"]
	if !strings.Contains(desc, "only HostName and User") || !strings.Contains(desc, "for hpc-tunnel") {
		t.Fatalf("description %q", desc)
	}

	s = &script{t: t, answers: []string{
		"", "gw.example.org", "n", "",
		"", "",
		"y", tunnelSeparate, "", "",
		"", "y",
		"", "done", "y",
	}}
	if _, _, err := Wizard(deps(s, &bytes.Buffer{}), nil); err != nil {
		t.Fatal(err)
	}
	s.done()
	if desc := s.inputs["Tunnel host"]; desc == "" || strings.Contains(desc, "HostName") {
		t.Fatalf("typed host description %q", desc)
	}
}
