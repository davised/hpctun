package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
)

const browseConfig = "gateway = \"gw\"\ndefault_host = \"node1\"\n" +
	"[tunnel.chat]\nsocks = 1080\nurl = \"https://chat.example.org\"\n" +
	"[tunnel.proxy]\nsocks = 5005\n" +
	"[tunnel.jup]\nport = 8888\nurl = \"http://localhost:8888/lab\"\n" +
	"[tunnel.dash]\nport = 8099\n" +
	"[tunnel.api]\nport = 8090\nbrowse = false\n"

// browseApp: tunnels per tunnelFake, Linux, HPCTUN_BROWSER set, Firefox the
// default browser; started records what a.start was given.
func browseApp(t *testing.T, listening map[int]bool) (*app, *bytes.Buffer, *[][]string) {
	a, out, _ := testApp(t, browseConfig)
	a.goos = "linux"
	base := (&tunnelFake{listening: listening}).runner()
	a.run = &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		switch c.Line() {
		case "xdg-settings get default-web-browser":
			return sshctl.Result{Stdout: []byte("firefox.desktop\n")}, nil
		case "ps -ww -eo args":
			return sshctl.Result{}, nil
		}
		return base.Handler(c)
	}}
	env := a.getenv
	a.getenv = func(k string) string {
		if k == "HPCTUN_BROWSER" {
			return "/opt/chrome/chrome"
		}
		return env(k)
	}
	var started [][]string
	a.start = func(argv []string) error { started = append(started, argv); return nil }
	return a, out, &started
}

func TestBrowseSocksOpensTunnelThenProxiedWindow(t *testing.T) {
	a, out, started := browseApp(t, nil)
	if err := runCmd(a, "browse", "chat"); err != nil {
		t.Fatal(err)
	}
	dir, _ := a.stateDir()
	want := []string{"/opt/chrome/chrome", "--user-data-dir=" + dir + "/browser/chat", "--proxy-server=socks5://127.0.0.1:1080",
		"--no-first-run", "--no-default-browser-check", "https://chat.example.org"}
	if len(*started) != 1 || strings.Join((*started)[0], " ") != strings.Join(want, " ") {
		t.Fatalf("started %q", *started)
	}
	if !strings.Contains(out.String(), "opened") || !strings.Contains(out.String(), "opened chat in chrome (proxy 127.0.0.1:1080)") {
		t.Fatalf("out:\n%s", out)
	}
	if len(a.run.(*sshctltest.Fake).Find("-O forward")) != 1 {
		t.Fatal("tunnel not opened")
	}
}

func TestBrowseAlreadyUpOpensNothingNew(t *testing.T) {
	a, _, started := browseApp(t, map[int]bool{1080: true})
	if err := runCmd(a, "browse", "chat"); err != nil {
		t.Fatal(err)
	}
	if len(a.run.(*sshctltest.Fake).Find("-O forward")) != 0 || len(*started) != 1 {
		t.Fatalf("forwards %d started %q", len(a.run.(*sshctltest.Fake).Find("-O forward")), *started)
	}
}

func TestBrowsePageChoice(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"jup"}, []string{"xdg-open", "http://localhost:8888/lab"}},
		{[]string{"dash"}, []string{"xdg-open", "http://localhost:8099/"}},
		{[]string{"8099"}, []string{"xdg-open", "http://localhost:8099/"}},
		{[]string{"jup", "http://localhost:8888/tree"}, []string{"xdg-open", "http://localhost:8888/tree"}},
		{[]string{"proxy"}, []string{"/opt/chrome/chrome", "--no-default-browser-check"}}, // blank window: argv ends with the flags
	}
	for _, c := range cases {
		a, _, started := browseApp(t, map[int]bool{8888: true, 8099: true, 5005: true})
		if err := runCmd(a, append([]string{"browse"}, c.args...)...); err != nil {
			t.Fatalf("%q: %v", c.args, err)
		}
		got := (*started)[0]
		if got[0] != c.want[0] || got[len(got)-1] != c.want[len(c.want)-1] {
			t.Errorf("%q: started %q", c.args, got)
		}
	}
}

func TestBrowseErrors(t *testing.T) {
	a, _, started := browseApp(t, nil)
	if err := runCmd(a, "browse", "nope"); exitCode(err) != 2 {
		t.Errorf("unknown name: %v", err)
	}
	if err := runCmd(a, "browse", "4003"); exitCode(err) != 2 || !strings.Contains(err.Error(), "no tunnel on port 4003") {
		t.Errorf("unknown port: %v", err)
	}
	if err := runCmd(a, "browse", "chat", "file:///etc/passwd"); exitCode(err) != 2 {
		t.Errorf("bad url: %v", err)
	}
	if err := runCmd(a, "browse"); exitCode(err) != 2 {
		t.Errorf("no args: %v", err)
	}
	if err := runCmd(a, "browse", "chat", "https://x.org", "extra"); exitCode(err) != 2 || !strings.Contains(err.Error(), "too many arguments") {
		t.Errorf("three args: %v", err)
	}
	if len(*started) != 0 {
		t.Fatalf("started %q", *started)
	}
}

