package slurmjob

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLogLine(t *testing.T) {
	cases := []struct {
		line string
		want LogEvent
	}{
		{"@login", LogEvent{Kind: "login"}},
		{"To sign in, use a web browser to open the page https://login.microsoft.com/device and enter the code BCACEB4AK to authenticate.",
			LogEvent{Kind: "code", URL: "https://login.microsoft.com/device", Code: "BCACEB4AK"}},
		{"To sign in, use a web browser to open the page https://login.microsoft.com/device and enter the code BCACEB4AK to authenticate.\r",
			LogEvent{Kind: "code", URL: "https://login.microsoft.com/device", Code: "BCACEB4AK"}},
		{"To grant access to the server, please log into https://github.com/login/device and use code C683-B903",
			LogEvent{Kind: "code", URL: "https://github.com/login/device", Code: "C683-B903"}},
		{"  ➜  Tunnel:   hpc-davised", LogEvent{Kind: "ready", Name: "hpc-davised"}},
		{"  ➜  Tunnel:   aspen1hpcoregonstate", LogEvent{Kind: "ready", Name: "aspen1hpcoregonstate"}},
		{"[2026-10-06 04:36:25] info hpc-me is already taken, using a random name instead", LogEvent{Kind: "renamed"}},
		{"@error cli the download from update.code.visualstudio.com failed", LogEvent{Kind: "error", What: "cli", Reason: "the download from update.code.visualstudio.com failed"}},
		{"@error login the sign-in did not finish", LogEvent{Kind: "error", What: "login", Reason: "the sign-in did not finish"}},
		{"@warn cli-refresh the download failed; using the CLI already there", LogEvent{Kind: "warn", What: "cli-refresh", Reason: "the download failed; using the CLI already there"}},
		{"@cli code 1.140.0 (commit 07f8)", LogEvent{Kind: "cli", Text: "code 1.140.0 (commit 07f8)"}},
		{"@cache /scratch/me/hpctun-vscode", LogEvent{Kind: "cache", Text: "/scratch/me/hpctun-vscode"}},
		{"@step download", LogEvent{Kind: "step", Text: "download"}},
		{"* Visual Studio Code Server", LogEvent{Text: "* Visual Studio Code Server"}},
		{"  ➜  Tunnel:   hpc-me;rm", LogEvent{Text: "  ➜  Tunnel:   hpc-me;rm"}},
		{"  ➜  Tunnel:   ../x/y", LogEvent{Text: "  ➜  Tunnel:   ../x/y"}},
		{"  ➜  Tunnel:   a.b_c-9 ", LogEvent{Kind: "ready", Name: "a.b_c-9"}},
	}
	for _, c := range cases {
		if got := ParseLogLine(c.line); got != c.want {
			t.Errorf("%q: got %+v, want %+v", c.line, got, c.want)
		}
	}
}

func TestWatchScriptShape(t *testing.T) {
	for _, s := range []string{WatchScript, tunnelStatusScript} {
		for _, bad := range []string{"'", "\n", "!"} {
			if strings.Contains(s, bad) {
				t.Errorf("script contains %q", bad)
			}
		}
	}
}

// watchLog writes the job log for job 42 under the world's home.
func (w *world) watchLog(s string) string {
	p := filepath.Join(w.dir, ".local/state/hpctun/vscode/job-42.log")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(s), 0o644)
	return p
}

func TestWatchScriptForwardsLinesAndEndsWhenJobGone(t *testing.T) {
	w := newWorld(t)
	w.watchLog("@cli code 1.140.0\n  ➜  Tunnel:   hpc-me\n")
	old := filepath.Join(w.dir, ".local/state/hpctun/vscode/job-7.log")
	os.WriteFile(old, []byte("x\n"), 0o644)
	// squeue: polls 5 and 10 see RUNNING, 15 sees nothing; sacct says COMPLETED
	w.reply("squeue", 1, "RUNNING\n", "", 0)
	w.reply("squeue", 2, "RUNNING\n", "", 0)
	w.reply("squeue", 3, "", "", 0)
	w.reply("sacct", 1, "COMPLETED\n", "", 0)
	_, errs, code := w.runArgv([]string{"sh", "-c", WatchScript, "_", "42", "15"}, nil)
	if code != 1 || !strings.Contains(errs, "@log @cli code 1.140.0\n@log   ➜  Tunnel:   hpc-me\n") || !strings.HasSuffix(errs, "@gone COMPLETED\n") {
		t.Fatalf("code %d err %q", code, errs)
	}
	if strings.Count(errs, "@log") != 2 {
		t.Fatalf("lines forwarded more than once: %q", errs)
	}
	if fileExistsT(old) {
		t.Fatal("old job log kept")
	}
}

func TestWatchScriptCompleteLinesOnly(t *testing.T) {
	w := newWorld(t)
	w.watchLog("first\nhalf a li")
	w.reply("squeue", 1, "", "", 0)
	w.reply("sacct", 1, "CANCELLED by 1\n", "", 0)
	_, errs, _ := w.runArgv([]string{"sh", "-c", WatchScript, "_", "42", "15"}, nil)
	if !strings.Contains(errs, "@log first\n") || strings.Contains(errs, "half") {
		t.Fatalf("err %q", errs)
	}
}

