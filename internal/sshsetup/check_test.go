package sshsetup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hpctun/internal/change"
	"hpctun/internal/config"
	"hpctun/internal/sshctl"
)

func TestCheckWithRealSSH(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh not installed")
	}
	p := setupHome(t, "")
	in := Input{Gateway: "shell", Source: "src",
		SSH:         config.SSH{User: "me", GatewayHost: "shell.example.org", Nodes: []string{"node*"}},
		UserAliases: []string{"node7"}}
	// An absolute Include: ssh resolves relative ones against the real
	// ~/.ssh (from the password database), not $HOME.
	os.MkdirAll(filepath.Dir(p.UserConfig), 0o700)
	os.WriteFile(p.UserConfig, []byte("Include "+p.TunConfig+"\n\nHost node7\n    ProxyJump elsewhere\n"), 0o600)
	writeTun(t, p, Generate(in))
	got, err := Check(context.Background(), RunnerSSHG(sshctl.ExecRunner{}, "-F", p.UserConfig), p, in,
		[]string{"node1", "node7", "web1", ""})
	if err != nil {
		t.Fatal(err)
	}
	want := []Finding{{Note, "node7: your own entry sets ProxyJump elsewhere; hpctun leaves it alone"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

// TestCheckFlagsLegacyTunConfig covers a ~/.ssh/tun_config left from before
// the rename: it is a Problem regardless of what else is going on, since
// only `hpctun ssh-config sync` migrates it.
func TestCheckFlagsLegacyTunConfig(t *testing.T) {
	p := setupHome(t, "")
	if err := os.MkdirAll(filepath.Dir(p.LegacyTunConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.LegacyTunConfig, []byte("Host old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := Input{Gateway: "shell", Source: "src", SSH: config.SSH{Transfer: "files"}}
	noSSH := func(context.Context, string) (map[string]string, error) {
		t.Fatal("ssh -G should not run for an empty [ssh] table")
		return nil, nil
	}
	got, err := Check(context.Background(), noSSH, p, in, []string{"node1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Finding{{Problem, "~/.ssh/tun_config is from before the rename; run `hpctun ssh-config sync`"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

// TestCheckEmptyInputHasNoFindings covers the [ssh] table the wizard can
// produce when only an existing alias is picked as the transfer host: tun
// defines nothing, so there is no Include to add and no hpctun_config to keep
// in sync. A missing hpctun_config is fine, not a Problem.
func TestCheckEmptyInputHasNoFindings(t *testing.T) {
	p := setupHome(t, "")
	in := Input{Gateway: "shell", Source: "src", SSH: config.SSH{Transfer: "files"}}
	if !in.Empty() {
		t.Fatal("test input is not Empty")
	}
	noSSH := func(context.Context, string) (map[string]string, error) {
		t.Fatal("ssh -G should not run for an empty [ssh] table")
		return nil, nil
	}
	got, err := Check(context.Background(), noSSH, p, in, []string{"node1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

// TestCheckEmptyInputFlagsLeftoverTunConfig covers a hpctun_config left behind
// by an earlier, non-empty setup: with nothing defined now, it should have
// been cleared, so a leftover file is still worth a Problem.
func TestCheckEmptyInputFlagsLeftoverTunConfig(t *testing.T) {
	p := setupHome(t, "")
	in := Input{Gateway: "shell", Source: "src", SSH: config.SSH{Transfer: "files"}}
	writeTun(t, p, "Host old\n    HostName old.example.org\n")
	noSSH := func(context.Context, string) (map[string]string, error) {
		t.Fatal("ssh -G should not run for an empty [ssh] table")
		return nil, nil
	}
	got, err := Check(context.Background(), noSSH, p, in, []string{"node1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != Problem || !strings.Contains(got[0].Msg, "out of date") {
		t.Fatalf("got %+v", got)
	}
}

func TestCheckMissingIncludeAndStale(t *testing.T) {
	p := setupHome(t, "Host a\n")
	EnsureSockets(p)
	in := Input{Gateway: "g", SSH: config.SSH{User: "me", Nodes: []string{"n*"}}}
	noSSH := func(context.Context, string) (map[string]string, error) {
		t.Fatal("ssh -G should not run without the Include")
		return nil, nil
	}
	got, err := Check(context.Background(), noSSH, p, in, []string{"n1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != Problem || !strings.Contains(got[0].Msg, "does not include hpctun_config") ||
		got[1].Kind != Problem || !strings.Contains(got[1].Msg, "out of date") {
		t.Fatalf("got %+v", got)
	}
}

func TestCheckEarlierEntryWins(t *testing.T) {
	p := setupHome(t, "Include hpctun_config\n")
	in := Input{Gateway: "shell", Source: "s",
		SSH:         config.SSH{User: "me", GatewayHost: "shell.example.org", Nodes: []string{"node*"}},
		UserAliases: []string{"shell"}}
	writeTun(t, p, Generate(in))
	fake := func(_ context.Context, host string) (map[string]string, error) {
		if host == "shell" {
			return map[string]string{"hostname": "other.example.org", "controlmaster": "false"}, nil
		}
		return map[string]string{"hostname": host}, nil // no proxyjump
	}
	got, _ := Check(context.Background(), fake, p, in, []string{"node1"})
	var msgs []string
	for _, f := range got {
		if f.Kind != Warning {
			t.Fatalf("kind %v: %s", f.Kind, f.Msg)
		}
		msgs = append(msgs, f.Msg)
	}
	all := strings.Join(msgs, "\n")
	for _, s := range []string{"shell is also defined", "connects to other.example.org", "ControlMaster is false", "node1: ProxyJump is none, not shell"} {
		if !strings.Contains(all, s) {
			t.Errorf("missing %q in\n%s", s, all)
		}
	}
}

func TestCheckTunnelGatewayEarlierEntryWins(t *testing.T) {
	p := setupHome(t, "")
	in := Input{Gateway: "shell", TunnelGateway: "shell-tunnels", Source: "src",
		SSH: config.SSH{User: "me", GatewayHost: "shell.example.org", TunnelGatewayHost: "shell.example.org"}}
	os.MkdirAll(filepath.Dir(p.UserConfig), 0o700)
	os.WriteFile(p.UserConfig, []byte("Include hpctun_config\n"), 0o600)
	writeTun(t, p, Generate(in))
	sshG := func(_ context.Context, h string) (map[string]string, error) {
		if h == "shell-tunnels" {
			return map[string]string{"hostname": "other.example.org", "controlmaster": "auto"}, nil
		}
		return map[string]string{"hostname": "shell.example.org", "controlmaster": "auto"}, nil
	}
	got, err := Check(context.Background(), sshG, p, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Finding{{Warning, "shell-tunnels: ssh connects to other.example.org, not shell.example.org; an entry earlier in your ssh config wins"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

// TestCheckShowsPathsWithTilde keeps Check's messages in step with the
// diffs, which name files as ~/.ssh/...
func TestCheckShowsPathsWithTilde(t *testing.T) {
	p := setupHome(t, "Host a\n    Include hpctun_config\n")
	EnsureSockets(p)
	in := Input{Gateway: "g", SSH: config.SSH{User: "me", Nodes: []string{"n*"}}}
	got, err := Check(context.Background(), nil, p, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Finding{
		{Problem, "~/.ssh/config line 2 includes hpctun_config inside a Host or Match block; move it above the first Host line"},
		{Problem, "~/.ssh/hpctun_config is out of date; run `hpctun ssh-config sync`"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

// TestCheckMissingSockets covers a deleted ControlPath directory: ssh then
// cannot create its control socket, so logins are not shared and tun login
// finds no master.
func TestCheckMissingSockets(t *testing.T) {
	p := setupHome(t, "Include hpctun_config\n")
	in := Input{Gateway: "g", SSH: config.SSH{User: "me", Nodes: []string{"n*"}}}
	writeTun(t, p, Generate(in))
	fine := func(context.Context, string) (map[string]string, error) { return nil, nil }
	want := func(msg string) {
		t.Helper()
		got, err := Check(context.Background(), fine, p, in, nil)
		if err != nil {
			t.Fatal(err)
		}
		if w := []Finding{{Problem, msg}}; !reflect.DeepEqual(got, w) {
			t.Fatalf("got %+v", got)
		}
	}
	os.Remove(p.Sockets)
	want("~/.ssh/sockets does not exist, so ssh cannot share logins; run `hpctun ssh-config sync` to create it")
	os.WriteFile(p.Sockets, nil, 0o600)
	want("~/.ssh/sockets is not a directory, so ssh cannot share logins; replace it with a directory")
}

// TestCheckNoControlMaster: without ControlMaster there is no sockets
// directory to miss and no ControlMaster setting to compare.
func TestCheckNoControlMaster(t *testing.T) {
	p := setupHome(t, "Include hpctun_config\n")
	in := Input{Gateway: "shell", NoControlMaster: true,
		SSH: config.SSH{User: "me", GatewayHost: "shell.example.org", Nodes: []string{"node*"}}}
	c, _ := TunConfigChange(p, Generate(in))
	if err := change.Apply([]change.Change{c}); err != nil {
		t.Fatal(err)
	}
	sshG := func(_ context.Context, host string) (map[string]string, error) {
		if host == "shell" {
			return map[string]string{"hostname": "shell.example.org"}, nil
		}
		return map[string]string{"hostname": host, "proxyjump": "shell"}, nil
	}
	got, err := Check(context.Background(), sshG, p, in, []string{"node1"})
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// TestCheckIgnoreUnknownHidesUseKeychain covers a user IgnoreUnknown read
// before hpctun's: ssh keeps the first one, so an ssh without UseKeychain
// (conda's, Homebrew's) rejects hpctun_config. ssh -G shows the value in use.
func TestCheckIgnoreUnknownHidesUseKeychain(t *testing.T) {
	cases := []struct {
		name     string
		keychain bool
		ignore   string // ssh -G's ignoreunknown; "" leaves it out
		warn     bool
	}{
		{"user list without UseKeychain", true, "SomethingElse", true},
		{"hpctun's own line", true, "UseKeychain", false},
		{"user list covers it", true, "AddKeysToAgent,UseKeychain", false},
		{"user wildcard covers it", true, "Use*", false},
		{"not reported", true, "", false},
		{"no Keychain", false, "SomethingElse", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := setupHome(t, "Include hpctun_config\n")
			EnsureSockets(p)
			in := Input{Gateway: "shell", Source: "s", Keychain: c.keychain,
				SSH: config.SSH{User: "me", GatewayHost: "shell.example.org"}}
			writeTun(t, p, Generate(in))
			sshG := func(context.Context, string) (map[string]string, error) {
				g := map[string]string{"hostname": "shell.example.org", "controlmaster": "auto"}
				if c.ignore != "" {
					g["ignoreunknown"] = c.ignore
				}
				return g, nil
			}
			got, err := Check(context.Background(), sshG, p, in, nil)
			if err != nil {
				t.Fatal(err)
			}
			var want []Finding
			if c.warn {
				want = []Finding{{Warning, "your ssh config sets IgnoreUnknown SomethingElse before hpctun's, and ssh uses the first one it reads, so an ssh other than Apple's (from conda or Homebrew, say) stops at UseKeychain and will not run; add UseKeychain to that line: IgnoreUnknown SomethingElse,UseKeychain"}}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

// TestCheckIgnoreUnknownWithOnlyNodes reads ssh -G for the gateway when
// hpctun defines no gateway, transfer or tunnel alias and no known host is a
// node: the node block still writes UseKeychain.
func TestCheckIgnoreUnknownWithOnlyNodes(t *testing.T) {
	p := setupHome(t, "Include hpctun_config\n")
	EnsureSockets(p)
	in := Input{Gateway: "shell", Source: "s", Keychain: true,
		SSH: config.SSH{User: "me", Nodes: []string{"node*"}}}
	writeTun(t, p, Generate(in))
	var asked []string
	sshG := func(_ context.Context, h string) (map[string]string, error) {
		asked = append(asked, h)
		return map[string]string{"ignoreunknown": "Other"}, nil
	}
	got, err := Check(context.Background(), sshG, p, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(asked, []string{"shell"}) || len(got) != 1 || got[0].Kind != Warning ||
		!strings.Contains(got[0].Msg, "IgnoreUnknown Other,UseKeychain") {
		t.Fatalf("asked %v, got %+v", asked, got)
	}
}

// TestCheckUseKeychainRejected covers running the check with an ssh that
// lacks UseKeychain while a user IgnoreUnknown hides hpctun's: ssh -G fails,
// and the finding says what to fix rather than repeating ssh's error.
func TestCheckUseKeychainRejected(t *testing.T) {
	p := setupHome(t, "Include hpctun_config\n")
	EnsureSockets(p)
	in := Input{Gateway: "shell", Source: "s", Keychain: true,
		SSH: config.SSH{User: "me", GatewayHost: "shell.example.org"}}
	writeTun(t, p, Generate(in))
	sshG := func(context.Context, string) (map[string]string, error) {
		return nil, errors.New("ssh -G shell: /Users/me/.ssh/hpctun_config: line 12: Bad configuration option: usekeychain")
	}
	got, err := Check(context.Background(), sshG, p, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Finding{{Problem, "the ssh first on your PATH is not Apple's (from conda or Homebrew, say) and stops at UseKeychain in hpctun_config, because an IgnoreUnknown earlier in your ssh config wins over hpctun's; add UseKeychain to that line, e.g. IgnoreUnknown UseKeychain"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

// TestCheckIgnoreUnknownWithRealSSH puts a user IgnoreUnknown above the
// Include. Apple's ssh knows UseKeychain and reports the IgnoreUnknown in
// force (a Warning); any other OpenSSH rejects UseKeychain (a Problem).
func TestCheckIgnoreUnknownWithRealSSH(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh not installed")
	}
	p := setupHome(t, "")
	EnsureSockets(p)
	in := Input{Gateway: "shell", Source: "src", Keychain: true,
		SSH: config.SSH{User: "me", GatewayHost: "shell.example.org"}}
	os.MkdirAll(filepath.Dir(p.UserConfig), 0o700)
	os.WriteFile(p.UserConfig, []byte("IgnoreUnknown Other\nInclude "+p.TunConfig+"\n"), 0o600)
	writeTun(t, p, Generate(in))
	got, err := Check(context.Background(), RunnerSSHG(sshctl.ExecRunner{}, "-F", p.UserConfig), p, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	switch got[0].Kind {
	case Warning:
		if !strings.Contains(got[0].Msg, "IgnoreUnknown Other,UseKeychain") {
			t.Fatalf("got %+v", got)
		}
	case Problem:
		if !strings.Contains(got[0].Msg, "stops at UseKeychain") {
			t.Fatalf("got %+v", got)
		}
	default:
		t.Fatalf("got %+v", got)
	}
	t.Logf("%v: %s", got[0].Kind, got[0].Msg)
}
