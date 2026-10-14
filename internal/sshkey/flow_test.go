package sshkey

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

	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
	"hpctun/internal/sshsetup"
)

const pubLine = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey me@lap (tun)"

// world fakes the machine and the gateway.
type world struct {
	t                  *testing.T
	key                string
	noPass             bool // ssh-keygen -y -P "" succeeds
	agent              int  // ssh-add -l exit: 0 lists keys, 1 none, 2 no agent, -1 ssh-add missing
	loaded             bool
	accepted           bool
	acceptAfterInstall bool
	installFails       bool
	sshAddFails        bool
	interacted         [][]string
	installed          string
	runner             *sshctltest.Fake
}

func newWorld(t *testing.T, withKey bool) *world {
	w := &world{t: t, key: filepath.Join(t.TempDir(), ".ssh", "id_ed25519")}
	if withKey {
		w.writeKey(pubLine + "\n")
	}
	w.runner = &sshctltest.Fake{Handler: w.run}
	return w
}

func (w *world) writeKey(pub string) {
	os.MkdirAll(filepath.Dir(w.key), 0o700)
	os.WriteFile(w.key, []byte("PRIVATE"), 0o600)
	os.WriteFile(w.key+".pub", []byte(pub), 0o644)
}

func (w *world) run(c sshctltest.Call) (sshctl.Result, error) {
	switch {
	case c.Name == "ssh-keygen" && slices.Contains(c.Args, "-y"):
		if w.noPass {
			return sshctl.Result{Stdout: []byte(pubLine)}, nil
		}
		return sshctl.Result{ExitCode: 255}, nil
	case c.Name == "ssh-keygen" && slices.Contains(c.Args, "-lf"):
		return sshctl.Result{Stdout: []byte("256 SHA256:abc me@lap (tun) (ED25519)\n")}, nil
	case c.Name == "ssh-add":
		if w.agent < 0 {
			return sshctl.Result{}, errors.New(`exec: "ssh-add": executable file not found in $PATH`)
		}
		if w.agent == 0 && w.loaded {
			return sshctl.Result{Stdout: []byte("256 SHA256:abc me@lap (tun) (ED25519)\n")}, nil
		}
		return sshctl.Result{ExitCode: max(w.agent, 1)}, nil
	case c.Name == "ssh":
		if w.accepted {
			return sshctl.Result{}, nil
		}
		return sshctl.Result{ExitCode: 255}, nil
	}
	w.t.Fatalf("unexpected command %s", c.Line())
	return sshctl.Result{}, nil
}

func (w *world) interact(argv []string, stdin io.Reader) error {
	w.interacted = append(w.interacted, argv)
	switch filepath.Base(argv[0]) {
	case "ssh-keygen":
		w.writeKey(pubLine + "\n")
	case "ssh-add":
		if w.sshAddFails {
			return errors.New("ssh-add exited with status 1")
		}
		w.loaded = true
	case "ssh":
		b, _ := io.ReadAll(stdin)
		w.installed = string(b)
		if w.installFails {
			return errors.New("ssh exited with status 255")
		}
		w.accepted = w.acceptAfterInstall
	}
	return nil
}

func (w *world) flow(out *bytes.Buffer) *Flow {
	return &Flow{Key: w.key, Show: "~/.ssh/id_ed25519", Host: "shell", Gateway: true, Comment: "me@lap (tun)",
		Runner: w.runner, Interact: w.interact, Out: out}
}

func names(argvs [][]string) []string {
	var n []string
	for _, a := range argvs {
		n = append(n, a[0])
	}
	return n
}

func TestFlowCreatesLoadsInstallsAndChecks(t *testing.T) {
	w := newWorld(t, false)
	os.RemoveAll(filepath.Dir(w.key)) // a fresh account: no ~/.ssh
	w.acceptAfterInstall = true
	var out bytes.Buffer
	if err := w.flow(&out).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := names(w.interacted); !slices.Equal(got, []string{"ssh-keygen", "ssh-add", "ssh"}) {
		t.Fatalf("interacted %q", w.interacted)
	}
	if want := []string{"ssh-keygen", "-t", "ed25519", "-f", w.key, "-C", "me@lap (tun)"}; !slices.Equal(w.interacted[0], want) {
		t.Fatalf("keygen %q", w.interacted[0])
	}
	if di, err := os.Stat(filepath.Dir(w.key)); err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("~/.ssh: %v %v", di, err)
	}
	if want := []string{"ssh-add", w.key}; !slices.Equal(w.interacted[1], want) {
		t.Fatalf("ssh-add %q", w.interacted[1])
	}
	if want := []string{"ssh", "-o", "ControlMaster=no", "-o", "ConnectTimeout=10", "--", "shell", "sh -c '" + InstallScript + "'"}; !slices.Equal(w.interacted[2], want) {
		t.Fatalf("install %q", w.interacted[2])
	}
	if w.installed != pubLine+"\n" {
		t.Fatalf("installed %q", w.installed)
	}
	if !strings.Contains(out.String(), "Done: shell accepts your key. Nodes and the transfer host") {
		t.Fatalf("out:\n%s", out.String())
	}
}

func TestFlowAlreadyAccepted(t *testing.T) {
	w := newWorld(t, true)
	w.loaded, w.accepted = true, true
	var out bytes.Buffer
	if err := w.flow(&out).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(w.interacted) != 0 {
		t.Fatalf("interacted %q", w.interacted)
	}
	for _, s := range []string{"using ~/.ssh/id_ed25519", "shell already lets you log in with a key"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("missing %q in\n%s", s, out.String())
		}
	}
}

