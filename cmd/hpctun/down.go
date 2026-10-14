package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"hpctun/internal/tunnel"
)

func newDownCmd(a *app) *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:               "down [name|port]...",
		Short:             "Close tunnels hpctun opened",
		ValidArgsFunction: completeTunnels(a),
		RunE: func(cmd *cobra.Command, args []string) error {
			return jsonErrors(a, asJSON, func() error { return runDown(cmd.Context(), a, args, all, asJSON) })
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "close every tunnel hpctun opened")
	cmd.Flags().BoolVar(&asJSON, "json", false, jsonFlagUsage)
	return cmd
}

func runDown(ctx context.Context, a *app, args []string, all, asJSON bool) error {
	if all == (len(args) > 0) {
		return usage(errors.New("name tunnels or ports to close, or use --all"))
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	env, closeEnv, err := a.openEnv(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeEnv()
	specs := tunnel.Specs(cfg, env.State)
	if !all {
		var unknown []int
		specs, unknown, err = tunnel.Select(cfg, env.State, args)
		if err != nil {
			return usage(err)
		}
		if len(unknown) > 0 {
			return usage(fmt.Errorf("no tunnel on port %d", unknown[0]))
		}
	}
	results := tunnel.Down(ctx, env, specs)
	if asJSON {
		if err := writeJSON(a.stdout, resultsDoc(results, nil, nil)); err != nil {
			return failed(err)
		}
	} else {
		report(a.stdout, results)
	}
	if n := countFailed(results); n > 0 {
		return failed(fmt.Errorf("%d of %d tunnels failed to close", n, len(results)))
	}
	return nil
}
