package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hpctun/internal/config"
	"hpctun/internal/slurmjob"
	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
)

const codeCfg = sshCfg + "\n[code]\n"

type remoteCall struct {
	argv  []string
	stdin string
}

func (c remoteCall) script() string { return c.argv[len(c.argv)-1] }

// fakeRemote stands in for a.stream: reply answers each remote script,
// except that the home folder (the folder check of ".") is /home/me.
type fakeRemote struct {
	calls []remoteCall
	reply func(ctx context.Context, c remoteCall, stderr io.Writer) (string, int, error)
}

func (f *fakeRemote) exec(ctx context.Context, argv []string, stdin string, stderr io.Writer) (string, int, error) {
	c := remoteCall{argv, stdin}
	f.calls = append(f.calls, c)
	if strings.Contains(c.script(), isPath) && stdin == ".\n" {
		return "/home/me\n", 0, nil
	}
	return f.reply(ctx, c, stderr)
}

func (f *fakeRemote) find(sub string) []remoteCall {
	var out []remoteCall
	for _, c := range f.calls {
		if strings.Contains(c.script(), sub) {
			out = append(out, c)
		}
	}
	return out
}

const (
	isEnsure       = "sbatch --test-only"
	isPath         = "IFS= read -r p; cd"
	isNodePath     = "--overlap"
	isWatch        = `printf "@log`
	isTunnelStatus = `grep "Tunnel:"`
)

// tunnelLog is the job log the default watcher reply streams.
const tunnelLog = "@log @cli code 1.140.0\n@log   ➜  Tunnel:   hpc-me\n"

type replyFunc = func(context.Context, remoteCall, io.Writer) (string, int, error)

// codeKeyProbe is the exact ssh -G-free probe codeLogin uses to check that
// the gateway lets the key in without going through a shared connection.
const codeKeyProbe = "ssh -o BatchMode=yes -o ConnectTimeout=10 -o ControlMaster=no -o ControlPath=none -- shell true"

// codeApp is testApp with codeCfg plus extra, a gateway that lets the key
// in, and remote scripts answered by reply.
func codeApp(t *testing.T, extra string, reply replyFunc) (*app, *fakeRemote, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	a, out, errb := testApp(t, codeCfg+extra)
	a.run = &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		switch c.Line() {
		case codeKeyProbe:
			return sshctl.Result{}, nil
		}
		t.Errorf("unexpected command: %s", c.Line())
		return sshctl.Result{ExitCode: 255}, nil
	}}
	f := &fakeRemote{reply: reply}
	a.stream = f.exec
	// Pre-record consent to the VS Code Server License Terms: the tests
	// below that do not exercise it then run unchanged.
	dir, _ := a.stateDir()
	os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(filepath.Join(dir, codeLicenseFile), []byte("accepted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return a, f, out, errb
}

// running answers the ensure script with job 42 running on chrom1, the
// watcher with a ready tunnel hpc-me, the tunnel status with hpc-me, and
// resolves folders under /home/me.
func running(submitted bool) replyFunc {
	return func(ctx context.Context, c remoteCall, stderr io.Writer) (string, int, error) {
		switch {
		case strings.Contains(c.script(), isEnsure):
			if submitted {
				io.WriteString(stderr, "@submitted 42|sbatch: Job 99 to start at 2026-09-24T13:29:56 a using 4 processors on nodes chrom1 in partition core\n")
			} else {
				io.WriteString(stderr, "@reuse 42\n")
			}
			return "42|RUNNING|chrom1|23:59:59|None\n", 0, nil
		case strings.Contains(c.script(), isWatch):
			io.WriteString(stderr, tunnelLog)
			<-ctx.Done()
			return "", 255, ctx.Err()
		case strings.Contains(c.script(), isTunnelStatus):
			return "  ➜  Tunnel:   hpc-me\n", 0, nil
		case strings.Contains(c.script(), isNodePath):
			fs := "nfs"
			if strings.HasPrefix(c.stdin, "/scratch/") || strings.HasPrefix(c.stdin, "/tmp/") {
				fs = "xfs"
			}
			return fs + "\n" + c.stdin, 0, nil
		case strings.Contains(c.script(), isPath):
			if p := strings.TrimSpace(c.stdin); strings.HasPrefix(p, "/") {
				return p + "\n", 0, nil
			}
			return "/home/me/" + strings.TrimSpace(c.stdin) + "\n", 0, nil
		}
		return "", 0, nil
	}
}

func contains(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
}

func TestCodeNeedsCodeTable(t *testing.T) {
	a, _, _ := testApp(t, sshCfg)
	err := runCmd(a, "code", "--status")
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "[code]") {
		t.Fatalf("err = %v", err)
	}
}

