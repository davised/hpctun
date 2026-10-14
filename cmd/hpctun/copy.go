package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"hpctun/internal/config"
	"hpctun/internal/copycmd"
	"hpctun/internal/sshsetup"
)

func newCopyCmds(a *app) []*cobra.Command {
	mk := func(tool, use, short, example string) *cobra.Command {
		cmd := &cobra.Command{
			Use:                use,
			Short:              short,
			Example:            example,
			DisableFlagParsing: true,
			ValidArgsFunction:  completeCopy(a),
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			rest, err := a.takeGlobalFlags(args)
			if err != nil {
				return usage(err)
			}
			return runCopy(cmd.Context(), a, tool, rest)
		}
		return cmd
	}
	return []*cobra.Command{
		mk("scp", "cp [scp args]", "Run scp, hopping through the transfer host for paths on nodes",
			"  hpctun cp node1:/scratch/run.log .\n  hpctun cp -r results/ node1:/scratch/me/"),
		mk("rsync", "rsync [rsync args]", "Run rsync, hopping through the transfer host for paths on nodes",
			"  hpctun rsync -av results/ node1:/scratch/me/"),
		mk("sftp", "sftp [sftp args]", "Run sftp, hopping through the transfer host for nodes",
			"  hpctun sftp node1"),
	}
}

// takeGlobalFlags applies hpctun's own --config and --verbose when they come
// before the tool's arguments; flag parsing is off for the copy commands.
// "-v" is not recognised here: it belongs to scp, rsync and sftp themselves.
func (a *app) takeGlobalFlags(args []string) ([]string, error) {
	for len(args) > 0 {
		switch arg := args[0]; {
		case arg == "--verbose":
			a.verbose = true
			args = args[1:]
		case arg == "--config":
			if len(args) < 2 {
				return nil, errors.New("--config needs a path")
			}
			a.configPath = args[1]
			args = args[2:]
		case strings.HasPrefix(arg, "--config="):
			a.configPath = strings.TrimPrefix(arg, "--config=")
			args = args[1:]
		default:
			return args, nil
		}
	}
	return args, nil
}

func runCopy(ctx context.Context, a *app, tool string, args []string) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	route, err := a.copyRoute(ctx, cfg)
	if err != nil {
		return err
	}
	argv, hopped, err := copycmd.Rewrite(tool, args, route)
	if err != nil {
		return usage(err)
	}
	if hopped {
		fmt.Fprintf(a.stderr, "→ %s\n", copycmd.Quote(argv))
	}
	if err := a.exec(argv); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			return err // the tool ran and failed (Windows): keep its status
		}
		return failed(fmt.Errorf("%s: %w", tool, err))
	}
	return nil
}

// copyRoute is how copies reach nodes under cfg. Without [ssh], nothing hops.
func (a *app) copyRoute(ctx context.Context, cfg *config.Config) (copycmd.Route, error) {
	if cfg.SSH == nil {
		return copycmd.Route{DriveLetters: a.windows()}, nil
	}
	in, err := a.sshInput(ctx, cfg)
	if err != nil {
		return copycmd.Route{}, failed(err)
	}
	return copycmd.Route{Transfer: cfg.SSH.Transfer, IsNode: in.IsNode, DriveLetters: a.windows()}, nil
}

// copyHosts are the host names offered for completion: the transfer host,
// the gateway, hosts the config names, and the user's ssh aliases.
func (a *app) copyHosts(cfg *config.Config) []string {
	var hs []string
	add := func(h string) {
		if h != "" && !slices.Contains(hs, h) {
			hs = append(hs, h)
		}
	}
	if cfg.SSH != nil {
		add(cfg.SSH.Transfer)
	}
	add(cfg.Gateway)
	add(cfg.DefaultHost)
	for _, t := range cfg.Tunnels {
		add(t.Host)
	}
	aliases, _ := sshsetup.UserAliases(a.sshPaths())
	for _, h := range aliases {
		add(h)
	}
	return hs
}

func completeCopy(a *app) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		cfg, err := a.loadConfig()
		if err != nil {
			return nil, cobra.ShellCompDirectiveDefault
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		route, err := a.copyRoute(ctx, cfg)
		if err != nil {
			return nil, cobra.ShellCompDirectiveDefault
		}
		c := copycmd.Completer{Runner: a.runner(), Route: route, Hosts: a.copyHosts(cfg)}
		cands, files := c.Complete(ctx, toComplete)
		if files {
			return nil, cobra.ShellCompDirectiveDefault
		}
		return cands, cobra.ShellCompDirectiveNoSpace | cobra.ShellCompDirectiveNoFileComp
	}
}
