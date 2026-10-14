package copycmd

import (
	"cmp"
	"context"
	"strings"
	"time"

	"hpctun/internal/sshctl"
)

// Completer produces Tab candidates for the copy commands.
type Completer struct {
	Runner  sshctl.Runner
	Route   Route
	Hosts   []string      // host names offered before a ':' is typed
	Timeout time.Duration // bound on the remote listing; 5s when zero
}

// Complete returns candidates for word. files reports that the shell should
// complete local file names instead. It never prompts: the remote listing
// runs with BatchMode, and any failure yields no candidates.
func (c Completer) Complete(ctx context.Context, word string) (cands []string, files bool) {
	if strings.HasPrefix(word, "-") {
		return nil, false // never treat an option as a host or a local path
	}
	if c.Route.drive(word) {
		return nil, true
	}
	host, path, remote := splitRemote(word)
	if !remote {
		if word != "" && strings.ContainsAny(word[:1], "/.~") {
			return nil, true
		}
		prefix, partial := "", word
		if at := strings.LastIndexByte(word, '@'); at >= 0 {
			prefix, partial = word[:at+1], word[at+1:]
		}
		for _, h := range c.Hosts {
			if strings.HasPrefix(h, partial) {
				cands = append(cands, prefix+h+":")
			}
		}
		return cands, len(cands) == 0
	}
	if strings.HasPrefix(path, ":") { // rsync daemon syntax
		return nil, false
	}
	userHost := word[:len(word)-len(path)-1]
	jump := ""
	if c.Route.Transfer != "" && c.Route.IsNode != nil && c.Route.IsNode(host) {
		jump = c.Route.Transfer
	}
	dest := strings.NewReplacer("[", "", "]", "").Replace(userHost)
	for _, l := range ListRemote(ctx, c.Runner, jump, dest, path, c.Timeout) {
		cands = append(cands, userHost+":"+l)
	}
	return cands, false
}

// ListRemote returns the entries on dest whose paths start with path, as
// `ls -dp` prints them (a directory ends in /), reaching dest through jump
// (ssh -J) when set. A leading ~/ is kept as typed: the listing runs
// relative to the home directory, since the remote shell would expand ~ to
// a path that no longer starts with the word being completed. It never
// prompts (BatchMode), is bounded by timeout (5s when zero), and gives
// nothing on any failure.
func ListRemote(ctx context.Context, r sshctl.Runner, jump, dest, path string, timeout time.Duration) []string {
	lead := ""
	if path == "~" {
		path = "~/"
	}
	if strings.HasPrefix(path, "~/") {
		lead, path = "~/", path[2:]
	}
	var args []string
	if jump != "" {
		args = append(args, "-J", jump)
	}
	// "--" keeps ssh from ever reading dest as an option.
	args = append(args, "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "--", dest, "ls -dp -- "+RemoteGlob(path))
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(timeout, 5*time.Second))
	defer cancel()
	res, err := r.Run(ctx, "ssh", args...)
	if err != nil || res.ExitCode != 0 {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.TrimRight(string(res.Stdout), "\n"), "\n") {
		if l != "" {
			out = append(out, lead+l)
		}
	}
	return out
}

// RemoteGlob quotes path for the remote shell and appends a bare '*', so the
// shell lists the entries starting with path. A leading ~/ stays unquoted so
// it still means the home directory.
func RemoteGlob(path string) string {
	lead := ""
	if path == "~" {
		path = "~/"
	}
	if strings.HasPrefix(path, "~/") {
		lead, path = "~/", path[2:]
	}
	if path == "" {
		return lead + "*"
	}
	return lead + "'" + strings.ReplaceAll(path, "'", `'\''`) + "'*"
}