func TestWatchScriptNoLog(t *testing.T) {
	w := newWorld(t)
	for i := 1; i <= 3; i++ {
		w.reply("squeue", i, "RUNNING\n", "", 0)
	}
	_, errs, code := w.runArgv([]string{"sh", "-c", WatchScript, "_", "42", "15"}, nil)
	if code != 1 || !strings.HasSuffix(errs, "@nolog\n") {
		t.Fatalf("code %d err %q", code, errs)
	}
}

// TestWatchScriptNoLogLimitZero: for a job hpctun just submitted the log
// may be slow to appear (NFS, a slow prolog); limit 0 never reports @nolog,
// and the watch ends only when the job does.
func TestWatchScriptNoLogLimitZero(t *testing.T) {
	w := newWorld(t)
	for i := 1; i <= 4; i++ {
		w.reply("squeue", i, "RUNNING\n", "", 0)
	}
	w.reply("squeue", 5, "", "", 0)
	w.reply("sacct", 1, "CANCELLED by 1\n", "", 0)
	_, errs, code := w.runArgv([]string{"sh", "-c", WatchScript, "_", "42", "0"}, nil)
	if code != 1 || strings.Contains(errs, "@nolog") || !strings.HasSuffix(errs, "@gone CANCELLED by 1\n") {
		t.Fatalf("code %d err %q", code, errs)
	}
}

func TestWatchScriptSqueueHiccupIsNotGone(t *testing.T) {
	w := newWorld(t)
	w.watchLog("x\n")
	w.reply("squeue", 1, "", "", 0) // one empty squeue
	w.reply("sacct", 1, "RUNNING\n", "", 0)
	w.reply("squeue", 2, "", "", 0)
	w.reply("sacct", 2, "TIMEOUT\n", "", 0)
	_, errs, _ := w.runArgv([]string{"sh", "-c", WatchScript, "_", "42", "15"}, nil)
	if strings.Count(errs, "@gone") != 1 || !strings.HasSuffix(errs, "@gone TIMEOUT\n") {
		t.Fatalf("err %q", errs)
	}
}

func TestWatchStopsWhenReady(t *testing.T) {
	var argv []string
	c := Client{Gateway: "shell", Multiplexing: true, Exec: func(ctx context.Context, a []string, _ string, stderr io.Writer) (string, int, error) {
		argv = a
		io.WriteString(stderr, "@log @login\n@log To sign in, use a web browser to open the page https://login.microsoft.com/device and enter the code AB12 to authenticate.\n@alive\n@log   ➜  Tunnel:   hpc-me\n")
		<-ctx.Done() // the real ssh runs until killed
		return "", 255, ctx.Err()
	}}
	var kinds []string
	err := c.Watch(context.Background(), "42", 0, func(e LogEvent) bool { kinds = append(kinds, e.Kind); return e.Kind == "ready" }, io.Discard)
	if err != nil || strings.Join(kinds, ",") != "login,code,ready" {
		t.Fatalf("err %v kinds %v", err, kinds)
	}
	if !strings.Contains(strings.Join(argv, " "), "ControlPath=none") || argv[len(argv)-1] != "sh -c '"+WatchScript+"' _ 42 0" {
		t.Fatalf("argv %q", argv)
	}
}

func TestWatchEnds(t *testing.T) {
	for _, c := range []struct {
		stderr string
		check  func(error) bool
	}{
		{"@gone CANCELLED\n", func(err error) bool { var g *GoneError; return errors.As(err, &g) && g.State == "CANCELLED" }},
		{"@lost UNKNOWN\n", func(err error) bool { return err != nil && strings.Contains(err.Error(), "may still be queued") }},
		{"@nolog\n", func(err error) bool { return errors.Is(err, ErrNoLog) }},
	} {
		cl := Client{Gateway: "shell", Exec: func(_ context.Context, _ []string, _ string, stderr io.Writer) (string, int, error) {
			io.WriteString(stderr, c.stderr)
			return "", 1, nil
		}}
		if err := cl.Watch(context.Background(), "42", 15, func(LogEvent) bool { return false }, io.Discard); !c.check(err) {
			t.Errorf("%q: err %v", c.stderr, err)
		}
	}
}

func TestTunnelName(t *testing.T) {
	reply := func(out string, code int) Client {
		return Client{Gateway: "shell", Exec: func(context.Context, []string, string, io.Writer) (string, int, error) { return out, code, nil }}
	}
	if n, ok, err := reply("  ➜  Tunnel:   hpc-me\n", 0).TunnelName(context.Background(), "42"); err != nil || !ok || n != "hpc-me" {
		t.Fatalf("%q %v %v", n, ok, err)
	}
	if _, ok, err := reply("", 0).TunnelName(context.Background(), "42"); err != nil || ok {
		t.Fatalf("starting: %v %v", ok, err)
	}
	if _, _, err := reply("", 3).TunnelName(context.Background(), "42"); !errors.Is(err, ErrNoLog) {
		t.Fatalf("no log: %v", err)
	}
}
