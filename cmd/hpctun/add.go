package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"hpctun/internal/config"
	"hpctun/internal/prompt"
)

type addFlags struct {
	port, remotePort, socks int
	host, url               string
	loopback, loopbackSet   bool
}

// addPreset builds the prompt preset from arguments and flags, and reports
// whether anything was given.
func addPreset(args []string, fl addFlags) (prompt.Preset, bool) {
	p := prompt.Preset{Port: fl.port, Host: fl.host, RemotePort: fl.remotePort, URL: fl.url}
	if len(args) > 0 {
		p.Name = args[0]
	}
	if fl.socks != 0 {
		p.Socks, p.Port = true, fl.socks
	}
	if fl.loopbackSet {
		p.Route = config.RouteMaster
		if fl.loopback {
			p.Route = config.RouteJump
		}
	}
	return p, p != (prompt.Preset{})
}

// missingForAdd lists required fields absent from p.
func missingForAdd(p prompt.Preset, defaultHost string) []string {
	var miss []string
	if p.Name == "" {
		miss = append(miss, "name")
	}
	if p.Port == 0 {
		miss = append(miss, "--port or --socks")
	}
	if !p.Socks && p.Host == "" && defaultHost == "" {
		miss = append(miss, "--host")
	}
	return miss
}

func newAddCmd(a *app) *cobra.Command {
	var fl addFlags
	cmd := &cobra.Command{
		Use:     "add [name]",
		Short:   "Define a named tunnel without opening it",
		Example: "  hpctun add\n  hpctun add jupyter2 --port 4002 --host web1 --loopback\n  hpctun add proxy --socks 5005\n  hpctun add chat --socks 1080 --url https://chat.example.org",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fl.loopbackSet = cmd.Flags().Changed("loopback")
			if fl.socks != 0 && (fl.port != 0 || fl.host != "" || fl.remotePort != 0 || fl.loopbackSet) {
				return usage(errors.New("--socks cannot be combined with --port, --host, --remote-port or --loopback"))
			}
			if fl.url != "" {
				if err := config.ValidateURL(fl.url); err != nil {
					return usage(err)
				}
			}
			if fl.host != "" {
				if err := config.ValidateDest(fl.host); err != nil {
					return usage(fmt.Errorf("--host: %w", err))
				}
			}
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			if err := requireGateway(cfg); err != nil {
				return err
			}
			preset, given := addPreset(args, fl)
			opts := prompt.FormOpts{
				DefaultHost: cfg.DefaultHost,
				Full:        !given,
				Hosts:       prompt.HostChoices(cfg),
				Context:     addContext(a, cfg),
				NameTaken:   func(n string) bool { _, ok := cfg.Lookup(n); return ok },
				PortTaken: func(p int) error {
					if t, ok := cfg.ByPort(p); ok {
						return fmt.Errorf("port %d is already used by tunnel %q", p, t.Name)
					}
					return nil
				},
			}
			var t config.Tunnel
			if miss := missingForAdd(preset, cfg.DefaultHost); len(miss) == 0 {
				t, err = prompt.TunnelForm(noQuestions{}, preset, opts)
			} else if a.interactive() {
				ask := a.asker("hpctun add", true)
				defer ask.Close()
				t, err = prompt.TunnelForm(ask, preset, opts)
				ask.Close() // back to the shell before anything is printed
			} else {
				return usage(fmt.Errorf("missing %s (run in a terminal to be asked)", strings.Join(miss, ", ")))
			}
			if errors.Is(err, prompt.ErrAborted) || errors.Is(err, prompt.ErrBack) {
				return failed(prompt.ErrAborted)
			}
			if err != nil {
				return usage(err)
			}
			if err := config.AppendTunnel(cfg.Path, t); err != nil {
				return usage(err)
			}
			fmt.Fprintf(a.stdout, "added %s to %s\n", describeTunnel(t), cfg.Path)
			return nil
		},
	}
	cmd.Flags().IntVar(&fl.port, "port", 0, "local port")
	cmd.Flags().StringVar(&fl.host, "host", "", "target host (default: default_host)")
	cmd.Flags().IntVar(&fl.remotePort, "remote-port", 0, "port on the target host (default: same as local)")
	cmd.Flags().BoolVar(&fl.loopback, "loopback", false, "the service listens only on localhost there (jump route)")
	cmd.Flags().IntVar(&fl.socks, "socks", 0, "define a SOCKS proxy on this local port")
	cmd.Flags().StringVar(&fl.url, "url", "", "page hpctun browse opens (http or https)")
	return cmd
}

func describeTunnel(t config.Tunnel) string {
	if t.Route == config.RouteSocks {
		return fmt.Sprintf("%s (SOCKS %d)", t.Name, t.Port)
	}
	return fmt.Sprintf("%s (%d → %s:%d, %s)", t.Name, t.Port, t.Host, t.RemotePort, t.Route)
}

// noQuestions is an Asker for when every required field is already given;
// TunnelForm with Full=false never asks in that case. Validation still runs
// on the preset through the checks below.
type noQuestions struct{ prompt.Plain }

func (noQuestions) Input(q prompt.InputQ) (string, error) {
	return "", fmt.Errorf("internal error: unexpected question %q", q.Title)
}
func (noQuestions) Select(q prompt.SelectQ) (string, error) {
	return "", fmt.Errorf("internal error: unexpected question %q", q.Title)
}
func (noQuestions) Confirm(title, _ string, _ bool) (bool, error) {
	return false, fmt.Errorf("internal error: unexpected question %q", title)
}
func (noQuestions) SetContext(prompt.Context) {}

// addContext is the recap shown while hpctun add asks its questions.
func addContext(a *app, cfg *config.Config) prompt.Context {
	path := a.tilde(cfg.Path)
	c := prompt.Context{Recap: []prompt.Line{{Label: "Config", Value: path}, {Label: "Gateway", Value: cfg.Gateway}}}
	if cfg.DefaultHost != "" {
		c.Recap = append(c.Recap, prompt.Line{Label: "Default host", Value: cfg.DefaultHost})
	}
	return c
}
