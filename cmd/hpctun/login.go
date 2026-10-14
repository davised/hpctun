package main

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"hpctun/internal/config"
	"hpctun/internal/gateway"
	"hpctun/internal/sshctl"
	"hpctun/internal/state"
	"hpctun/internal/tunnel"
)

func newLoginCmds(a *app) []*cobra.Command {
	complete := func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		cfg, err := a.loadConfig()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names, _ := loginTargets(cfg, nil)
		if cfg.TunnelGateway != "" {
			names = append(names, cfg.TunnelGateway)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
	cmds := []*cobra.Command{
		{
			Use:   "login [gateway|transfer|tunnel gateway...]",
			Short: "Open the shared connections to the gateway and transfer host",
			Long: "Open the shared ssh connections to the gateway and the transfer host, so passphrase,\n" +
				"password and 2FA prompts happen now rather than in the middle of a copy. With no\n" +
				"arguments, both; name one to open only that.\n" +
				"The tunnel gateway, when set, is opened only by name (hpctun up opens it too).",
			Example:           "  hpctun login\n  hpctun login files",
			ValidArgsFunction: complete,
			RunE:              func(cmd *cobra.Command, args []string) error { return runLogin(cmd.Context(), a, args) },
		},
		{
			Use:   "logout [gateway|transfer|tunnel gateway...]",
			Short: "Close the shared connections hpctun login opened",
			Long: "Close the shared ssh connections hpctun login opened. Logging out of the gateway also ends any\n" +
				"ssh sessions and copies (scp, sftp, rsync, hpctun cp) riding its connection.",
			Example:           "  hpctun logout files",
			ValidArgsFunction: complete,
			RunE:              func(cmd *cobra.Command, args []string) error { return runLogout(cmd.Context(), a, args) },
		},
	}
	if a.windows() {
		cmds[0].Short = "Check that ssh signs in to the gateway and transfer host without prompting"
		cmds[0].Long = "Check that ssh signs in to the gateway and the transfer host without asking for anything.\n" +
			"On Windows each tunnel signs in by itself with no terminal to ask on, so it needs a key in\n" +
			"the ssh agent. With no arguments, both; name one to check only that."
		cmds[1].Short = "Close the tunnels opened through the gateway"
		cmds[1].Long = "Close every tunnel hpctun opened through the gateway (or the named host). On Windows there is\n" +
			"no shared connection to end."
	}
	return cmds
}

// loginTargets returns the aliases login and logout act on, in login order.
func loginTargets(cfg *config.Config, args []string) ([]string, error) {
	if err := requireGateway(cfg); err != nil {
		return nil, err
	}
	transfer := ""
	if cfg.SSH != nil {
		transfer = cfg.SSH.Transfer
	}
	if len(args) == 0 {
		if transfer == "" {
			return []string{cfg.Gateway}, nil
		}
		return []string{cfg.Gateway, transfer}, nil
	}
	var out []string
	for _, arg := range args {
		switch {
		case arg == cfg.Gateway || (transfer != "" && arg == transfer) || (cfg.TunnelGateway != "" && arg == cfg.TunnelGateway):
		case transfer == "":
			return nil, usage(fmt.Errorf("%q is not the gateway (%s), and no transfer host is set: add one with `hpctun ssh-config setup`", arg, cfg.Gateway))
		default:
			return nil, usage(fmt.Errorf("%q is neither the gateway (%s) nor the transfer host (%s)", arg, cfg.Gateway, transfer))
		}
		if !slices.Contains(out, arg) {
			out = append(out, arg)
		}
	}
	return out, nil
}

func runLogin(ctx context.Context, a *app, args []string) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	targets, err := loginTargets(cfg, args)
	if err != nil {
		return err
	}
	if a.windows() {
		return a.loginStandalone(ctx, cfg, targets)
	}
	dir, err := a.stateDir()
	if err != nil {
		return err
	}
	if err := state.EnsureDir(dir); err != nil {
		return failed(err)
	}
	r := a.runner()
	for _, alias := range targets {
		g, err := gateway.Resolve(ctx, r, alias, dir)
		if err != nil {
			return failed(err)
		}
		if alias != cfg.Gateway && alias != cfg.TunnelGateway && g.Mode == gateway.ModeManaged {
			return failed(fmt.Errorf("%s has no ControlMaster in your ssh config, so a connection hpctun opened would not be used by scp; run `hpctun ssh-config setup` to let hpctun define it", alias))
		}
		st, err := g.Check(ctx, r)
		if err != nil {
			return failed(err)
		}
		if st.Running {
			fmt.Fprintf(a.stdout, "%s  already connected (pid %d)\n", alias, st.PID)
			continue
		}
		fmt.Fprintf(a.stdout, "%s  logging in...\n", alias)
		log := "master.log"
		if alias != cfg.TunnelDest() {
			log = "login-" + alias + ".log"
		}
		st, err = g.EnsureStarted(ctx, r, filepath.Join(dir, log))
		if err != nil {
			return failed(err)
		}
		fmt.Fprintf(a.stdout, "%s  connected (pid %d)\n", alias, st.PID)
	}
	return nil
}

