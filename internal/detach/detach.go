// Package detach starts programs that outlive hpctun: tunnel ssh processes
// and browsers.
package detach

import "os/exec"

// Start starts the command newCmd builds so it outlives hpctun and is not
// reached by a Ctrl-C at hpctun's terminal. newCmd is called once per
// attempt, since an exec.Cmd cannot be started twice.
func Start(newCmd func() *exec.Cmd) (*exec.Cmd, error) { return start(newCmd) }
