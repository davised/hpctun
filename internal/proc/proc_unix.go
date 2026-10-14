//go:build !windows

package proc

import (
	"errors"
	"time"
)

// lookup is not needed outside Windows: tunnels there ride a shared master,
// and jump tunnels are checked by command line (probe.ProcessCommand).
func lookup(int) (Info, bool) { return Info{}, false }

// terminate is not supported outside Windows: down ends a tunnel's ssh
// through the master it was opened with, not by a verified signal to a pid.
func terminate(int, time.Time) error { return errors.New("terminate is not supported outside Windows") }
