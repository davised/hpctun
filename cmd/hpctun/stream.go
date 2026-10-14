package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// streamExec is the real slurmjob.Exec: it runs argv, feeds it stdin,
// copies its stderr to stderr as it arrives and returns its stdout and exit
// status.
func (a *app) streamExec(ctx context.Context, argv []string, stdin string, stderr io.Writer) (string, int, error) {
	if a.verbose {
		slog.New(slog.NewTextHandler(a.stderr, &slog.HandlerOptions{Level: slog.LevelDebug})).
			Debug("exec", "cmd", strings.Join(argv, " "))
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, stderr
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return out.String(), -1, ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), ee.ExitCode(), nil
	}
	return out.String(), 0, err
}
