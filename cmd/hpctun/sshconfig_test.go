package main

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"hpctun/internal/config"
	"hpctun/internal/prompt"
	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
)

// sshG answers ssh -G the way the generated config would.
func sshG() *sshctltest.Fake {
	return &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		host := c.Args[len(c.Args)-1]
		out := "hostname " + host + ".example.org\ncontrolmaster auto\nproxyjump shell\n"
		if host == "shell" || host == "files" {
			out = "hostname " + host + ".example.org\ncontrolmaster auto\n"
		}
		return sshctl.Result{Stdout: []byte(out)}, nil
	}}
}

type answerAsker struct {
	prompt.Asker
	yes bool
}

func (a answerAsker) Confirm(string, string, bool) (bool, error) { return a.yes, nil }
func (answerAsker) SetContext(prompt.Context)                    {}

// Outside and Close are spelled out: prompt.Plain would clash with the
// embedded Asker's.
func (answerAsker) Outside(fn func() error) error { return fn() }
func (answerAsker) Close()                        {}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestSSHConfigShowReportsMissingInclude(t *testing.T) {
	a, out, _ := testApp(t, sshCfg)
	a.run = sshG()
	err := runCmd(a, "ssh-config")
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("err = %v", err)
	}
	for _, s := range []string{"Host shell\n", "ProxyJump shell", "problem: ", "does not include hpctun_config"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("missing %q in\n%s", s, out.String())
		}
	}
}

func TestSSHConfigSyncNonInteractiveOnlyShowsDiffs(t *testing.T) {
	a, out, _ := testApp(t, sshCfg)
	a.run = sshG()
	err := runCmd(a, "ssh-config", "sync")
	if err == nil || exitCode(err) != 1 || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
	for _, s := range []string{
		"--- /dev/null\n+++ ~/.ssh/hpctun_config\n",
		"+Host shell\n",
		"--- /dev/null\n+++ ~/.ssh/config\n@@ -0,0 +1,1 @@\n+Include hpctun_config\n",
	} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("missing %q in\n%s", s, out.String())
		}
	}
	p := a.sshPaths()
	if exists(p.TunConfig) || exists(p.UserConfig) {
		t.Fatal("files written without approval")
	}
}

func TestSSHConfigSyncYesApplies(t *testing.T) {
	a, out, _ := testApp(t, sshCfg)
	a.run = sshG()
	if err := runCmd(a, "ssh-config", "sync", "--yes"); err != nil {
		t.Fatal(err)
	}
	p := a.sshPaths()
	if b, _ := os.ReadFile(p.UserConfig); string(b) != "Include hpctun_config\n" {
		t.Fatalf("user config %q", b)
	}
	if !exists(p.Sockets) || !strings.Contains(out.String(), "ssh sees the setup as written") {
		t.Fatalf("sockets %v, out\n%s", exists(p.Sockets), out.String())
	}
	out.Reset()
	if err := runCmd(a, "ssh-config", "sync"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ssh config is up to date") {
		t.Fatalf("second sync: %s", out.String())
	}
}

// TestSyncMigratesLegacy covers a beta user's ~/.ssh from before the rename:
// sync migrates tun_config's Include, moves tun_known_hosts, and removes the
// leftover tun_config, so a second sync then has nothing left to do.
func TestSyncMigratesLegacy(t *testing.T) {
	a, out, _ := testApp(t, sshCfg)
	a.run = sshG()
	p := a.sshPaths()
	writeFile(t, p.LegacyTunConfig, "Host old\n")
	writeFile(t, p.UserConfig, "Include tun_config\n")
	writeFile(t, p.LegacyKnownHosts, "@cert-authority * ssh-ed25519 AAAA\n")

	if err := runCmd(a, "ssh-config", "sync", "--yes"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "removed ~/.ssh/tun_config") || !strings.Contains(out.String(), "wrote ~/.ssh/hpctun_config") {
		t.Fatalf("out: %s", out.String())
	}
	if !exists(p.TunConfig) {
		t.Fatal("hpctun_config not written")
	}
	if exists(p.LegacyTunConfig) || exists(p.LegacyKnownHosts) {
		t.Fatal("legacy files not removed")
	}
	if b, err := os.ReadFile(p.KnownHosts); err != nil || string(b) != "@cert-authority * ssh-ed25519 AAAA\n" {
		t.Fatalf("known hosts: %q %v", b, err)
	}
	uc, _ := os.ReadFile(p.UserConfig)
	if !strings.Contains(string(uc), "Include hpctun_config") || strings.Contains(string(uc), "Include tun_config") {
		t.Fatalf("user config: %q", uc)
	}
	if b, err := os.ReadFile(p.UserConfig + ".bak-hpctun"); err != nil || string(b) != "Include tun_config\n" {
		t.Fatalf("backup: %q %v", b, err)
	}

	out.Reset()
	if err := runCmd(a, "ssh-config", "sync"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ssh config is up to date") {
		t.Fatalf("second sync: %s", out.String())
	}
}