func TestCodeStartSubmits(t *testing.T) {
	a, f, out, _ := codeApp(t, "", running(true))
	if err := runCmd(a, "code", "--no-open"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e := f.find(isEnsure)
	if len(e) != 1 || !strings.HasPrefix(e[0].stdin, "ensure\nhpctun-code,tun-code\n4\n24:00:00\nnow+1470minutes\n\n\n\n@script\n#!/bin/sh\n") {
		t.Fatalf("ensure calls %+v", e)
	}
	if w := f.find(isWatch); len(w) != 1 || !strings.HasSuffix(w[0].script(), " _ 42 0") {
		t.Fatalf("watch calls %+v", w)
	}
	contains(t, out.String(), "submitted job 42 (4 CPUs, 24:00:00) on core", "job 42 running on chrom1",
		"Remote Explorer → Tunnels → hpc-me", "23:59:59 left", "hpctun code --stop",
		"For a web app on the node: start it on 127.0.0.1 there, then run hpctun up <port> --host chrom1 --loopback")
}

// TestCodeStartPassthroughGoesToStderr (M5): ssh's and sbatch's own lines
// (anything the ensure script's stderr writes that is not an "@kind" event)
// are pass-through, and must land in tun code's stderr, not its stdout.
func TestCodeStartPassthroughGoesToStderr(t *testing.T) {
	base := running(true)
	a, _, out, errb := codeApp(t, "", func(ctx context.Context, c remoteCall, stderr io.Writer) (string, int, error) {
		if strings.Contains(c.script(), isEnsure) {
			io.WriteString(stderr, "@submitted 42|x\nsbatch: a warning\n")
			return "42|RUNNING|chrom1|23:59:59|None\n", 0, nil
		}
		return base(ctx, c, stderr)
	})
	if err := runCmd(a, "code", "--no-open"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out.String(), "a warning") {
		t.Fatalf("passthrough went to stdout: %q", out.String())
	}
	contains(t, errb.String(), "a warning")
}

func TestCodeStartReuseNotesOptions(t *testing.T) {
	a, _, out, _ := codeApp(t, "", running(false))
	if err := runCmd(a, "code", "--no-open", "--cpus", "8"); err != nil {
		t.Fatal(err)
	}
	contains(t, out.String(), "using job 42 on chrom1 (options apply to new jobs only)")
}

func TestCodeStartOptionsAndPassthrough(t *testing.T) {
	a, f, _, _ := codeApp(t, "sbatch = [\"--qos=x\"]\nstart_within = \"30m\"\n", running(true))
	if err := runCmd(a, "code", "--no-open", "--cpus", "8", "--time", "2:00:00", "--node", "chrom1", "--partition", "core", "--", "--gres=gpu:1", "a b"); err != nil {
		t.Fatal(err)
	}
	want := "ensure\nhpctun-code,tun-code\n8\n2:00:00\nnow+150minutes\ncore\nchrom1\n\n--qos=x\n--gres=gpu:1\na b\n@script\n#!/bin/sh\n"
	if got := f.find(isEnsure)[0].stdin; !strings.HasPrefix(got, want) {
		t.Fatalf("stdin %q\nwant prefix %q", got, want)
	}
}

func TestCodeStartBadTime(t *testing.T) {
	a, _, _, _ := codeApp(t, "", running(true))
	if err := runCmd(a, "code", "--no-open", "--time", "soon"); exitCode(err) != 2 {
		t.Fatalf("err = %v", err)
	}
}

func TestCodeStartFolder(t *testing.T) {
	a, f, out, _ := codeApp(t, "", running(true))
	if err := runCmd(a, "code", "--no-open", "My Project#2"); err != nil {
		t.Fatal(err)
	}
	if p := f.find(isPath); len(p) != 1 || p[0].stdin != "My Project#2\n" {
		t.Fatalf("path calls %+v", p)
	}
	contains(t, out.String(), "then open /home/me/My Project#2")

	// The shell expanded ~ here: the path is taken as relative to the
	// cluster home.
	a, f, out, _ = codeApp(t, "", running(true))
	if err := runCmd(a, "code", "--no-open", filepath.Join(a.home(), "project")); err != nil {
		t.Fatal(err)
	}
	if p := f.find(isPath); p[0].stdin != "project\n" {
		t.Fatalf("stdin %q", p[0].stdin)
	}
	contains(t, out.String(), "using ~/project on the cluster")
}

func TestCodeStartMissingFolder(t *testing.T) {
	a, f, _, _ := codeApp(t, "", func(_ context.Context, c remoteCall, _ io.Writer) (string, int, error) {
		return "", 1, nil
	})
	err := runCmd(a, "code", "--no-open", "nope")
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "not a folder") || len(f.find(isEnsure)) != 0 {
		t.Fatalf("err = %v, calls %+v", err, f.calls)
	}
}

