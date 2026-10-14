package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/spf13/cobra"

	"hpctun/internal/config"
	"hpctun/internal/jsondoc"
	"hpctun/internal/prompt"
	"hpctun/internal/state"
	"hpctun/internal/tunnel"
)

// adhocFlags are the flags that describe an ad-hoc tunnel. set is true when
// any of them was given.
type adhocFlags struct {
	host       string
	remotePort int
	loopback   bool
	socks      int
	set        bool
}

func newUpCmd(a *app) *cobra.Command {
	var f adhocFlags
	var all, noSave, asJSON bool
	var save string
	cmd := &cobra.Command{
		Use:     "up [name|port]...",
		Short:   "Open tunnels (named, recorded, or new ad-hoc ones)",
		Example: "  hpctun up jupyter\n  hpctun up --all\n  hpctun up 4002 --host web1 --loopback\n  hpctun up --socks 5005 --save proxy",
		RunE: func(cmd *cobra.Command, args []string) error {
			return jsonErrors(a, asJSON, func() error {
				fl := cmd.Flags()
				f.set = fl.Changed("host") || fl.Changed("remote-port") || fl.Changed("loopback") || fl.Changed("socks")
				if save != "" && noSave {
					return usage(errors.New("--save and --no-save cannot be combined"))
				}
				return runUp(cmd.Context(), a, args, all, f, save, noSave, asJSON)
			})
		},
		ValidArgsFunction: completeTunnels(a),
	}
	cmd.Flags().BoolVar(&all, "all", false, "open every named tunnel and every recorded ad-hoc tunnel")
	cmd.Flags().StringVar(&f.host, "host", "", "ad-hoc: target host (default: default_host)")
	cmd.Flags().IntVar(&f.remotePort, "remote-port", 0, "ad-hoc: port on the target host (default: same as local)")
	cmd.Flags().BoolVar(&f.loopback, "loopback", false, "ad-hoc: the service listens only on localhost there (jump route)")
	cmd.Flags().IntVar(&f.socks, "socks", 0, "ad-hoc: open a SOCKS proxy on this local port")
	cmd.Flags().StringVar(&save, "save", "", "ad-hoc: save the tunnel to the config under this name")
	cmd.Flags().BoolVar(&noSave, "no-save", false, "ad-hoc: do not offer to save the tunnel")
	cmd.Flags().BoolVar(&asJSON, "json", false, jsonFlagUsage+" (never asks to save)")
	return cmd
}

// planUp turns arguments and flags into the tunnels to open.
func planUp(cfg *config.Config, st *state.State, args []string, all bool, f adhocFlags) ([]tunnel.Spec, error) {
	switch {
	case all && (len(args) > 0 || f.set):
		return nil, errors.New("--all cannot be combined with tunnel arguments or ad-hoc flags")
	case all:
		return tunnel.Specs(cfg, st), nil
	case f.socks != 0:
		if len(args) > 0 || f.host != "" || f.remotePort != 0 || f.loopback {
			return nil, errors.New("--socks takes no arguments and no other ad-hoc flags")
		}
		if !config.ValidPort(f.socks) {
			return nil, fmt.Errorf("--socks %d is out of range", f.socks)
		}
		return []tunnel.Spec{{Port: f.socks, Route: config.RouteSocks}}, nil
	case len(args) == 0:
		return nil, errors.New("name a tunnel or port, or use --all")
	}
	specs, unknown, err := tunnel.Select(cfg, st, args)
	if err != nil {
		return nil, err
	}
	adhoc := func(port int) tunnel.Spec {
		s := tunnel.Spec{Port: port, Route: config.RouteMaster, Host: cmp.Or(f.host, cfg.DefaultHost), RemotePort: cmp.Or(f.remotePort, port)}
		if f.loopback {
			s.Route = config.RouteJump
		}
		return s
	}
	if f.set {
		for _, s := range specs {
			if s.Name == "" {
				if want := adhoc(s.Port); want != s {
					return nil, fmt.Errorf("an ad-hoc tunnel on port %d is already recorded as %s; run `hpctun down %d` first", s.Port, describe(s), s.Port)
				}
				continue
			}
			if slices.Contains(args, strconv.Itoa(s.Port)) {
				return nil, fmt.Errorf("port %d belongs to tunnel %q; ad-hoc flags cannot change it", s.Port, s.Name)
			}
			return nil, errors.New("--host, --remote-port and --loopback only apply to ad-hoc tunnels")
		}
	}
	for _, port := range unknown {
		s := adhoc(port)
		if s.Host == "" {
			return nil, fmt.Errorf("port %d: no default_host in the config; pass --host", port)
		}
		specs = append(specs, s)
	}
	return specs, nil
}

