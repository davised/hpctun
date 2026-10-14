package main

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"hpctun/internal/tunnel"
)

// signIn checks that ssh signs in to alias without prompting, as tunnels
// on Windows must: they run with no terminal to prompt on.
func (a *app) signIn(ctx context.Context, alias string) error {
	res, err := a.runner().Run(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "--", alias, "true")
	if errors.Is(err, exec.ErrNotFound) {
		return tunnel.ErrNoSSH
	}
	if err != nil {
		return err
	}
	if res.ExitCode == 0 {
		return nil
	}
	msg := strings.TrimSpace(string(res.Stderr))
	if msg == "" {
		msg = "ssh exited " + strconv.Itoa(res.ExitCode)
	}
	if h := tunnel.SignInHint(msg, alias); h != "" {
		msg += "\n" + h
	}
	return errors.New(msg)
}

// indent prefixes every line of s.
func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}