func TestCodeStartRefused(t *testing.T) {
	a, _, _, _ := codeApp(t, "", func(_ context.Context, _ remoteCall, stderr io.Writer) (string, int, error) {
		io.WriteString(stderr, "@error test|allocation failure: Requested nodes not in this partition \n")
		return "", 2, nil
	})
	err := runCmd(a, "code", "--no-open", "--node", "cqls-gpu3", "--partition", "core")
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "--node") {
		t.Fatalf("err = %v", err)
	}
}

func TestCodeStartNeedsKey(t *testing.T) {
	a, f, _, _ := codeApp(t, "", running(true))
	a.run = &sshctltest.Fake{Handler: func(sshctltest.Call) (sshctl.Result, error) {
		return sshctl.Result{ExitCode: 255}, nil
	}}
	err := runCmd(a, "code", "--no-open")
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "key") || len(f.calls) != 0 {
		t.Fatalf("err = %v, calls %d", err, len(f.calls))
	}
}

// TestCodeStartKeyPreflightIgnoresSharedMaster (I1): the old probe (no
// ControlMaster=no) succeeds, as it would through a live `tun login` master,
// but the fixed one fails, so a keyless user is still asked to set up a key.
func TestCodeStartKeyPreflightIgnoresSharedMaster(t *testing.T) {
	a, f, _, _ := codeApp(t, "", running(true))
	a.run = &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		switch c.Line() {
		case "ssh -o BatchMode=yes -o ConnectTimeout=10 -- shell true":
			return sshctl.Result{}, nil // the old, master-tolerant probe
		}
		return sshctl.Result{ExitCode: 255}, nil
	}}
	err := runCmd(a, "code", "--no-open")
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "key") || !strings.Contains(err.Error(), "hpctun ssh-config key") || len(f.calls) != 0 {
		t.Fatalf("err = %v, calls %d", err, len(f.calls))
	}
}

// TestCodeStartKeyPreflightOffersKeyInteractive (I1): interactively, a
// failed probe reaches offerKey.
func TestCodeStartKeyPreflightOffersKeyInteractive(t *testing.T) {
	a, f, out, _ := codeApp(t, "", running(true))
	a.run = &sshctltest.Fake{Handler: func(sshctltest.Call) (sshctl.Result, error) {
		return sshctl.Result{ExitCode: 255}, nil
	}}
	yes := true
	a.tty, a.ask = &yes, answerAsker{yes: false}
	err := runCmd(a, "code", "--no-open")
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "key") || len(f.calls) != 0 {
		t.Fatalf("err = %v, calls %d", err, len(f.calls))
	}
	contains(t, out.String(), "Later: `hpctun ssh-config key`")
}

func interrupted(submitted bool, cancel context.CancelFunc) replyFunc {
	return func(_ context.Context, c remoteCall, stderr io.Writer) (string, int, error) {
		if !strings.Contains(c.script(), isEnsure) {
			return "", 0, nil // scancel
		}
		if submitted {
			io.WriteString(stderr, "@submitted 42|x\n")
		} else {
			io.WriteString(stderr, "@reuse 42\n")
		}
		cancel()
		return "", -1, context.Canceled
	}
}

