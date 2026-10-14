package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"hpctun/internal/browser"
	"hpctun/internal/config"
	"hpctun/internal/jsondoc"
	"hpctun/internal/tunnel"
)

func newBrowseCmd(a *app) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "browse <name|port> [url]",
		Short: "Open a tunnel's page in a browser, opening the tunnel first if needed",
		Long: "A port forward opens in your default browser (its url, else http://localhost:<port>/).\n" +
			"A SOCKS tunnel opens its url (or an empty window) in a Chrome, Edge, Brave or Chromium\n" +
			"window of its own that sends its web traffic through the proxy (localhost and link-local\n" +
			"addresses still go direct); your other windows are not affected. HPCTUN_BROWSER names a\n" +
			"different Chromium browser.",
		Example: "  hpctun browse jupyter\n  hpctun browse proxy https://intranet.example.org",
		RunE: func(cmd *cobra.Command, args []string) error {
			return jsonErrors(a, asJSON, func() error { return runBrowse(cmd.Context(), a, args, asJSON) })
		},
		ValidArgsFunction: completeTunnels(a),
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, jsonFlagUsage)
	return cmd
}

func runBrowse(ctx context.Context, a *app, args []string, asJSON bool) error {
	switch {
	case len(args) == 0:
		return usage(errors.New("name a tunnel or port"))
	case len(args) > 2:
		return usage(fmt.Errorf("too many arguments: want a tunnel or port and at most one url, got %d", len(args)))
	}
	page := ""
	if len(args) == 2 {
		if err := config.ValidateURL(args[1]); err != nil {
			return usage(err)
		}
		page = args[1]
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if err := requireGateway(cfg); err != nil {
		return err
	}
	env, closeEnv, err := a.openEnv(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeEnv()
	specs, unknown, err := tunnel.Select(cfg, env.State, args[:1])
	if err != nil {
		return usage(err)
	}
	if len(unknown) > 0 {
		return usage(fmt.Errorf("no tunnel on port %d; open one with `hpctun up %d` first", unknown[0], unknown[0]))
	}
	s := specs[0]
	if t, ok := cfg.Lookup(s.Name); ok && s.Name != "" && t.NoBrowse && page == "" {
		return usage(fmt.Errorf("tunnel %q has no page to open (browse = false); name one: hpctun browse %s <url>", s.Name, s.Name))
	}
	results := tunnel.Up(ctx, env, specs)
	r := results[0]
	doc := jsondoc.Browse{Version: jsondoc.Version, Result: resultDoc(r)}
	if !asJSON && r.Outcome == tunnel.Opened {
		report(a.stdout, results)
	}
	if r.Outcome != tunnel.Opened && r.Outcome != tunnel.AlreadyUp {
		if asJSON {
			if err := writeJSON(a.stdout, doc); err != nil {
				return failed(err)
			}
		} else {
			report(a.stdout, results)
		}
		msg := s.Label() + " did not open"
		if d := firstDetail(r.Detail); d != "" {
			msg += ": " + d
		}
		return failed(errors.New(msg))
	}

	if page == "" {
		if t, ok := cfg.Lookup(s.Name); ok && s.Name != "" {
			page = t.URL
		}
	}
	if page == "" && s.Route != config.RouteSocks {
		page = fmt.Sprintf("http://localhost:%d/", s.Port)
	}
	name := s.Name
	if name == "" {
		name = "port " + strconv.Itoa(s.Port)
	}
	b := a.browsers()
	var line, used, warning string
	if s.Route == config.RouteSocks {
		dir, err := a.stateDir()
		if err != nil {
			return err
		}
		profile := s.Name
		if profile == "" {
			profile = "socks-" + strconv.Itoa(s.Port)
		}
		br, old, err := b.OpenProxied(ctx, filepath.Join(dir, "browser", profile), s.Port, page)
		if errors.Is(err, browser.ErrNoChromium) {
			return failed(fmt.Errorf("%w; set HPCTUN_BROWSER to one, or set Firefox's SOCKS proxy to 127.0.0.1:%d", err, s.Port))
		}
		if err != nil {
			return failed(err)
		}
		used = br.Name
		line = fmt.Sprintf("opened %s in %s (proxy 127.0.0.1:%d)", name, br.Name, s.Port)
		if old != 0 {
			warning = fmt.Sprintf("the %s window still uses port %d; close it and run hpctun browse %s again", name, old, args[0])
		}
	} else {
		used, err = b.OpenPage(ctx, page)
		if err != nil {
			return failed(err)
		}
		line = fmt.Sprintf("opened %s in %s", name, used)
	}
	if asJSON {
		doc.Browser, doc.URL, doc.Warning = &used, strOrNil(page), strOrNil(warning)
		if err := writeJSON(a.stdout, doc); err != nil {
			return failed(err)
		}
		return nil
	}
	fmt.Fprintln(a.stdout, line)
	if warning != "" {
		fmt.Fprintln(a.stderr, "warning: "+warning)
	}
	return nil
}

// firstDetail is the first line of a failure's detail, for a one-line error.
// A first line that ends in a colon introduces the next one ("could not
// connect (ssh exited 255):" then ssh's own words), so that one comes too.
func firstDetail(detail string) string {
	var out []string
	for _, l := range strings.Split(detail, "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		out = append(out, l)
		if !strings.HasSuffix(l, ":") {
			break
		}
	}
	return strings.Join(out, " ")
}
