// Package sshctltest provides a recording fake sshctl.Runner.
package sshctltest

import (
	"context"
	"fmt"
	"strings"

	"hpctun/internal/sshctl"
)

// Call is one recorded command.
type Call struct {
	Name       string
	Args       []string
	LogPath    string
	Background bool
	Start      bool // Start: started with Runner.Start
}

// Line is the command as one space-joined string, for matching in tests.
func (c Call) Line() string { return strings.Join(append([]string{c.Name}, c.Args...), " ") }

// Fake records every call and answers with Handler (zero Result if nil).
type Fake struct {
	Calls   []Call
	Handler func(Call) (sshctl.Result, error)
	// OnStart answers Start after Handler accepted the call; nil starts
	// nothing and returns pid 0.
	OnStart func(Call) (int, error)
}

func (f *Fake) Run(ctx context.Context, name string, args ...string) (sshctl.Result, error) {
	return f.do(Call{Name: name, Args: args})
}

func (f *Fake) Background(ctx context.Context, logPath, name string, args ...string) (sshctl.Result, error) {
	return f.do(Call{Name: name, Args: args, LogPath: logPath, Background: true})
}

func (f *Fake) Start(ctx context.Context, logPath, name string, args ...string) (int, error) {
	c := Call{Name: name, Args: args, LogPath: logPath, Start: true}
	res, err := f.do(c)
	if err != nil {
		return 0, err
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("%s: start refused by the fake (exit %d)", name, res.ExitCode)
	}
	if f.OnStart == nil {
		return 0, nil
	}
	return f.OnStart(c)
}

func (f *Fake) do(c Call) (sshctl.Result, error) {
	f.Calls = append(f.Calls, c)
	if f.Handler == nil {
		return sshctl.Result{}, nil
	}
	return f.Handler(c)
}

// Find returns the recorded calls whose Line contains sub.
func (f *Fake) Find(sub string) []Call {
	var out []Call
	for _, c := range f.Calls {
		if strings.Contains(c.Line(), sub) {
			out = append(out, c)
		}
	}
	return out
}
