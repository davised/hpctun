package sshsetup

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"hpctun/internal/gateway"
	"hpctun/internal/sshconfig"
	"hpctun/internal/sshctl"
)

// Kind grades a Finding.
type Kind int

const (
	Problem Kind = iota // the setup does not work as written
	Warning             // ssh does something other than what hpctun wrote
	Note                // expected, worth knowing
)

// Finding is one line of the check.
type Finding struct {
	Kind Kind
	Msg  string
}

// errRejected stands in for every ssh -G that failed on UseKeychain.
var errRejected = errors.New("ssh rejects UseKeychain")

// SSHG returns the settings ssh would use for host (ssh -G).
type SSHG func(ctx context.Context, host string) (map[string]string, error)

// RunnerSSHG runs `ssh [extra...] -G host` through r.
func RunnerSSHG(r sshctl.Runner, extra ...string) SSHG {
	return func(ctx context.Context, host string) (map[string]string, error) {
		res, err := r.Run(ctx, "ssh", append(slices.Clone(extra), "-G", "--", host)...)
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("ssh -G %s: %s", host, strings.TrimSpace(string(res.Stderr)))
		}
		return gateway.ParseSSHG(res.Stdout), nil
	}
}

// Check compares what ssh would do with what hpctun generated. known are
// concrete hosts hpctun knows about (the default host and tunnel hosts); those
// matching a node pattern are checked for their ProxyJump.
func Check(ctx context.Context, sshG SSHG, p Paths, in Input, known []string) ([]Finding, error) {
	var out []Finding
	add := func(k Kind, format string, a ...any) { out = append(out, Finding{k, fmt.Sprintf(format, a...)}) }

	if _, err := os.Lstat(p.LegacyTunConfig); err == nil {
		add(Problem, "%s is from before the rename; run `hpctun ssh-config sync`", p.Show(p.LegacyTunConfig))
	}

	// An [ssh] table that defines nothing (for example only transfer set to
	// an existing alias) writes no hpctun_config and needs no Include: there
	// is nothing for ssh to see, so the Include finding does not apply, and a
	// missing hpctun_config is expected rather than stale. Only a leftover
	// hpctun_config from an earlier, non-empty setup is worth flagging.
	if in.Empty() {
		b, err := os.ReadFile(p.TunConfig)
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if string(b) != Generate(in) {
			add(Problem, "%s is out of date; run `hpctun ssh-config sync`", p.Show(p.TunConfig))
		}
		return out, nil
	}

	inc, err := FindInclude(p)
	if err != nil {
		return nil, err
	}
	switch inc.Status {
	case IncludeMissing:
		add(Problem, "%s does not include hpctun_config; run `hpctun ssh-config sync` to add it", p.Show(p.UserConfig))
	case IncludeMisplaced:
		add(Problem, "%s line %d includes hpctun_config inside a Host or Match block; move it above the first Host line", p.Show(p.UserConfig), inc.Line)
	}
	stale, err := Stale(p, Generate(in))
	if err != nil {
		return nil, err
	}
	if stale {
		add(Problem, "%s is out of date; run `hpctun ssh-config sync`", p.Show(p.TunConfig))
	}
	if !in.NoControlMaster {
		// Every generated block sets ControlPath under Sockets; without the
		// directory ssh cannot create the control socket, so logins are not
		// shared and hpctun login finds no master.
		switch fi, err := os.Stat(p.Sockets); {
		case errors.Is(err, fs.ErrNotExist):
			add(Problem, "%s does not exist, so ssh cannot share logins; run `hpctun ssh-config sync` to create it", p.Show(p.Sockets))
		case err != nil:
			return nil, err
		case !fi.IsDir():
			add(Problem, "%s is not a directory, so ssh cannot share logins; replace it with a directory", p.Show(p.Sockets))
		}
	}
	if inc.Status != IncludePresent {
		return out, nil
	}

	// ignore is the IgnoreUnknown in force, from the first ssh -G that runs:
	// it is not per host, so any one will do.
	ignore, haveIgnore := "", false
	// rejected: the ssh on PATH lacks UseKeychain and saw it anyway, so
	// every ssh -G fails the same way; one finding says what to do.
	rejected := false
	sshGOnce := func(host string) (map[string]string, error) {
		g, err := sshG(ctx, host)
		if err == nil && !haveIgnore {
			ignore, haveIgnore = g["ignoreunknown"], true
		}
		if err != nil && in.Keychain && strings.Contains(strings.ToLower(err.Error()), "bad configuration option: usekeychain") {
			rejected = true
			return nil, errRejected
		}
		return g, err
	}

	type defined struct{ alias, host string }
	var defs []defined
	if in.SSH.GatewayHost != "" {
		defs = append(defs, defined{in.Gateway, in.SSH.GatewayHost})
	}
	if in.SSH.TransferHost != "" {
		defs = append(defs, defined{in.SSH.Transfer, in.SSH.TransferHost})
	}
	if in.SSH.TunnelGatewayHost != "" {
		defs = append(defs, defined{in.TunnelGateway, in.SSH.TunnelGatewayHost})
	}
	for _, d := range defs {
		if slices.Contains(in.UserAliases, d.alias) {
			add(Warning, "%s is also defined in your ssh config; hpctun's entry comes first and wins for the settings it writes", d.alias)
		}
		g, err := sshGOnce(d.alias)
		if err != nil {
			if errors.Is(err, errRejected) {
				continue
			}
			add(Warning, "%s: %v", d.alias, err)
			continue
		}
		if !strings.EqualFold(g["hostname"], d.host) {
			add(Warning, "%s: ssh connects to %s, not %s; an entry earlier in your ssh config wins", d.alias, g["hostname"], d.host)
		}
		if !in.NoControlMaster && g["controlmaster"] != "auto" {
			add(Warning, "%s: ControlMaster is %s, not auto; an entry earlier in your ssh config wins, so logins are not shared", d.alias, cmp.Or(g["controlmaster"], "unset"))
		}
	}

	seen := map[string]bool{}
	for _, h := range known {
		if h == "" || seen[h] || !sshconfig.MatchAny(in.SSH.Nodes, h) {
			continue
		}
		seen[h] = true
		g, err := sshGOnce(h)
		if err != nil {
			if errors.Is(err, errRejected) {
				continue
			}
			add(Warning, "%s: %v", h, err)
			continue
		}
		jump := cmp.Or(g["proxyjump"], "none")
		switch {
		case jump == in.Gateway:
		case !in.IsNode(h):
			add(Note, "%s: your own entry sets ProxyJump %s; hpctun leaves it alone", h, jump)
		default:
			add(Warning, "%s: ProxyJump is %s, not %s; an entry earlier in your ssh config wins", h, jump, in.Gateway)
		}
	}

	// hpctun_config sets IgnoreUnknown UseKeychain, but ssh keeps the first
	// IgnoreUnknown it reads, and a user's above the Include wins. Apple's
	// ssh knows UseKeychain anyway; conda's or Homebrew's then refuses the
	// whole config.
	if in.Keychain {
		if !haveIgnore && in.Gateway != "" {
			sshGOnce(in.Gateway) // only the node block was generated, and no known host is a node
		}
		if rejected {
			add(Problem, "the ssh first on your PATH is not Apple's (from conda or Homebrew, say) and stops at UseKeychain in hpctun_config, because an IgnoreUnknown earlier in your ssh config wins over hpctun's; add UseKeychain to that line, e.g. IgnoreUnknown UseKeychain")
		} else if ignore != "" && !sshconfig.MatchList(ignore, "UseKeychain") {
			add(Warning, "your ssh config sets IgnoreUnknown %s before hpctun's, and ssh uses the first one it reads, so an ssh other than Apple's (from conda or Homebrew, say) stops at UseKeychain and will not run; add UseKeychain to that line: IgnoreUnknown %s,UseKeychain", ignore, ignore)
		}
	}
	return out, nil
}
