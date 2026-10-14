package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"hpctun/internal/prompt"
	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
)

// TestMacTunConfigUsesKeychain: on macOS the generated blocks read the
// passphrase ssh-add stored in the Keychain.
func TestMacTunConfigUsesKeychain(t *testing.T) {
	a, _, _ := testApp(t, sshCfg)
	a.goos, a.run = "darwin", sshG()
	if err := runCmd(a, "ssh-config", "sync", "--yes"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(a.sshPaths().TunConfig)
	if !strings.Contains(string(b), "\nIgnoreUnknown UseKeychain\n\nHost ") || !strings.Contains(string(b), "    User me\n    UseKeychain yes\n") {
		t.Fatalf("hpctun_config:\n%s", b)
	}
}

// keyWorld fakes the silent key commands: an agent that holds the key, and a
// gateway that accepts it once installed.
type keyWorld struct {
	accepted   bool
	interacted [][]string
}

func (k *keyWorld) runner() *sshctltest.Fake {
	return &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		switch {
		case c.Name == "ssh-keygen" && slices.Contains(c.Args, "-y"):
			return sshctl.Result{ExitCode: 255}, nil // has a passphrase
		case c.Name == "ssh-keygen":
			return sshctl.Result{Stdout: []byte("256 SHA256:abc x (ED25519)\n")}, nil
		case c.Name == "ssh-add":
			return sshctl.Result{Stdout: []byte("256 SHA256:abc x (ED25519)\n")}, nil
		case c.Name == "ssh" && slices.Contains(c.Args, "BatchMode=yes"):
			if k.accepted {
				return sshctl.Result{}, nil
			}
			return sshctl.Result{ExitCode: 255}, nil
		}
		return sshctl.Result{ExitCode: 255}, nil
	}}
}

func (k *keyWorld) interact(argv []string, _ io.Reader) error {
	k.interacted = append(k.interacted, argv)
	if argv[0] == "ssh" {
		k.accepted = true
	}
	return nil
}

func keyApp(t *testing.T, cfg string) (*app, *keyWorld) {
	t.Helper()
	a, _, _ := testApp(t, cfg)
	yes := true
	k := &keyWorld{}
	a.tty, a.run, a.interact = &yes, k.runner(), k.interact
	key := filepath.Join(a.home(), ".ssh", "id_ed25519")
	os.MkdirAll(filepath.Dir(key), 0o700)
	os.WriteFile(key, []byte("PRIVATE"), 0o600)
	os.WriteFile(key+".pub", []byte("ssh-ed25519 AAAA x\n"), 0o644)
	return a, k
}

func TestSSHConfigKeyInstallsOnGateway(t *testing.T) {
	a, k := keyApp(t, sshCfg)
	if err := runCmd(a, "ssh-config", "key"); err != nil {
		t.Fatal(err)
	}
	if out := a.stdout.(*bytes.Buffer).String(); !strings.Contains(out, "Nodes and the transfer host share your home directory") {
		t.Fatalf("out:\n%s", out)
	}
	if len(k.interacted) != 1 || k.interacted[0][0] != "ssh" || !slices.Contains(k.interacted[0], "shell") {
		t.Fatalf("interacted %q", k.interacted)
	}
}

func TestSSHConfigKeyOtherHost(t *testing.T) {
	a, k := keyApp(t, sshCfg)
	if err := runCmd(a, "ssh-config", "key", "files"); err != nil {
		t.Fatal(err)
	}
	if len(k.interacted) != 1 || !slices.Contains(k.interacted[0], "files") {
		t.Fatalf("interacted %q", k.interacted)
	}
	if out := a.stdout.(*bytes.Buffer).String(); strings.Contains(out, "Nodes and the transfer host") {
		t.Fatalf("shared-home sentence for another host:\n%s", out)
	}
}

func TestSSHConfigKeyNeedsGatewayAndTerminal(t *testing.T) {
	a, _ := keyApp(t, "default_host = \"node1\"\n")
	if err := runCmd(a, "ssh-config", "key"); exitCode(err) != 2 || !strings.Contains(err.Error(), "hpctun init") {
		t.Fatalf("no gateway: %v", err)
	}
	a, _ = keyApp(t, sshCfg)
	no := false
	a.tty = &no
	if err := runCmd(a, "ssh-config", "key"); exitCode(err) != 2 || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("no terminal: %v", err)
	}
}

func TestSSHConfigKeyFailureExits1(t *testing.T) {
	a, k := keyApp(t, sshCfg)
	a.interact = func(argv []string, _ io.Reader) error {
		k.interacted = append(k.interacted, argv)
		return errors.New("ssh exited with status 255")
	}
	if err := runCmd(a, "ssh-config", "key"); exitCode(err) != 1 || !strings.Contains(err.Error(), "could not install the key on shell") {
		t.Fatalf("err = %v", err)
	}
}

// confirmAsker answers the key offer and records whether it was asked.
type confirmAsker struct {
	prompt.Asker
	yes   bool
	asked []string
}

func (c *confirmAsker) Confirm(title, _ string, _ bool) (bool, error) {
	c.asked = append(c.asked, title)
	return c.yes, nil
}
func (*confirmAsker) SetContext(prompt.Context) {}

// Outside and Close are spelled out: prompt.Plain would clash with the
// embedded Asker's.
func (*confirmAsker) Outside(fn func() error) error { return fn() }
func (*confirmAsker) Close()                        {}

