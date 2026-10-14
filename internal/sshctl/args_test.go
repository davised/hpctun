package sshctl

import (
	"slices"
	"strings"
	"testing"
)

var sshConfigGW = GW{Dest: "gw", ClientOpts: []string{"-o", "ControlMaster=no"}}

var managedGW = GW{
	Dest:       "gw.example.org",
	ClientOpts: []string{"-o", "ControlMaster=no", "-o", "ControlPath=/s/cm-abc"},
	MasterOpts: []string{"-o", "ControlMaster=auto", "-o", "ControlPath=/s/cm-abc", "-o", "ControlPersist=yes", "-o", "ServerAliveInterval=60", "-o", "ServerAliveCountMax=3"},
}

func TestArgs(t *testing.T) {
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"check", CheckArgs(sshConfigGW), []string{"-o", "ControlMaster=no", "-O", "check", "--", "gw"}},
		{"forward managed", ForwardArgs(managedGW, "-L4001:node1:4001"),
			[]string{"-o", "ControlMaster=no", "-o", "ControlPath=/s/cm-abc", "-O", "forward", "-L4001:node1:4001", "--", "gw.example.org"}},
		{"cancel", CancelArgs(sshConfigGW, "-Dlocalhost:5005"), []string{"-o", "ControlMaster=no", "-O", "cancel", "-Dlocalhost:5005", "--", "gw"}},
		{"exit", ExitArgs(sshConfigGW), []string{"-o", "ControlMaster=no", "-O", "exit", "--", "gw"}},
		{"start ssh-config", StartArgs(sshConfigGW), []string{"-o", "ConnectTimeout=15", "-fN", "--", "gw"}},
		{"start managed", StartArgs(managedGW), []string{"-o", "ConnectTimeout=15",
			"-o", "ControlMaster=auto", "-o", "ControlPath=/s/cm-abc", "-o", "ControlPersist=yes",
			"-o", "ServerAliveInterval=60", "-o", "ServerAliveCountMax=3", "-fN", "--", "gw.example.org"}},
		{"jump", JumpArgs(sshConfigGW, "node1", "-L4001:localhost:4001"), []string{
			"-Nf", "-o", "ExitOnForwardFailure=yes", "-o", "ControlMaster=no", "-o", "ControlPath=none",
			"-o", "ConnectTimeout=15", "-o", "ProxyCommand=ssh -o ControlMaster=no -W %h:%p -- gw",
			"-L4001:localhost:4001", "--", "node1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !slices.Equal(c.got, c.want) {
				t.Fatalf("got  %q\nwant %q", c.got, c.want)
			}
		})
	}
}

func TestProxyCommandQuotesAndEscapes(t *testing.T) {
	gw := GW{Dest: "me@gw", ClientOpts: []string{"-o", "ControlMaster=no", "-o", `ControlPath="/Users/A B/state/cm-1%x"`}}
	got := ProxyCommand(gw)
	want := `ssh -o ControlMaster=no -o 'ControlPath="/Users/A B/state/cm-1%%x"' -W %h:%p -- me@gw`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// A destination is never read as an option, even one starting with '-'.
// Config validation refuses those already; this is the second line.
func TestArgsEndOptionsBeforeDestination(t *testing.T) {
	gw := GW{Dest: "-oProxyCommand=evil", ClientOpts: []string{"-o", "ControlMaster=no"}}
	for name, args := range map[string][]string{
		"check": CheckArgs(gw), "exit": ExitArgs(gw), "start": StartArgs(gw),
		"forward": ForwardArgs(gw, "-L1:h:1"), "cancel": CancelArgs(gw, "-L1:h:1"),
		"standalone": StandaloneArgs(gw.Dest, "-L1:h:1", ""),
	} {
		if i := slices.Index(args, gw.Dest); i < 1 || args[i-1] != "--" {
			t.Errorf("%s: %q", name, args)
		}
	}
	if j := JumpArgs(gw, "-onode", "-L1:localhost:1"); j[len(j)-2] != "--" || j[len(j)-1] != "-onode" {
		t.Errorf("jump: %q", j)
	}
	if p := ProxyCommand(gw); !strings.HasSuffix(p, " -W %h:%p -- -oProxyCommand=evil") {
		t.Errorf("proxy command: %s", p)
	}
}

func TestSpecs(t *testing.T) {
	if got := LocalSpec(4001, "node1", 8888); got != "-Llocalhost:4001:node1:8888" {
		t.Fatal(got)
	}
	if got := SocksSpec(5005); got != "-Dlocalhost:5005" {
		t.Fatal(got)
	}
}

func TestStandaloneArgs(t *testing.T) {
	common := "-N -o BatchMode=yes -o ExitOnForwardFailure=yes -o ConnectTimeout=15 -o ServerAliveInterval=30 -o ServerAliveCountMax=3"
	cases := []struct{ dest, spec, jump, want string }{
		{"gw", "-L8888:node1:8888", "", common + " -L8888:node1:8888 -- gw"},
		{"gw", "-Dlocalhost:5005", "", common + " -Dlocalhost:5005 -- gw"},
		{"gw", "-L4001:localhost:4001", "node1", common + " -L4001:localhost:4001 -o ProxyCommand=ssh -o BatchMode=yes -W %h:%p -- gw -- node1"},
	}
	for _, c := range cases {
		if got := strings.Join(StandaloneArgs(c.dest, c.spec, c.jump), " "); got != c.want {
			t.Errorf("StandaloneArgs(%q, %q, %q)\n got %s\nwant %s", c.dest, c.spec, c.jump, got, c.want)
		}
		if got := strings.Join(StandaloneArgs(c.dest, c.spec, c.jump), " "); strings.Contains(got, "Control") {
			t.Errorf("connection-sharing option in %s", got)
		}
	}
}

func TestWindowsProxyCommand(t *testing.T) {
	cases := map[string]string{
		"gw":        `ssh -o BatchMode=yes -W %h:%p -- gw`,
		"me@gw":     `ssh -o BatchMode=yes -W %h:%p -- me@gw`,
		"100%gw":    `ssh -o BatchMode=yes -W %h:%p -- 100%%gw`,
		`my gw`:     `ssh -o BatchMode=yes -W %h:%p -- "my gw"`,
		`a"b`:       `ssh -o BatchMode=yes -W %h:%p -- "a\"b"`,
		`dir\ end\`: `ssh -o BatchMode=yes -W %h:%p -- "dir\ end\\"`,
	}
	for dest, want := range cases {
		if got := WindowsProxyCommand(dest); got != want {
			t.Errorf("WindowsProxyCommand(%q) = %s, want %s", dest, got, want)
		}
	}
}
