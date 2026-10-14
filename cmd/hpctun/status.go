package main

import (
	"context"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"hpctun/internal/config"
	"hpctun/internal/gateway"
	"hpctun/internal/probe"
	"hpctun/internal/state"
	"hpctun/internal/tunnel"
)

func newStatusCmd(a *app) *cobra.Command {
	var asJSON, noCode bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show tunnels and whether they are up",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return jsonErrors(a, asJSON, func() error { return runStatus(cmd.Context(), a, asJSON, noCode) })
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, jsonFlagUsage)
	cmd.Flags().BoolVar(&noCode, "no-code", false, "do not ask Slurm about the hpctun code job (for frequent polling)")
	return cmd
}

func runStatus(ctx context.Context, a *app, asJSON, noCode bool) error {
	h, rows, err := gatherStatus(ctx, a, noCode)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSON(a.stdout, statusDoc(h, rows))
	}
	_, err = lipgloss.Fprint(a.stdout, renderStatus(h, rows))
	return err
}

// gatherStatus observes the connections and tunnels status shows. noCode
// skips the Slurm query for the code job, which runs over ssh.
func gatherStatus(ctx context.Context, a *app, noCode bool) (header, []tunnel.Row, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return header{}, nil, err
	}
	dir, err := a.stateDir()
	if err != nil {
		return header{}, nil, err
	}
	st, err := state.Read(dir)
	if err != nil {
		return header{}, nil, failed(err)
	}
	r := a.runner()
	snap, err := probe.Take(ctx, r)
	if err != nil {
		return header{}, nil, failed(err)
	}
	h := header{Config: cfg.Path, Dest: cfg.TunnelDest(), Tunnels: cfg.TunnelGateway != ""}
	socket := ""
	if a.windows() {
		standaloneHeader(&h, cfg)
	} else {
		if cfg.TunnelDest() != "" {
			if g, err := a.resolveGateway(ctx, cfg, dir); err != nil {
				h.Err = err
			} else {
				h.Mode, h.Hints = g.Mode, g.Hints
				h.Status, h.Err = g.Check(ctx, r)
				socket = g.SocketPath
			}
		}
		if h.Tunnels {
			c := &conn{Role: "gateway", Alias: cfg.Gateway}
			if g, err := gateway.Resolve(ctx, r, c.Alias, dir); err != nil {
				c.Err = err
			} else {
				c.Mode = g.Mode
				c.Status, c.Err = g.Check(ctx, r)
			}
			h.Login = c
		}
		if cfg.SSH != nil && cfg.SSH.Transfer != "" {
			c := &conn{Role: "transfer", Alias: cfg.SSH.Transfer}
			if g, err := gateway.Resolve(ctx, r, c.Alias, dir); err != nil {
				c.Err = err
			} else if c.Mode = g.Mode; g.Mode == gateway.ModeSSHConfig {
				c.Status, c.Err = g.Check(ctx, r)
			}
			h.Transfer = c
		}
	}
	// Asking Slurm costs a sign-in on every poll on Windows.
	if cfg.Code != nil && cfg.Gateway != "" && !noCode && !a.windows() {
		h.Code = a.codeStatus(ctx, cfg, h.gatewayConnected())
	}
	v := tunnel.View{
		Config: cfg, State: st, Snap: snap, Master: h.Status, Gateway: cfg.TunnelDest(), Socket: socket,
		ProcCmd: func(pid int) (string, bool) {
			cmd, ok, err := probe.ProcessCommand(ctx, r, pid)
			return cmd, ok && err == nil
		},
		Standalone: a.windows(), Ident: a.identFn(),
	}
	if !a.windows() {
		v.Sockets = tunnel.RecordedMasters(ctx, r, st, v.Socket)
	}
	return h, v.Rows(), nil
}

// standaloneHeader fills h on Windows, where nothing is shared and
// nothing needs checking.
func standaloneHeader(h *header, cfg *config.Config) {
	if h.Dest != "" {
		h.Mode = gateway.ModeStandalone
	}
	if h.Tunnels {
		h.Login = &conn{Role: "gateway", Alias: cfg.Gateway, Mode: gateway.ModeStandalone}
	}
	if cfg.SSH != nil && cfg.SSH.Transfer != "" {
		h.Transfer = &conn{Role: "transfer", Alias: cfg.SSH.Transfer, Mode: gateway.ModeStandalone}
	}
}
