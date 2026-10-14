// Package tray is the logic of hpctun-tray, the menu-bar app: it runs the
// hpctun CLI with --json and turns what it says into a menu. The menu
// itself is drawn by cmd/hpctun-tray.
package tray

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"hpctun/internal/jsondoc"
)

// CLI runs the hpctun command line. A non-zero exit is reported in exit,
// not err; err is for a CLI that could not be started.
type CLI interface {
	Run(ctx context.Context, args ...string) (stdout, stderr []byte, exit int, err error)
}

// ExecCLI runs the hpctun binary at Path. Its stdin is empty, so ssh can
// only ask through --askpass.
type ExecCLI struct{ Path string }

func (c ExecCLI) Run(ctx context.Context, args ...string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, c.Path, args...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	ownGroup(cmd)
	// A process that escaped the kill may still hold stdout open; stop
	// waiting for it.
	cmd.WaitDelay = 5 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return out.Bytes(), errb.Bytes(), -1, fmt.Errorf("hpctun %s did not finish in time", args[0])
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.Bytes(), errb.Bytes(), ee.ExitCode(), nil
	}
	return out.Bytes(), errb.Bytes(), 0, err
}

// FindCLI returns the hpctun next to the tray (exe is the tray's own
// path; in a macOS bundle both sit in Contents/MacOS), else the one on
// PATH.
func FindCLI(exe string, lookPath func(string) (string, error)) (string, error) {
	name := "hpctun"
	if strings.HasSuffix(strings.ToLower(exe), ".exe") {
		name += ".exe"
	}
	if exe != "" {
		p := filepath.Join(filepath.Dir(exe), name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	p, err := lookPath(name)
	if err != nil {
		return "", fmt.Errorf("hpctun-tray needs hpctun next to it or on your PATH: %w", err)
	}
	return p, nil
}

// errorOf returns the failure a --json run reports: its error document,
// else the message hpctun wrote to stderr, else the exit status. It is nil
// when the run succeeded.
func errorOf(stdout, stderr []byte, exit int) error {
	var doc struct {
		Error *jsondoc.ErrorBody `json:"error"`
	}
	if json.Unmarshal(stdout, &doc) == nil && doc.Error != nil {
		return errors.New(doc.Error.Message)
	}
	if exit == 0 {
		return nil
	}
	if msg := message(stderr); msg != "" {
		return errors.New(msg)
	}
	return fmt.Errorf("hpctun exited %d", exit)
}

// message is hpctun's error text from stderr without fang's box around it:
// the "ERROR" title, blank lines and the usage hint go, and the rest joins
// into one line.
func message(stderr []byte) string {
	var words []string
	for line := range strings.SplitSeq(string(stderr), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "ERROR" || strings.HasPrefix(line, "Try --help") {
			continue
		}
		words = append(words, line)
	}
	return strings.Join(words, " ")
}

// Status runs `hpctun status --json`, with --no-code unless code is set.
func Status(ctx context.Context, cli CLI, code bool) (*jsondoc.Status, error) {
	args := []string{"status", "--json"}
	if !code {
		args = append(args, "--no-code")
	}
	out, errb, exit, err := cli.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	if err := errorOf(out, errb, exit); err != nil {
		return nil, err
	}
	var st jsondoc.Status
	if err := json.Unmarshal(out, &st); err != nil {
		return nil, fmt.Errorf("hpctun status --json: %w", err)
	}
	return &st, nil
}

// Results runs an up or down command with --json. A run where some
// tunnels failed returns its results and no error: the failures are in
// the results.
func Results(ctx context.Context, cli CLI, args ...string) (*jsondoc.Results, error) {
	out, errb, exit, err := cli.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var res jsondoc.Results
	if json.Unmarshal(out, &res) == nil && res.Results != nil {
		return &res, nil
	}
	if err := errorOf(out, errb, exit); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("hpctun %s: no results", strings.Join(args, " "))
}

// Plain runs a command that has no --json (login, logout) and returns its
// failure, if any.
func Plain(ctx context.Context, cli CLI, args ...string) error {
	out, errb, exit, err := cli.Run(ctx, args...)
	if err != nil {
		return err
	}
	return errorOf(out, errb, exit)
}
