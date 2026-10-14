package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunChildPassesStdioAndSucceeds(t *testing.T) {
	var out bytes.Buffer
	if err := runChild([]string{"cat"}, strings.NewReader("hi\n"), &out, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hi\n" {
		t.Fatalf("out %q", out.String())
	}
}

func TestRunChildExitStatusBecomesTuns(t *testing.T) {
	err := runChild([]string{"sh", "-c", "exit 3"}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if exitCode(err) != 3 || err.Error() != "sh exited with status 3" {
		t.Fatalf("err = %v, code %d", err, exitCode(err))
	}
}

func TestRunChildMissingTool(t *testing.T) {
	err := runChild([]string{"tun-no-such-tool"}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if exitCode(err) != 2 || err.Error() != "tun-no-such-tool is not installed (it is not on PATH)" {
		t.Fatalf("err = %v, code %d", err, exitCode(err))
	}
}

// TestExitMessage: Windows reports crashes and Ctrl-C as NTSTATUS codes
// (0xC000013A is "terminated by Ctrl-C"); they read better in hex.
func TestExitMessage(t *testing.T) {
	if got := exitMessage("scp", 3); got != "scp exited with status 3" {
		t.Errorf("got %q", got)
	}
	if got := exitMessage("scp", 0xC000013A); got != "scp exited with status 0xC000013A" {
		t.Errorf("got %q", got)
	}
}
