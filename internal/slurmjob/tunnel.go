package slurmjob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// stateDir is the cluster scripts' S (spec: "Files").
const stateDir = `S=${XDG_STATE_HOME:-$HOME/.local/state}/hpctun/vscode; `

// WatchScript follows job $1's log on the gateway (same home as the node).
// It forwards complete new lines as "@log <line>" on stderr every 2
// seconds, and writes "@alive" after each poll: once hpctun is gone that
// write fails and the script stops. Every 5th poll it checks squeue; a job
// squeue no longer lists ends the watch with "@gone <state>" when sacct
// names an end state, or after 12 such checks with "@lost <state>" (a busy
// slurmctld, as in the ensure script). A log that has not appeared after $2
// polls is "@nolog": a job hpctun started before tunnels; $2 = 0 never
// reports it (a job just submitted, whose log may be slow to appear). One
// line, no single quote, no "!".
const WatchScript = stateDir + `f="$S/job-$1.log"; ev() { printf "@%s\n" "$*" >&2; }; ` +
	`for o in "$S"/job-*.log; do [ "$o" = "$f" ] || rm -f "$o"; done; ` +
	`n=0; i=0; miss=0; while :; do i=$((i+1)); ` +
	`if [ -f "$f" ]; then m=$(wc -l <"$f" | tr -d " "); if [ "$m" -gt "$n" ]; then sed -n "$((n+1)),${m}p" "$f" | while IFS= read -r l; do printf "@log %s\n" "$l" >&2; done; n=$m; fi; ` +
	`elif [ "$2" -gt 0 ] && [ $i -ge "$2" ]; then ev nolog; exit 1; fi; ` +
	`if [ $((i % 5)) -eq 0 ]; then st=$(squeue -h -j "$1" -o %T 2>/dev/null); case $st in RUNNING) miss=0;; "") ` +
	`s=$(sacct -X -n -P -j "$1" --format=State 2>/dev/null | sed -n 1p); case $s in ""|PENDING|RUNNING|CONFIGURING|REQUEUED|RESIZING|SUSPENDED) ` +
	`miss=$((miss+1)); [ $miss -lt 12 ] || { ev "lost ${s:-UNKNOWN}"; exit 1; };; *) ev "gone $s"; exit 1;; esac;; ` +
	`*) ev "gone $st"; exit 1;; esac; fi; ` +
	`ev alive || exit 1; sleep 2; done`

// tunnelStatusScript prints job $1's last Tunnel: line, exiting 3 when the
// job has no log.
const tunnelStatusScript = stateDir + `f="$S/job-$1.log"; [ -f "$f" ] || exit 3; grep "Tunnel:" "$f" | tail -n 1`

var (
	codeRE       = regexp.MustCompile(`open the page (\S+) and enter the code (\S+) to authenticate|log into (\S+) and use code (\S+)`)
	tunnelRE     = regexp.MustCompile(`Tunnel:\s+(\S+)`)
	tunnelNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`) // a tunnel name hpctun accepts
	renamedRE    = regexp.MustCompile(`is already taken, using a random name instead`)
	// ErrNoLog: the job has no log, so it was not started by this hpctun.
	ErrNoLog = errors.New("the job has no log")
)

// LogEvent is one line of the job log, as ParseLogLine reads it.
type LogEvent struct {
	Kind   string // "login", "code", "ready", "renamed", "error", "warn", "cli", "cache", "step"; "" for any other line
	URL    string // code: where to sign in
	Code   string // code: the device code
	Name   string // ready: the tunnel's name
	What   string // error, warn: the step, e.g. "cli"
	Reason string // error, warn
	Text   string // cli, cache: the rest; step: start, setup, download or tunnel; "": the whole line
}

// ParseLogLine reads one line of the job log: the node script's "@kind"
// markers and the CLI's own sign-in and Tunnel: lines.
func ParseLogLine(line string) LogEvent {
	line = strings.TrimRight(line, "\r")
	if rest, ok := strings.CutPrefix(line, "@"); ok {
		kind, arg, _ := strings.Cut(rest, " ")
		switch kind {
		case "login":
			return LogEvent{Kind: kind}
		case "error", "warn":
			what, reason, _ := strings.Cut(arg, " ")
			return LogEvent{Kind: kind, What: what, Reason: reason}
		case "cli", "cache", "step":
			return LogEvent{Kind: kind, Text: arg}
		}
	}
	if m := codeRE.FindStringSubmatch(line); m != nil {
		if m[1] != "" {
			return LogEvent{Kind: "code", URL: m[1], Code: m[2]}
		}
		return LogEvent{Kind: "code", URL: m[3], Code: m[4]}
	}
	if m := tunnelRE.FindStringSubmatch(line); m != nil && tunnelNameRE.MatchString(m[1]) {
		return LogEvent{Kind: "ready", Name: m[1]}
	}
	if renamedRE.MatchString(line) {
		return LogEvent{Kind: "renamed"}
	}
	return LogEvent{Text: line}
}

// Watch runs WatchScript for job id, calling on for each line of its log
// until on returns true (then Watch returns nil), the job ends, or ctx
// ends. A log still missing after nologPolls polls (2 s apart) gives
// ErrNoLog; 0 waits for it as long as the job runs. Other stderr lines
// (ssh's own) go to stderr.
func (c Client) Watch(ctx context.Context, id string, nologPolls int, on func(LogEvent) bool, stderr io.Writer) error {
	if !digits.MatchString(id) {
		return fmt.Errorf("bad job id %q", id)
	}
	if nologPolls < 0 {
		return fmt.Errorf("bad poll limit %d", nologPolls)
	}
	run, stop := context.WithCancel(ctx)
	defer stop()
	done := false
	var gone, lost string
	var nolog bool
	w := &eventWriter{pass: stderr, on: func(kind, rest string) {
		if done {
			return
		}
		switch kind {
		case "log":
			if on(ParseLogLine(rest)) {
				done = true
				stop()
			}
		case "gone":
			gone = rest
		case "lost":
			lost = rest
		case "nolog":
			nolog = true
		}
	}}
	var opts []string
	if c.Multiplexing {
		opts = ownConnection
	}
	_, code, err := c.Exec(run, c.remoteWith(opts, WatchScript, id, strconv.Itoa(nologPolls)), "", w)
	w.flush()
	switch {
	case done:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		return err
	case nolog:
		return ErrNoLog
	case gone != "":
		return &GoneError{ID: id, State: strings.Fields(gone)[0]}
	case lost != "":
		return lostError(c.Gateway, id, strings.TrimSpace(lost))
	}
	return fmt.Errorf("watching job %s on %s failed (exit status %d)", id, c.Gateway, code)
}

// TunnelName reads job id's log once: the tunnel's name and whether the
// CLI has printed it (ready). A job with no log gives ErrNoLog.
func (c Client) TunnelName(ctx context.Context, id string) (string, bool, error) {
	if !digits.MatchString(id) {
		return "", false, fmt.Errorf("bad job id %q", id)
	}
	out, code, err := c.Exec(ctx, c.remote(tunnelStatusScript, id), "", io.Discard)
	switch {
	case err != nil:
		return "", false, err
	case code == 3:
		return "", false, ErrNoLog
	case code != 0 && code != 1: // grep finds nothing: 1, still starting
		return "", false, fmt.Errorf("reading job %s's log on %s failed (exit status %d)", id, c.Gateway, code)
	}
	if e := ParseLogLine(lastLine(out)); e.Kind == "ready" {
		return e.Name, true, nil
	}
	return "", false, nil
}
