package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
)

const vsCfg = sshCfg + "[vscode]\nsettings = \"~/site dir/it's.jsonc\"\n"

const machine = ".vscode-server/data/Machine/settings.json"

// remote fakes the gateway: files maps a path (as sent, unquoted) to its
// content; a missing key is a missing file. writes records each write's
// argv and stdin; writeCode is the write script's exit status.
type remote struct {
	t         *testing.T
	files     map[string]string
	unreach   bool
	writes    []string
	writeArgv [][]string
	writeCode int
	readFail  map[string]bool // paths whose read fails (exit 1)
}

func (r *remote) install(a *app) *sshctltest.Fake {
	f := &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		if r.unreach {
			return sshctl.Result{ExitCode: 255, Stderr: []byte("Permission denied (publickey,keyboard-interactive).\n")}, nil
		}
		// argv: ssh <opts> -- <gateway> "sh -c '<script>' _ <path>"
		n := len(c.Args)
		if c.Name != "ssh" || n < 3 || c.Args[n-3] != "--" || c.Args[n-2] != "shell" || !strings.HasPrefix(c.Args[n-1], "sh -c '") {
			r.t.Errorf("unexpected command (not to the gateway): %s", c.Line())
			return sshctl.Result{ExitCode: 255}, nil
		}
		cmd := c.Args[n-1]
		path := cmd[strings.LastIndex(cmd, "' _ ")+4:]
		path = strings.Trim(strings.ReplaceAll(path, `'\''`, "'"), "'")
		if r.readFail[path] {
			return sshctl.Result{ExitCode: 1, Stderr: []byte("cat: Permission denied\n")}, nil
		}
		body, ok := r.files[path]
		if !ok {
			return sshctl.Result{ExitCode: 3}, nil
		}
		return sshctl.Result{Stdout: []byte(body)}, nil
	}}
	a.run = f
	a.stream = func(_ context.Context, argv []string, stdin string, _ io.Writer) (string, int, error) {
		r.writeArgv = append(r.writeArgv, argv)
		r.writes = append(r.writes, stdin)
		if r.writeCode != 0 {
			return "", r.writeCode, nil
		}
		return machine + ".bak-20261001-120000\n", 0, nil
	}
	return f
}

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestVSCodeSettingsApplies(t *testing.T) {
	a, out, _ := testApp(t, vsCfg)
	user := "{\n\t\"search.maxResults\": 50 // mine\n}\n"
	r := &remote{t: t, files: map[string]string{
		machine:               user,
		"site dir/it's.jsonc": `{"search.searchOnType": null, "http.noProxy": ["example.org"]}`,
	}}
	f := r.install(a)
	yes := true
	a.tty, a.ask = &yes, answerAsker{yes: true}
	if err := a.runVSCodeSettings(context.Background(), false, false); err != nil {
		t.Fatal(err)
	}
	if len(r.writes) != 1 {
		t.Fatalf("writes: %d", len(r.writes))
	}
	w := r.writes[0]
	for _, want := range []string{`"search.maxResults": 50 // mine`, `"http.noProxy"`, `"files.watcherExclude"`} {
		if !strings.Contains(w, want) {
			t.Errorf("written file lacks %q:\n%s", want, w)
		}
	}
	if strings.Contains(w, "searchOnType") {
		t.Errorf("site null ignored:\n%s", w)
	}
	argv := strings.Join(r.writeArgv[0], " ")
	if !strings.HasPrefix(argv, "ssh -o BatchMode=yes -o ConnectTimeout=15 -- shell sh -c '") || !strings.HasSuffix(argv, " _ "+sum(user)) {
		t.Errorf("write argv: %s", argv)
	}
	if len(f.Calls) != 2 { // the site file and the user's file, both via the gateway (checked in install)
		t.Errorf("reads: %v", f.Calls)
	}
	for _, want := range []string{"--- ~/" + machine, "+\t\"http.noProxy\"", "backup: ~/" + machine + ".bak-20261001-120000", "wrote ~/" + machine} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestVSCodeSettingsNewFile(t *testing.T) {
	a, _, _ := testApp(t, sshCfg) // no [vscode]: built-in defaults only
	r := &remote{t: t, files: map[string]string{}}
	r.install(a)
	if err := a.runVSCodeSettings(context.Background(), false, true); err != nil {
		t.Fatal(err)
	}
	if len(r.writes) != 1 || !strings.Contains(r.writes[0], "search.followSymlinks") {
		t.Fatalf("writes: %q", r.writes)
	}
	if !strings.HasSuffix(strings.Join(r.writeArgv[0], " "), " _ absent") {
		t.Errorf("argv: %q", r.writeArgv[0])
	}
}

func TestVSCodeSettingsDeclinedAndDryRun(t *testing.T) {
	for _, dry := range []bool{false, true} {
		a, out, _ := testApp(t, sshCfg)
		r := &remote{t: t, files: map[string]string{machine: "{}"}}
		r.install(a)
		yes := true
		a.tty, a.ask = &yes, answerAsker{yes: false}
		err := a.runVSCodeSettings(context.Background(), dry, false)
		if dry && err != nil || !dry && err == nil {
			t.Errorf("dry=%v: err %v", dry, err)
		}
		if len(r.writes) != 0 {
			t.Errorf("dry=%v: wrote", dry)
		}
		if !strings.Contains(out.String(), "+++ ~/"+machine) {
			t.Errorf("dry=%v: no diff:\n%s", dry, out)
		}
	}
}

func TestVSCodeSettingsNoTerminal(t *testing.T) {
	a, _, _ := testApp(t, sshCfg) // tty false
	r := &remote{t: t, files: map[string]string{machine: "{}"}}
	r.install(a)
	err := a.runVSCodeSettings(context.Background(), false, false)
	if err == nil || !strings.Contains(err.Error(), "--yes") || len(r.writes) != 0 {
		t.Fatalf("err %v, writes %d", err, len(r.writes))
	}
}

func TestVSCodeSettingsNothingToAdd(t *testing.T) {
	a, out, _ := testApp(t, sshCfg)
	r := &remote{t: t, files: map[string]string{}}
	r.install(a)
	if err := a.runVSCodeSettings(context.Background(), false, true); err != nil {
		t.Fatal(err)
	}
	r.files[machine] = r.writes[0]
	r.writes = nil
	out.Reset()
	if err := a.runVSCodeSettings(context.Background(), false, true); err != nil || len(r.writes) != 0 {
		t.Fatalf("err %v, writes %d", err, len(r.writes))
	}
	if !strings.Contains(out.String(), "already has every recommended setting") {
		t.Errorf("output:\n%s", out)
	}
}

func TestVSCodeSettingsOnlySkipped(t *testing.T) {
	a, out, _ := testApp(t, sshCfg+"[vscode]\nsettings = \"/site/vscode.jsonc\"\ndefaults = false\n")
	r := &remote{t: t, files: map[string]string{
		machine:              `{"w": 1}`,
		"/site/vscode.jsonc": `{"w": {"x": true}}`,
	}}
	r.install(a)
	if err := a.runVSCodeSettings(context.Background(), false, true); err != nil || len(r.writes) != 0 {
		t.Fatalf("err %v, writes %d", err, len(r.writes))
	}
	want := "kept your w: it is not an object, so hpctun adds nothing to it\n~/" + machine + " on shell: nothing else to add\n"
	if out.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
}

func TestVSCodeSettingsSiteOnlyAbsolute(t *testing.T) {
	a, out, _ := testApp(t, sshCfg+"[vscode]\nsettings = \"/site/vscode.jsonc\"\ndefaults = false\n")
	r := &remote{t: t, files: map[string]string{machine: "{}", "/site/vscode.jsonc": `{"x": 1}`}}
	f := r.install(a)
	if err := a.runVSCodeSettings(context.Background(), false, true); err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, c := range f.Calls {
		if strings.HasSuffix(c.Args[len(c.Args)-1], " _ /site/vscode.jsonc") {
			seen = true
		}
	}
	if !seen {
		t.Errorf("site path not sent unchanged: %v", f.Calls)
	}
	if len(r.writes) != 1 {
		t.Fatalf("writes: %d", len(r.writes))
	}
	if w := r.writes[0]; !strings.Contains(w, `"x": 1`) || strings.Contains(w, "search.followSymlinks") {
		t.Errorf("written file:\n%s", w)
	}
	if strings.Contains(out.String(), "\n\n") {
		t.Errorf("blank line in output:\n%s", out)
	}
}

func TestVSCodeSettingsReadFails(t *testing.T) {
	for _, c := range []struct{ cfg, path, want string }{
		{sshCfg, machine, "cannot read ~/" + machine + " on shell: cat: Permission denied"},
		{vsCfg, "site dir/it's.jsonc", "cannot read ~/site dir/it's.jsonc on shell"},
		{sshCfg + "[vscode]\nsettings = \"/site/vscode.jsonc\"\n", "/site/vscode.jsonc", "cannot read /site/vscode.jsonc on shell"},
	} {
		a, _, _ := testApp(t, c.cfg)
		r := &remote{t: t, files: map[string]string{machine: "{}"}, readFail: map[string]bool{c.path: true}}
		r.install(a)
		err := a.runVSCodeSettings(context.Background(), false, true)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want %q", c.path, err, c.want)
		}
	}
}