func runLogout(ctx context.Context, a *app, args []string) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	targets, err := loginTargets(cfg, args)
	if err != nil {
		return err
	}
	if a.windows() {
		return a.logoutStandalone(ctx, cfg, targets)
	}
	slices.Reverse(targets) // transfer host first, so the gateway refusal comes last
	dir, err := a.stateDir()
	if err != nil {
		return err
	}
	r := a.runner()
	for _, alias := range targets {
		if alias == cfg.TunnelDest() || cfg.TunnelGateway != "" {
			st, err := state.Read(dir)
			if err != nil {
				return failed(err)
			}
			if using := tunnelsUsing(cfg, st, alias); len(using) > 0 {
				return failed(fmt.Errorf("tunnels are using %s (%s); close them first with `hpctun down --all`", alias, tunnelList(using)))
			}
		}
		g, err := gateway.Resolve(ctx, r, alias, dir)
		if err != nil {
			return failed(err)
		}
		st, err := g.Check(ctx, r)
		if err != nil {
			return failed(err)
		}
		if !st.Running {
			fmt.Fprintf(a.stdout, "%s  not connected\n", alias)
			continue
		}
		res, err := r.Run(ctx, "ssh", sshctl.ExitArgs(g.GW)...)
		if err != nil {
			return failed(err)
		}
		if res.ExitCode != 0 {
			return failed(fmt.Errorf("%s: ssh -O exit failed: %s", alias, strings.TrimSpace(string(res.Stderr))))
		}
		fmt.Fprintf(a.stdout, "%s  logged out\n", alias)
	}
	return nil
}

// tunnelsUsing lists the open tunnels that logging out of alias would close:
// those opened through it, an entry with no gateway recorded counting as the
// tunnel gateway's. With tunnel_gateway set, that includes tunnels opened
// through the gateway before it was. Without it, every tunnel counts against
// the gateway, as it always has.
func tunnelsUsing(cfg *config.Config, st *state.State, alias string) []state.Entry {
	if cfg.TunnelGateway == "" {
		if alias == cfg.Gateway {
			return st.Tunnels
		}
		return nil
	}
	var using []state.Entry
	for _, e := range st.Tunnels {
		if cmp.Or(e.Gateway, cfg.TunnelDest()) == alias {
			using = append(using, e)
		}
	}
	return using
}

func tunnelList(entries []state.Entry) string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, cmp.Or(e.Name, strconv.Itoa(e.Port)))
	}
	return strings.Join(names, ", ")
}

// loginStandalone checks each target signs in without prompting: on
// Windows there is no connection to open ahead of time.
func (a *app) loginStandalone(ctx context.Context, cfg *config.Config, targets []string) error {
	bad := 0
	for _, alias := range targets {
		if err := a.signIn(ctx, alias); err != nil {
			bad++
			fmt.Fprintf(a.stdout, "%s  needs a prompt to sign in\n%s\n", alias, indent(err.Error(), "    "))
			continue
		}
		suffix := ""
		if alias == cfg.TunnelDest() {
			suffix = ": tunnels will work"
		}
		fmt.Fprintf(a.stdout, "%s  signs in without prompting%s\n", alias, suffix)
	}
	if bad > 0 {
		return failed(fmt.Errorf("%d of %d hosts need a prompt to sign in", bad, len(targets)))
	}
	return nil
}

// logoutStandalone closes the tunnels opened through each target, the
// nearest thing on Windows to ending a shared connection.
func (a *app) logoutStandalone(ctx context.Context, cfg *config.Config, targets []string) error {
	env, closeEnv, err := a.openEnv(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeEnv()
	failures := 0
	for _, alias := range targets {
		using := tunnelsUsing(cfg, env.State, alias)
		if len(using) == 0 {
			if cfg.SSH != nil && alias == cfg.SSH.Transfer {
				fmt.Fprintf(a.stdout, "%s  no shared connection to close\n", alias)
			} else {
				fmt.Fprintf(a.stdout, "%s  no tunnels open\n", alias)
			}
			continue
		}
		specs := make([]tunnel.Spec, 0, len(using))
		for _, e := range using {
			s := tunnel.FromEntry(e)
			if t, ok := cfg.ByPort(e.Port); ok {
				s = tunnel.FromConfig(t)
			}
			specs = append(specs, s)
		}
		results := tunnel.Down(ctx, env, specs)
		closed := 0
		for _, r := range results {
			if r.Outcome == tunnel.Closed {
				closed++
			}
		}
		failures += countFailed(results)
		noun := "tunnels"
		if closed == 1 {
			noun = "tunnel"
		}
		fmt.Fprintf(a.stdout, "%s  closed %d %s\n", alias, closed, noun)
		if closed != len(results) {
			report(a.stdout, results)
		}
	}
	if failures > 0 {
		return failed(fmt.Errorf("%d tunnels could not be closed", failures))
	}
	return nil
}
