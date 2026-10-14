// Package sshctl starts external processes and builds the ssh command lines
// hpctun uses. Runner is the only way hpctun starts a process.
package sshctl

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"hpctun/internal/detach"
)

// Result is a finished command. ExitCode is -1 if a signal ended the process.
type Result struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// Runner starts processes. Tests substitute a fake.
type Runner interface {
	// Run executes name with stdout and stderr captured. err is non-nil only
	// when the process could not be started or ctx ended; a non-zero exit
	// status is reported in Result.ExitCode.
	Run(ctx context.Context, name string, args ...string) (Result, error)
	// Background executes a command that may leave a process running after it
	// returns (ssh -f). Stdin is inherited so ssh can prompt through /dev/tty,
	// stdout is discarded, and stderr goes to logPath (truncated first), so the
	// leftover process holds none of hpctun's pipes. Result.Stderr is the log
	// file's contents.
	Background(ctx context.Context, logPath, name string, args ...string) (Result, error)
	// Start starts a process that outlives hpctun (a tunnel's ssh on
	// Windows, which has no ssh -f) and does not wait for it. Stdin and
	// stdout are the null device; stderr goes to logPath (truncated
	// first). It returns the pid. A missing program is exec.ErrNotFound.
	Start(ctx context.Context, logPath, name string, args ...string) (int, error)
}

// ExecRunner runs real processes. When Log is set, each command line is
// logged at debug level before it runs. Env is added to every process's
// environment (hpctun's askpass variables); with Env set, Background also
// sets HPCTUN_ASKPASS_LOG to its log path, so the askpass dialog can show
// what ssh wrote there.
type ExecRunner struct {
	Log *slog.Logger
	Env []string
}

func (r ExecRunner) env(extra ...string) []string {
	if len(r.Env) == 0 {
		return nil // inherit
	}
	return append(append(os.Environ(), r.Env...), extra...)
}

func (r ExecRunner) logCmd(name string, args []string) {
	if r.Log != nil {
		r.Log.Debug("exec", "cmd", strings.Join(append([]string{name}, args...), " "))
	}
}

func (r ExecRunner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	r.logCmd(name, args)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = r.env()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	// If the child leaves a grandchild holding our pipes, stop waiting after 2s.
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	return finish(ctx, Result{Stdout: out.Bytes(), Stderr: errb.Bytes()}, err)
}

func (r ExecRunner) Background(ctx context.Context, logPath, name string, args ...string) (Result, error) {
	r.logCmd(name, args)
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return Result{}, err
	}
	defer logf.Close()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = r.env("HPCTUN_ASKPASS_LOG=" + logPath)
	cmd.Stdin = os.Stdin
	cmd.Stderr = logf // an *os.File is handed to the child directly: no pipe
	runErr := cmd.Run()
	res := Result{}
	res.Stderr, _ = os.ReadFile(logPath)
	return finish(ctx, res, runErr)
}

func (r ExecRunner) Start(ctx context.Context, logPath, name string, args ...string) (int, error) {
	r.logCmd(name, args)
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	cmd, err := detach.Start(func() *exec.Cmd {
		// Not CommandContext: the process outlives ctx and hpctun.
		cmd := exec.Command(name, args...)
		cmd.Env = r.env("HPCTUN_ASKPASS_LOG=" + logPath)
		cmd.Stderr = logf
		return cmd
	})
	if err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	return pid, cmd.Process.Release()
}

func finish(ctx context.Context, res Result, err error) (Result, error) {
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, err
}
