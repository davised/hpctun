package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
)

const sinfoCanned = "chrom1|core|mix|64|(null)\nchrom1|ood_test|mix|64|(null)\ncqls-gpu3|cqls_gpu*|idle|48|gpu:a100:2\nneo|core|drain|64|(null)\n"

func sinfoReply(context.Context, remoteCall, io.Writer) (string, int, error) {
	return sinfoCanned, 0, nil
}

func TestCodeCompleteNodes(t *testing.T) {
	a, f, out, _ := codeApp(t, "", sinfoReply)
	if err := runCmd(a, "__complete", "code", "--node", "c"); err != nil {
		t.Fatal(err)
	}
	contains(t, out.String(), "chrom1\tcore, ood_test · mix · 64 CPUs\n", "cqls-gpu3\tcqls_gpu · idle · 48 CPUs · gpu:a100:2\n")
	if strings.Contains(out.String(), "neo") {
		t.Fatalf("offered a drained node:\n%s", out)
	}
	if !slices.Contains(f.calls[0].argv, "BatchMode=yes") {
		t.Fatalf("argv %q", f.calls[0].argv)
	}
	// Served from the cache the second time.
	if err := runCmd(a, "__complete", "code", "--node", ""); err != nil || len(f.calls) != 1 {
		t.Fatalf("err %v, calls %d", err, len(f.calls))
	}
	// A stale cache is refreshed.
	dir, _ := a.stateDir()
	old := time.Now().Add(-3 * time.Minute)
	os.Chtimes(filepath.Join(dir, "sinfo.cache"), old, old)
	runCmd(a, "__complete", "code", "--node", "")
	if len(f.calls) != 2 {
		t.Fatalf("calls %d", len(f.calls))
	}
}

func TestCodeCompletePartitions(t *testing.T) {
	a, _, out, _ := codeApp(t, "", sinfoReply)
	if err := runCmd(a, "__complete", "code", "--partition", ""); err != nil {
		t.Fatal(err)
	}
	contains(t, out.String(), "core\t1 node, 0 idle\n", "ood_test\t1 node, 0 idle\n", "cqls_gpu\t1 node, 1 idle\n")
}

func TestCodeCompleteFailureOffersNothing(t *testing.T) {
	a, _, out, _ := codeApp(t, "", func(context.Context, remoteCall, io.Writer) (string, int, error) { return "", 255, nil })
	if err := runCmd(a, "__complete", "code", "--node", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\t") {
		t.Fatalf("offered candidates:\n%s", out)
	}
}

// lsReply answers the folder listing on the gateway ("shell").
func lsReply(t *testing.T, a *app, lines map[string]string) *sshctltest.Fake {
	t.Helper()
	f := &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		if out, ok := lines[c.Line()]; ok {
			return sshctl.Result{Stdout: []byte(out)}, nil
		}
		t.Errorf("unexpected command: %s", c.Line())
		return sshctl.Result{ExitCode: 255}, nil
	}}
	a.run = f
	return f
}

// TestCodeCompleteFolder (tun-94l): the folder completes from the cluster
// home, directories only, as tun cp completes remote paths.
func TestCodeCompleteFolder(t *testing.T) {
	a, _, out, _ := codeApp(t, "", sinfoReply)
	lsReply(t, a, map[string]string{
		"ssh -o BatchMode=yes -o ConnectTimeout=5 -- shell ls -dp -- 'pro'*": "projects/\nprompt.txt\n",
		"ssh -o BatchMode=yes -o ConnectTimeout=5 -- shell ls -dp -- 'x'*":   "xdir/\n",
	})
	if err := runCmd(a, "__complete", "code", "pro"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.HasPrefix(got, "projects/\n:6\n") {
		t.Fatalf("got %q", got)
	}
	out.Reset()
	if err := runCmd(a, "__complete", "code", "~/x"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.HasPrefix(got, "~/xdir/\n:6\n") {
		t.Fatalf("got %q", got)
	}
}

// TestCodeCompleteFolderOnlyWhereOneFits: no listing once a folder is
// given, for sbatch arguments after --, or with --status or --stop, which
// take no folder. (The first word right after a bare
// -- is the one gap: cobra hides that --.)
func TestCodeCompleteFolderOnlyWhereOneFits(t *testing.T) {
	for _, args := range [][]string{
		{"projects", ""},
		{"projects", "--", "--gr"},
		{"--", "--gres=gpu:1", ""},
		{"--status", ""},
		{"--stop", ""},
	} {
		a, _, out, _ := codeApp(t, "", sinfoReply)
		f := lsReply(t, a, nil)
		if err := runCmd(a, append([]string{"__complete", "code"}, args...)...); err != nil {
			t.Fatal(err)
		}
		if len(f.Calls) != 0 || strings.Contains(out.String(), "/\n") {
			t.Errorf("%q: calls %v, out %q", args, f.Calls, out.String())
		}
	}
}
