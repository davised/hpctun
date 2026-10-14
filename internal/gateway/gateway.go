// Package gateway decides how hpctun reaches a gateway's ssh master connection,
// and checks and starts that master.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hpctun/internal/sshctl"
)

// Mode says who configures the master connection.
type Mode string

const (
	ModeSSHConfig  Mode = "ssh-config" // the user's ~/.ssh/config sets ControlMaster/ControlPath
	ModeManaged    Mode = "managed"    // hpctun adds its own ControlMaster options
	ModeStandalone Mode = "standalone" // no master: each tunnel is its own ssh (Windows)
)

// maxSocketPath is the longest unix socket path macOS accepts: sun_path is
// 104 bytes including the terminating NUL.
const maxSocketPath = 103

// RetryDelay is the pause between checks while waiting for a new master.
var RetryDelay = 100 * time.Millisecond

var ErrSocketPathTooLong = errors.New("control socket path too long")

// Gateway is a resolved gateway destination.
type Gateway struct {
	Dest       string
	Mode       Mode
	GW         sshctl.GW
	SocketPath string   // control socket (from ssh -G in ssh-config mode)
	Hints      []string // advice about the user's ssh settings (ssh-config mode only)
}

// Status is the state of the master connection.
type Status struct {
	Running bool
	PID     int
	Stale   bool // a socket file exists but nothing is listening on it
}

// ParseSSHG parses `ssh -G` output into lowercased keys; the first value of a
// repeated key wins.
func ParseSSHG(out []byte) map[string]string {
	m := map[string]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		k = strings.ToLower(k)
		if _, seen := m[k]; !seen {
			m[k] = v
		}
	}
	return m
}

// ManagedSocketPath is hpctun's own control socket for dest.
func ManagedSocketPath(stateDir, dest string) string {
	sum := sha256.Sum256([]byte(dest))
	return filepath.Join(stateDir, "cm-"+hex.EncodeToString(sum[:])[:16])
}

// FromSSHG picks the mode from `ssh -G` settings and builds the ssh options.
func FromSSHG(dest, stateDir string, sshG map[string]string) *Gateway {
	cm, cp := sshG["controlmaster"], sshG["controlpath"]
	if cm != "" && cm != "false" && cp != "" && cp != "none" {
		return &Gateway{
			Dest: dest, Mode: ModeSSHConfig, SocketPath: cp, Hints: hints(sshG),
			GW: sshctl.GW{Dest: dest, ClientOpts: []string{"-o", "ControlMaster=no"}},
		}
	}
	sock := ManagedSocketPath(stateDir, dest)
	cpOpt := controlPathOpt(sock)
	return &Gateway{
		Dest: dest, Mode: ModeManaged, SocketPath: sock,
		GW: sshctl.GW{
			Dest:       dest,
			ClientOpts: []string{"-o", "ControlMaster=no", "-o", cpOpt},
			MasterOpts: []string{"-o", "ControlMaster=auto", "-o", cpOpt, "-o", "ControlPersist=yes",
				"-o", "ServerAliveInterval=60", "-o", "ServerAliveCountMax=3"},
		},
	}
}

// maxKeepaliveWindow is the longest time to notice a dead master that hpctun
// accepts without a hint.
const maxKeepaliveWindow = 30 * time.Minute

// hints reports ssh settings that make the user's own master a poor gateway.
// Managed mode passes its own options, so it never needs hints.
func hints(sshG map[string]string) []string {
	var hs []string
	if sshG["controlmaster"] == "true" {
		hs = append(hs, "ControlMaster yes: later connections through this host skip the shared connection and open their own; use ControlMaster auto")
	}
	interval, _ := strconv.Atoi(sshG["serveraliveinterval"])
	count, _ := strconv.Atoi(sshG["serveralivecountmax"])
	window := time.Duration(interval*count) * time.Second
	switch {
	case interval == 0:
		hs = append(hs, "no ServerAliveInterval: a dead connection is never noticed and tunnels hang; set ServerAliveInterval 30 or 60")
	case window > maxKeepaliveWindow:
		hs = append(hs, fmt.Sprintf("keepalives take %s to notice a dead connection (ServerAliveInterval %d x ServerAliveCountMax %d); tunnels hang that long before `hpctun up` can restore them",
			window, interval, count))
	}
	return hs
}

