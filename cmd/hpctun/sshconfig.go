package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"hpctun/internal/change"
	"hpctun/internal/config"
	"hpctun/internal/prompt"
	"hpctun/internal/sshsetup"
)

func newSSHConfigCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh-config",
		Short: "Show the ssh setup hpctun writes to ~/.ssh/hpctun_config, and check it",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return runSSHConfigShow(cmd.Context(), a) },
	}
	var yes bool
	sync := &cobra.Command{
		Use:   "sync",
		Short: "Show the changes that bring ~/.ssh up to date with [ssh], and apply them once approved",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return runSSHConfigSync(cmd.Context(), a, yes) },
	}
	sync.Flags().BoolVar(&yes, "yes", false, "apply the changes without asking")
	setup := &cobra.Command{
		Use:   "setup",
		Short: "Answer the ssh setup questions from hpctun init, review the changes, and apply them",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return runSSHConfigSetup(cmd.Context(), a) },
	}
	key := &cobra.Command{
		Use:   "key [host]",
		Short: "Create an ssh key if needed, keep it in the ssh agent, and install it on the gateway",
		Args:  cobra.MaximumNArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return runSSHConfigKey(cmd.Context(), a, args) },
	}
	cmd.AddCommand(sync, setup, key)
	return cmd
}

func runSSHConfigShow(ctx context.Context, a *app) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if !cfg.Exists {
		fmt.Fprintf(a.stdout, "%s does not exist yet; run `hpctun init` to set up hpctun.\n", a.tilde(cfg.Path))
		return nil
	}
	if cfg.SSH == nil {
		fmt.Fprintf(a.stdout, "%s has no [ssh] table, so hpctun writes no ssh config.\nRun `hpctun ssh-config setup` to set it up.\n", a.tilde(cfg.Path))
		return nil
	}
	p := a.sshPaths()
	in, err := a.sshInput(ctx, cfg)
	if err != nil {
		return failed(err)
	}
	fmt.Fprintf(a.stdout, "%s, generated from %s:\n\n%s\n", a.tilde(p.TunConfig), a.tilde(cfg.Path), sshsetup.Generate(in))
	return a.reportSSH(ctx, cfg)
}

func runSSHConfigSync(ctx context.Context, a *app, yes bool) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if cfg.SSH == nil {
		return usage(fmt.Errorf("%s has no [ssh] table; run `hpctun ssh-config setup`", a.tilde(cfg.Path)))
	}
	cs, err := a.sshChanges(ctx, config.FromConfig(cfg), cfg.Path)
	if err != nil {
		return failed(err)
	}
	// sync is the one command whose whole job is bringing ~/.ssh up to date,
	// so it alone announces having nothing to do; proposeAndApply stays
	// silent so callers earlier in a flow (init, setup) do not print it too.
	if diffs(cs) == "" {
		created, err := a.restoreSockets(cfg.SSH)
		if err != nil {
			return err
		}
		if !created {
			fmt.Fprintln(a.stdout, "ssh config is up to date")
		}
	} else if err := a.proposeAndApply(cs, yes); err != nil {
		return err
	}
	return a.reportSSH(ctx, cfg)
}

