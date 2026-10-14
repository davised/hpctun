package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"charm.land/fang/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
)

func TestExitCode(t *testing.T) {
	base := errors.New("boom")
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"failed", failed(base), 1},
		{"usage", usage(base), 2},
		{"wrapped failed", fmt.Errorf("context: %w", failed(base)), 1},
		{"untyped is usage", base, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := exitCode(c.err); got != c.want {
				t.Fatalf("exitCode(%v) = %d, want %d", c.err, got, c.want)
			}
		})
	}
}

func TestExitErrorMessageAndUnwrap(t *testing.T) {
	base := errors.New("boom")
	err := failed(base)
	if err.Error() != "boom" {
		t.Fatalf("Error() = %q, want %q", err.Error(), "boom")
	}
	if !errors.Is(err, base) {
		t.Fatal("failed(base) does not unwrap to base")
	}
}

// TestErrorBoxKeepsFirstWord (tun-bny): fang title-cases an error's first
// word, which turned a config path into /Private/Tmp/.../Run.toml. Only a
// plain word is capitalized; a path, flag or quoted command stays as is.
func TestErrorBoxKeepsFirstWord(t *testing.T) {
	for _, c := range []struct{ msg, want string }{
		{"/tmp/Mixed-case/abc0Def/run.toml: requires [ssh]", "/tmp/Mixed-case/abc0Def/run.toml: requires [ssh]."},
		{"--time: not a duration", "--time: not a duration."},
		{"`tun code` needs key login", "`tun code` needs key login."},
		{"cannot find your home directory", "Cannot find your home directory."},
		{"context canceled", "Context canceled."},
	} {
		root := &cobra.Command{Use: "tun", RunE: func(*cobra.Command, []string) error { return errors.New(c.msg) }}
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(nil)
		_ = fang.Execute(context.Background(), root, fang.WithErrorHandler(errorHandler))
		if got := ansi.Strip(out.String()); !bytes.Contains([]byte(got), []byte(c.want)) {
			t.Errorf("%q: box %q", c.msg, got)
		}
	}
}