// A tunnel that fails to open starts no browser; --json still prints one document.
func TestBrowseUpFails(t *testing.T) {
	a, out, started := browseApp(t, nil)
	a.run = (&tunnelFake{masterDown: true}).runner()
	err := runCmd(a, "browse", "--json", "chat")
	if exitCode(err) != 1 || len(*started) != 0 {
		t.Fatalf("exit %d started %q", exitCode(err), *started)
	}
	doc := mustOneJSON(t, out.Bytes())
	if doc["result"].(map[string]any)["outcome"] != "failed" || doc["browser"] != nil {
		t.Fatalf("doc %v", doc)
	}
}

// The error line (all the tray reads) carries why the tunnel did not open.
func TestBrowseUpFailsErrorCarriesDetail(t *testing.T) {
	a, _, _ := browseApp(t, nil)
	a.run = (&tunnelFake{masterDown: true}).runner()
	err := runCmd(a, "browse", "chat")
	if err == nil || !strings.Contains(err.Error(), "did not open: ") || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("error spans lines: %q", err)
	}
}

// With --json and no argument the error is the JSON error document.
func TestBrowseJSONNoArgs(t *testing.T) {
	a, out, _ := browseApp(t, nil)
	err := runCmd(a, "browse", "--json")
	if exitCode(err) != 2 {
		t.Fatalf("exit %d: %v", exitCode(err), err)
	}
	doc := mustOneJSON(t, out.Bytes())
	if !strings.Contains(fmt.Sprint(doc["error"]), "name a tunnel or port") {
		t.Fatalf("doc %v", doc)
	}
}

func TestBrowseJSON(t *testing.T) {
	a, out, _ := browseApp(t, map[int]bool{1080: true})
	if err := runCmd(a, "browse", "--json", "chat"); err != nil {
		t.Fatal(err)
	}
	doc := mustOneJSON(t, out.Bytes())
	if doc["browser"] != "chrome" || doc["url"] != "https://chat.example.org" || doc["warning"] != nil ||
		doc["result"].(map[string]any)["outcome"] != "already_up" {
		t.Fatalf("doc %v", doc)
	}
}

func TestBrowseNoChromium(t *testing.T) {
	a, _, started := browseApp(t, map[int]bool{1080: true})
	env := a.getenv
	a.getenv = func(k string) string {
		if k == "HPCTUN_BROWSER" {
			return ""
		}
		return env(k)
	}
	// No Chromium on PATH (a.lookPath is otherwise used only by askpass).
	a.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	err := runCmd(a, "browse", "chat")
	want := "no Chrome, Edge, Brave or Chromium found; set HPCTUN_BROWSER to one, or set Firefox's SOCKS proxy to 127.0.0.1:1080"
	if exitCode(err) != 1 || err.Error() != want || len(*started) != 0 {
		t.Fatalf("err %v", err)
	}
}

func TestBrowseOldPortWarns(t *testing.T) {
	a, out, _ := browseApp(t, map[int]bool{1080: true})
	dir, _ := a.stateDir()
	base := a.run.(*sshctltest.Fake).Handler
	a.run = &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		if c.Line() == "ps -ww -eo args" {
			return sshctl.Result{Stdout: []byte("/opt/chrome/chrome --user-data-dir=" + dir + "/browser/chat --proxy-server=socks5://127.0.0.1:1081\n")}, nil
		}
		return base(c)
	}}
	if err := runCmd(a, "browse", "chat"); err != nil {
		t.Fatal(err)
	}
	const warn = "the chat window still uses port 1081; close it and run hpctun browse chat again"
	errb := a.stderr.(*bytes.Buffer)
	if !strings.Contains(errb.String(), "warning: "+warn) || strings.Contains(out.String(), warn) {
		t.Fatalf("out:\n%s\nerr:\n%s", out, errb)
	}
}

// A tunnel with browse = false has no page: browse refuses before opening
// anything, unless it is given a url.
func TestBrowseFalse(t *testing.T) {
	a, _, started := browseApp(t, map[int]bool{8090: true})
	err := runCmd(a, "browse", "api")
	if exitCode(err) != 2 || !strings.Contains(err.Error(), `tunnel "api" has no page to open (browse = false)`) {
		t.Fatalf("err %v", err)
	}
	if len(*started) != 0 || len(a.run.(*sshctltest.Fake).Find("-O")) != 0 {
		t.Fatalf("started %q, ssh %v", *started, a.run.(*sshctltest.Fake).Calls)
	}
	if err := runCmd(a, "browse", "api", "http://localhost:8090/docs"); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 1 || (*started)[0][len((*started)[0])-1] != "http://localhost:8090/docs" {
		t.Fatalf("started %q", *started)
	}
}
