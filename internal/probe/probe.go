// Package probe observes which local TCP ports are listening or connected, and which process owns them: with lsof and ps on macOS and Linux, and Windows' TCP table on Windows.
package probe

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"hpctun/internal/sshctl"
)

// Listener is a process listening on a local TCP port.
type Listener struct {
	Port    int
	PID     int
	Command string
}

// Snapshot is the listening and connected ports at one moment.
type Snapshot struct {
	Listeners map[int]Listener // by local port
	Conns     map[int]int      // established connections by local port
}

var ErrNoLsof = errors.New("lsof not found in PATH; hpctun needs it to see which ports are open")

// ParseListeners parses `lsof -FpcPn` output. IPv4 and IPv6 listeners on the
// same port collapse to the first one seen.
func ParseListeners(out []byte) map[int]Listener {
	ls := map[int]Listener{}
	var pid int
	var cmd string
	for line := range strings.SplitSeq(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
			cmd = ""
		case 'c':
			cmd = line[1:]
		case 'n':
			if strings.Contains(line, "->") {
				continue
			}
			port, ok := portOf(line[1:])
			if !ok {
				continue
			}
			if _, seen := ls[port]; !seen {
				ls[port] = Listener{Port: port, PID: pid, Command: cmd}
			}
		}
	}
	return ls
}

// ParseEstablished parses `lsof -Fpn` output for established connections and
// counts distinct sockets by local port. A socket shared by several file
// descriptors of one process counts once.
func ParseEstablished(out []byte) map[int]int {
	conns := map[int]int{}
	seen := map[string]bool{}
	var pid string
	for line := range strings.SplitSeq(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid = line[1:]
		case 'n':
			local, _, ok := strings.Cut(line[1:], "->")
			if !ok {
				continue
			}
			key := pid + " " + line[1:]
			if seen[key] {
				continue
			}
			seen[key] = true
			if port, ok := portOf(local); ok {
				conns[port]++
			}
		}
	}
	return conns
}

func portOf(addr string) (int, bool) {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return 0, false
	}
	p, err := strconv.Atoi(addr[i+1:])
	return p, err == nil
}

// ProcessCommand returns pid's full command line; ok is false if no such
// process exists.
func ProcessCommand(ctx context.Context, r sshctl.Runner, pid int) (string, bool, error) {
	res, err := r.Run(ctx, "ps", "-o", "command=", "-p", strconv.Itoa(pid))
	if err != nil {
		return "", false, err
	}
	if res.ExitCode != 0 {
		return "", false, nil
	}
	return strings.TrimSpace(string(res.Stdout)), true, nil
}