func runUp(ctx context.Context, a *app, args []string, all bool, f adhocFlags, save string, noSave, asJSON bool) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if f.host != "" {
		if err := config.ValidateDest(f.host); err != nil {
			return usage(fmt.Errorf("--host: %w", err))
		}
	}
	if err := requireGateway(cfg); err != nil {
		return err
	}
	if err := checkSaveName(cfg, save); err != nil {
		return usage(err)
	}
	env, closeEnv, err := a.openEnv(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeEnv()
	specs, err := planUp(cfg, env.State, args, all, f)
	if err != nil {
		return usage(err)
	}
	var adhoc []tunnel.Spec
	for _, s := range specs {
		if s.Name == "" {
			adhoc = append(adhoc, s)
		}
	}
	if save != "" && len(adhoc) != 1 {
		return usage(errors.New("--save needs exactly one ad-hoc tunnel"))
	}
	results := tunnel.Up(ctx, env, specs)
	failures := countFailed(results)
	if !asJSON {
		report(a.stdout, results)
	}

	var saved *jsondoc.Saved
	for _, r := range results {
		if r.Spec.Name != "" || (r.Outcome != tunnel.Opened && r.Outcome != tunnel.AlreadyUp) {
			continue
		}
		name := save
		if name == "" && r.Outcome == tunnel.Opened && !noSave && !all && !asJSON && a.interactive() {
			name, err = prompt.SaveName(a.asker("", false), func(n string) bool { _, ok := cfg.Lookup(n); return ok })
			if errors.Is(err, prompt.ErrAborted) || errors.Is(err, prompt.ErrBack) {
				break
			}
			if err != nil {
				return failed(err)
			}
		}
		if name == "" {
			continue
		}
		if err := saveAdhoc(cfg, env, r.Spec, name); err != nil {
			if !asJSON {
				return usage(err)
			}
			// The tunnel is up all the same: report it beside the error.
			msg := err.Error()
			if werr := writeJSON(a.stdout, resultsDoc(results, nil, &msg)); werr != nil {
				return failed(werr)
			}
			return usage(err)
		}
		if asJSON {
			saved = &jsondoc.Saved{Name: name, Config: cfg.Path}
		} else {
			fmt.Fprintf(a.stdout, "saved as %q in %s\n", name, cfg.Path)
		}
	}
	if asJSON {
		if err := writeJSON(a.stdout, resultsDoc(results, saved, nil)); err != nil {
			return failed(err)
		}
	}
	if failures > 0 {
		return failed(fmt.Errorf("%d of %d tunnels failed", failures, len(results)))
	}
	return nil
}

// checkSaveName validates the --save name before anything is opened: it must
// be a legal tunnel name that does not collide with an existing one. An empty
// name means "not saving" and is always fine.
func checkSaveName(cfg *config.Config, name string) error {
	if name == "" {
		return nil
	}
	if err := config.ValidateName(name); err != nil {
		return fmt.Errorf("tunnel name %q: %w", name, err)
	}
	if _, ok := cfg.Lookup(name); ok {
		return fmt.Errorf("a tunnel named %q already exists", name)
	}
	return nil
}

// saveAdhoc appends an ad-hoc tunnel to the config and relabels its state entry.
func saveAdhoc(cfg *config.Config, env *tunnel.Env, s tunnel.Spec, name string) error {
	if err := config.ValidateName(name); err != nil {
		return fmt.Errorf("tunnel name %q: %w", name, err)
	}
	s.Name = name
	if err := config.AppendTunnel(cfg.Path, s.Tunnel()); err != nil {
		return err
	}
	if e, ok := env.State.ByPort(s.Port); ok {
		e.Name = name
		env.State.Put(e)
		return env.Store.Save(env.State)
	}
	return nil
}

// completeTunnels offers tunnel names from the config for shell completion.
func completeTunnels(a *app) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		cfg, err := a.loadConfig()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names := make([]string, 0, len(cfg.Tunnels))
		for _, t := range cfg.Tunnels {
			names = append(names, t.Name)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}