func runCodeCtx(ctx context.Context, a *app, args ...string) error {
	root := newRootCmd(a)
	root.SetArgs(append([]string{"code"}, args...))
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	return root.ExecuteContext(ctx)
}

func TestCodeInterruptCancelsOnlyItsOwnJob(t *testing.T) {
	for _, submitted := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		a, f, _, _ := codeApp(t, "", nil)
		f.reply = interrupted(submitted, cancel)
		err := runCodeCtx(ctx, a, "--no-open")
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("submitted=%v: err = %v", submitted, err)
		}
		if n := len(f.find("scancel 42")); (n == 1) != submitted {
			t.Fatalf("submitted=%v: %d scancel calls", submitted, n)
		}
	}
}

func TestCodeStatus(t *testing.T) {
	const run = "42|RUNNING|chrom1|3:00:00|None\n"
	for _, c := range []struct {
		out        string
		code       int
		tunnel     string // the tunnel status script's output
		tunnelCode int
		want       string
		not        string
	}{
		{"none\n", 3, "", 0, "no job running", "tunnel"},
		{run, 0, "  ➜  Tunnel:   hpc-me\n", 0, "job 42: running on chrom1, 3:00:00 left\ntunnel hpc-me: ready\n", ""},
		{run, 0, "", 1, "job 42: running on chrom1, 3:00:00 left\ntunnel: starting\n", ""},
		{run, 0, "", 3, "job 42: running on chrom1, 3:00:00 left\ntunnel: none (job started by an older hpctun; run hpctun code --stop, then hpctun code)\n", ""}, // no log: an older job
		{"42|PENDING||24:00:00|Priority\n", 0, "", 0, "job 42: pending (Priority)", "tunnel"},
	} {
		a, f, out, _ := codeApp(t, "", func(_ context.Context, rc remoteCall, _ io.Writer) (string, int, error) {
			if strings.Contains(rc.script(), isTunnelStatus) {
				return c.tunnel, c.tunnelCode, nil
			}
			return c.out, c.code, nil
		})
		if err := runCmd(a, "code", "--status"); err != nil {
			t.Fatal(err)
		}
		contains(t, out.String(), c.want)
		if c.not != "" && strings.Contains(out.String(), c.not) {
			t.Errorf("%q in\n%s", c.not, out.String())
		}
		if !strings.HasPrefix(f.calls[0].stdin, "find\nhpctun-code,tun-code\n") {
			t.Fatalf("stdin %q", f.calls[0].stdin)
		}
	}
}

func TestCodeStatusTakesNoJobOptions(t *testing.T) {
	a, _, _, _ := codeApp(t, "", running(true))
	if err := runCmd(a, "code", "--status", "--cpus", "2"); exitCode(err) != 2 {
		t.Fatalf("err = %v", err)
	}
}

func TestCodeStatusLine(t *testing.T) {
	a, f, _, _ := codeApp(t, "", func(context.Context, remoteCall, io.Writer) (string, int, error) {
		return "42|RUNNING|chrom1|23:59:59|None\n", 0, nil
	})
	cfg, _ := a.loadConfig()
	if got := a.codeStatus(context.Background(), cfg, false).String(); got != "code job: unknown (gateway not connected)" || len(f.calls) != 0 {
		t.Fatalf("got %q, calls %d", got, len(f.calls))
	}
	if got := a.codeStatus(context.Background(), cfg, true).String(); got != "code job 42: running on chrom1, 23:59:59 left" {
		t.Fatalf("got %q", got)
	}
	f.reply = func(context.Context, remoteCall, io.Writer) (string, int, error) { return "none\n", 3, nil }
	if got := a.codeStatus(context.Background(), cfg, true).String(); got != "code job: none" {
		t.Fatalf("got %q", got)
	}
}

func TestCodeRunsOnWindows(t *testing.T) {
	a, _, out, _ := codeApp(t, "", func(context.Context, remoteCall, io.Writer) (string, int, error) { return "none\n", 3, nil })
	a.goos = "windows"
	getenv := a.getenv
	a.getenv = func(k string) string {
		if k == "USERPROFILE" {
			return getenv("HOME")
		}
		return getenv(k)
	}
	if err := runCmd(a, "code", "--status"); err != nil {
		t.Fatal(err)
	}
	contains(t, out.String(), "no job running")
}

