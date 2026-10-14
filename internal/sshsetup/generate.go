// Package sshsetup turns the [ssh] table into ~/.ssh/hpctun_config, keeps the
// Include line in ~/.ssh/config, and checks that ssh sees what hpctun wrote.
package sshsetup

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"hpctun/internal/config"
	"hpctun/internal/sshconfig"
)

// Input is everything Generate needs.
type Input struct {
	Gateway       string     // the gateway alias (config's top-level gateway)
	TunnelGateway string     // config's tunnel_gateway; "" when tunnels use the gateway
	SSH           config.SSH // the [ssh] table
	UserAliases   []string   // concrete Host names in the user's own ssh config, hpctun_config excluded
	HostNames     []string   // real host names of gateway and transfer aliases hpctun does not define; see ResolveHostNames

	NoControlMaster bool   // OpenSSH for Windows: leave out the connection-sharing options
	Keychain        bool   // macOS: read key passphrases from the Keychain (UseKeychain)
	Source          string // the config path named in the header
}

// Control paths: tunnels get their own, so a tunnel alias for the gateway's
// host (same host, user and port, so the same %C) still opens a connection
// of its own.
const (
	controlPath       = "~/.ssh/sockets/%C"
	tunnelControlPath = "~/.ssh/sockets/tunnels-%C"
)

// loginOptions are the options every generated block carries. Without
// ControlMaster (OpenSSH for Windows) the connection-sharing lines are left
// out. With the Keychain (macOS), ssh reads key passphrases stored by
// ssh-add --apple-use-keychain; see Generate for the IgnoreUnknown line.
func loginOptions(user, path string, noControlMaster, keychain bool) []string {
	opts := []string{"User " + user}
	if keychain {
		opts = append(opts, "UseKeychain yes")
	}
	if !noControlMaster {
		opts = append(opts, "ControlMaster auto", "ControlPath "+path, "ControlPersist yes")
	}
	return append(opts, "ServerAliveInterval 30", "ServerAliveCountMax 10", "AddKeysToAgent yes")
}

// tunnelAlias is the tunnel gateway when it is an ssh alias; a destination
// such as me@host is no Host name, so there is nothing to resolve or exclude.
func (in Input) tunnelAlias() string {
	if config.ValidateAlias(in.TunnelGateway) != nil {
		return ""
	}
	return in.TunnelGateway
}

// Empty reports whether there is nothing to write beyond the header.
func (in Input) Empty() bool {
	return in.SSH.GatewayHost == "" && in.SSH.TransferHost == "" && len(in.SSH.Nodes) == 0 && in.SSH.TunnelGatewayHost == ""
}

// ResolveHostNames sets HostNames to the host names ssh uses for the gateway
// and transfer aliases when the user defines them (hpctun knows GatewayHost and
// TransferHost already). With nodes = ["*.example.org"] and a gateway alias
// for login.example.org, the node block would otherwise send ssh
// login.example.org through the gateway to itself. Only needed when there
// are node patterns; an alias ssh -G cannot resolve is skipped.
func (in *Input) ResolveHostNames(ctx context.Context, sshG SSHG) {
	in.HostNames = nil
	if len(in.SSH.Nodes) == 0 {
		return
	}
	var aliases []string
	if in.SSH.GatewayHost == "" {
		aliases = append(aliases, in.Gateway)
	}
	if in.SSH.TransferHost == "" {
		aliases = append(aliases, in.SSH.Transfer)
	}
	if in.SSH.TunnelGatewayHost == "" {
		aliases = append(aliases, in.tunnelAlias())
	}
	for _, alias := range aliases {
		if alias == "" {
			continue
		}
		g, err := sshG(ctx, alias)
		if err != nil {
			continue
		}
		if h := g["hostname"]; h != "" && !strings.EqualFold(h, alias) {
			in.HostNames = append(in.HostNames, h)
		}
	}
}

// NodeExclusions are the names the node block must not capture: the gateway,
// transfer and tunnel gateway aliases and host names, and every alias the
// user configured that matches a node pattern (ssh keeps the first value it
// finds, and hpctun_config is included first, so these would otherwise lose
// their own route).
func (in Input) NodeExclusions() []string {
	var ex []string
	add := func(h string) {
		if h != "" && !slices.Contains(ex, h) {
			ex = append(ex, h)
		}
	}
	add(in.Gateway)
	add(in.SSH.GatewayHost)
	add(in.SSH.Transfer)
	add(in.SSH.TransferHost)
	add(in.tunnelAlias())
	add(in.SSH.TunnelGatewayHost)
	for _, h := range in.HostNames {
		add(h)
	}
	for _, a := range in.UserAliases {
		if sshconfig.MatchAny(in.SSH.Nodes, a) {
			add(a)
		}
	}
	return ex
}

// IsNode reports whether ssh reaches host through the generated node block.
func (in Input) IsNode(host string) bool {
	if !sshconfig.MatchAny(in.SSH.Nodes, host) {
		return false
	}
	return !slices.ContainsFunc(in.NodeExclusions(), func(e string) bool { return sshconfig.Match(e, host) })
}

// commentText keeps s on one line of a comment: a newline in a config path
// would end the comment and start ssh directives of the path's choosing.
func commentText(s string) string {
	if !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	q := strconv.Quote(s)
	return q[1 : len(q)-1]
}

// Generate renders ~/.ssh/hpctun_config.
func Generate(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by hpctun from %s. Edits are overwritten;\n", commentText(in.Source))
	b.WriteString("# change the [ssh] table there and run `hpctun ssh-config sync`.\n")
	if in.NoControlMaster {
		b.WriteString("# No shared connections: OpenSSH for Windows lacks ControlMaster.\n")
	}
	if in.Keychain {
		// An OpenSSH without UseKeychain (Homebrew's) checks the options of
		// every block, matching or not, so IgnoreUnknown must come before
		// the first Host to cover them all.
		b.WriteString("\nIgnoreUnknown UseKeychain\n")
	}
	block := func(hostLine, first, path string) {
		fmt.Fprintf(&b, "\nHost %s\n    %s\n", hostLine, first)
		for _, l := range loginOptions(in.SSH.User, path, in.NoControlMaster, in.Keychain) {
			b.WriteString("    " + l + "\n")
		}
	}
	s := in.SSH
	if s.GatewayHost != "" {
		block(in.Gateway, "HostName "+s.GatewayHost, controlPath)
	}
	if s.TunnelGatewayHost != "" {
		block(in.TunnelGateway, "HostName "+s.TunnelGatewayHost, tunnelControlPath)
	}
	if s.TransferHost != "" {
		block(s.Transfer, "HostName "+s.TransferHost, controlPath)
	}
	if len(s.Nodes) > 0 {
		words := slices.Clone(s.Nodes)
		for _, e := range in.NodeExclusions() {
			words = append(words, "!"+e)
		}
		block(strings.Join(words, " "), "ProxyJump "+in.Gateway, controlPath)
	}
	return b.String()
}
