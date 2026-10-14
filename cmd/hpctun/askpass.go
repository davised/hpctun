package main

import (
	"errors"
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"hpctun/internal/askpass"
)

// newAskpassCmd is the program ssh runs as SSH_ASKPASS under --askpass: it
// shows ssh's prompt in a window and prints the answer for ssh.
func newAskpassCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:    "askpass PROMPT",
		Short:  "Ask for ssh's password in a window (run by ssh)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			prompt, extra := args[0], ""
			if askpass.SecondFactor(prompt) {
				extra = askpass.LogTail(a.getenv(askpass.EnvLog), 8)
			}
			look, run := a.lookPath, a.dialog
			if look == nil {
				look = exec.LookPath
			}
			if run == nil {
				run = runDialog
			}
			// What askpass prints on stderr lands in ssh's log, and from
			// there in the reason a login failed: one plain line, or
			// nothing when the user cancelled (ssh then says the login
			// was refused).
			answer, err := askpass.Show(askpass.ForPrompt(prompt, extra), a.goos, look, run)
			if errors.Is(err, askpass.ErrCancelled) {
				return quiet(1, err)
			}
			if err != nil {
				fmt.Fprintf(a.stderr, "hpctun askpass: %v\n", err)
				return quiet(1, err)
			}
			_, err = fmt.Fprintln(a.stdout, answer)
			return err
		},
	}
}

// runDialog runs a dialog program, returning its stdout and exit status.
func runDialog(argv []string) (string, int, error) {
	out, err := exec.Command(argv[0], argv[1:]...).Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode(), nil
	}
	return string(out), 0, err
}