func TestOfferKey(t *testing.T) {
	ctx := context.Background()

	a, k := keyApp(t, sshCfg) // the gateway accepts the key: nothing asked
	k.accepted = true
	ask := &confirmAsker{}
	a.ask = ask
	out := a.stdout.(*bytes.Buffer)
	a.offerKey(ctx, "shell")
	if len(ask.asked) != 0 || !strings.Contains(out.String(), "shell already lets you log in with a key.") {
		t.Fatalf("accepted: asked %q out %s", ask.asked, out.String())
	}

	a, k = keyApp(t, sshCfg) // not accepted, yes: the flow runs
	ask = &confirmAsker{yes: true}
	a.ask = ask
	a.offerKey(ctx, "shell")
	if !slices.Equal(ask.asked, []string{"Set up an ssh key for shell now?"}) || len(k.interacted) != 1 {
		t.Fatalf("yes: asked %q interacted %q", ask.asked, k.interacted)
	}

	a, k = keyApp(t, sshCfg) // not accepted, no: a hint
	a.ask = &confirmAsker{}
	out = a.stdout.(*bytes.Buffer)
	a.offerKey(ctx, "shell")
	if len(k.interacted) != 0 || !strings.Contains(out.String(), "Later: `hpctun ssh-config key`") {
		t.Fatalf("no: interacted %q out %s", k.interacted, out.String())
	}

	a, k = keyApp(t, sshCfg) // no key file: ask without a test login
	os.Remove(filepath.Join(a.home(), ".ssh", "id_ed25519"))
	f := k.runner()
	a.run, a.ask = f, &confirmAsker{}
	a.offerKey(ctx, "shell")
	if len(f.Find("BatchMode=yes")) != 0 {
		t.Fatal("tested a key that does not exist")
	}

	a, _ = keyApp(t, sshCfg) // the flow fails: a warning, nothing else
	a.ask = &confirmAsker{yes: true}
	a.interact = func([]string, io.Reader) error { return errors.New("ssh exited with status 255") }
	errb := a.stderr.(*bytes.Buffer)
	a.offerKey(ctx, "shell")
	if !strings.Contains(errb.String(), "warning: could not install the key on shell") {
		t.Fatalf("stderr %s", errb.String())
	}
}

// TestInitOffersKey: init ends by offering the key, and declining it does
// not fail init.
func TestInitOffersKey(t *testing.T) {
	a, out, s := initApp(t, []string{"gw.example.org", "n", "", "", "", "", "", "y", "y", "n"})
	if err := runCmd(a, "init"); err != nil {
		t.Fatal(err)
	}
	s.done()
	if !strings.Contains(out.String(), "Later: `hpctun ssh-config key`") {
		t.Fatalf("out:\n%s", out.String())
	}
}

// TestSSHConfigKeyRefusesNodes: a node shares the gateway's home directory,
// and its test login would go through ProxyJump, which does not pass on
// BatchMode, so the gateway could prompt for a password.
func TestSSHConfigKeyRefusesNodes(t *testing.T) {
	a, k := keyApp(t, sshCfg)
	err := runCmd(a, "ssh-config", "key", "node1")
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "node1 shares its home directory with shell") {
		t.Fatalf("err = %v", err)
	}
	if len(k.interacted) != 0 {
		t.Fatalf("interacted %q", k.interacted)
	}
}

// TestSSHConfigKeyRefusesOptionAsHost: a destination that starts with '-'
// would be read by ssh as an option, so it is refused before anything runs.
func TestSSHConfigKeyRefusesOptionAsHost(t *testing.T) {
	a, k := keyApp(t, sshCfg)
	err := runCmd(a, "ssh-config", "key", "--", "-oProxyCommand=touch /tmp/x")
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "starts with '-'") || len(k.interacted) != 0 {
		t.Fatalf("err = %v, interacted %q", err, k.interacted)
	}
	if n := len(a.run.(*sshctltest.Fake).Calls); n != 0 {
		t.Fatalf("%d commands ran", n)
	}
}

// TestSetupOffersKeyWithoutSSHTable: setup offers the key even when tun
// defines no ssh entries (an existing gateway alias, nothing else), as
// init does.
func TestSetupOffersKeyWithoutSSHTable(t *testing.T) {
	a, _ := keyApp(t, "gateway = \"shell\"\n")
	writeFile(t, a.sshPaths().UserConfig, "Host shell\n    HostName shell.example.org\n")
	s := &initScript{t: t, answers: []string{"shell", "", "", "", "n"}}
	a.ask = s
	if err := runCmd(a, "ssh-config", "setup"); err != nil {
		t.Fatal(err)
	}
	s.done()
	if !strings.Contains(a.stdout.(*bytes.Buffer).String(), "Later: `hpctun ssh-config key`") {
		t.Fatalf("out:\n%s", a.stdout.(*bytes.Buffer).String())
	}
}

// TestOfferKeyStopsOnCtrlC: after Ctrl-C (a cancelled context) the offer
// asks nothing and reports the interruption, and init stops there instead
// of going on to its closing questions.
func TestOfferKeyStopsOnCtrlC(t *testing.T) {
	a, _ := keyApp(t, sshCfg)
	ask := &confirmAsker{yes: true}
	a.ask = ask
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.offerKey(ctx, "shell"); !errors.Is(err, context.Canceled) || len(ask.asked) != 0 {
		t.Fatalf("err = %v, asked %q", err, ask.asked)
	}

	a, out, s := initApp(t, []string{"gw.example.org", "n", "", "", "", "", "", "y", "y", "y"})
	ctx, cancel = context.WithCancel(context.Background())
	a.interact = func([]string, io.Reader) error { cancel(); return errors.New("interrupted") }
	root := newRootCmd(a)
	root.SetArgs([]string{"init"})
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	if err := root.ExecuteContext(ctx); err == nil {
		t.Fatal("init went on after Ctrl-C")
	}
	s.done()
	if strings.Contains(out.String(), "Tab completion") {
		t.Fatalf("init printed its closing lines:\n%s", out.String())
	}
}
