// Package copycmd runs scp, rsync and sftp so that paths on nodes hop
// through the transfer host, and completes remote paths for them.
package copycmd

import (
	"fmt"
	"strings"

	"hpctun/internal/sshctl"
)

// Route says how copies reach nodes.
type Route struct {
	Transfer string                 // the transfer alias; "" when none is configured
	IsNode   func(host string) bool // hosts reached through the node block
	// DriveLetters is set on Windows: a letter and a colon (C:\x, C:/x, C:x)
	// begin a local path there, not a host named C.
	DriveLetters bool
}

// valueFlags are the single-letter options that take a value.
var valueFlags = map[string]string{
	"scp":  "cDFiJloPSX",
	"sftp": "BbcDFiJloPRSsX",
}

// Rewrite returns the command to run for tool (scp, rsync or sftp) with
// args, and whether it hops through the transfer host. Arguments are never
// changed; the hop is prepended as options.
func Rewrite(tool string, args []string, r Route) ([]string, bool, error) {
	unchanged := append([]string{tool}, args...)
	var ops []string
	var own bool // the user already chose how ssh connects
	switch tool {
	case "scp", "sftp":
		ops, own = operandsGetopt(args, valueFlags[tool])
		if tool == "sftp" && len(ops) > 1 {
			ops = ops[:1] // only the destination is remote
		}
	case "rsync":
		ops, own = operandsRsync(args)
	default:
		return nil, false, fmt.Errorf("unknown copy tool %q", tool)
	}
	var nodes, others []string
	for _, op := range ops {
		if r.drive(op) {
			continue
		}
		host, ok := remoteHost(tool, op)
		if !ok {
			continue
		}
		if r.IsNode != nil && r.IsNode(host) {
			nodes = append(nodes, host)
		} else {
			others = append(others, host)
		}
	}
	if len(nodes) == 0 || r.Transfer == "" {
		return unchanged, false, nil
	}
	if own {
		if tool == "rsync" {
			return nil, false, fmt.Errorf("rsync already has -e/--rsh, so hpctun cannot add its hop through %s; put it in your ssh command instead, e.g. -e 'ssh -J %s'", r.Transfer, r.Transfer)
		}
		return unchanged, false, nil
	}
	if len(others) > 0 {
		return nil, false, fmt.Errorf("%s is reached through %s but %s is not; copy in two steps", nodes[0], r.Transfer, others[0])
	}
	if tool == "rsync" {
		return append([]string{"rsync", "-e", "ssh -J " + r.Transfer}, args...), true, nil
	}
	return append([]string{tool, "-J", r.Transfer}, args...), true, nil
}

// drive reports whether op is a local Windows path with a drive letter.
func (r Route) drive(op string) bool {
	if !r.DriveLetters || len(op) < 2 || op[1] != ':' {
		return false
	}
	c := op[0] | 0x20 // lower case
	return c >= 'a' && c <= 'z'
}

// operandsGetopt splits getopt-style args (options first; "--" ends them)
// and reports whether the user chose a hop (-J, or -o ProxyJump/ProxyCommand).
func operandsGetopt(args []string, withValue string) (ops []string, hop bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if len(a) < 2 || a[0] != '-' {
			break
		}
		i++
		for j := 1; j < len(a); j++ {
			c := a[j]
			if strings.IndexByte(withValue, c) < 0 {
				continue
			}
			value := a[j+1:]
			if value == "" && i < len(args) {
				value = args[i]
				i++
			}
			if c == 'J' || (c == 'o' && choosesHop(value)) {
				hop = true
			}
			break
		}
	}
	return args[min(i, len(args)):], hop
}

func choosesHop(opt string) bool {
	k := strings.ToLower(opt)
	return strings.HasPrefix(k, "proxyjump") || strings.HasPrefix(k, "proxycommand")
}

// rsyncShortValue are rsync's single-letter options that take a value.
const rsyncShortValue = "BefMT@"