// watchReply answers the watcher with stderr, then waits for ctx (or
// returns code when wait is false).
func watchReply(stderr string, wait bool, code int) replyFunc {
	base := running(true)
	return func(ctx context.Context, c remoteCall, w io.Writer) (string, int, error) {
		if !strings.Contains(c.script(), isWatch) {
			return base(ctx, c, w)
		}
		io.WriteString(w, stderr)
		if wait {
			<-ctx.Done()
			return "", 255, ctx.Err()
		}
		return "", code, nil
	}
}

func TestCodeRelaysDeviceCode(t *testing.T) {
	a, _, out, _ := codeApp(t, "", watchReply("@log @login\n@log To sign in, use a web browser to open the page https://login.microsoft.com/device and enter the code AB12CD34 to authenticate.\n"+tunnelLog, true, 0))
	if err := runCmd(a, "code", "--no-open"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	contains(t, out.String(), "First time on chrom1: open https://login.microsoft.com/device and enter AB12CD34 (sign in with your ONID)")
}

func TestCodeNodeScriptErrors(t *testing.T) {
	for _, c := range []struct{ log, want string }{
		{"@log @error cli the download from update.code.visualstudio.com failed\n", "could not download VS Code's CLI on chrom1: the download from update.code.visualstudio.com failed"},
		{"@log @error arch ppc64le\n", "chrom1 is ppc64le; VS Code's CLI is available for x86_64 and aarch64"},
		{"@log @error login the sign-in did not finish\n", "the sign-in on chrom1 did not finish; run hpctun code again"},
		{"@log @error dir cannot create /x\n", "could not prepare chrom1 for VS Code: cannot create /x"},
	} {
		a, _, _, _ := codeApp(t, "", watchReply(c.log, true, 0))
		err := runCmd(a, "code", "--no-open")
		if exitCode(err) != 1 || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v", c.log, err)
		}
	}
}

func TestCodeTunnelTimeout(t *testing.T) {
	defer func(a, b time.Duration) { tunnelWait, tunnelWaitAfterCode = a, b }(tunnelWait, tunnelWaitAfterCode)
	tunnelWait = 50 * time.Millisecond
	var lines strings.Builder
	for i := 1; i <= 25; i++ {
		fmt.Fprintf(&lines, "@log line %d\n", i)
	}
	a, _, _, _ := codeApp(t, "", watchReply(lines.String(), true, 0))
	err := runCmd(a, "code", "--no-open")
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "did not start on chrom1 within") || !strings.Contains(err.Error(), "line 25") ||
		strings.Contains(err.Error(), "line 5\n") || !strings.Contains(err.Error(), "hpctun code --stop") {
		t.Fatalf("err = %v", err)
	}
}

