package config

import (
	"path/filepath"
	"testing"
)

// TestSiteFilesLoad keeps every shipped sites/*.toml loadable by
// hpctun init --from as the schema changes, and keeps them site settings
// only: no user (the wizard asks), and no nodes, default host or tunnel
// gateway (personal). [vscode], [code] and tunnels to site services are
// allowed: they hold nothing personal, and the wizard asks before it keeps
// [code] or tunnels (site-config spec, amendments 2026-09-28 and
// 2026-10-08). scripts/dist.sh ships every sites/*.toml.
func TestSiteFilesLoad(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "sites", "*.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no sites/*.toml found")
	}
	for _, p := range paths {
		c, err := LoadSeed(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if !c.Exists || c.Gateway == "" {
			t.Errorf("%s: no gateway", p)
		}
		if c.SSH != nil && c.SSH.User != "" {
			t.Errorf("%s sets ssh.user %q; the wizard asks for it", p, c.SSH.User)
		}
		if c.DefaultHost != "" || c.TunnelGateway != "" ||
			(c.SSH != nil && c.SSH.TunnelGatewayHost != "") {
			t.Errorf("%s sets a default host or tunnel gateway; leave them to each user", p)
		}
		if c.SSH != nil && len(c.SSH.Nodes) > 0 {
			t.Errorf("%s sets ssh.nodes; node patterns are each user's choice", p)
		}
		if filepath.Base(p) == "osu.toml" {
			if chat, ok := c.Lookup("chat"); !ok || chat.URL != "https://chat.ceoas.oregonstate.edu" {
				t.Errorf("osu.toml chat = %+v", chat)
			}
			if llm, _ := c.Lookup("llm"); llm.URL != "" || !llm.NoBrowse {
				t.Errorf("osu.toml llm = %+v: its base URL is for tools, not a browser (no url, browse = false)", llm)
			}
		}
	}
}
