package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hpctun/internal/config"
	"hpctun/internal/prompt"
	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
)

// initScript is a minimal scripted prompt.Asker for the runInit tests:
// answers are consumed in order, "" accepts the pre-filled value (else the
// default), and "y"/"n" answer a Confirm.
type initScript struct {
	prompt.Plain
	t       *testing.T
	answers []string
	codeQ   bool // the script answers prompt.CodeQuestion itself
}

func (s *initScript) next(title string) string {
	s.t.Helper()
	if len(s.answers) == 0 {
		s.t.Fatalf("unexpected question %q", title)
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a
}

func (s *initScript) Input(q prompt.InputQ) (string, error) {
	a := s.next(q.Title)
	if a == "" {
		a = q.Value
	}
	if a == "" {
		a = q.Default
	}
	return a, nil
}

func (s *initScript) Select(q prompt.SelectQ) (string, error) {
	a := s.next(q.Title)
	if a == "" {
		a = q.Default
	}
	return a, nil
}

func (s *initScript) Confirm(title, _ string, def bool) (bool, error) {
	if title == prompt.CodeQuestion && !s.codeQ {
		return def, nil // taken at its default unless the test answers it
	}
	switch s.next(title) {
	case "y":
		return true, nil
	case "n":
		return false, nil
	default:
		return def, nil
	}
}

func (s *initScript) SetContext(prompt.Context) {}

func (s *initScript) done() {
	s.t.Helper()
	if len(s.answers) != 0 {
		s.t.Fatalf("unused answers %q", s.answers)
	}
}

// initRunner answers lsof (probe.Take) with no listeners and ssh -G with
// hostname <dest>.example.org, controlmaster auto: enough for the gateway
// mode lookups and the post-write conflict check that runInit makes, never
// backgrounding a real connection since the tests always decline the
// connection test.
func initRunner() *sshctltest.Fake {
	return &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		if c.Name == "lsof" {
			return sshctl.Result{}, nil
		}
		if c.Name == "ssh" && strings.Contains(c.Line(), " -G ") {
			dest := c.Args[len(c.Args)-1]
			return sshctl.Result{Stdout: []byte("hostname " + dest + ".example.org\ncontrolmaster auto\n")}, nil
		}
		return sshctl.Result{}, nil
	}}
}

func initApp(t *testing.T, answers []string) (*app, *bytes.Buffer, *initScript) {
	t.Helper()
	a, out, _ := testApp(t, "")
	yes := true
	s := &initScript{t: t, answers: answers}
	a.run, a.tty, a.ask = initRunner(), &yes, s
	return a, out, s
}

