package main

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"hpctun/internal/tunnel"
)

func newSaveCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "save <port> <name>",
		Short: "Keep an open ad-hoc tunnel as a named tunnel",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(args[0])
			if err != nil {
				return usage(fmt.Errorf("port %q is not a number", args[0]))
			}
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			if err := requireGateway(cfg); err != nil {
				return err
			}
			env, closeEnv, err := a.openEnv(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			defer closeEnv()
			e, ok := env.State.ByPort(port)
			if _, named := cfg.ByPort(port); !ok || named {
				return usage(fmt.Errorf("no ad-hoc tunnel on port %d", port))
			}
			if _, taken := cfg.Lookup(args[1]); taken {
				return usage(fmt.Errorf("a tunnel named %q already exists", args[1]))
			}
			if err := saveAdhoc(cfg, env, tunnel.FromEntry(e), args[1]); err != nil {
				return usage(err)
			}
			fmt.Fprintf(a.stdout, "saved port %d as %q in %s\n", port, args[1], cfg.Path)
			return nil
		},
	}
}