func TestVSCodeSettingsChangedWhileRunning(t *testing.T) {
	a, _, _ := testApp(t, sshCfg)
	r := &remote{t: t, files: map[string]string{machine: "{}"}, writeCode: 3}
	r.install(a)
	err := a.runVSCodeSettings(context.Background(), false, true)
	if err == nil || !strings.Contains(err.Error(), "changed while hpctun was running; run it again") {
		t.Fatalf("err %v", err)
	}
}

func TestVSCodeSettingsUnreachable(t *testing.T) {
	a, _, _ := testApp(t, sshCfg)
	r := &remote{t: t, unreach: true}
	r.install(a)
	err := a.runVSCodeSettings(context.Background(), false, true)
	if err == nil || !strings.Contains(err.Error(), "hpctun login") || !strings.Contains(err.Error(), "hpctun ssh-config key") || len(r.writes) != 0 {
		t.Fatalf("err %v", err)
	}
}

func TestVSCodeSettingsSiteFileErrors(t *testing.T) {
	for _, c := range []struct{ site, want string }{
		{"", "~/site dir/it's.jsonc: no such file on shell"},
		{"{\n\"a\" 1}", "~/site dir/it's.jsonc: line 2"},
	} {
		a, _, _ := testApp(t, vsCfg)
		files := map[string]string{machine: "{}"}
		if c.site != "" {
			files["site dir/it's.jsonc"] = c.site
		}
		r := &remote{t: t, files: files}
		r.install(a)
		err := a.runVSCodeSettings(context.Background(), false, true)
		if err == nil || !strings.Contains(err.Error(), c.want) || len(r.writes) != 0 {
			t.Errorf("site %q: err %v", c.site, err)
		}
	}
}

