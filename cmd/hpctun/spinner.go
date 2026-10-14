package main

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// spinner shows what hpctun code is waiting for: one line at the bottom of
// the terminal, redrawn ten times a second, with the current step and how
// long it has taken. Lines written through Writer go above it. A nil
// *spinner (output is not a terminal) does nothing, so callers need not
// check.
type spinner struct {
	mu     sync.Mutex
	out    io.Writer // the terminal the line is drawn on
	width  func() int
	msg    string
	since  time.Time
	frame  int
	shown  bool // the line is on the screen
	paused bool
	open   bool // a write left a line unfinished: do not draw under it
	stop   chan struct{}
	done   chan struct{}
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// newSpinner starts a spinner on hpctun's terminal: stderr, when stdout
// and stderr are both terminals (so all of hpctun's output goes through
// it), TERM is not dumb, and ssh shares one connection (on Windows each
// connection could ask for a password, which the line would draw over).
// a.spinTo, set by tests, is drawn on instead. nil: no spinner.
func (a *app) newSpinner() *spinner {
	out, width := a.spinTo, func() int { return 80 }
	if out == nil {
		if a.windows() || a.getenv("TERM") == "dumb" || a.stdout != io.Writer(os.Stdout) || a.stderr != io.Writer(os.Stderr) ||
			!term.IsTerminal(os.Stdout.Fd()) || !term.IsTerminal(os.Stderr.Fd()) {
			return nil
		}
		out, width = os.Stderr, func() int {
			if w, _, err := term.GetSize(os.Stderr.Fd()); err == nil && w > 0 {
				return w
			}
			return 80
		}
	}
	s := &spinner{out: out, width: width, since: time.Now(), stop: make(chan struct{}), done: make(chan struct{})}
	go s.loop()
	return s
}

func (s *spinner) loop() {
	defer close(s.done)
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.mu.Lock()
			s.frame++
			s.draw()
			s.mu.Unlock()
		}
	}
}

// draw writes the line; s.mu is held.
func (s *spinner) draw() {
	if s.msg == "" || s.paused || s.open {
		return
	}
	line := spinFrames[s.frame%len(spinFrames)] + " " + s.msg
	if d := time.Since(s.since); d >= 2*time.Second {
		line += fmt.Sprintf(" (%d:%02d)", int(d.Minutes()), int(d.Seconds())%60)
	}
	// The line must not wrap: \r only returns to the start of the last row.
	fmt.Fprint(s.out, "\r"+ansi.Truncate(line, s.width()-1, "…")+"\x1b[K")
	s.shown = true
}

// clear removes the line; s.mu is held.
func (s *spinner) clear() {
	if s.shown {
		fmt.Fprint(s.out, "\r\x1b[K")
		s.shown = false
	}
}

// Step makes msg the current step and restarts its clock.
func (s *spinner) Step(msg string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg != s.msg {
		s.msg, s.since = msg, time.Now()
	}
	s.draw()
}

// Pause removes the line until the returned func is called, for a prompt.
func (s *spinner) Pause() func() {
	if s == nil {
		return func() {}
	}
	s.mu.Lock()
	s.clear()
	s.paused = true
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.paused = false
		s.draw()
		s.mu.Unlock()
	}
}

// Stop removes the line for good.
func (s *spinner) Stop() {
	if s == nil {
		return
	}
	select {
	case <-s.stop:
		return
	default:
	}
	close(s.stop)
	<-s.done
	s.mu.Lock()
	s.clear()
	s.msg = ""
	s.mu.Unlock()
}

// Writer is w with the line taken away before each write and drawn again
// after it, once the write ends a line.
func (s *spinner) Writer(w io.Writer) io.Writer {
	if s == nil {
		return w
	}
	return spinWriter{s, w}
}

type spinWriter struct {
	s *spinner
	w io.Writer
}

func (sw spinWriter) Write(p []byte) (int, error) {
	s := sw.s
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clear()
	n, err := sw.w.Write(p)
	if len(p) > 0 {
		s.open = p[len(p)-1] != '\n'
	}
	s.draw()
	return n, err
}
