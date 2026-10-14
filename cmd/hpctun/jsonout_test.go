package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"hpctun/internal/config"
	"hpctun/internal/gateway"
	"hpctun/internal/jsondoc"
	"hpctun/internal/slurmjob"
	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
	"hpctun/internal/tunnel"
)

// mustOneJSON decodes out as exactly one JSON object with version 1, failing
// on anything after it.
func mustOneJSON(t *testing.T, out []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(out))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s", err, out)
	}
	if dec.More() {
		t.Fatalf("stdout has more than one JSON value:\n%s", out)
	}
	if rest := strings.TrimSpace(string(out[dec.InputOffset():])); rest != "" {
		t.Fatalf("stdout has text after the document: %q", rest)
	}
	if doc["version"] != float64(1) {
		t.Fatalf("version = %v, want 1\n%s", doc["version"], out)
	}
	return doc
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	var b bytes.Buffer
	if err := writeJSON(&b, v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func TestStatusDocGateway(t *testing.T) {
	cases := []struct {
		name string
		h    header
		want string
	}{
		{"not configured", header{}, `null`},
		{"running", header{Dest: "gw", Mode: gateway.ModeManaged, Status: gateway.Status{Running: true, PID: 42}},
			`{"dest":"gw","tunnels_only":false,"mode":"managed","running":true,"pid":42,"stale":false,"error":null,"hints":[]}`},
		{"not running, stale, hints", header{Dest: "gw", Mode: gateway.ModeSSHConfig, Status: gateway.Status{Stale: true, PID: 7}, Hints: []string{"set ServerAliveInterval"}},
			`{"dest":"gw","tunnels_only":false,"mode":"ssh-config","running":false,"pid":null,"stale":true,"error":null,"hints":["set ServerAliveInterval"]}`},
		{"resolve error", header{Dest: "gw", Tunnels: true, Err: errors.New("ssh -G gw: exit 255")},
			`{"dest":"gw","tunnels_only":true,"mode":null,"running":false,"pid":null,"stale":false,"error":"ssh -G gw: exit 255","hints":[]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := marshal(t, statusDoc(c.h, nil).Gateway); got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestConnDoc(t *testing.T) {
	cases := []struct {
		name string
		c    *conn
		want string
	}{
		{"absent", nil, `null`},
		{"login connected", &conn{Role: "gateway", Alias: "shell", Mode: gateway.ModeManaged, Status: gateway.Status{Running: true, PID: 9}},
			`{"alias":"shell","mode":"managed","shared":true,"connected":true,"pid":9,"error":null}`},
		{"transfer not connected", &conn{Role: "transfer", Alias: "files", Mode: gateway.ModeSSHConfig},
			`{"alias":"files","mode":"ssh-config","shared":true,"connected":false,"pid":null,"error":null}`},
		{"transfer managed", &conn{Role: "transfer", Alias: "files", Mode: gateway.ModeManaged},
			`{"alias":"files","mode":"managed","shared":false,"connected":false,"pid":null,"error":null}`},
		{"error", &conn{Role: "transfer", Alias: "files", Err: errors.New("boom")},
			`{"alias":"files","mode":null,"shared":true,"connected":false,"pid":null,"error":"boom"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := marshal(t, connDoc(c.c)); got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestCodeDoc(t *testing.T) {
	job := slurmjob.Job{ID: "123", State: "RUNNING", Node: "node7", Left: "23:10:00"}
	cases := []struct {
		name string
		c    *codeLine
		want string
	}{
		{"no [code]", nil, `null`},
		{"not connected", &codeLine{NotConnected: true}, `{"state":"unknown","job":null,"error":null}`},
		{"error", &codeLine{Err: errors.New("squeue: timeout")}, `{"state":"error","job":null,"error":"squeue: timeout"}`},
		{"none", &codeLine{None: true}, `{"state":"none","job":null,"error":null}`},
		{"running", &codeLine{Job: &job},
			`{"state":"running","job":{"id":"123","state":"RUNNING","node":"node7","time_left":"23:10:00","reason":""},"error":null}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := marshal(t, codeDoc(c.c)); got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestRowDoc(t *testing.T) {
	cases := []struct {
		name string
		r    tunnel.Row
		want string
	}{
		{"named up", tunnel.Row{Kind: tunnel.KindNamed, Name: "dash", Port: 8099, Route: "master", Target: "node1:8099", State: tunnel.StateUp, Conns: 2},
			`{"kind":"named","name":"dash","port":8099,"route":"master","target":"node1:8099","state":"up","conns":2,"owner":null,"warning":null,"url":null,"browse":true}`},
		{"named down", tunnel.Row{Kind: tunnel.KindNamed, Name: "jup", Port: 4001, Route: "jump", Target: "node1:4001", State: tunnel.StateDown, Conns: -1},
			`{"kind":"named","name":"jup","port":4001,"route":"jump","target":"node1:4001","state":"down","conns":null,"owner":null,"warning":null,"url":null,"browse":true}`},
		{"socks", tunnel.Row{Kind: tunnel.KindNamed, Name: "proxy", Port: 5005, Route: "socks", Target: "-", State: tunnel.StateUp, Conns: 0},
			`{"kind":"named","name":"proxy","port":5005,"route":"socks","target":null,"state":"up","conns":0,"owner":null,"warning":null,"url":null,"browse":true}`},
		{"adhoc conflict with warning", tunnel.Row{Kind: tunnel.KindAdhoc, Name: "(ad-hoc)", Port: 4002, Route: "master", Target: "node1:4002", State: tunnel.StateConflict, Conns: 0, Owner: "pid 5 (python)", Warning: "opened through old-gw, not the configured gateway"},
			`{"kind":"adhoc","name":null,"port":4002,"route":"master","target":"node1:4002","state":"conflict","conns":0,"owner":"pid 5 (python)","warning":"opened through old-gw, not the configured gateway","url":null,"browse":true}`},
		{"external", tunnel.Row{Kind: tunnel.KindExternal, Name: "-", Port: 43723, Route: "?", Target: "ssh pid 500", State: tunnel.StateExternal, Conns: 1},
			`{"kind":"external","name":null,"port":43723,"route":null,"target":"ssh pid 500","state":"external","conns":1,"owner":null,"warning":null,"url":null,"browse":true}`},
		{"named with url", tunnel.Row{Kind: tunnel.KindNamed, Name: "chat", Port: 1080, Route: "socks", Target: "-", State: tunnel.StateUp, Conns: 0, URL: "https://chat.example.org"},
			`{"kind":"named","name":"chat","port":1080,"route":"socks","target":null,"state":"up","conns":0,"owner":null,"warning":null,"url":"https://chat.example.org","browse":true}`},
		{"named, browse = false", tunnel.Row{Kind: tunnel.KindNamed, Name: "llm", Port: 8090, Route: "master", Target: "gw:8090", State: tunnel.StateDown, Conns: -1, NoBrowse: true},
			`{"kind":"named","name":"llm","port":8090,"route":"master","target":"gw:8090","state":"down","conns":null,"owner":null,"warning":null,"url":null,"browse":false}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := marshal(t, rowDoc(c.r)); got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestStatusDocEmpty(t *testing.T) {
	got := marshal(t, statusDoc(header{}, nil))
	want := `{"version":1,"config":"","gateway":null,"login":null,"transfer":null,"code":null,"tunnels":[]}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestResultsDoc(t *testing.T) {
	results := []tunnel.Result{
		{Spec: tunnel.Spec{Name: "dash", Port: 8099, Route: config.RouteMaster, Host: "node1", RemotePort: 8099}, Outcome: tunnel.AlreadyUp},
		{Spec: tunnel.Spec{Port: 5006, Route: config.RouteSocks}, Outcome: tunnel.Failed, Detail: "bind: Address already in use\nsecond line"},
	}
	got := marshal(t, resultsDoc(results, &jsondoc.Saved{Name: "web", Config: "/c.toml"}, nil))
	want := `{"version":1,"results":[` +
		`{"kind":"named","name":"dash","port":8099,"route":"master","host":"node1","remote_port":8099,"outcome":"already_up","detail":null},` +
		`{"kind":"adhoc","name":null,"port":5006,"route":"socks","host":null,"remote_port":null,"outcome":"failed","detail":"bind: Address already in use\nsecond line"}` +
		`],"saved":{"name":"web","config":"/c.toml"},"save_error":null}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := marshal(t, resultsDoc(nil, nil, nil)); got != `{"version":1,"results":[],"saved":null,"save_error":null}` {
		t.Errorf("empty: %s", got)
	}
}

func TestOutcomeNames(t *testing.T) {
	for _, o := range []tunnel.Outcome{tunnel.Opened, tunnel.AlreadyUp, tunnel.Closed, tunnel.AlreadyDown, tunnel.Skipped, tunnel.Failed} {
		got := resultDoc(tunnel.Result{Outcome: o}).Outcome
		if strings.ContainsAny(got, " -") || got == "" {
			t.Errorf("outcome %q encodes as %q", o, got)
		}
	}
}

// Targets and messages keep <, > and & as they are.
func TestWriteJSONNoHTMLEscape(t *testing.T) {
	if got := marshal(t, map[string]string{"m": "a <b> & c"}); got != `{"m":"a <b> & c"}` {
		t.Errorf("got %s", got)
	}
}

// jsonError runs args and returns the error document on stdout, checking
// that its exit matches the process's.
func jsonError(t *testing.T, a *app, out *bytes.Buffer, args ...string) map[string]any {
	t.Helper()
	err := runCmd(a, args...)
	if err == nil {
		t.Fatalf("%q succeeded:\n%s", args, out)
	}
	e, ok := mustOneJSON(t, out.Bytes())["error"].(map[string]any)
	if !ok {
		t.Fatalf("%q: no error document:\n%s", args, out)
	}
	if e["exit"] != float64(exitCode(err)) || e["message"] != err.Error() {
		t.Fatalf("%q: error = %v, want exit %d and %q", args, e, exitCode(err), err)
	}
	return e
}

func TestJSONErrorBadConfig(t *testing.T) {
	for _, args := range [][]string{{"status", "--json"}, {"up", "--json", "--all"}, {"down", "--json", "--all"}} {
		a, out, _ := testApp(t, "gateway = \n")
		if e := jsonError(t, a, out, args...); e["exit"] != float64(2) {
			t.Errorf("%q: exit %v, want 2", args, e["exit"])
		}
	}
}

func TestJSONErrorUsage(t *testing.T) {
	a, out, _ := testApp(t, jsonUpConfig)
	if e := jsonError(t, a, out, "down", "--json"); e["exit"] != float64(2) {
		t.Errorf("exit %v, want 2", e["exit"])
	}
}

func TestJSONErrorProbeFails(t *testing.T) {
	a, out, _ := testApp(t, jsonUpConfig)
	a.run = &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		return sshctl.Result{ExitCode: 2, Stderr: []byte("lsof: broken")}, nil
	}}
	if e := jsonError(t, a, out, "status", "--json"); e["exit"] != float64(1) {
		t.Errorf("exit %v, want 1", e["exit"])
	}
}

// A partial failure has its results document and no error document after it.
func TestJSONErrorNotAfterResults(t *testing.T) {
	a, out, _ := testApp(t, jsonUpConfig)
	a.run = (&tunnelFake{}).runner()
	err := runCmd(a, "up", "--json", "jup")
	if exitCode(err) != 1 {
		t.Fatalf("exit %d, want 1", exitCode(err))
	}
	if doc := mustOneJSON(t, out.Bytes()); doc["error"] != nil || doc["results"] == nil {
		t.Fatalf("got %s", out)
	}
}

// Cobra rejects a bad flag before hpctun runs: stdout stays empty, exit 2.
func TestJSONUnknownFlag(t *testing.T) {
	a, out, _ := testApp(t, jsonUpConfig)
	if err := runCmd(a, "status", "--json", "--bogus"); exitCode(err) != 2 || out.Len() != 0 {
		t.Fatalf("err %v (exit %d), stdout %q", err, exitCode(err), out)
	}
}