func TestSSHConfigSyncAsks(t *testing.T) {
	yes := true
	a, _, _ := testApp(t, sshCfg)
	a.run, a.tty, a.ask = sshG(), &yes, answerAsker{yes: false}
	if err := runCmd(a, "ssh-config", "sync"); err == nil || !strings.Contains(err.Error(), "not applied") {
		t.Fatalf("declined: %v", err)
	}
	if exists(a.sshPaths().TunConfig) {
		t.Fatal("declined but written")
	}
	a.ask = answerAsker{yes: true}
	if err := runCmd(a, "ssh-config", "sync"); err != nil {
		t.Fatal(err)
	}
	if !exists(a.sshPaths().TunConfig) {
		t.Fatal("approved but not written")
	}
}

// transferOnlyCfg is the [ssh] table the wizard produces when only an
// existing alias is picked for the transfer host: tun defines nothing.
const transferOnlyCfg = `gateway = "shell"
[ssh]
transfer = "files"
`

func TestSSHConfigTransferOnlyExitsZero(t *testing.T) {
	a, out, _ := testApp(t, transferOnlyCfg)
	a.run = sshG()
	if err := runCmd(a, "ssh-config"); err != nil {
		t.Fatalf("err = %v, out %s", err, out.String())
	}
	if strings.Contains(out.String(), "problem: ") {
		t.Fatalf("out %s", out.String())
	}
}

func TestSSHConfigSyncTransferOnlyIsUpToDate(t *testing.T) {
	a, out, _ := testApp(t, transferOnlyCfg)
	a.run = sshG()
	if err := runCmd(a, "ssh-config", "sync", "--yes"); err != nil {
		t.Fatalf("err = %v, out %s", err, out.String())
	}
	if !strings.Contains(out.String(), "ssh config is up to date") {
		t.Fatalf("out %s", out.String())
	}
	if exists(a.sshPaths().TunConfig) {
		t.Fatal("hpctun_config written for a transfer-only table")
	}
}

func TestSSHConfigWithoutTable(t *testing.T) {
	a, out, _ := testApp(t, "gateway = \"me@gw.example.org\"\n")
	if err := runCmd(a, "ssh-config"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hpctun ssh-config setup") {
		t.Fatalf("out %s", out.String())
	}
	if err := runCmd(a, "ssh-config", "sync"); err == nil || exitCode(err) != 2 {
		t.Fatalf("sync without [ssh]: %v", err)
	}
}

// TestSSHConfigSyncRecreatesSockets covers a deleted ~/.ssh/sockets with the
// files otherwise up to date: sync creates it again instead of reporting
// the problem it could fix.
func TestSSHConfigSyncRecreatesSockets(t *testing.T) {
	a, out, _ := testApp(t, sshCfg)
	a.run = sshG()
	if err := runCmd(a, "ssh-config", "sync", "--yes"); err != nil {
		t.Fatal(err)
	}
	p := a.sshPaths()
	if err := os.Remove(p.Sockets); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runCmd(a, "ssh-config", "sync"); err != nil {
		t.Fatalf("err = %v, out %s", err, out.String())
	}
	if !exists(p.Sockets) || !strings.Contains(out.String(), "created ~/.ssh/sockets\n") ||
		strings.Contains(out.String(), "up to date") {
		t.Fatalf("sockets %v, out\n%s", exists(p.Sockets), out.String())
	}
}

// TestSSHConfigSyncExcludesUserGatewayHost covers a gateway alias defined in
// the user's own ssh config whose host name matches a node pattern: the node
// block leaves that host name alone, and so do copies.
func TestSSHConfigSyncExcludesUserGatewayHost(t *testing.T) {
	a, _, _ := testApp(t, "gateway = \"hpc\"\n[ssh]\nuser = \"me\"\nnodes = [\"*.example.org\"]\n")
	a.run = &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		out := "hostname login.example.org\ncontrolmaster auto\n"
		if host := c.Args[len(c.Args)-1]; host != "hpc" {
			out = "hostname " + host + "\ncontrolmaster auto\nproxyjump hpc\n"
		}
		return sshctl.Result{Stdout: []byte(out)}, nil
	}}
	if err := runCmd(a, "ssh-config", "sync", "--yes"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(a.sshPaths().TunConfig)
	if !strings.Contains(string(b), "Host *.example.org !hpc !login.example.org\n") {
		t.Fatalf("hpctun_config:\n%s", b)
	}
	route, err := a.copyRoute(context.Background(), mustLoad(t, a))
	if err != nil || route.IsNode("login.example.org") || !route.IsNode("n1.example.org") {
		t.Fatalf("route: %v", err)
	}
}

