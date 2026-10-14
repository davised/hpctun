// Package prompt holds hpctun's interactive questions: the tunnel form shared by
// init and add, the save prompt, and the init wizard. All questions go
// through Asker so the flows are testable without a terminal.
package prompt

import (
	"errors"
	"slices"
)

// ErrAborted means the user cancelled a prompt (Ctrl-C).
var ErrAborted = errors.New("aborted")

// ErrBack means the user pressed Esc to return to the previous question.
// Flows step back on it; a flow that has nothing to go back to returns it to
// its caller.
var ErrBack = errors.New("back")

// Option is one choice in a Select.
type Option struct {
	Label string
	Value string
}

// Line is one answered question in the recap shown above the next question.
// A blank Label continues the previous line's list.
type Line struct {
	Label string
	Value string
}

// Context is what the asker shows above the questions that follow: the
// answers so far and an optional notice, such as a failed connection test.
type Context struct {
	Recap  []Line
	Notice string
	// Body is preformatted text shown between the recap and the question,
	// such as the ssh config hpctun would write. Full-screen only.
	Body string
}

// With returns a copy of c with lines appended to the recap.
func (c Context) With(lines ...Line) Context {
	c.Recap = append(slices.Clip(c.Recap), lines...)
	return c
}

// InputQ is a text question.
type InputQ struct {
	Title       string
	Description string
	// Default is the answer when the user leaves the field blank; it shows
	// as the placeholder.
	Default string
	// Value pre-fills the field, for example with the previous answer when
	// the user comes back to the question.
	Value string
	// Suggestions complete the typed text with Tab.
	Suggestions []string
	// Validate runs on the final value (after Default is applied); the
	// question stays open until it passes.
	Validate func(string) error
}

// SelectQ is a choice among options.
type SelectQ struct {
	Title       string
	Description string
	Options     []Option
	Default     string // the Value of the option selected at first
}

// Asker asks one question at a time. Every question may return ErrBack or
// ErrAborted.
type Asker interface {
	Input(q InputQ) (string, error)
	Select(q SelectQ) (string, error)
	Confirm(title, description string, def bool) (bool, error)
	// SetContext replaces what is shown above the following questions.
	SetContext(c Context)
	// Outside runs fn with the terminal handed back: the normal screen, and
	// ssh free to prompt. A full-screen asker takes the terminal again
	// afterwards. fn must not ask questions through the same asker: a
	// full-screen asker runs fn on its program's goroutine (tea.Exec), so a
	// question from fn would deadlock.
	Outside(fn func() error) error
	// Close ends a full-screen asker's screen. Call it before printing;
	// safe to call more than once.
	Close()
}

// Plain provides Outside and Close for an Asker that keeps no terminal
// between questions.
type Plain struct{}

func (Plain) Outside(fn func() error) error { return fn() }
func (Plain) Close()                        {}

// step is one question (or a group of them) in a flow. It reports whether it
// asked anything, so going back skips steps that had nothing to ask.
type step func() (asked bool, err error)

// runSteps runs steps in order. When a step returns ErrBack, the most recent
// step that asked something runs again; its closure still holds the earlier
// answer, which it offers as the default. ErrBack from the first step that
// asks is returned to the caller.
func runSteps(steps ...step) error {
	var asked []int
	for i := 0; i < len(steps); {
		ok, err := steps[i]()
		if errors.Is(err, ErrBack) {
			if len(asked) == 0 {
				return ErrBack
			}
			i = asked[len(asked)-1]
			asked = asked[:len(asked)-1]
			continue
		}
		if err != nil {
			return err
		}
		if ok {
			asked = append(asked, i)
		}
		i++
	}
	return nil
}