// TestInitSSHSetupAccepted covers a fresh HOME with no ~/.ssh/config: a
// typed gateway is accepted at the ssh review, so hpctun_config and the
// Include are written under the temp HOME, then config.toml.
func TestInitSSHSetupAccepted(t *testing.T) {
	a, _, s := initApp(t, []string{
		"gw.example.org", // typed gateway hostname
		"n",              // skip the connection test
		"",               // short name: gw (default)
		"",               // transfer: skip (default)
		"",               // node patterns: none
		"",               // no tunnels
		"",               // username: me (default, from $USER)
		"y",              // apply the ssh changes
		"y",              // write the config
		"n",              // decline the ssh key offer
	})
	if err := runCmd(a, "init"); err != nil {
		t.Fatal(err)
	}
	s.done()

	home := a.getenv("HOME")
	tc, err := os.ReadFile(filepath.Join(home, ".ssh", "hpctun_config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tc), "Host gw\n") || !strings.Contains(string(tc), "HostName gw.example.org") {
		t.Fatalf("hpctun_config:\n%s", tc)
	}
	uc, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(uc) != "Include hpctun_config\n" {
		t.Fatalf("user config %q", uc)
	}
	cfg, err := os.ReadFile(a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "gateway = \"gw\"") || !strings.Contains(string(cfg), "[ssh]") {
		t.Fatalf("config.toml:\n%s", cfg)
	}
}

// TestInitSSHReviewDeclined covers declining the ssh review: nothing is
// written under ~/.ssh, and config.toml keeps the typed user@host gateway
// with no [ssh] table, exactly as tun already worked before this feature.
func TestInitSSHReviewDeclined(t *testing.T) {
	a, _, s := initApp(t, []string{
		"you@gw.example.org", // typed gateway, user@host
		"n",                  // skip the connection test
		"",                   // short name: gw (default, unused once declined)
		"",                   // transfer: skip (default)
		"",                   // node patterns: none
		"",                   // no tunnels
		"",                   // username: you (default)
		"n",                  // decline the ssh changes
		"y",                  // write the config
		"n",                  // decline the ssh key offer
	})
	if err := runCmd(a, "init"); err != nil {
		t.Fatal(err)
	}
	s.done()

	home := a.getenv("HOME")
	if exists(filepath.Join(home, ".ssh", "hpctun_config")) || exists(filepath.Join(home, ".ssh", "config")) {
		t.Fatal("ssh files written despite declining the review")
	}
	cfg, err := os.ReadFile(a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "gateway = \"you@gw.example.org\"") || strings.Contains(string(cfg), "[ssh]") {
		t.Fatalf("config.toml:\n%s", cfg)
	}
}

// TestInitTransferOnlyDefinesNothing covers I1: picking an existing alias as
// the transfer host, with nothing else defined, produces an [ssh] table
// tun writes nothing for. No ~/.ssh files are written or need to be, and the
// post-write conflict check must not report a problem.
func TestInitTransferOnlyDefinesNothing(t *testing.T) {
	a, out, s := initApp(t, []string{
		"gw",    // gateway: pick the existing "gw" alias
		"n",     // skip the connection test
		"files", // transfer: pick the existing "files" alias
		"",      // node patterns: none
		"",      // no tunnels
		"y",     // write the config
		"n",     // decline the ssh key offer
	})
	home := a.getenv("HOME")
	writeFile(t, filepath.Join(home, ".ssh", "config"),
		"Host gw\n    HostName gw.example.org\n\nHost files\n    HostName files.example.org\n")

	if err := runCmd(a, "init"); err != nil {
		t.Fatal(err)
	}
	s.done()

	if exists(filepath.Join(home, ".ssh", "hpctun_config")) {
		t.Fatal("hpctun_config written for a transfer-only table")
	}
	if strings.Contains(out.String(), "problem: ") {
		t.Fatalf("out:\n%s", out.String())
	}
	cfg, err := os.ReadFile(a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "gateway = \"gw\"") || !strings.Contains(string(cfg), "transfer = \"files\"") {
		t.Fatalf("config.toml:\n%s", cfg)
	}
}

// TestInitDecliningToClearLeftoverHPCTunConfigContinues covers M11: when
// nothing is defined (transfer set to an existing alias), the only possible
// ssh change is clearing a hpctun_config left over from an earlier setup.
// Declining that must not fail init; the file is simply left alone.
func TestInitDecliningToClearLeftoverHPCTunConfigContinues(t *testing.T) {
	a, _, s := initApp(t, []string{
		"gw",    // gateway: pick the existing "gw" alias
		"n",     // skip the connection test
		"files", // transfer: pick the existing "files" alias
		"",      // node patterns: none
		"",      // no tunnels
		"y",     // write the config
		"n",     // decline clearing the leftover hpctun_config
		"n",     // decline the ssh key offer
	})
	home := a.getenv("HOME")
	writeFile(t, filepath.Join(home, ".ssh", "config"),
		"Host gw\n    HostName gw.example.org\n\nHost files\n    HostName files.example.org\n")
	const leftover = "Host old\n    HostName old.example.org\n"
	writeFile(t, filepath.Join(home, ".ssh", "hpctun_config"), leftover)

	if err := runCmd(a, "init"); err != nil {
		t.Fatalf("init failed on a declined clear: %v", err)
	}
	s.done()

	got, err := os.ReadFile(filepath.Join(home, ".ssh", "hpctun_config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != leftover {
		t.Fatalf("leftover hpctun_config changed despite declining:\n%s", got)
	}
	cfg, err := os.ReadFile(a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "gateway = \"gw\"") || !strings.Contains(string(cfg), "transfer = \"files\"") {
		t.Fatalf("config.toml:\n%s", cfg)
	}
}

// TestInitDecliningLegacyTunConfigMigrationContinues covers a beta user's
// leftover ~/.ssh/tun_config from before the rename: init still proposes
// migrating/removing it (sshsetup.LegacyChanges), and declining that must
// not fail init either; the legacy file is simply left alone.
func TestInitDecliningLegacyTunConfigMigrationContinues(t *testing.T) {
	a, _, s := initApp(t, []string{
		"gw",    // gateway: pick the existing "gw" alias
		"n",     // skip the connection test
		"files", // transfer: pick the existing "files" alias
		"",      // node patterns: none
		"",      // no tunnels
		"y",     // write the config
		"n",     // decline migrating the leftover tun_config
		"n",     // decline the ssh key offer
	})
	home := a.getenv("HOME")
	writeFile(t, filepath.Join(home, ".ssh", "config"),
		"Host gw\n    HostName gw.example.org\n\nHost files\n    HostName files.example.org\n")
	const leftover = "Host old\n    HostName old.example.org\n"
	writeFile(t, filepath.Join(home, ".ssh", "tun_config"), leftover)

	if err := runCmd(a, "init"); err != nil {
		t.Fatalf("init failed on a declined migration: %v", err)
	}
	s.done()

	got, err := os.ReadFile(filepath.Join(home, ".ssh", "tun_config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != leftover {
		t.Fatalf("leftover tun_config changed despite declining:\n%s", got)
	}
	cfg, err := os.ReadFile(a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "gateway = \"gw\"") || !strings.Contains(string(cfg), "transfer = \"files\"") {
		t.Fatalf("config.toml:\n%s", cfg)
	}
}

// editOnApprove runs edit right after the ssh review is approved, as if the
// user changed a file while tun was still asking questions.
type editOnApprove struct {
	*initScript
	edit func()
}

func (e editOnApprove) Confirm(title, desc string, def bool) (bool, error) {
	ok, err := e.initScript.Confirm(title, desc, def)
	if title == "Apply these changes?" && ok {
		e.edit()
	}
	return ok, err
}

// TestInitReviewedFileChangedMeanwhile covers ~/.ssh/config changing after
// the review was approved: tun writes none of the ssh changes rather than
// something other than what was shown, still writes config.toml, and says
// how to finish.
func TestInitReviewedFileChangedMeanwhile(t *testing.T) {
	a, _, errb := testApp(t, "")
	yes := true
	s := &initScript{t: t, answers: []string{"gw.example.org", "n", "", "", "", "", "", "y", "y", "n"}}
	userConfig := filepath.Join(a.getenv("HOME"), ".ssh", "config")
	const edited = "Host mine\n    HostName mine.example.org\n"
	a.run, a.tty = initRunner(), &yes
	a.ask = editOnApprove{s, func() { writeFile(t, userConfig, edited) }}
	if err := runCmd(a, "init"); err != nil {
		t.Fatal(err)
	}
	s.done()
	if got, _ := os.ReadFile(userConfig); string(got) != edited {
		t.Fatalf("~/.ssh/config:\n%s", got)
	}
	if exists(filepath.Join(a.getenv("HOME"), ".ssh", "hpctun_config")) {
		t.Fatal("hpctun_config written")
	}
	if !strings.Contains(errb.String(), "~/.ssh/config changed since") || !strings.Contains(errb.String(), "hpctun ssh-config sync") {
		t.Fatalf("stderr: %s", errb.String())
	}
	if cfg, _ := os.ReadFile(a.configPath); !strings.Contains(string(cfg), "[ssh]") {
		t.Fatalf("config.toml:\n%s", cfg)
	}
}

// osuSite is the shipped site file, relative to this package.
var osuSite = filepath.Join("..", "..", "sites", "osu.toml")

// TestInitFromOSUSite covers onboarding from the shipped site file with a
// fresh HOME: every seeded answer is accepted as the default, the wizard
// asks for the username, and shell/files are defined with the OSU hosts.
func TestInitFromOSUSite(t *testing.T) {
	a, _, s := initApp(t, []string{
		"",  // gateway hostname: shell.hpc.oregonstate.edu (seeded)
		"n", // skip the connection test
		"",  // short name: shell (seeded)
		"",  // transfer: Enter a hostname (seeded)
		"",  // transfer hostname: files.hpc.oregonstate.edu (seeded)
		"",  // short name: files (seeded)
		"",  // node patterns: none
		"",  // no tunnels
		"",  // hpctun code: Yes (the site file has [code])
		"",  // username: me (from $USER; the site file has none)
		"y", // apply the ssh changes
		"y", // write the config
		"n", // decline the ssh key offer
	})
	s.codeQ = true
	if err := runCmd(a, "init", "--from", osuSite); err != nil {
		t.Fatal(err)
	}
	s.done()

	tc, err := os.ReadFile(filepath.Join(a.getenv("HOME"), ".ssh", "hpctun_config"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Host shell\n", "HostName shell.hpc.oregonstate.edu",
		"Host files\n", "HostName files.hpc.oregonstate.edu",
		"User me",
	} {
		if !strings.Contains(string(tc), want) {
			t.Errorf("hpctun_config lacks %q:\n%s", want, tc)
		}
	}
	cfg, err := config.Load(a.configPath) // the written config passes the strict loader
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gateway != "shell" || cfg.SSH == nil || cfg.SSH.User != "me" ||
		cfg.SSH.GatewayHost != "shell.hpc.oregonstate.edu" ||
		cfg.SSH.Transfer != "files" || cfg.SSH.TransferHost != "files.hpc.oregonstate.edu" {
		t.Fatalf("config = %+v %+v", cfg, cfg.SSH)
	}
	if !reflect.DeepEqual(cfg.Code, config.DefaultCode()) {
		t.Fatalf("code = %+v", cfg.Code)
	}
	if len(cfg.Tunnels) != 0 {
		t.Fatalf("tunnels = %+v; the tunnel question defaults to No", cfg.Tunnels)
	}
}

// TestInitFromOSUSiteWithTunnels: Yes to the tunnel question keeps the
// site file's LLM gateway tunnels.
func TestInitFromOSUSiteWithTunnels(t *testing.T) {
	a, _, s := initApp(t, []string{
		"", "n", "", "", "", "", "", // gateway, no test, short name, transfer, host, short name, nodes
		"y",    // set up tunnels
		"",     // share the gateway's connection
		"",     // hpctun code: Yes
		"",     // username: me
		"y",    // apply the ssh changes
		"",     // default host: none
		"done", // keep the site's tunnels
		"y",    // write the config
		"n",    // decline the ssh key offer
		"n",    // do not open the tunnels now
	})
	s.codeQ = true
	if err := runCmd(a, "init", "--from", osuSite); err != nil {
		t.Fatal(err)
	}
	s.done()
	cfg, err := config.Load(a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []config.Tunnel{
		{Name: "chat", Port: 1080, Route: config.RouteSocks, URL: "https://chat.ceoas.oregonstate.edu"},
		{Name: "llm", Port: 8090, Host: "ai-gateway.hpc.oregonstate.edu", RemotePort: 8090, Route: config.RouteMaster, NoBrowse: true},
	}
	if !reflect.DeepEqual(cfg.Tunnels, want) {
		t.Fatalf("tunnels = %+v", cfg.Tunnels)
	}
}

// TestInitFromOSUSiteWithoutCode: answering No to hpctun code leaves the
// site's [code] out.
func TestInitFromOSUSiteWithoutCode(t *testing.T) {
	a, _, s := initApp(t, []string{"", "n", "", "", "", "", "", "", "n", "", "y", "y", "n"})
	s.codeQ = true
	if err := runCmd(a, "init", "--from", osuSite); err != nil {
		t.Fatal(err)
	}
	s.done()
	cfg, err := config.Load(a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Code != nil {
		t.Fatalf("code = %+v", cfg.Code)
	}
}

// TestInitFromSeedKeepsVSCode covers a site file with a [vscode] table: the
// wizard asks nothing new and the written config keeps the table.
func TestInitFromSeedKeepsVSCode(t *testing.T) {
	osu, err := os.ReadFile(osuSite)
	if err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(t.TempDir(), "site.toml")
	body := string(osu) + "\n[vscode]\nsettings = \"/local/site/vscode.jsonc\"\n"
	if err := os.WriteFile(seed, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a, _, s := initApp(t, []string{
		"",  // gateway hostname (seeded)
		"n", // skip the connection test
		"",  // short name (seeded)
		"",  // transfer: Enter a hostname (seeded)
		"",  // transfer hostname (seeded)
		"",  // short name (seeded)
		"",  // node patterns: none
		"",  // no tunnels
		"",  // username
		"y", // apply the ssh changes
		"y", // write the config
		"n", // decline the ssh key offer
	})
	if err := runCmd(a, "init", "--from", seed); err != nil {
		t.Fatal(err)
	}
	s.done()
	raw, err := os.ReadFile(a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[vscode]", `settings = "/local/site/vscode.jsonc"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("config lacks %q:\n%s", want, raw)
		}
	}
	cfg, err := config.Load(a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VSCode == nil || cfg.VSCode.Settings != "/local/site/vscode.jsonc" {
		t.Fatalf("VSCode = %+v", cfg.VSCode)
	}
}

// TestInitFromMissingSiteFile covers running the onboarding command
// outside the unpacked folder: a usage error naming the path, nothing
// written.
func TestInitFromMissingSiteFile(t *testing.T) {
	a, _, s := initApp(t, nil)
	missing := filepath.Join(t.TempDir(), "osu.toml")
	err := runCmd(a, "init", "--from", missing)
	if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v", err)
	}
	s.done()
	if exists(a.configPath) {
		t.Fatal("config written")
	}
}

// TestInitFromRelativeMissingSiteFile covers `hpctun init --from osu.toml`
// typed outside the unpacked folder: the error names the folder it looked
// in and how to fix it.
func TestInitFromRelativeMissingSiteFile(t *testing.T) {
	a, _, s := initApp(t, nil)
	dir := t.TempDir()
	t.Chdir(dir)
	err := runCmd(a, "init", "--from", "osu.toml")
	if err == nil || !strings.Contains(err.Error(), "osu.toml does not exist in ") ||
		!strings.Contains(err.Error(), filepath.Base(dir)) || !strings.Contains(err.Error(), "full path") {
		t.Fatalf("err = %v", err)
	}
	s.done()
	if exists(a.configPath) {
		t.Fatal("config written")
	}
}

// TestInitFromSeedWithCodeAndAliasGateway covers a team seed whose gateway
// is an alias the user already has, with [code] and nothing else for hpctun
// to define: init keeps [code] and records the user's own username, which
// [code] needs.
func TestInitFromSeedWithCodeAndAliasGateway(t *testing.T) {
	a, out, s := initApp(t, []string{
		"",  // gateway: hpc, from ~/.ssh/config (seeded)
		"n", // skip the connection test
		"",  // transfer: skip
		"",  // node patterns: none
		"",  // no tunnels
		"",  // username: me
		"y", // apply the ssh changes
		"y", // write the config
		"n", // decline the ssh key offer
	})
	home := a.getenv("HOME")
	writeFile(t, filepath.Join(home, ".ssh", "config"), "Host hpc\n    HostName login.example.org\n")
	seed := filepath.Join(t.TempDir(), "team.toml")
	writeFile(t, seed, "gateway = \"hpc\"\n\n[ssh]\nuser = \"alice\"\n\n[code]\n")
	if err := runCmd(a, "init", "--from", seed); err != nil {
		t.Fatalf("err = %v\nunused answers %q\nout:\n%s", err, s.answers, out.String())
	}
	s.done()
	cfg, err := config.Load(a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Code == nil || cfg.SSH == nil || cfg.SSH.User != "me" {
		t.Fatalf("config code %+v ssh %+v", cfg.Code, cfg.SSH)
	}
}