// controlPathOpt builds the full ControlPath=... option value for path. ssh
// percent-expands ControlPath and splits -o values like config lines, so the
// path is escaped for both.
func controlPathOpt(path string) string {
	p := strings.ReplaceAll(path, "%", "%%")
	if !strings.ContainsAny(p, " \t\"'\\") {
		return "ControlPath=" + p
	}
	return `ControlPath="` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p) + `"`
}

// Resolve runs `ssh -G dest` and builds the Gateway.
func Resolve(ctx context.Context, r sshctl.Runner, dest, stateDir string) (*Gateway, error) {
	res, err := r.Run(ctx, "ssh", "-G", "--", dest)
	if err != nil {
		return nil, fmt.Errorf("gateway %s: %w", dest, err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("gateway %s: ssh -G failed: %s", dest, strings.TrimSpace(string(res.Stderr)))
	}
	g := FromSSHG(dest, stateDir, ParseSSHG(res.Stdout))
	if g.Mode == ModeManaged && len(g.SocketPath) > maxSocketPath {
		return nil, fmt.Errorf("%w: %s is %d bytes, the limit is %d; set XDG_STATE_HOME to a shorter directory of your own in your home, such as ~/.hst",
			ErrSocketPathTooLong, g.SocketPath, len(g.SocketPath), maxSocketPath)
	}
	return g, nil
}

// AtSocket returns a copy of g that talks to the master at path, for closing
// tunnels opened through a socket the gateway no longer uses.
func (g *Gateway) AtSocket(path string) *Gateway {
	return &Gateway{
		Dest: g.Dest, Mode: g.Mode, SocketPath: path,
		GW: sshctl.GW{Dest: g.Dest, ClientOpts: []string{"-o", "ControlMaster=no", "-o", controlPathOpt(path)}},
	}
}

var masterPID = regexp.MustCompile(`Master running \(pid=(\d+)\)`)

// Check asks the master whether it is running.
func (g *Gateway) Check(ctx context.Context, r sshctl.Runner) (Status, error) {
	res, err := r.Run(ctx, "ssh", sshctl.CheckArgs(g.GW)...)
	if err != nil {
		return Status{}, err
	}
	out := string(res.Stderr) + string(res.Stdout)
	if res.ExitCode == 0 {
		if m := masterPID.FindStringSubmatch(out); m != nil {
			pid, _ := strconv.Atoi(m[1])
			return Status{Running: true, PID: pid}, nil
		}
		return Status{}, fmt.Errorf("gateway %s: unexpected ssh -O check output: %s", g.Dest, strings.TrimSpace(out))
	}
	if strings.Contains(out, "Control socket connect") {
		return Status{Stale: strings.Contains(out, "Connection refused")}, nil
	}
	return Status{}, fmt.Errorf("gateway %s: ssh -O check exited %d: %s", g.Dest, res.ExitCode, strings.TrimSpace(out))
}

// EnsureStarted starts the master if it is not running. ssh runs in the
// background with its stderr in logPath; prompts still reach the terminal.
func (g *Gateway) EnsureStarted(ctx context.Context, r sshctl.Runner, logPath string) (Status, error) {
	st, err := g.Check(ctx, r)
	if err != nil || st.Running {
		return st, err
	}
	if st.Stale {
		if err := removeSocket(g.SocketPath); err != nil {
			return st, fmt.Errorf("gateway %s: removing stale control socket: %w", g.Dest, err)
		}
	}
	res, err := r.Background(ctx, logPath, "ssh", sshctl.StartArgs(g.GW)...)
	if err != nil {
		return Status{}, err
	}
	if res.ExitCode != 0 {
		return Status{}, fmt.Errorf("gateway %s: could not connect (ssh exited %d):\n%s", g.Dest, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	for range 10 {
		st, err = g.Check(ctx, r)
		if err != nil || st.Running {
			return st, err
		}
		time.Sleep(RetryDelay)
	}
	if dir := filepath.Dir(g.SocketPath); dirMissing(dir) {
		return st, fmt.Errorf("gateway %s: ssh connected but could not create its control socket: %s does not exist; create it (mode 700), or run `hpctun ssh-config sync` if hpctun set up this host",
			g.Dest, dir)
	}
	return st, fmt.Errorf("gateway %s: ssh connected but no master is listening on %s; check ControlMaster and ControlPath for this host:\n%s",
		g.Dest, g.SocketPath, strings.TrimSpace(string(res.Stderr)))
}

func dirMissing(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}

func removeSocket(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSocket == 0 {
		return fmt.Errorf("%s is not a socket; not removing it", path)
	}
	return os.Remove(path)
}