func TestFlowTestLoginOptions(t *testing.T) {
	w := newWorld(t, true)
	w.loaded, w.accepted = true, true
	(w.flow(&bytes.Buffer{})).Run(context.Background())
	logins := w.runner.Find("BatchMode=yes")
	want := []string{"-o", "BatchMode=yes", "-o", "PreferredAuthentications=publickey", "-o", "IdentitiesOnly=yes",
		"-i", w.key, "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ConnectTimeout=10", "--", "shell", "true"}
	if len(logins) != 1 || !slices.Equal(logins[0].Args, want) {
		t.Fatalf("test logins %+v", logins)
	}
	w = newWorld(t, true)
	w.loaded, w.accepted = true, true
	f := w.flow(&bytes.Buffer{})
	f.Windows = true
	f.Run(context.Background())
	if l := w.runner.Find("BatchMode=yes"); len(l) != 1 || slices.Contains(l[0].Args, "ControlPath=none") {
		t.Fatalf("windows test login %+v", l)
	}
}

func TestFlowNoPassphraseNote(t *testing.T) {
	w := newWorld(t, true)
	w.noPass, w.loaded, w.accepted = true, true, true
	var out bytes.Buffer
	w.flow(&out).Run(context.Background())
	if !strings.Contains(out.String(), "has no passphrase") || !strings.Contains(out.String(), "ssh-keygen -p -f ~/.ssh/id_ed25519") {
		t.Fatalf("out:\n%s", out.String())
	}
}

func TestFlowKeychainOnMac(t *testing.T) {
	w := newWorld(t, true)
	w.accepted = true
	f := w.flow(&bytes.Buffer{})
	f.Keychain = true
	if err := f.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Apple's ssh-add, whatever comes first on PATH: Homebrew's rejects the flag.
	if want := []string{"/usr/bin/ssh-add", "--apple-use-keychain", w.key}; len(w.interacted) != 1 || !slices.Equal(w.interacted[0], want) {
		t.Fatalf("interacted %q", w.interacted)
	}
}

// TestFlowNoAgent: with a passphrase and no agent, a test login cannot use
// the key, so none runs; the key is installed and the user told why ssh
// will still ask for the passphrase.
func TestFlowNoAgent(t *testing.T) {
	for _, c := range []struct {
		agent   int
		windows bool
		note    string
	}{
		{2, false, "no ssh agent is running"},
		{-1, false, "no ssh agent is running"}, // ssh-add missing
		{2, true, sshsetup.AgentNote},
	} {
		w := newWorld(t, true)
		w.agent = c.agent
		var out bytes.Buffer
		f := w.flow(&out)
		f.Windows = c.windows
		if err := f.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(w.runner.Find("BatchMode=yes")) != 0 {
			t.Error("ran a test login that could not use the key")
		}
		if !slices.Equal(names(w.interacted), []string{"ssh"}) {
			t.Errorf("interacted %q", w.interacted)
		}
		if !strings.Contains(out.String(), c.note) || !strings.Contains(out.String(), "installed; ssh will ask for the key's passphrase until an agent holds it") {
			t.Errorf("agent %d windows %v: out\n%s", c.agent, c.windows, out.String())
		}
	}
}

func TestFlowInstallFails(t *testing.T) {
	w := newWorld(t, true)
	w.loaded, w.installFails = true, true
	err := w.flow(&bytes.Buffer{}).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not install the key on shell") {
		t.Fatalf("err = %v", err)
	}
}

func TestFlowStillRefusedAfterInstall(t *testing.T) {
	w := newWorld(t, true)
	w.loaded = true
	err := w.flow(&bytes.Buffer{}).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "shell does not accept the key") {
		t.Fatalf("err = %v", err)
	}
}

func TestFlowSSHAddFails(t *testing.T) {
	w := newWorld(t, true)
	w.sshAddFails = true
	err := w.flow(&bytes.Buffer{}).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not add the key to the ssh agent") {
		t.Fatalf("err = %v", err)
	}
}

// TestFlowTrimsPublicKey: a .pub edited on Windows (CRLF, trailing space)
// must install the bare key line, or the duplicate check never matches.
func TestFlowTrimsPublicKey(t *testing.T) {
	w := newWorld(t, false)
	w.writeKey(pubLine + " \r\n")
	w.loaded, w.acceptAfterInstall = true, true
	if err := w.flow(&bytes.Buffer{}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.installed != pubLine+"\n" {
		t.Fatalf("installed %q", w.installed)
	}
}

// TestFlowSharedHomeOnlyForGateway: the "nodes share your home" sentence is
// about the gateway; for another host it would be a guess.
func TestFlowSharedHomeOnlyForGateway(t *testing.T) {
	w := newWorld(t, true)
	w.loaded, w.acceptAfterInstall = true, true
	var out bytes.Buffer
	f := w.flow(&out)
	f.Gateway = false
	if err := f.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Done: shell accepts your key.") || strings.Contains(out.String(), "Nodes") {
		t.Fatalf("out:\n%s", out.String())
	}
}

// TestFlowMissingPublicKey: a private key without its .pub cannot be
// fingerprinted or installed; say how to recreate it instead of failing on
// a raw file error later.
func TestFlowMissingPublicKey(t *testing.T) {
	w := newWorld(t, true)
	os.Remove(w.key + ".pub")
	err := w.flow(&bytes.Buffer{}).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "~/.ssh/id_ed25519.pub is missing") ||
		!strings.Contains(err.Error(), "ssh-keygen -y -f ~/.ssh/id_ed25519 > ~/.ssh/id_ed25519.pub") {
		t.Fatalf("err = %v", err)
	}
	if len(w.interacted) != 0 {
		t.Fatalf("interacted %q", w.interacted)
	}
}
