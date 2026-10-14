package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"

	"hpctun/internal/atomicfile"
)

// FormatTunnel renders t as a [tunnel.<name>] block, omitting fields that
// equal their defaults under defaultHost.
func FormatTunnel(t Tunnel, defaultHost string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[tunnel.%s]\n", t.Name)
	if t.Route == RouteSocks {
		fmt.Fprintf(&b, "socks = %d\n", t.Port)
		writePage(&b, t)
		return b.String()
	}
	fmt.Fprintf(&b, "port = %d\n", t.Port)
	if t.Host != defaultHost {
		fmt.Fprintf(&b, "host = %s\n", strconv.Quote(t.Host))
	}
	if t.RemotePort != t.Port {
		fmt.Fprintf(&b, "remote_port = %d\n", t.RemotePort)
	}
	if t.Route == RouteJump {
		b.WriteString("route = \"jump\"\n")
	}
	writePage(&b, t)
	return b.String()
}

// writePage writes what hpctun browse opens: url, or browse = false.
func writePage(b *strings.Builder, t Tunnel) {
	if t.URL != "" {
		fmt.Fprintf(b, "url = %s\n", strconv.Quote(t.URL))
	}
	if t.NoBrowse {
		b.WriteString("browse = false\n")
	}
}

// AppendTunnel appends t to the config at path as text, so existing comments
// and formatting are kept. The file is created if missing. The combined text
// is validated before anything is written; on error the file is unchanged.
func AppendTunnel(path string, t Tunnel) error {
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	prev, err := Parse(old)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, dup := prev.Lookup(t.Name); dup {
		return &ValidationError{Tunnel: t.Name, Field: "name", Msg: "already exists"}
	}
	var b bytes.Buffer
	b.Write(old)
	if len(old) > 0 {
		if !bytes.HasSuffix(old, []byte("\n")) {
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	b.WriteString(FormatTunnel(t, prev.DefaultHost))
	if _, err := Parse(b.Bytes()); err != nil {
		return err
	}
	return atomicfile.WriteFile(path, b.Bytes(), 0o600)
}

// NewConfig is the content of a freshly written config.
type NewConfig struct {
	Gateway       string
	TunnelGateway string
	DefaultHost   string
	Tunnels       []Tunnel
	SSH           *SSH
	Code          *Code
	VSCode        *VSCode
}

// FromConfig converts a loaded config into a NewConfig, for seeding init.
func FromConfig(c *Config) NewConfig {
	return NewConfig{Gateway: c.Gateway, TunnelGateway: c.TunnelGateway, DefaultHost: c.DefaultHost, Tunnels: append([]Tunnel(nil), c.Tunnels...), SSH: cloneSSH(c.SSH), Code: cloneCode(c.Code), VSCode: cloneVSCode(c.VSCode)}
}

func cloneSSH(s *SSH) *SSH {
	if s == nil {
		return nil
	}
	c := *s
	c.Nodes = slices.Clone(s.Nodes)
	return &c
}

// Render produces a commented config file for nc.
func Render(nc NewConfig) string {
	var b strings.Builder
	b.WriteString("# hpctun configuration. The Config section of hpctun's README explains every setting.\n\n")
	b.WriteString("# Any ssh destination: an alias from ~/.ssh/config, a hostname, or user@host.\n")
	fmt.Fprintf(&b, "gateway = %s\n\n", strconv.Quote(nc.Gateway))
	if nc.TunnelGateway != "" {
		b.WriteString("# What tunnels go through, on a connection of their own.\n")
		fmt.Fprintf(&b, "tunnel_gateway = %s\n\n", strconv.Quote(nc.TunnelGateway))
	}
	b.WriteString("# Target host for tunnels that do not name one.\n")
	if nc.DefaultHost == "" {
		b.WriteString("# default_host = \"\"\n")
	} else {
		fmt.Fprintf(&b, "default_host = %s\n", strconv.Quote(nc.DefaultHost))
	}
	if nc.SSH != nil {
		b.WriteString("\n# What hpctun writes to ~/.ssh/hpctun_config; see `hpctun ssh-config`.\n")
		b.WriteString(FormatSSH(nc.SSH))
	}
	if nc.Code != nil {
		b.WriteString("\n# The Slurm job and ssh alias of `hpctun code`; see `hpctun code --help`.\n")
		b.WriteString(FormatCode(nc.Code))
	}
	if nc.VSCode != nil {
		b.WriteString("\n# Where `hpctun vscode-settings` finds settings; see `hpctun vscode-settings --help`.\n")
		b.WriteString(FormatVSCode(nc.VSCode))
	}
	for _, t := range nc.Tunnels {
		b.WriteString("\n")
		b.WriteString(FormatTunnel(t, nc.DefaultHost))
	}
	return b.String()
}

// WriteNew writes a fresh config for nc, refusing to replace an existing file
// unless force is set.
func WriteNew(path string, nc NewConfig, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("%s already exists", path)
	}
	data := []byte(Render(nc))
	if _, err := Parse(data); err != nil {
		return err
	}
	// Private: a config can hold URLs with tokens.
	return atomicfile.WriteFile(path, data, 0o600)
}
