package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"hpctun/internal/config"
	"hpctun/internal/prompt"
	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
	"hpctun/internal/state"
	"hpctun/internal/tunnel"
)

func upConfig(t *testing.T) *config.Config {
	c, err := config.Parse([]byte("gateway = \"gw\"\ndefault_host = \"node1\"\n[tunnel.jup]\nport = 4001\nroute = \"jump\"\n[tunnel.dash]\nport = 8099\n"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPlanUp(t *testing.T) {
	cfg := upConfig(t)
	st := &state.State{Tunnels: []state.Entry{{Port: 4002, Route: config.RouteMaster, Host: "node1", RemotePort: 4002}}}
	cases := []struct {
		name    string
		args    []string
		all     bool
		f       adhocFlags
		want    []tunnel.Spec
		wantErr string
	}{
		{"named", []string{"jup"}, false, adhocFlags{}, []tunnel.Spec{{Name: "jup", Port: 4001, Route: config.RouteJump, Host: "node1", RemotePort: 4001}}, ""},
		{"new adhoc defaults", []string{"4003"}, false, adhocFlags{}, []tunnel.Spec{{Port: 4003, Route: config.RouteMaster, Host: "node1", RemotePort: 4003}}, ""},
		{"new adhoc flags", []string{"4003"}, false, adhocFlags{host: "web1", remotePort: 80, loopback: true, set: true},
			[]tunnel.Spec{{Port: 4003, Route: config.RouteJump, Host: "web1", RemotePort: 80}}, ""},
		{"socks", nil, false, adhocFlags{socks: 5005, set: true}, []tunnel.Spec{{Port: 5005, Route: config.RouteSocks}}, ""},
		{"recorded adhoc", []string{"4002"}, false, adhocFlags{}, []tunnel.Spec{{Port: 4002, Route: config.RouteMaster, Host: "node1", RemotePort: 4002}}, ""},
		{"recorded adhoc same flags", []string{"4002"}, false, adhocFlags{host: "node1", set: true}, []tunnel.Spec{{Port: 4002, Route: config.RouteMaster, Host: "node1", RemotePort: 4002}}, ""},
		{"all", nil, true, adhocFlags{}, []tunnel.Spec{
			{Name: "dash", Port: 8099, Route: config.RouteMaster, Host: "node1", RemotePort: 8099},
			{Name: "jup", Port: 4001, Route: config.RouteJump, Host: "node1", RemotePort: 4001},
			{Port: 4002, Route: config.RouteMaster, Host: "node1", RemotePort: 4002}}, ""},
		{"flags on named", []string{"8099"}, false, adhocFlags{host: "x", set: true}, nil, "belongs to tunnel \"dash\""},
		{"flags on name", []string{"jup"}, false, adhocFlags{loopback: true, set: true}, nil, "only apply to ad-hoc"},
		{"conflicting recorded adhoc", []string{"4002"}, false, adhocFlags{host: "web1", set: true}, nil, "hpctun down 4002"},
		{"all with args", []string{"jup"}, true, adhocFlags{}, nil, "--all"},
		{"socks with args", []string{"jup"}, false, adhocFlags{socks: 5005, set: true}, nil, "--socks"},
		{"nothing", nil, false, adhocFlags{}, nil, "name a tunnel"},
		{"unknown name", []string{"nope"}, false, adhocFlags{}, nil, "no tunnel named"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := planUp(cfg, st, c.args, c.all, c.f)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %+v", got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("spec %d = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestPlanUpNoDefaultHost(t *testing.T) {
	cfg, _ := config.Parse([]byte("gateway = \"gw\"\n"))
	if _, err := planUp(cfg, &state.State{}, []string{"4003"}, false, adhocFlags{}); err == nil || !strings.Contains(err.Error(), "--host") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckSaveName(t *testing.T) {
	cfg := upConfig(t)
	cases := []struct {
		name    string
		wantErr string
	}{
		{"", ""},
		{"newname", ""},
		{"Bad Name", "Bad Name"},
		{"dash", "already exists"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkSaveName(cfg, c.name)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("checkSaveName(%q) = %v, want nil", c.name, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("checkSaveName(%q) = %v, want it to contain %q", c.name, err, c.wantErr)
			}
		})
	}
}

// TestUpSaveNameCheckedBeforeOpen pins that a bad --save name is rejected
// with exit 2 before any ssh command runs: the ssh stub leaves a marker file,
// and the marker must be absent afterwards.
func TestUpSaveNameCheckedBeforeOpen(t *testing.T) {
	home := t.TempDir()
	stateHome := t.TempDir()
	// The ssh stub marks that it ran, then fails, so a regression that
	// reaches openEnv fails fast instead of dialing the gateway.
	bin := t.TempDir()
	sshRan := filepath.Join(t.TempDir(), "ssh-was-run")
	stub(t, filepath.Join(bin, "ssh"), ": > '"+sshRan+"'\nexit 255")
	t.Setenv("PATH", bin)

	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("gateway = \"gw.example.org\"\ndefault_host = \"node1\"\n[tunnel.jup]\nport = 4001\n[tunnel.dash]\nport = 8099\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		switch k {
		case "HOME":
			return home
		case "XDG_STATE_HOME":
			return stateHome
		}
		return ""
	}

	a := newApp()
	a.getenv = getenv
	a.configPath = cfgPath
	a.stdout, a.stderr = io.Discard, io.Discard
	err := runUp(context.Background(), a, []string{"4002"}, false, adhocFlags{}, "dash", false, false)
	if exitCode(err) != 2 {
		t.Fatalf("exitCode(err) = %d, want 2 (err = %v)", exitCode(err), err)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want it to contain %q", err, "already exists")
	}
	if _, err := os.Stat(sshRan); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("runUp ran ssh before rejecting the --save name (stat err = %v)", err)
	}
}

func TestUpRefusesOptionAsHost(t *testing.T) {
	a, _, _ := testApp(t, jsonUpConfig) // its runner fails the test on any command
	err := runCmd(a, "up", "4002", "--host=-oProxyCommand=touch /tmp/x")
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "--host") || !strings.Contains(err.Error(), "starts with '-'") {
		t.Fatalf("err = %v", err)
	}
}

func TestReport(t *testing.T) {
	var b bytes.Buffer
	n := report(&b, []tunnel.Result{
		{Spec: tunnel.Spec{Name: "jup", Port: 4001, Route: config.RouteJump, Host: "n1", RemotePort: 4001}, Outcome: tunnel.Opened},
		{Spec: tunnel.Spec{Port: 4002, Route: config.RouteMaster, Host: "n1", RemotePort: 4002}, Outcome: tunnel.Failed, Detail: "ssh exited 255:\nPermission denied"},
	})
	if n != 1 {
		t.Fatalf("failures = %d", n)
	}
	want := "jup (4001 → n1:4001, jump): opened\n(ad-hoc) (4002 → n1:4002, master): failed\n    ssh exited 255:\n    Permission denied\n"
	if b.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", b.String(), want)
	}
}

// tunnelFake is a gateway whose master runs as pid 100 (unless down):
// ssh -O forward starts listening on the forwarded port, -O cancel stops,
// and starting a master fails.
type tunnelFake struct {
	masterDown bool
	listening  map[int]bool
}

func (f *tunnelFake) runner() *sshctltest.Fake {
	if f.listening == nil {
		f.listening = map[int]bool{}
	}
	return &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		line := c.Line()
		switch {
		case c.Name == "lsof" && slices.Contains(c.Args, "-sTCP:LISTEN"):
			var out strings.Builder
			for port := range f.listening {
				fmt.Fprintf(&out, "p100\ncssh\nf6\nPTCP\nn127.0.0.1:%d\n", port)
			}
			if out.Len() == 0 {
				return sshctl.Result{ExitCode: 1}, nil
			}
			return sshctl.Result{Stdout: []byte(out.String())}, nil
		case c.Name == "lsof":
			return sshctl.Result{ExitCode: 1}, nil
		case strings.Contains(line, " -G "):
			return sshctl.Result{Stdout: []byte("hostname gw.example.org\n")}, nil
		case strings.Contains(line, "-O check"):
			if f.masterDown {
				return sshctl.Result{ExitCode: 255, Stderr: []byte("Control socket connect(x): No such file or directory\n")}, nil
			}
			return sshctl.Result{Stderr: []byte("Master running (pid=100)\n")}, nil
		case strings.Contains(line, "-O forward") || strings.Contains(line, "-O cancel"):
			m := regexp.MustCompile(`-[LD] ?(?:127\.0\.0\.1:|localhost:)?(\d+)`).FindStringSubmatch(line)
			if m == nil {
				return sshctl.Result{ExitCode: 255, Stderr: []byte("bad forward")}, nil
			}
			port, _ := strconv.Atoi(m[1])
			f.listening[port] = strings.Contains(line, "-O forward")
			if !f.listening[port] {
				delete(f.listening, port)
			}
			return sshctl.Result{}, nil
		case c.Background:
			return sshctl.Result{ExitCode: 255, Stderr: []byte("Permission denied (publickey).\n")}, nil
		}
		return sshctl.Result{ExitCode: 255, Stderr: []byte("unexpected: " + line)}, nil
	}}
}