// TestCodeWaitExtendsAfterCode: once a device code is shown the wait is
// tunnelWaitAfterCode, not tunnelWait.
func TestCodeWaitExtendsAfterCode(t *testing.T) {
	defer func(a, b time.Duration) { tunnelWait, tunnelWaitAfterCode = a, b }(tunnelWait, tunnelWaitAfterCode)
	tunnelWait, tunnelWaitAfterCode = 30*time.Millisecond, 5*time.Second
	base := running(true)
	a, _, out, _ := codeApp(t, "", func(ctx context.Context, c remoteCall, w io.Writer) (string, int, error) {
		if !strings.Contains(c.script(), isWatch) {
			return base(ctx, c, w)
		}
		io.WriteString(w, "@log To sign in, use a web browser to open the page https://login.microsoft.com/device and enter the code X1 to authenticate.\n")
		time.Sleep(100 * time.Millisecond) // longer than tunnelWait
		io.WriteString(w, tunnelLog)
		<-ctx.Done()
		return "", 255, ctx.Err()
	})
	if err := runCmd(a, "code", "--no-open"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// nologWatch answers the watcher as the real one would with no job log:
// "@nolog" after its poll limit, or never (limit 0) until ctx ends; the
// tunnel status says there is no log.
func nologWatch(submitted bool) replyFunc {
	base := running(submitted)
	return func(ctx context.Context, c remoteCall, w io.Writer) (string, int, error) {
		switch {
		case strings.Contains(c.script(), isWatch):
			if strings.HasSuffix(c.script(), " _ 42 0") {
				<-ctx.Done()
				return "", 255, ctx.Err()
			}
			io.WriteString(w, "@nolog\n")
			return "", 1, nil
		case strings.Contains(c.script(), isTunnelStatus):
			return "", 3, nil
		}
		return base(ctx, c, w)
	}
}

func TestCodeOldJobWithoutLog(t *testing.T) {
	a, f, _, _ := codeApp(t, "", nologWatch(false))
	err := runCmd(a, "code", "--no-open")
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "started by an older hpctun") || !strings.Contains(err.Error(), "hpctun code --stop") {
		t.Fatalf("err = %v", err)
	}
	if w := f.find(isWatch); len(w) != 1 || !strings.HasSuffix(w[0].script(), " _ 42 15") {
		t.Fatalf("watch calls %+v", w)
	}
}

// TestCodeSubmittedJobNeverNoLog: a job this run submitted whose log is
// slow to appear is watched with no poll limit, so it is never called an
// older hpctun's job; the wait's own timer ends it.
func TestCodeSubmittedJobNeverNoLog(t *testing.T) {
	defer func(a, b time.Duration) { tunnelWait, tunnelWaitAfterCode = a, b }(tunnelWait, tunnelWaitAfterCode)
	tunnelWait = 50 * time.Millisecond
	a, f, _, _ := codeApp(t, "", nologWatch(true))
	err := runCmd(a, "code", "--no-open")
	if exitCode(err) != 1 || strings.Contains(err.Error(), "older hpctun") || !strings.Contains(err.Error(), "did not start on chrom1 within") {
		t.Fatalf("err = %v", err)
	}
	if len(f.find(isTunnelStatus)) != 0 {
		t.Fatal("read the tunnel status of a job just submitted")
	}
}

// TestCodeReusedJobReadyOpensAtOnce: a reused job whose log already names
// the tunnel opens at once, without replaying the log (whose old device
// code would be shown again).
func TestCodeReusedJobReadyOpensAtOnce(t *testing.T) {
	base := running(false)
	a, f, out, _ := codeApp(t, "", func(ctx context.Context, c remoteCall, w io.Writer) (string, int, error) {
		if strings.Contains(c.script(), isWatch) {
			io.WriteString(w, "@log To sign in, use a web browser to open the page https://login.microsoft.com/device and enter the code OLD1 to authenticate.\n"+tunnelLog)
			<-ctx.Done()
			return "", 255, ctx.Err()
		}
		return base(ctx, c, w)
	})
	if err := runCmd(a, "code", "--no-open"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out.String(), "First time on") || len(f.find(isWatch)) != 0 {
		t.Fatalf("watched the log again:\n%s", out.String())
	}
	contains(t, out.String(), "Remote Explorer → Tunnels → hpc-me")
}

// TestCodeReusedJobStartingIsWatched: a reused job whose tunnel is not up
// yet (a sign-in still pending) is watched, and its code shown.
func TestCodeReusedJobStartingIsWatched(t *testing.T) {
	base := running(false)
	a, f, out, _ := codeApp(t, "", func(ctx context.Context, c remoteCall, w io.Writer) (string, int, error) {
		switch {
		case strings.Contains(c.script(), isTunnelStatus):
			return "", 1, nil
		case strings.Contains(c.script(), isWatch):
			io.WriteString(w, "@log To sign in, use a web browser to open the page https://login.microsoft.com/device and enter the code NOW1 to authenticate.\n"+tunnelLog)
			<-ctx.Done()
			return "", 255, ctx.Err()
		}
		return base(ctx, c, w)
	})
	if err := runCmd(a, "code", "--no-open"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if w := f.find(isWatch); len(w) != 1 || !strings.HasSuffix(w[0].script(), " _ 42 15") {
		t.Fatalf("watch calls %+v", w)
	}
	contains(t, out.String(), "enter NOW1")
}

// TestCodeRenamedWarning names the expected tunnel.
func TestCodeRenamedWarning(t *testing.T) {
	a, _, out, errb := codeApp(t, "", watchReply("@log [2026-10-06 04:36:25] info hpc-me is already taken, using a random name instead\n@log   ➜  Tunnel:   aspen1hpcoregonstate\n", true, 0))
	if err := runCmd(a, "code", "--no-open"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	contains(t, errb.String(), "warning: the tunnel name chrom1-me is taken", "tunnel record")
	if strings.Contains(errb.String(), "<you>") {
		t.Fatalf("placeholder in %q", errb.String())
	}
	contains(t, out.String(), "Tunnels → aspen1hpcoregonstate")
}

func TestCodeJobEndsWhileWaiting(t *testing.T) {
	a, _, _, _ := codeApp(t, "", watchReply("@gone FAILED\n", false, 1))
	if err := runCmd(a, "code", "--no-open"); exitCode(err) != 1 || !strings.Contains(err.Error(), "FAILED") {
		t.Fatalf("err = %v", err)
	}
}

// TestCodeInterruptWhileWaitingKeepsJob: Ctrl-C after the job runs cancels
// nothing.
func TestCodeInterruptWhileWaitingKeepsJob(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := running(true)
	a, f, _, _ := codeApp(t, "", func(c2 context.Context, c remoteCall, w io.Writer) (string, int, error) {
		if strings.Contains(c.script(), isWatch) {
			cancel()
			<-c2.Done()
			return "", 255, c2.Err()
		}
		return base(c2, c, w)
	})
	err := runCmdContext(ctx, a, "code", "--no-open")
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "job 42 keeps running") ||
		!strings.Contains(err.Error(), "`hpctun code` reconnects") || !strings.Contains(err.Error(), "`hpctun code --stop` ends it") {
		t.Fatalf("err = %v", err)
	}
	if len(f.find("scancel 42")) != 0 { // the ensure script itself holds "scancel $extra"
		t.Fatal("cancelled the running job")
	}
}

func TestCodeLicenseNoticeOnFirstRun(t *testing.T) {
	base := running(true)
	a, _, out, _ := codeApp(t, "", func(ctx context.Context, c remoteCall, w io.Writer) (string, int, error) {
		if strings.Contains(c.script(), isEnsure) {
			io.WriteString(w, "@new\n")
		}
		return base(ctx, c, w)
	})
	if err := runCmd(a, "code", "--no-open"); err != nil {
		t.Fatal(err)
	}
	contains(t, out.String(), "https://aka.ms/vscode-server-license", "--accept-server-license-terms")
}

func TestCodeLicenseNeedsAnswerWithoutTerminal(t *testing.T) {
	a, f, _, _ := codeApp(t, "", running(true))
	dir, _ := a.stateDir()
	os.Remove(filepath.Join(dir, codeLicenseFile))
	err := runCmd(a, "code", "--no-open")
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "VS Code Server License Terms") || len(f.calls) != 0 {
		t.Fatalf("err = %v, remote calls %d", err, len(f.calls))
	}
	if err := runCmd(a, "code", "--status"); err != nil && strings.Contains(err.Error(), "License") {
		t.Fatalf("--status asked for the license: %v", err)
	}
}

func TestCodeLicenseAskedOnceAndRecorded(t *testing.T) {
	a, _, out, _ := codeApp(t, "", running(true))
	dir, _ := a.stateDir()
	os.Remove(filepath.Join(dir, codeLicenseFile))
	yes := true
	a.tty, a.ask = &yes, answerAsker{yes: false}
	err := runCmd(a, "code", "--no-open")
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "nothing was started") {
		t.Fatalf("declined: %v", err)
	}
	contains(t, out.String(), "https://aka.ms/vscode-server-license", "https://privacy.microsoft.com/en-US/privacystatement", "dev tunnels")
	a.ask = answerAsker{yes: true}
	if err := runCmd(a, "code", "--no-open"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, codeLicenseFile)); !strings.HasPrefix(string(b), "accepted") {
		t.Fatalf("recorded %q", b)
	}
	a.ask = answerAsker{yes: false} // not asked again
	if err := runCmd(a, "code", "--no-open"); err != nil {
		t.Fatalf("asked again: %v", err)
	}
}

