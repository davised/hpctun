package main

import (
	"io"
	"testing"
)

// TestStrayArgsExit2 pins that a stray positional argument is a usage error
// (exit 2) now that the root command has a RunE: `tun bogus` must fail
// validation instead of running status, and so must `tun status bogus`.
func TestStrayArgsExit2(t *testing.T) {
	for _, args := range [][]string{{"bogus"}, {"status", "bogus"}} {
		a, _, _ := testApp(t, "") // a fake runner: a regression here must not run real ssh
		root := newRootCmd(a)
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs(args)
		err := root.Execute()
		if err == nil {
			t.Fatalf("`tun %v` returned nil error, want a usage error", args)
		}
		if got := exitCode(err); got != 2 {
			t.Fatalf("`tun %v`: exitCode(%v) = %d, want 2", args, err, got)
		}
	}
}