const jsonUpConfig = "gateway = \"gw\"\ndefault_host = \"node1\"\n[tunnel.dash]\nport = 8099\n[tunnel.jup]\nport = 4001\nroute = \"jump\"\n"

func TestUpDownJSON(t *testing.T) {
	a, out, errb := testApp(t, jsonUpConfig)
	f := &tunnelFake{listening: map[int]bool{8099: true}}
	a.run = f.runner()
	if err := runCmd(a, "up", "--json", "dash", "4003"); err != nil {
		t.Fatalf("up: %v\nstderr: %s", err, errb)
	}
	want := `{"version":1,"results":[` +
		`{"kind":"named","name":"dash","port":8099,"route":"master","host":"node1","remote_port":8099,"outcome":"already_up","detail":null},` +
		`{"kind":"adhoc","name":null,"port":4003,"route":"master","host":"node1","remote_port":4003,"outcome":"opened","detail":null}` +
		`],"saved":null,"save_error":null}`
	mustOneJSON(t, out.Bytes())
	if got := strings.TrimSpace(out.String()); got != want {
		t.Fatalf("up: got\n%s\nwant\n%s", got, want)
	}

	out.Reset()
	if err := runCmd(a, "down", "--json", "4003", "4001"); err != nil {
		t.Fatalf("down: %v\nstderr: %s", err, errb)
	}
	mustOneJSON(t, out.Bytes())
	want = `{"version":1,"results":[` +
		`{"kind":"adhoc","name":null,"port":4003,"route":"master","host":"node1","remote_port":4003,"outcome":"closed","detail":null},` +
		`{"kind":"named","name":"jup","port":4001,"route":"jump","host":"node1","remote_port":4001,"outcome":"already_down","detail":null}` +
		`],"saved":null,"save_error":null}`
	if got := strings.TrimSpace(out.String()); got != want {
		t.Fatalf("down: got\n%s\nwant\n%s", got, want)
	}
}