// rsyncLongValue are rsync's long options that take a value, which may be
// the next argument (--exclude PATTERN) as well as --exclude=PATTERN.
var rsyncLongValue = map[string]bool{
	"address": true, "backup-dir": true, "block-size": true, "bwlimit": true,
	"cc": true, "checksum-choice": true, "checksum-seed": true, "chmod": true,
	"chown": true, "compare-dest": true, "compress-choice": true,
	"compress-level": true, "contimeout": true, "copy-as": true,
	"copy-dest": true, "debug": true, "early-input": true, "exclude": true,
	"exclude-from": true, "files-from": true, "filter": true, "groupmap": true,
	"iconv": true, "include": true, "include-from": true, "info": true,
	"link-dest": true, "log-file": true, "log-file-format": true,
	"log-format": true, "max-alloc": true, "max-delete": true, "max-size": true,
	"min-size": true, "modify-window": true, "only-write-batch": true,
	"out-format": true, "outbuf": true, "partial-dir": true,
	"password-file": true, "port": true, "protocol": true, "read-batch": true,
	"remote-option": true, "rsh": true, "rsync-path": true,
	"skip-compress": true, "sockopts": true, "stderr": true, "stop-after": true,
	"stop-at": true, "suffix": true, "temp-dir": true, "time-limit": true,
	"timeout": true, "usermap": true, "write-batch": true, "zc": true,
	"zl": true,
}

// operandsRsync returns rsync's non-option arguments and whether -e/--rsh
// was given. rsync takes options anywhere before "--", so options after
// operands count too. An unknown long option that takes a separate value
// leaves the value among the operands.
func operandsRsync(args []string) (ops []string, rsh bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(ops, args[i+1:]...), rsh
		case strings.HasPrefix(a, "--"):
			name, _, hasValue := strings.Cut(a[2:], "=")
			if name == "rsh" {
				rsh = true
			}
			if !hasValue && rsyncLongValue[name] {
				i++
			}
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				if strings.IndexByte(rsyncShortValue, a[j]) < 0 {
					continue
				}
				if a[j] == 'e' {
					rsh = true
				}
				if j == len(a)-1 {
					i++ // the value is the next argument
				}
				break
			}
		default:
			ops = append(ops, a)
		}
	}
	return ops, rsh
}

// remoteHost returns the host of a remote operand. rsync daemon operands
// (host::module, rsync://) are not ssh and are ignored.
func remoteHost(tool, op string) (string, bool) {
	switch {
	case tool != "rsync" && (strings.HasPrefix(op, "scp://") || strings.HasPrefix(op, "sftp://")):
		return uriHost(op)
	case strings.HasPrefix(op, "rsync://"):
		return "", false
	}
	host, path, ok := splitRemote(op)
	if ok && tool == "rsync" && strings.HasPrefix(path, ":") {
		return "", false
	}
	if !ok && tool == "sftp" { // sftp's destination may be a bare [user@]host
		host = op[strings.LastIndexByte(op, '@')+1:]
		return host, host != ""
	}
	return host, ok
}

// splitRemote parses [user@]host:path, where host may be [ipv6]. ok is
// false for local paths: no colon, or a '/' before the first colon.
func splitRemote(op string) (host, path string, ok bool) {
	colon := strings.IndexByte(op, ':')
	if colon <= 0 {
		return "", "", false
	}
	start := 0
	if at := strings.LastIndexByte(op[:colon], '@'); at >= 0 {
		start = at + 1
	}
	if strings.Contains(op[:start], "/") {
		return "", "", false
	}
	if start < len(op) && op[start] == '[' {
		end := strings.IndexByte(op[start:], ']')
		if end < 0 || start+end+1 >= len(op) || op[start+end+1] != ':' {
			return "", "", false
		}
		return op[start+1 : start+end], op[start+end+2:], true
	}
	if strings.Contains(op[:colon], "/") {
		return "", "", false
	}
	host = op[start:colon]
	return host, op[colon+1:], host != ""
}

func uriHost(op string) (string, bool) {
	rest := op[strings.Index(op, "://")+3:]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		rest = rest[at+1:]
	}
	if strings.HasPrefix(rest, "[") {
		if end := strings.IndexByte(rest, ']'); end > 0 {
			return rest[1:end], true
		}
		return "", false
	}
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		rest = rest[:i]
	}
	return rest, rest != ""
}

// Quote renders argv as a command line that can be pasted into a shell.
func Quote(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = sshctl.ShellQuote(a)
	}
	return strings.Join(q, " ")
}