func TestCodeStop(t *testing.T) {
	a, f, out, _ := codeApp(t, "", running(true))
	dir, _ := a.stateDir()
	writeFile(t, filepath.Join(dir, "code-stopped"), "")
	if err := runCmd(a, "code", "--stop"); err != nil {
		t.Fatal(err)
	}
	if len(f.find("scancel")) != 1 || fileExists(filepath.Join(dir, "code-stopped")) {
		t.Fatalf("calls %+v", f.calls)
	}
	contains(t, out.String(), "cancelled your hpctun-code job")
}

func TestCodeWarnsObsoleteKeys(t *testing.T) {
	a, _, _, errb := codeApp(t, "alias = \"hpc-job\"\n", running(true))
	if err := runCmd(a, "code", "--no-open"); err != nil {
		t.Fatal(err)
	}
	contains(t, errb.String(), "warning: [code] alias is no longer used: hpctun code connects through VS Code's Remote - Tunnels; remove it from")
}

func TestCodeServerDirsReachNodeScript(t *testing.T) {
	a, f, _, _ := codeApp(t, "server_dirs = [\"/local/a\"]\n", running(true))
	if err := runCmd(a, "code", "--no-open"); err != nil {
		t.Fatal(err)
	}
	if e := f.find(isEnsure); len(e) != 1 || !strings.Contains(e[0].stdin, "\nserver_dirs=\"/local/a\"\n") {
		t.Fatalf("ensure stdin %q", e[0].stdin)
	}
}

