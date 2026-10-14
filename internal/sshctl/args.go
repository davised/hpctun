package sshctl

import (
	"fmt"
	"regexp"
	"strings"
)

// GW is how to reach the gateway's master connection.
type GW struct {
	Dest string
	// ClientOpts talk to an existing master and never start one.
	ClientOpts []string
	// MasterOpts are added when starting the master (managed mode only).
	MasterOpts []string
}

var connectTimeout = []string{"-o", "ConnectTimeout=15"}

// Every builder ends with "--" before the destination: ssh then reads it
// as a host even if it starts with '-'. Options and forward specs go
// before "--", since everything after the host is the remote command.
func CheckArgs(gw GW) []string { return join(gw.ClientOpts, []string{"-O", "check", "--", gw.Dest}) }
func ExitArgs(gw GW) []string  { return join(gw.ClientOpts, []string{"-O", "exit", "--", gw.Dest}) }
func StartArgs(gw GW) []string {
	return join(connectTimeout, gw.MasterOpts, []string{"-fN", "--", gw.Dest})
}
func ForwardArgs(gw GW, spec string) []string {
	return join(gw.ClientOpts, []string{"-O", "forward", spec, "--", gw.Dest})
}
func CancelArgs(gw GW, spec string) []string {
	return join(gw.ClientOpts, []string{"-O", "cancel", spec, "--", gw.Dest})
}

// JumpArgs opens a standalone ssh to host that tunnels through the gateway
// master. It never multiplexes itself, so a ControlMaster configured for host
// cannot end up carrying the tunnel.
func JumpArgs(gw GW, host, spec string) []string {
	return join(
		[]string{"-Nf", "-o", "ExitOnForwardFailure=yes", "-o", "ControlMaster=no", "-o", "ControlPath=none"},
		connectTimeout,
		[]string{"-o", "ProxyCommand=" + ProxyCommand(gw), spec, "--", host},
	)
}

// ProxyCommand is the jump helper: ssh through the existing master to %h:%p.
// ssh expands %-tokens in ProxyCommand before handing it to /bin/sh -c, so any
// '%' in our own words is doubled, and each word is shell-quoted.
func ProxyCommand(gw GW) string {
	words := []string{"ssh"}
	for _, o := range gw.ClientOpts {
		words = append(words, ShellQuote(strings.ReplaceAll(o, "%", "%%")))
	}
	words = append(words, "-W", "%h:%p", "--", ShellQuote(strings.ReplaceAll(gw.Dest, "%", "%%")))
	return strings.Join(words, " ")
}

// LocalSpec is an ssh -L forward spec bound to localhost. Without a bind
// address, ssh binds by the user's GatewayPorts setting, and "yes" (in
// ~/.ssh/config, or a master started with -g) would put every tunnel on
// all of the laptop's network interfaces.
func LocalSpec(port int, host string, remotePort int) string {
	return fmt.Sprintf("-Llocalhost:%d:%s:%d", port, host, remotePort)
}

// SocksSpec is an ssh -D forward spec bound to localhost.
func SocksSpec(port int) string { return fmt.Sprintf("-Dlocalhost:%d", port) }

var safeWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// ShellQuote quotes s for /bin/sh when it contains anything but safe characters.
func ShellQuote(s string) string {
	if safeWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func join(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// standaloneOpts start a tunnel's own ssh: no remote command, no prompts
// (no terminal to answer them on), exit when the forward cannot be set up,
// and notice a dead connection within about 90 s.
var standaloneOpts = []string{"-N", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes",
	"-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=30", "-o", "ServerAliveCountMax=3"}

// StandaloneArgs is a tunnel's own ssh where there is no shared master
// (Windows): the forward spec on a connection to dest, or, with jumpHost
// set, on a login to jumpHost through dest. The hop is a ProxyCommand
// rather than -J so it runs with BatchMode too.
func StandaloneArgs(dest, spec, jumpHost string) []string {
	if jumpHost == "" {
		return join(standaloneOpts, []string{spec, "--", dest})
	}
	return join(standaloneOpts, []string{spec, "-o", "ProxyCommand=" + WindowsProxyCommand(dest), "--", jumpHost})
}

// WindowsProxyCommand is the hop for StandaloneArgs. OpenSSH for Windows
// starts a ProxyCommand with CreateProcess rather than /bin/sh, so words
// are quoted by the CommandLineToArgvW rules; '%' is doubled because ssh
// expands %-tokens first.
func WindowsProxyCommand(dest string) string {
	return "ssh -o BatchMode=yes -W %h:%p -- " + windowsArg(strings.ReplaceAll(dest, "%", "%%"))
}

// windowsArg quotes s as one CommandLineToArgvW argument when it needs it.
func windowsArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range s {
		switch r {
		case '\\':
			slashes++
			continue
		case '"':
			b.WriteString(strings.Repeat(`\`, 2*slashes+1))
		default:
			b.WriteString(strings.Repeat(`\`, slashes))
		}
		slashes = 0
		b.WriteRune(r)
	}
	b.WriteString(strings.Repeat(`\`, 2*slashes))
	b.WriteByte('"')
	return b.String()
}
