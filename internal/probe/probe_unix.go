//go:build !windows

package probe

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"hpctun/internal/sshctl"
)

// Take runs lsof twice: once for listeners, once for established connections.
func Take(ctx context.Context, r sshctl.Runner) (Snapshot, error) {
	listen, err := lsof(ctx, r, "-nP", "-iTCP", "-sTCP:LISTEN", "-FpcPn")
	if err != nil {
		return Snapshot{}, err
	}
	est, err := lsof(ctx, r, "-nP", "-iTCP", "-sTCP:ESTABLISHED", "-Fpn")
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Listeners: ParseListeners(listen), Conns: ParseEstablished(est)}, nil
}

func lsof(ctx context.Context, r sshctl.Runner, args ...string) ([]byte, error) {
	res, err := r.Run(ctx, "lsof", args...)
	if errors.Is(err, exec.ErrNotFound) {
		return nil, ErrNoLsof
	}
	if err != nil {
		return nil, err
	}
	// lsof exits 1 when nothing matched.
	if res.ExitCode != 0 && res.ExitCode != 1 {
		return nil, fmt.Errorf("lsof exited %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return res.Stdout, nil
}
