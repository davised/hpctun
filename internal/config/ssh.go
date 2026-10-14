package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// SSH is the [ssh] table: what hpctun writes to ~/.ssh/hpctun_config.
type SSH struct {
	User              string   // login name in every block hpctun writes
	GatewayHost       string   // when set, hpctun defines the gateway alias with this HostName
	TunnelGatewayHost string   // when set, hpctun defines tunnel_gateway with this HostName
	Transfer          string   // alias used for copies
	TransferHost      string   // when set, hpctun defines Transfer with this HostName
	Nodes             []string // ssh Host patterns reached through the gateway
}

type rawSSH struct {
	User              string   `toml:"user"`
	GatewayHost       string   `toml:"gateway_host"`
	TunnelGatewayHost string   `toml:"tunnel_gateway_host"`
	Transfer          string   `toml:"transfer"`
	TransferHost      string   `toml:"transfer_host"`
	Nodes             []string `toml:"nodes"`
}

var (
	aliasRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	hostRE    = regexp.MustCompile(`^[A-Za-z0-9._:][A-Za-z0-9._:-]*$`)
	userRE    = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]*$`)
	patternRE = regexp.MustCompile(`^[A-Za-z0-9.*?_][A-Za-z0-9.*?_-]*$`)
)

// ValidateAlias reports whether s can be an ssh Host alias hpctun writes.
func ValidateAlias(s string) error {
	if !aliasRE.MatchString(s) {
		return errors.New("must be a short name such as shell or files: letters, digits, . _ -")
	}
	return nil
}

// ValidateHostName reports whether s looks like a host name or address.
func ValidateHostName(s string) error {
	if !hostRE.MatchString(s) {
		return errors.New("must be a host name such as login.example.edu")
	}
	return nil
}

// ValidateDest reports whether s can be handed to ssh as a destination
// (gateway, tunnel_gateway, a host argument) or used as a tunnel's
// target host. ssh reads a leading '-' as an option, so a shared config
// could otherwise run a command on the reader's computer
// (-oProxyCommand=...); whitespace and control characters never belong
// in a host name.
func ValidateDest(s string) error {
	switch {
	case s == "":
		return errors.New("must not be empty")
	case strings.HasPrefix(s, "-"):
		return fmt.Errorf("%q starts with '-', which ssh would read as an option", s)
	case strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }):
		return fmt.Errorf("%q contains spaces or control characters", s)
	}
	return nil
}

// ValidateUser reports whether s can be a login name.
func ValidateUser(s string) error {
	if !userRE.MatchString(s) {
		return errors.New("must be a user name: letters, digits, . _ -")
	}
	return nil
}

// ValidatePattern reports whether s is a Host pattern hpctun accepts.
func ValidatePattern(s string) error {
	if !patternRE.MatchString(s) {
		return fmt.Errorf("%q is not a host pattern: use letters, digits, . _ - and the wildcards * and ?", s)
	}
	return nil
}

// sshFromRaw validates the [ssh] table. seed (LoadSeed) allows a missing
// user: hpctun init asks for it.
func sshFromRaw(gateway string, r *rawSSH, seed bool) (*SSH, error) {
	if r == nil {
		return nil, nil
	}
	bad := func(field string, err error) (*SSH, error) {
		return nil, &ValidationError{Field: field, Msg: err.Error()}
	}
	if gateway == "" {
		return bad("gateway", errors.New("required when [ssh] is set"))
	}
	if err := ValidateAlias(gateway); err != nil {
		return bad("gateway", errors.New("must be an ssh alias (no user@, spaces or patterns) when [ssh] is set"))
	}
	s := &SSH{User: r.User, GatewayHost: r.GatewayHost, TunnelGatewayHost: r.TunnelGatewayHost, Transfer: r.Transfer, TransferHost: r.TransferHost, Nodes: r.Nodes}
	if s.GatewayHost != "" {
		if err := ValidateHostName(s.GatewayHost); err != nil {
			return bad("ssh.gateway_host", err)
		}
	}
	if s.TunnelGatewayHost != "" {
		if err := ValidateHostName(s.TunnelGatewayHost); err != nil {
			return bad("ssh.tunnel_gateway_host", err)
		}
	}
	if s.TransferHost != "" {
		if s.Transfer == "" {
			return bad("ssh.transfer_host", errors.New("requires ssh.transfer"))
		}
		if err := ValidateHostName(s.TransferHost); err != nil {
			return bad("ssh.transfer_host", err)
		}
	}
	if s.Transfer != "" {
		if err := ValidateAlias(s.Transfer); err != nil {
			return bad("ssh.transfer", err)
		}
		if s.Transfer == gateway {
			return bad("ssh.transfer", errors.New("must differ from gateway"))
		}
	}
	for _, n := range s.Nodes {
		if err := ValidatePattern(n); err != nil {
			return bad("ssh.nodes", err)
		}
	}
	if !seed && (s.GatewayHost != "" || s.TransferHost != "" || s.TunnelGatewayHost != "" || len(s.Nodes) > 0) {
		if s.User == "" {
			return bad("ssh.user", errors.New("required when hpctun writes ssh entries (gateway_host, transfer_host, tunnel_gateway_host or nodes)"))
		}
	}
	if s.User != "" {
		if err := ValidateUser(s.User); err != nil {
			return bad("ssh.user", err)
		}
	}
	return s, nil
}

// FormatSSH renders s as an [ssh] table, omitting unset keys.
func FormatSSH(s *SSH) string {
	var b strings.Builder
	b.WriteString("[ssh]\n")
	kv := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s = %s\n", k, strconv.Quote(v))
		}
	}
	kv("user", s.User)
	kv("gateway_host", s.GatewayHost)
	kv("tunnel_gateway_host", s.TunnelGatewayHost)
	kv("transfer", s.Transfer)
	kv("transfer_host", s.TransferHost)
	if len(s.Nodes) > 0 {
		q := make([]string, len(s.Nodes))
		for i, n := range s.Nodes {
			q[i] = strconv.Quote(n)
		}
		fmt.Fprintf(&b, "nodes = [%s]\n", strings.Join(q, ", "))
	}
	return b.String()
}

// stringValue reads the quoted string value of a top-level `key = ...` line,
// ignoring a trailing comment. ok is false when it cannot be parsed as a
// plain TOML string, so the caller always rewrites the line rather than risk
// keeping a value it misread.
func stringValue(line string) (string, bool) {
	_, v, ok := strings.Cut(line, "=")
	if !ok {
		return "", false
	}
	q, err := strconv.QuotedPrefix(strings.TrimSpace(v))
	if err != nil {
		return "", false
	}
	s, err := strconv.Unquote(q)
	if err != nil {
		return "", false
	}
	return s, true
}

// topLevel is the index in lines of the top-level "key = ..." line, or -1.
func topLevel(lines []string, key string) int {
	return slices.IndexFunc(lines, func(l string) bool {
		k, _, ok := strings.Cut(l, "=")
		return ok && strings.TrimSpace(k) == key
	})
}

// ReplaceSSHText rewrites the [ssh] table in the config text old, removing
// it when s is nil, and sets the top-level gateway and tunnel_gateway,
// removing tunnel_gateway when it is "". It edits the text so everything
// else is kept; comments inside the old [ssh] table are lost. The result is
// validated; nothing is written here.
func ReplaceSSHText(old []byte, s *SSH, gateway, tunnelGateway string) ([]byte, error) {
	lines := strings.SplitAfter(string(old), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
		lines[n-1] += "\n"
	}
	isTable := func(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "[") }
	isComment := func(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "#") }
	isBlank := func(l string) bool { return strings.TrimSpace(l) == "" }

	firstTable := slices.IndexFunc(lines, isTable)
	if firstTable < 0 {
		firstTable = len(lines)
	}
	gw := "gateway = " + strconv.Quote(gateway) + "\n"
	gi := topLevel(lines[:firstTable], "gateway")
	switch {
	case gi >= 0:
		// Only rewrite the line when the value actually changes, so a
		// trailing comment or other formatting on it survives untouched.
		if v, ok := stringValue(lines[gi]); !ok || v != gateway {
			lines[gi] = gw
		}
	default:
		lines = slices.Insert(lines, 0, gw)
		firstTable++
	}

	ti := topLevel(lines[:firstTable], "tunnel_gateway")
	tg := "tunnel_gateway = " + strconv.Quote(tunnelGateway) + "\n"
	switch {
	case ti >= 0 && tunnelGateway == "":
		lines = slices.Delete(lines, ti, ti+1)
		firstTable--
	case ti >= 0:
		if v, ok := stringValue(lines[ti]); !ok || v != tunnelGateway {
			lines[ti] = tg
		}
	case tunnelGateway != "":
		gi = topLevel(lines[:firstTable], "gateway") // present after the switch above
		lines = slices.Insert(lines, gi+1, tg)
		firstTable++
	}

	var block []string
	if s != nil {
		block = strings.SplitAfter(FormatSSH(s), "\n")
		block = block[:len(block)-1] // FormatSSH ends in "\n"
	}
	start := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "[ssh]") })
	switch {
	case start >= 0:
		end := len(lines)
		for i := start + 1; i < len(lines); i++ {
			if isTable(lines[i]) {
				end = i
				break
			}
		}
		for end > start+1 && (isBlank(lines[end-1]) || isComment(lines[end-1])) {
			end--
		}
		lines = slices.Replace(lines, start, end, block...)
	case s != nil:
		at := firstTable
		for at > 0 && isComment(lines[at-1]) {
			at--
		}
		ins := append(block, "\n")
		if at == len(lines) {
			ins = append([]string{"\n"}, block...)
		}
		lines = slices.Insert(lines, at, ins...)
	}

	out := []byte(strings.Join(lines, ""))
	if _, err := Parse(out); err != nil {
		return nil, err
	}
	return out, nil
}
