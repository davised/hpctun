package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"hpctun/internal/change"
	"hpctun/internal/config"
	"hpctun/internal/gateway"
	"hpctun/internal/probe"
	"hpctun/internal/prompt"
	"hpctun/internal/sshctl"
	"hpctun/internal/state"
)

func newInitCmd(a *app) *cobra.Command {
	var from string
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a config: choose a gateway and define tunnels",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !a.interactive() {
				return usage(errors.New("hpctun init asks questions and needs a terminal"))
			}
			return runInit(cmd.Context(), a, from, force)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "start from this config file (for example a team config, or osu.toml from the release)")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing config")
	return cmd
}

func runInit(ctx context.Context, a *app, from string, force bool) error {
	path, err := a.configFile()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil && !force {
		return usage(fmt.Errorf("%s already exists; use `hpctun add` to add tunnels, or --force to start over", path))
	}
	var seed *config.NewConfig
	if from != "" {
		c, err := config.LoadSeed(from)
		if err != nil {
			return usage(err)
		}
		if !c.Exists {
			if wd, err := os.Getwd(); err == nil && !filepath.IsAbs(from) {
				return usage(fmt.Errorf("%s does not exist in %s; run this in the folder that holds it, or give its full path", from, wd))
			}
			return usage(fmt.Errorf("%s does not exist", from))
		}
		nc := config.FromConfig(c)
		seed = &nc
	}
	var seedCode *config.Code
	if seed != nil {
		seedCode = seed.Code
	}
	tested := map[string]bool{}
	dir, err := a.stateDir()
	if err != nil {
		return err
	}
	if err := state.EnsureDir(dir); err != nil {
		return failed(err)
	}
	snap, err := probe.Take(ctx, a.runner())
	if err != nil {
		return failed(err)
	}
	d := a.wizardDeps(ctx, "hpctun init", path, dir, false, seedCode)
	d.PortInUse = func(p int) bool { _, ok := snap.Listeners[p]; return ok }
	if !a.windows() {
		test := d.TestGateway
		d.TestGateway = func(dest string) error { tested[dest] = true; return test(dest) }
	}
	defer d.Asker.Close()
	nc, approved, err := prompt.Wizard(d, seed)
	d.Asker.Close() // back to the shell before anything is printed
	if errors.Is(err, prompt.ErrAborted) {
		return failed(errors.New("config not written"))
	}
	if err != nil {
		return failed(err)
	}
	if nc.SSH != nil {
		err := a.applyWizard(approved, func() ([]change.Change, error) {
			return a.sshChanges(ctx, nc, path)
		})
		switch {
		case errors.Is(err, errChangesDeclined):
			// Nothing was defined (e.g. transfer set to an existing alias):
			// the only possible change here is clearing a leftover
			// hpctun_config, and declining that is not a reason to fail init;
			// leave the file as it is.
		case err != nil:
			// The answers are worth keeping: config.toml records the [ssh]
			// table, from which sync can write the ssh files later.
			fmt.Fprintf(a.stderr, "warning: the ssh setup was not written: %v\nRun `hpctun ssh-config sync` to write it.\n", err)
		}
	}
	if err := config.WriteNew(path, nc, force); err != nil {
		return usage(err)
	}
	fmt.Fprintf(a.stdout, "%s\nwrote %s\n", config.Render(nc), path)
	a.closeTested(ctx, dir, tested, nc.Gateway)
	if nc.SSH != nil {
		if cfg, err := config.Load(path); err == nil {
			_ = a.reportSSH(ctx, cfg) // findings are printed; a problem does not undo init
		}
	}
	if err := a.offerKey(ctx, nc.Gateway); err != nil {
		return failed(err)
	}
	if a.windows() {
		fmt.Fprintf(a.stdout, "Try `ssh %s`.\n", nc.Gateway)
	} else {
		fmt.Fprintln(a.stdout, "Tab completion: see `hpctun completion --help` (zsh also needs `autoload -Uz compinit && compinit` in ~/.zshrc).")
	}
	if len(nc.Tunnels) == 0 {
		fmt.Fprintln(a.stdout, "Add tunnels with `hpctun add`, or open one without saving it with `hpctun up <port> --host <host>`.")
		return nil
	}
	if a.windows() {
		dest := cmp.Or(nc.TunnelGateway, nc.Gateway)
		if err := a.signIn(ctx, dest); err != nil {
			fmt.Fprintf(a.stdout, "Tunnels cannot open yet: signing in to %s needs a prompt.\n%s\nThen `hpctun up --all` opens them.\n", dest, err)
			return nil
		}
	}
	open, err := a.asker("", false).Confirm("Open all tunnels now?", "Later, `hpctun up --all` opens them.", false)
	if err != nil || !open {
		return nil
	}
	return runUp(ctx, a, nil, true, adhocFlags{}, "", true, false)
}

// closeTested closes managed masters the connection test opened for
// gateways the finished config does not use (a typed host that became an
// alias), so no second connection lingers.
func (a *app) closeTested(ctx context.Context, dir string, tested map[string]bool, keep string) {
	r := a.runner()
	for dest := range tested {
		if dest == keep {
			continue
		}
		g, err := gateway.Resolve(ctx, r, dest, dir)
		if err != nil || g.Mode != gateway.ModeManaged {
			continue
		}
		r.Run(ctx, "ssh", sshctl.ExitArgs(g.GW)...)
	}
}
