package prompt

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
)

func newTestScreen(v *string) *screen {
	in := huh.NewInput().Title("Name").Value(v)
	form := huh.NewForm(huh.NewGroup(in)).WithShowHelp(false).WithKeyMap(keyMap())
	return &screen{form: form, heading: "tun test", full: true, title: "Name", answer: func() string { return *v }}
}

func press(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: rune(k[0]), Text: k}
}

// feed runs msg and every command it returns, recursively, the way the
// program would (batches expanded; tea.Quit/Interrupt reported), and draws
// after each update as the program does.
func feed(t *testing.T, s *session, msg tea.Msg) (quit bool) {
	t.Helper()
	_, cmd := s.Update(msg)
	s.View()
	return runCmds(t, s, cmd)
}

func runCmds(t *testing.T, s *session, cmd tea.Cmd) (quit bool) {
	if cmd == nil {
		return false
	}
	msg, ok := result(cmd)
	if !ok {
		return false
	}
	switch m := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range m {
			quit = runCmds(t, s, c) || quit
		}
	case tea.QuitMsg, tea.InterruptMsg:
		return true
	default:
		quit = feed(t, s, m) || quit
	}
	return quit
}

// result runs cmd and returns its message, or false for a command that
// waits, such as the cursor blink's timer, which would otherwise feed the
// session forever; nothing these tests check waits for one.
func result(cmd tea.Cmd) (tea.Msg, bool) {
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case m := <-ch:
		return m, true
	case <-time.After(200 * time.Millisecond):
		return nil, false
	}
}

func TestSessionKeepsFrameBetweenQuestions(t *testing.T) {
	s := &session{done: make(chan struct{}, 1)}
	var a, b string
	first := newTestScreen(&a)
	feed(t, s, tea.WindowSizeMsg{Width: 80, Height: 24})
	feed(t, s, askMsg{first})
	for _, k := range []string{"x", "enter"} {
		feed(t, s, press(k))
	}
	select {
	case <-s.done:
	default:
		t.Fatal("no result after enter")
	}
	if a != "x" || first.form.State != huh.StateCompleted {
		t.Fatalf("answer %q state %v", a, first.form.State)
	}
	v := s.View()
	if !v.AltScreen || !strings.Contains(v.Content, "tun test") || !strings.Contains(v.Content, "Name") {
		t.Fatalf("frame after answering: alt=%v %q", v.AltScreen, v.Content)
	}
	second := newTestScreen(&b)
	feed(t, s, askMsg{second})
	if second.width != 80 {
		t.Fatalf("width not carried over: %d", second.width)
	}
	feed(t, s, press("esc"))
	<-s.done
	if !second.back {
		t.Fatal("esc did not go back")
	}
}

func TestSessionCtrlCBetweenQuestions(t *testing.T) {
	s := &session{done: make(chan struct{}, 1)}
	var a string
	feed(t, s, askMsg{newTestScreen(&a)})
	feed(t, s, press("enter"))
	<-s.done
	if !feed(t, s, press("ctrl+c")) {
		t.Fatal("ctrl+c with no question open did not end the program")
	}
}

func TestSessionCtrlCAbortsQuestion(t *testing.T) {
	s := &session{done: make(chan struct{}, 1)}
	var a string
	sc := newTestScreen(&a)
	feed(t, s, askMsg{sc})
	if feed(t, s, press("ctrl+c")) {
		t.Fatal("ctrl+c on a question ended the program; it should end the question")
	}
	<-s.done
	if sc.form.State != huh.StateAborted {
		t.Fatalf("state %v", sc.form.State)
	}
}

// TestHuhAskerSharedProgram runs a full-screen HuhAsker's real program on a
// pipe: questions share it, Outside runs fn in it, and after Ctrl-C with no
// question open every later call returns ErrAborted instead of hanging or
// starting a new screen.
func TestHuhAskerSharedProgram(t *testing.T) {
	if raceEnabled {
		// Upstream race: after Ctrl-C, bubbletea v2.0.2's Program.shutdown(kill)
		// closes muesli/cancelreader v0.2.2's kqueue reader without waiting for
		// its read loop, whose kqueueCancelReader.wait still calls Fd() on the
		// files being closed (the cancel pipe, and the test's pipe).
		t.Skip("bubbletea/cancelreader shutdown race under -race")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	h := &HuhAsker{Heading: "tun test", FullScreen: true,
		opts: []tea.ProgramOption{tea.WithInput(r), tea.WithOutput(io.Discard), tea.WithoutSignalHandler()}}
	type answer struct {
		v   string
		err error
	}
	input := func(keys string) answer {
		got := make(chan answer, 1)
		go func() { v, err := h.Input(InputQ{Title: "Name"}); got <- answer{v, err} }()
		time.Sleep(50 * time.Millisecond) // let the question open before typing
		if _, err := w.WriteString(keys); err != nil {
			t.Fatal(err)
		}
		select {
		case a := <-got:
			return a
		case <-time.After(5 * time.Second):
			t.Fatal("no answer")
		}
		return answer{}
	}

	if a := input("x\r"); a.v != "x" || a.err != nil {
		t.Fatalf("first = %+v", a)
	}
	prog := h.prog
	if a := input("y\r"); a.v != "y" || a.err != nil || h.prog != prog {
		t.Fatalf("second = %+v, same program %v", a, h.prog == prog)
	}
	ran := false
	if err := h.Outside(func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("Outside = %v, ran %v", err, ran)
	}

	w.WriteString("\x03") // Ctrl-C while tun works between questions
	select {
	case err := <-h.exited:
		h.exited <- err // leave it for the asker to notice
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-C with no question open did not end the program")
	}
	ran = false
	if err := h.Outside(func() error { ran = true; return nil }); err != ErrAborted || ran {
		t.Fatalf("Outside after Ctrl-C = %v, ran %v", err, ran)
	}
	if _, err := h.Input(InputQ{Title: "Name"}); err != ErrAborted || h.prog != nil {
		t.Fatalf("Input after Ctrl-C = %v, program %v", err, h.prog)
	}
	h.Close()
	h.Close()
}

func TestPlainAsker(t *testing.T) {
	var p Plain
	want := errors.New("x")
	if err := p.Outside(func() error { return want }); err != want {
		t.Fatalf("Outside = %v", err)
	}
	p.Close()
}