func TestTunnelNameFor(t *testing.T) {
	if got := tunnelNameFor(&config.Config{SSH: &config.SSH{User: "me"}}, "chrom1"); got != "the tunnel name chrom1-me" {
		t.Errorf("got %q", got)
	}
	if got := tunnelNameFor(&config.Config{SSH: &config.SSH{User: "longusername"}}, "yatagarasu12"); got != "the tunnel name yatagarasu12-longuse" {
		t.Errorf("got %q", got)
	}
	if got := tunnelNameFor(&config.Config{}, "chrom1"); got != "this node's usual tunnel name" {
		t.Errorf("got %q", got)
	}
}

// TestCodeAccount: --account goes to the ensure script, an -A among the
// sbatch arguments is marked "-" so the script picks none, and the
// script's pick is reported.
func TestCodeAccount(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--account", "lpi"}, "\nhaughton\nlpi\n@script\n"},
		{[]string{"--", "-A", "lpi"}, "\nhaughton\n-\n-A\nlpi\n@script\n"},
		{[]string{"--", "--account=lpi"}, "\nhaughton\n-\n--account=lpi\n@script\n"},
		{nil, "\nhaughton\n\n@script\n"},
	}
	for _, c := range cases {
		a, f, _, _ := codeApp(t, "", running(true))
		if err := runCmd(a, append([]string{"code", "--no-open", "--node", "haughton"}, c.args...)...); err != nil {
			t.Fatalf("%q: %v", c.args, err)
		}
		if e := f.find(isEnsure); len(e) != 1 || !strings.Contains(e[0].stdin, c.want) {
			t.Errorf("%q: stdin %q", c.args, e[0].stdin[:min(len(e[0].stdin), 120)])
		}
	}
	if m := eventMessage(slurmjob.Event{Kind: "account", Account: "lpi", Partition: "lpi"}, slurmjob.Params{}, time.Now()); m != "using Slurm account lpi, which partition lpi takes (choose another with --account)" {
		t.Errorf("message %q", m)
	}
}

// TestCodeShortFlags: the job options take sbatch's short names too.
func TestCodeShortFlags(t *testing.T) {
	a, f, _, _ := codeApp(t, "", running(true))
	if err := runCmd(a, "code", "--no-open", "-c", "8", "-t", "2:00:00", "-p", "lpi", "-w", "haughton", "-A", "lpi"); err != nil {
		t.Fatal(err)
	}
	if e := f.find(isEnsure); len(e) != 1 || !strings.HasPrefix(e[0].stdin, "ensure\nhpctun-code,tun-code\n8\n2:00:00\nnow+150minutes\nlpi\nhaughton\nlpi\n@script\n") {
		t.Fatalf("ensure %q", e[0].stdin[:80])
	}
}