func TestVSCodeSettingsUserFileInvalid(t *testing.T) {
	a, _, _ := testApp(t, sshCfg)
	r := &remote{t: t, files: map[string]string{machine: "{\n\"a\": }"}}
	r.install(a)
	err := a.runVSCodeSettings(context.Background(), false, true)
	if err == nil || !strings.Contains(err.Error(), "~/"+machine+": line 2") || len(r.writes) != 0 {
		t.Fatalf("err %v", err)
	}
}

func TestVSCodeSettingsSitePathQuoted(t *testing.T) {
	a, _, _ := testApp(t, vsCfg)
	r := &remote{t: t, files: map[string]string{machine: "{}", "site dir/it's.jsonc": "{}"}}
	f := r.install(a)
	if err := a.runVSCodeSettings(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, c := range f.Calls {
		if strings.HasSuffix(c.Args[len(c.Args)-1], ` _ 'site dir/it'\''s.jsonc'`) {
			seen = true
		}
	}
	if !seen {
		t.Errorf("site path not sent quoted and relative to home: %v", f.Calls)
	}
}

func TestVSCodeSettingsNeedsGateway(t *testing.T) {
	a, _, _ := testApp(t, "")
	if err := a.runVSCodeSettings(context.Background(), false, true); err == nil || !strings.Contains(err.Error(), "hpctun init") {
		t.Fatalf("err %v", err)
	}
}

func TestVSCodeSettingsScriptsAreTcshSafe(t *testing.T) {
	for name, sc := range map[string]string{"readScript": readScript, "writeScript": writeScript} {
		if strings.ContainsAny(sc, "'\n!") {
			t.Errorf("%s must be one line without a single quote or \"!\" (wrapped in sh -c '...' for the login shell)", name)
		}
	}
}

// TestVSCodeSettingsScriptsRun runs both scripts the way ssh does (through
// a login shell's -c) in a scratch directory standing in for the home (the
// working directory, as ssh commands start in the home), under each shell
// present. Never touches a host.
func TestVSCodeSettingsScriptsRun(t *testing.T) {
	for _, shell := range []string{"sh", "tcsh"} {
		if _, err := exec.LookPath(shell); err != nil {
			t.Logf("%s not installed; skipped", shell)
			continue
		}
		home := t.TempDir()
		run := func(script, stdin string, args ...string) (string, int) {
			cmd := exec.Command(shell, "-c", "sh -c '"+script+"' _ "+strings.Join(args, " "))
			cmd.Dir, cmd.Stdin = home, strings.NewReader(stdin)
			out, err := cmd.Output()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			return string(out), code
		}
		file := filepath.Join(home, machine)
		if _, code := run(readScript, "", ".vscode-server/nope"); code != 3 {
			t.Errorf("%s: read missing: exit %d", shell, code)
		}
		if out, code := run(writeScript, "one\n", "absent"); code != 0 || out != "" {
			t.Fatalf("%s: new file: exit %d, out %q", shell, code, out)
		}
		if err := os.Chmod(file, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, code := run(writeScript, "x", "deadbeef"); code != 3 {
			t.Errorf("%s: stale sha: exit %d", shell, code)
		}
		if b, _ := os.ReadFile(file); string(b) != "one\n" {
			t.Errorf("%s: stale write changed the file: %q", shell, b)
		}
		out, code := run(writeScript, "two\n", sum("one\n"))
		bak := strings.TrimSpace(out)
		if code != 0 || !strings.HasPrefix(bak, machine+".bak-") {
			t.Fatalf("%s: write: exit %d, out %q", shell, code, out)
		}
		if b, _ := os.ReadFile(file); string(b) != "two\n" {
			t.Errorf("%s: content %q", shell, b)
		}
		if b, _ := os.ReadFile(filepath.Join(home, bak)); string(b) != "one\n" {
			t.Errorf("%s: backup %q", shell, b)
		}
		if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v, want 0600", shell, st.Mode().Perm())
		}
		if _, code := run(writeScript, "x", "absent"); code != 3 {
			t.Errorf("%s: absent but exists: exit %d", shell, code)
		}
		if out, code := run(readScript, "", sshctl.ShellQuote(machine)); code != 0 || out != "two\n" {
			t.Errorf("%s: read: exit %d, out %q", shell, code, out)
		}
		left, _ := filepath.Glob(file + ".hpctun-*")
		if len(left) != 0 {
			t.Errorf("%s: temp files left: %v", shell, left)
		}
	}
}
