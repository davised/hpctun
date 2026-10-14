package sshctl

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	res, err := ExecRunner{}.Run(context.Background(), "sh", "-c", "echo out; echo err >&2; exit 3")
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "out\n" || string(res.Stderr) != "err\n" || res.ExitCode != 3 {
		t.Fatalf("got %+v", res)
	}
}

func TestRunMissingBinary(t *testing.T) {
	if _, err := (ExecRunner{}).Run(context.Background(), "tun-no-such-binary"); err == nil {
		t.Fatal("expected an error for a missing binary")
	}
}

// Review Focus 1: a leftover background process must not keep tun waiting.
func TestBackgroundDoesNotWaitForLeftoverProcess(t *testing.T) {
	log := filepath.Join(t.TempDir(), "bg.log")
	start := time.Now()
	res, err := ExecRunner{}.Background(context.Background(), log, "sh", "-c", "sleep 5 & echo oops >&2; exit 4")
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Background waited %v for the leftover sleep", d)
	}
	if res.ExitCode != 4 || string(res.Stderr) != "oops\n" {
		t.Fatalf("got %+v", res)
	}
	data, _ := os.ReadFile(log)
	if string(data) != "oops\n" {
		t.Fatalf("log file = %q", data)
	}
}

func TestBackgroundTruncatesLog(t *testing.T) {
	log := filepath.Join(t.TempDir(), "bg.log")
	if err := os.WriteFile(log, []byte("old content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ExecRunner{}.Background(context.Background(), log, "true")
	if err != nil || len(res.Stderr) != 0 {
		t.Fatalf("got %+v, %v; want empty stderr", res, err)
	}
}

// Env reaches both kinds of process, Background adds the log path, and the
// rest of the environment is kept.
func TestExecRunnerEnv(t *testing.T) {
	t.Setenv("TUN_KEEP", "kept")
	r := ExecRunner{Env: []string{"SSH_ASKPASS=/x/askpass.sh", "SSH_ASKPASS_REQUIRE=force"}}
	res, err := r.Run(context.Background(), "sh", "-c", `echo "$SSH_ASKPASS $SSH_ASKPASS_REQUIRE $TUN_KEEP $HPCTUN_ASKPASS_LOG"`)
	if err != nil || string(res.Stdout) != "/x/askpass.sh force kept \n" {
		t.Fatalf("Run: %q, %v", res.Stdout, err)
	}
	log := filepath.Join(t.TempDir(), "bg.log")
	res, err = r.Background(context.Background(), log, "sh", "-c", `echo "$SSH_ASKPASS_REQUIRE $HPCTUN_ASKPASS_LOG" >&2`)
	if err != nil || string(res.Stderr) != "force "+log+"\n" {
		t.Fatalf("Background: %q, %v", res.Stderr, err)
	}
	res, err = ExecRunner{}.Run(context.Background(), "sh", "-c", `echo "${SSH_ASKPASS_REQUIRE-unset}"`)
	if err != nil || string(res.Stdout) != "unset\n" {
		t.Fatalf("no Env: %q, %v", res.Stdout, err)
	}
}

func TestExecRunnerStart(t *testing.T) {
	log := filepath.Join(t.TempDir(), "start.log")
	pid, err := ExecRunner{}.Start(context.Background(), log, "sh", "-c", "echo to-stderr >&2")
	if err != nil || pid <= 0 {
		t.Fatalf("pid %d, err %v", pid, err)
	}
	// The child runs on its own; wait for it to write its stderr.
	for range 100 {
		if b, _ := os.ReadFile(log); strings.Contains(string(b), "to-stderr") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	b, _ := os.ReadFile(log)
	t.Fatalf("log = %q", b)
}

func TestExecRunnerStartMissing(t *testing.T) {
	_, err := ExecRunner{}.Start(context.Background(), filepath.Join(t.TempDir(), "l"), "hpctun-no-such-program")
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("err = %v, want exec.ErrNotFound", err)
	}
}
