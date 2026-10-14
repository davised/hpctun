package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"hpctun/internal/config"
	"hpctun/internal/sshkey"
)

// runInteractive runs a program that prompts on the terminal (ssh-keygen,
// ssh-add, ssh asking for a password) and waits for it. stdin nil means the
// terminal's.
func (a *app) runInteractive(argv []string, stdin io.Reader) error {
	if a.interact != nil {
		return a.interact(argv, stdin)
	}
	if stdin == nil {
		stdin = os.Stdin
	}
	return runChild(argv, stdin, os.Stdout, os.Stderr)
}

// keyFlow sets up key login to host with ~/.ssh/id_ed25519; gateway is the
// configured gateway alias.
func (a *app) keyFlow(host, gateway string) *sshkey.Flow {
	key := filepath.Join(a.home(), ".ssh", "id_ed25519")
	computer, _ := os.Hostname()
	return &sshkey.Flow{
		Key: key, Show: a.tilde(key), Host: host, Gateway: host == gateway,
		Comment:  fmt.Sprintf("%s@%s (hpctun)", cmp.Or(a.localUser(), "me"), cmp.Or(computer, "computer")),
		Keychain: a.mac() && fileExists(sshkey.AppleSSHAdd), Windows: a.windows(),
		Runner: a.runner(), Interact: a.runInteractive, Out: a.stdout,
	}
}

func runSSHConfigKey(ctx context.Context, a *app, args []string) error {
	if !a.interactive() {
		return usage(errors.New("hpctun ssh-config key asks for passphrases and passwords and needs a terminal"))
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	host := cfg.Gateway
	if len(args) > 0 {
		host = args[0]
	}
	if host == "" {
		return usage(errNoGateway)
	}
	if err := config.ValidateDest(host); err != nil {
		return usage(err)
	}
	// A node shares the gateway's home, and its test login would go through
	// ProxyJump, which does not pass on BatchMode: the gateway could prompt.
	if cfg.SSH != nil && host != cfg.Gateway {
		if in, err := a.sshInput(ctx, cfg); err == nil && in.IsNode(host) {
			return usage(fmt.Errorf("%s shares its home directory with %s; run `hpctun ssh-config key` to set up the key there", host, cfg.Gateway))
		}
	}
	if err := a.keyFlow(host, cfg.Gateway).Run(ctx); err != nil {
		return failed(err)
	}
	return nil
}

// offerKey ends init and setup: if gateway does not accept a key yet, offer
// to set one up. A failure is only a warning, since the config is already
// written; the error it returns is Ctrl-C (ctx cancelled), which should end
// the command.
func (a *app) offerKey(ctx context.Context, gateway string) error {
	if gateway == "" || !a.interactive() || ctx.Err() != nil {
		return ctx.Err()
	}
	f := a.keyFlow(gateway, gateway)
	if _, err := os.Stat(f.Key); err == nil && f.Accepted(ctx) {
		fmt.Fprintf(a.stdout, "%s already lets you log in with a key.\n", gateway)
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err // Ctrl-C during the test login
	}
	ok, err := a.asker("", false).Confirm("Set up an ssh key for "+gateway+" now?",
		"`hpctun ssh-config key` creates one if needed, installs it on the gateway and keeps it in the ssh agent.", true)
	if err != nil || !ok {
		fmt.Fprintln(a.stdout, "Later: `hpctun ssh-config key`")
		return nil
	}
	if err := f.Run(ctx); err != nil {
		fmt.Fprintf(a.stderr, "warning: %v\n", err)
	}
	return ctx.Err()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