// A failure still prints every result, and hpctun exits 1.
func TestUpJSONPartialFailure(t *testing.T) {
	a, out, _ := testApp(t, jsonUpConfig)
	a.run = (&tunnelFake{}).runner()
	err := runCmd(a, "up", "--json", "dash", "jup")
	if exitCode(err) != 1 {
		t.Fatalf("exit %d (%v), want 1", exitCode(err), err)
	}
	results := mustOneJSON(t, out.Bytes())["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("got %s", out)
	}
	jup := results[1].(map[string]any)
	if jup["outcome"] != "failed" || !strings.Contains(fmt.Sprint(jup["detail"]), "Permission denied") {
		t.Fatalf("jup = %v", jup)
	}
}

func TestUpJSONSave(t *testing.T) {
	a, out, _ := testApp(t, jsonUpConfig)
	a.run = (&tunnelFake{}).runner()
	if err := runCmd(a, "up", "--json", "4003", "--save", "web"); err != nil {
		t.Fatal(err)
	}
	saved, _ := mustOneJSON(t, out.Bytes())["saved"].(map[string]any)
	if saved["name"] != "web" || saved["config"] != a.configPath {
		t.Fatalf("saved = %v", saved)
	}
	cfg, err := a.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Lookup("web"); !ok {
		t.Fatal("web was not saved to the config")
	}
}

// A save that fails after the tunnel opened still reports the tunnel,
// with the save's error beside it, and exits as the text output does.
func TestUpJSONSaveFails(t *testing.T) {
	a, out, _ := testApp(t, jsonUpConfig)
	a.run = (&tunnelFake{}).runner()
	dir := filepath.Dir(a.configPath)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	err := runCmd(a, "up", "--json", "4003", "--save", "web")
	if err == nil {
		t.Fatal("no error")
	}
	doc := mustOneJSON(t, out.Bytes())
	if doc["error"] != nil {
		t.Fatalf("error document: %s", out)
	}
	results, _ := doc["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["outcome"] != "opened" {
		t.Fatalf("results = %v", doc["results"])
	}
	if doc["saved"] != nil || !strings.Contains(fmt.Sprint(doc["save_error"]), "permission denied") {
		t.Fatalf("saved = %v, save_error = %v", doc["saved"], doc["save_error"])
	}
}

// failAsker fails the test on any question.
type failAsker struct {
	prompt.Asker
	t *testing.T
}

func (f failAsker) Input(q prompt.InputQ) (string, error) {
	f.t.Fatalf("asked %+v", q)
	return "", nil
}

func (f failAsker) Select(q prompt.SelectQ) (string, error) {
	f.t.Fatalf("asked %+v", q)
	return "", nil
}

func (f failAsker) Confirm(title, _ string, _ bool) (bool, error) {
	f.t.Fatalf("asked %q", title)
	return false, nil
}

func TestUpJSONNeverPrompts(t *testing.T) {
	a, out, _ := testApp(t, jsonUpConfig)
	yes := true
	a.tty, a.ask = &yes, failAsker{t: t}
	a.run = (&tunnelFake{}).runner()
	if err := runCmd(a, "up", "--json", "4003"); err != nil {
		t.Fatal(err)
	}
	if doc := mustOneJSON(t, out.Bytes()); doc["saved"] != nil {
		t.Fatalf("saved = %v", doc["saved"])
	}
}