func runSSHConfigSetup(ctx context.Context, a *app) error {
	if !a.interactive() {
		return usage(errors.New("hpctun ssh-config setup asks questions and needs a terminal"))
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if !cfg.Exists {
		return usage(fmt.Errorf("%s does not exist; run `hpctun init`", a.tilde(cfg.Path)))
	}
	var dir string
	if !a.windows() {
		if dir, err = a.stateDir(); err != nil {
			return err
		}
	}
	d := a.wizardDeps(ctx, "hpctun ssh-config setup", cfg.Path, dir, true, cfg.Code)
	defer d.Asker.Close()
	nc, approved, err := prompt.SSHSetup(d, config.FromConfig(cfg))
	d.Asker.Close() // back to the shell before anything is printed
	if errors.Is(err, prompt.ErrAborted) {
		fmt.Fprintln(a.stdout, "nothing changed")
		return nil
	}
	if err != nil {
		return failed(err)
	}
	err = a.applyWizard(approved, func() ([]change.Change, error) {
		cs, err := a.sshChanges(ctx, nc, cfg.Path)
		if err != nil {
			return nil, failed(err)
		}
		cc, err := a.configChange(cfg.Path, nc)
		if err != nil {
			return nil, usage(err)
		}
		return append(cs, cc), nil
	})
	if err != nil {
		return err
	}
	cfg, err = a.loadConfig()
	if err != nil {
		return err
	}
	if cfg.SSH != nil {
		err = a.reportSSH(ctx, cfg)
	}
	if kerr := a.offerKey(ctx, cfg.Gateway); kerr != nil {
		return failed(kerr)
	}
	return err
}

// restoreSockets creates the ControlPath directory again when s defines
// entries and the directory is gone, as it is after writing hpctun_config (see
// apply). It reports whether it created it.
func (a *app) restoreSockets(s *config.SSH) (bool, error) {
	if a.windows() {
		return false, nil // no ControlPath, no sockets directory
	}
	p := a.sshPaths()
	if (sshsetup.Input{SSH: *s}).Empty() {
		return false, nil
	}
	if _, err := os.Lstat(p.Sockets); !errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err := sshsetup.EnsureSockets(p); err != nil {
		return false, failed(err)
	}
	fmt.Fprintf(a.stdout, "created %s\n", a.tilde(p.Sockets))
	return true, nil
}

// applyWizard writes what the wizard settled: exactly the changes approved
// in its ssh review, or, when it showed none (nothing for hpctun to define, so
// at most a leftover hpctun_config to clear), the changes from pending, shown
// and asked about now.
func (a *app) applyWizard(approved []change.Change, pending func() ([]change.Change, error)) error {
	if approved != nil {
		return a.apply(approved)
	}
	cs, err := pending()
	if err != nil {
		return err
	}
	return a.proposeAndApply(cs, false)
}

// sshChanges are the edits that make ~/.ssh match nc's gateway, tunnel
// gateway, [ssh] and [code] tables: hpctun_config, and the Include line when
// hpctun_config has entries. With nothing to define, only a hpctun_config
// left from an earlier setup is cleared. A tun_config left from before the
// rename is migrated (or, with nothing else to define, just removed)
// alongside whatever else is proposed; see sshsetup.LegacyChanges.
func (a *app) sshChanges(ctx context.Context, nc config.NewConfig, source string) ([]change.Change, error) {
	p := a.sshPaths()
	aliases, err := sshsetup.UserAliases(p)
	if err != nil {
		return nil, err
	}
	in := sshsetup.Input{Gateway: nc.Gateway, TunnelGateway: nc.TunnelGateway, UserAliases: aliases, Source: a.tilde(source),
		NoControlMaster: a.windows(), Keychain: a.mac()}
	if nc.SSH != nil {
		in.SSH = *nc.SSH
	}
	in.ResolveHostNames(ctx, sshsetup.RunnerSSHG(a.runner()))
	tc, err := sshsetup.TunConfigChange(p, sshsetup.Generate(in))
	if err != nil {
		return nil, err
	}
	legacy, err := sshsetup.LegacyChanges(p)
	if err != nil {
		return nil, err
	}
	var cs []change.Change
	switch {
	case !in.Empty():
		cs = append(cs, tc)
		if len(legacy) == 0 {
			// The legacy user-config change, when there is one, already
			// carries the Include; adding IncludeChange too would read the
			// same file and propose a second, conflicting edit.
			ic, err := sshsetup.IncludeChange(p)
			if err != nil {
				return nil, err
			}
			cs = append(cs, ic)
		}
	case tc.Exists:
		cs = append(cs, tc)
	}
	return append(cs, legacy...), nil
}

// configChange is the edit of hpctun's config file for nc's [ssh] table,
// gateway and tunnel gateway.
func (a *app) configChange(path string, nc config.NewConfig) (change.Change, error) {
	c, err := change.Read(path, a.tilde(path))
	if err != nil {
		return change.Change{}, err
	}
	out, err := config.ReplaceSSHText([]byte(c.Old), nc.SSH, nc.Gateway, nc.TunnelGateway)
	if err != nil {
		return change.Change{}, err
	}
	c.New = string(out)
	return c, nil
}

// diffs renders the changes that change something, separated by blank lines.
func diffs(cs []change.Change) string {
	var parts []string
	for _, c := range cs {
		if d := c.Diff(); d != "" {
			parts = append(parts, d)
		}
	}
	return strings.Join(parts, "\n")
}

// errChangesDeclined marks a decline of the "Apply these changes?" question,
// so a caller for whom nothing was proposed to begin with (see runInit) can
// tell it apart from a real failure and continue.
var errChangesDeclined = errors.New("changes not applied")

// proposeAndApply prints the diffs and applies them once the user approves
// (or with yes). With no terminal and no yes, nothing is written. With
// nothing to propose, it does nothing and prints nothing: the caller decides
// whether "up to date" is worth announcing.
func (a *app) proposeAndApply(cs []change.Change, yes bool) error {
	d := diffs(cs)
	if d == "" {
		return nil
	}
	fmt.Fprintln(a.stdout, d)
	if !yes {
		if !a.interactive() {
			return failed(errors.New("changes not applied; run again in a terminal to approve them, or with --yes"))
		}
		ok, err := a.asker("", false).Confirm("Apply these changes?", "", false)
		if err != nil || !ok {
			return failed(errChangesDeclined)
		}
	}
	return a.apply(cs)
}

// apply writes approved changes, and creates the ControlPath directory when
// hpctun_config is among them.
func (a *app) apply(cs []change.Change) error {
	if err := change.Apply(cs); err != nil {
		return failed(err)
	}
	p := a.sshPaths()
	for _, c := range cs {
		if c.Empty() {
			continue
		}
		if c.Remove {
			fmt.Fprintf(a.stdout, "removed %s\n", c.Display)
		} else {
			fmt.Fprintf(a.stdout, "wrote %s\n", c.Display)
		}
	}
	if !a.windows() && slices.ContainsFunc(cs, func(c change.Change) bool { return c.Path == p.TunConfig && !c.Empty() }) {
		if err := sshsetup.EnsureSockets(p); err != nil {
			return failed(err)
		}
	}
	return nil
}

// sshInput is the generator input for cfg, which must have [ssh], on this
// platform.
func (a *app) sshInput(ctx context.Context, cfg *config.Config) (sshsetup.Input, error) {
	in, err := sshsetup.NewInput(ctx, sshsetup.RunnerSSHG(a.runner()), a.sshPaths(), cfg, a.tilde(cfg.Path))
	in.NoControlMaster = a.windows()
	in.Keychain = a.mac()
	return in, err
}

// reportSSH prints the conflict check for cfg; problems make it fail.
func (a *app) reportSSH(ctx context.Context, cfg *config.Config) error {
	p := a.sshPaths()
	in, err := a.sshInput(ctx, cfg)
	if err != nil {
		return failed(err)
	}
	known := []string{cfg.DefaultHost}
	for _, t := range cfg.Tunnels {
		known = append(known, t.Host)
	}
	found, err := sshsetup.Check(ctx, sshsetup.RunnerSSHG(a.runner()), p, in, known)
	if err != nil {
		return failed(err)
	}
	if a.windows() {
		found = append(found, sshsetup.CheckAgent(ctx, sshsetup.RunnerSSHAdd(a.runner()))...)
	}
	label := map[sshsetup.Kind]string{sshsetup.Problem: "problem", sshsetup.Warning: "warning", sshsetup.Note: "note"}
	problems := 0
	for _, f := range found {
		fmt.Fprintf(a.stdout, "%s: %s\n", label[f.Kind], f.Msg)
		if f.Kind == sshsetup.Problem {
			problems++
		}
	}
	if problems > 0 {
		return failed(fmt.Errorf("%d problem(s) with the ssh setup", problems))
	}
	if len(found) == 0 {
		fmt.Fprintln(a.stdout, "ssh sees the setup as written.")
	}
	return nil
}
