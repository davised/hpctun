package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// runChild runs argv[0], found on PATH, with the given stdio and waits for
// it. It is how hpctun hands over to scp, rsync or sftp where it cannot replace
// itself (Windows). The child's non-zero exit status becomes hpctun's, and a
// missing program is reported as not installed. The child is not tied to a
// context: Ctrl-C reaches it straight from the console, while hpctun, whose
// interrupt fang's signal context catches, waits for it to finish.
func runChild(argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return usage(fmt.Errorf("%s is not installed (it is not on PATH)", argv[0]))
	}
	cmd := exec.Command(path, argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code := ee.ExitCode()
		if code < 1 {
			code = 1 // ended by a signal
		}
		return &exitError{code: code, err: errors.New(exitMessage(argv[0], code))}
	}
	return err
}

// exitMessage reports a tool's exit status. Windows NTSTATUS codes (a crash,
// or 0xC000013A for Ctrl-C) are shown in hex, as Windows documents them.
func exitMessage(tool string, code int) string {
	if code >= 0xC0000000 {
		return fmt.Sprintf("%s exited with status 0x%X", tool, code)
	}
	return fmt.Sprintf("%s exited with status %d", tool, code)
}