func mustLoad(t *testing.T, a *app) *config.Config {
	t.Helper()
	cfg, err := a.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// setupAnswers accept every pre-filled answer of tun ssh-config setup for
// sshCfg (no aliases in ~/.ssh/config, so no gateway list) and approve.
var setupAnswers = []string{"", "", "", "", "", "", "", "", "y"}

func TestSSHConfigSetupAppliesReview(t *testing.T) {
	a, _, _ := testApp(t, sshCfg)
	yes := true
	s := &initScript{t: t, answers: append(slices.Clone(setupAnswers), "n")}
	a.run, a.tty, a.ask = sshG(), &yes, s
	before, _ := os.ReadFile(a.configPath)
	if err := runCmd(a, "ssh-config", "setup"); err != nil {
		t.Fatal(err)
	}
	s.done()
	p := a.sshPaths()
	if b, _ := os.ReadFile(p.UserConfig); string(b) != "Include hpctun_config\n" || !exists(p.TunConfig) {
		t.Fatalf("~/.ssh/config %q, hpctun_config %v", b, exists(p.TunConfig))
	}
	if after, _ := os.ReadFile(a.configPath); string(after) != string(before) {
		t.Fatalf("config changed:\n%s", after)
	}
}

// TestSSHConfigSetupKeepsTunnelConnection: setup pre-fills a separate
// tunnel connection, and accepting everything keeps it, in the reviewed
// hpctun_config and config file alike.
func TestSSHConfigSetupKeepsTunnelConnection(t *testing.T) {
	cfg := strings.Replace(sshCfg, "[ssh]\n", "tunnel_gateway = \"shell-tunnels\"\n[ssh]\ntunnel_gateway_host = \"shell.example.org\"\n", 1)
	a, _, _ := testApp(t, cfg)
	yes := true
	// gateway, short name, transfer, its host and name, nodes, tunnels (Yes),
	// connection (separate), tunnel host and name, username; apply; no key.
	s := &initScript{t: t, answers: []string{"", "", "", "", "", "", "", "", "", "", "", "y", "n"}}
	a.run, a.tty, a.ask = sshG(), &yes, s
	if err := runCmd(a, "ssh-config", "setup"); err != nil {
		t.Fatal(err)
	}
	s.done()
	tc, _ := os.ReadFile(a.sshPaths().TunConfig)
	if !strings.Contains(string(tc), "Host shell-tunnels\n") || !strings.Contains(string(tc), "tunnels-%C") {
		t.Fatalf("hpctun_config:\n%s", tc)
	}
	after, _ := os.ReadFile(a.configPath)
	if !strings.Contains(string(after), "tunnel_gateway = \"shell-tunnels\"") || !strings.Contains(string(after), "tunnel_gateway_host = \"shell.example.org\"") {
		t.Fatalf("config:\n%s", after)
	}
}

// TestSSHConfigSetupReviewedFileChangedMeanwhile covers a file changing
// after the review was approved: nothing at all is written.
func TestSSHConfigSetupReviewedFileChangedMeanwhile(t *testing.T) {
	a, _, _ := testApp(t, sshCfg)
	yes := true
	s := &initScript{t: t, answers: setupAnswers}
	p := a.sshPaths()
	const edited = "Host mine\n"
	a.run, a.tty = sshG(), &yes
	a.ask = editOnApprove{s, func() { writeFile(t, p.UserConfig, edited) }}
	err := runCmd(a, "ssh-config", "setup")
	if err == nil || !strings.Contains(err.Error(), "~/.ssh/config changed since") {
		t.Fatalf("err = %v", err)
	}
	s.done()
	if b, _ := os.ReadFile(p.UserConfig); string(b) != edited || exists(p.TunConfig) {
		t.Fatalf("~/.ssh/config %q, hpctun_config %v", b, exists(p.TunConfig))
	}
}

// TestSSHConfigShowBeforeInit: with no config yet, point at tun init, not at
// tun ssh-config setup (which needs a config). Bare tun shows this on
// Windows on first run.
func TestSSHConfigShowBeforeInit(t *testing.T) {
	a, out, _ := testApp(t, "")
	if err := runCmd(a, "ssh-config"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "run `hpctun init`") || strings.Contains(out.String(), "ssh-config setup") {
		t.Fatalf("out %s", out.String())
	}
}

// TestSSHConfigSyncWritesNoCodeAlias: hpctun code connects through VS
// Code's Remote - Tunnels, so [code] adds nothing to hpctun_config.
func TestSSHConfigSyncWritesNoCodeAlias(t *testing.T) {
	a, out, _ := testApp(t, sshCfg+"\n[code]\n")
	a.run = sshG()
	if err := runCmd(a, "ssh-config", "sync", "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	b, err := os.ReadFile(a.sshPaths().TunConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"hpc-job", "code --proxy", "HostKeyAlias"} {
		if strings.Contains(string(b), s) {
			t.Errorf("hpctun_config has %q:\n%s", s, b)
		}
	}
}
