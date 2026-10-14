package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
)

// lockedBuffer is a bytes.Buffer safe to share with the spinner's ticker.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestSpinnerOffWithoutTerminal(t *testing.T) {
	a, out, _ := testApp(t, "")
	s := a.newSpinner()
	if s != nil {
		t.Fatal("spinner on a buffer")
	}
	// A nil spinner does nothing and passes writes through.
	s.Step("x")
	s.Pause()()
	s.Stop()
	s.Writer(out).Write([]byte("line\n"))
	if out.String() != "line\n" {
		t.Fatalf("%q", out.String())
	}
}

func TestSpinnerLine(t *testing.T) {
	a, _, _ := testApp(t, "")
	var term lockedBuffer
	a.spinTo = &term
	s := a.newSpinner()
	var out lockedBuffer
	w := s.Writer(&out)
	s.Step("downloading VS Code's CLI on chrom1")
	w.Write([]byte("First time on chrom1\n"))
	resume := s.Pause()
	if got := term.String(); !strings.HasSuffix(got, "\r\x1b[K") {
		t.Fatalf("pause leaves the line up: %q", got)
	}
	resume()
	s.Step(strings.Repeat("long ", 40))
	s.Stop()
	got := term.String()
	contains(t, got, "\r⠋ downloading VS Code's CLI on chrom1\x1b[K", "\r\x1b[K")
	if !strings.HasSuffix(got, "\r\x1b[K") {
		t.Errorf("stop leaves the line up: %q", got)
	}
	for _, l := range strings.Split(got, "\r") {
		if l = strings.TrimSuffix(l, "\x1b[K"); len([]rune(l)) > 79 {
			t.Errorf("line wider than the terminal: %q", l)
		}
	}
	if out.String() != "First time on chrom1\n" {
		t.Errorf("output %q", out.String())
	}
	s.Stop() // twice is fine
}

// TestSpinnerWaitsForLineEnd: a write that leaves a line open (a prompt,
// ssh's partial output) is not drawn over.
func TestSpinnerWaitsForLineEnd(t *testing.T) {
	a, _, _ := testApp(t, "")
	var term lockedBuffer
	a.spinTo = &term
	s := a.newSpinner()
	defer s.Stop()
	w := s.Writer(&term)
	w.Write([]byte("Password: "))
	n := len(term.String())
	s.Step("waiting")
	if got := term.String()[n:]; got != "" {
		t.Fatalf("drew under an open line: %q", got)
	}
	w.Write([]byte("\n"))
	contains(t, term.String()[n:], "waiting")
}

// TestCodeSpinnerSteps: hpctun code shows each step it waits on, from the
// key check to the tunnel, and the job's own lines still reach stdout.
func TestCodeSpinnerSteps(t *testing.T) {
	a, _, out, _ := codeApp(t, "", func(ctx context.Context, c remoteCall, stderr io.Writer) (string, int, error) {
		if strings.Contains(c.script(), isWatch) {
			io.WriteString(stderr, "@log @step start\n@log @step setup\n@log @cache /scratch/me/hpctun-vscode\n@log @step download\n@log @cli code 1.140.0\n@log @login\n"+
				"@log To sign in, use a web browser to open the page https://login.microsoft.com/device and enter the code AB12CD34 to authenticate.\n@log @step tunnel\n@log   ➜  Tunnel:   hpc-me\n")
			<-ctx.Done()
			return "", 255, ctx.Err()
		}
		return running(true)(ctx, c, stderr)
	})
	var term lockedBuffer
	a.spinTo = &term
	if err := runCmd(a, "code", "--no-open", "proj"); err != nil {
		t.Fatal(err)
	}
	contains(t, term.String(), "checking key login to shell", "checking proj on the cluster", "looking for your hpctun-code job",
		"waiting for Slurm to start job 42", "waiting for job 42's output on chrom1", "running your login setup on chrom1", "setting up on chrom1",
		"downloading VS Code's CLI on chrom1", "checking VS Code sign-in on chrom1", "starting VS Code's sign-in on chrom1",
		"waiting for you to sign in with code AB12CD34", "starting VS Code's tunnel on chrom1")
	if !strings.HasSuffix(term.String(), "\r\x1b[K") {
		t.Errorf("the line is left up: %q", term.String())
	}
	contains(t, out.String(), "submitted job 42", "First time on chrom1", "enter AB12CD34", "The job has")
	if a.spin != nil {
		t.Error("spinner kept after the command")
	}
}
