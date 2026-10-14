package detach

import (
	"os/exec"
	"testing"
)

func TestStartCallsNewCmdAndStarts(t *testing.T) {
	calls := 0
	cmd, err := Start(func() *exec.Cmd { calls++; return exec.Command("true") })
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || cmd.Process == nil {
		t.Fatalf("calls %d, process %v", calls, cmd.Process)
	}
	cmd.Wait()
}

func TestStartMissingProgram(t *testing.T) {
	if _, err := Start(func() *exec.Cmd { return exec.Command("hpctun-no-such-program") }); err == nil {
		t.Fatal("started a missing program")
	}
}
