package prompt

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// reviewScreen is the ssh review's full-screen question with a body of n
// numbered lines, sized to width x rows.
func reviewScreen(t *testing.T, n, rows int) (*screen, *bool, func(tea.Msg)) {
	t.Helper()
	var lines []string
	for i := 1; i <= n; i++ {
		lines = append(lines, fmt.Sprintf("+diff line %02d", i))
	}
	v := new(bool)
	c := huh.NewConfirm().Title("Apply these changes?").
		Description("Above are the changes to your files. Nothing is written until the end.\nChoose No to leave your ssh config alone.").
		Value(v)
	form := huh.NewForm(huh.NewGroup(c)).WithShowHelp(false).WithKeyMap(keyMap())
	m := &screen{form: form, heading: "hpctun init", full: true, title: "Apply these changes?", answer: func() string { return "" },
		ctx: Context{Recap: []Line{{"Gateway", "shell"}, {"Transfer", "files"}}, Body: strings.Join(lines, "\n")}}
	// Run it in a session, as hpctun does: each message's commands run and
	// a frame is drawn after every update.
	sess := &session{done: make(chan struct{}, 1)}
	send := func(msg tea.Msg) { feed(t, sess, msg) }
	send(tea.WindowSizeMsg{Width: 100, Height: rows})
	send(askMsg{m})
	return m, v, send
}

func pressCode(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

// TestReviewFitsTallDiff covers a diff taller than the terminal: the frame
// fits, the question and its keys stay on screen, and the diff shows where
// it is and how to scroll.
func TestReviewFitsTallDiff(t *testing.T) {
	m, _, _ := reviewScreen(t, 60, 24)
	got := m.View().Content
	if h := lipgloss.Height(got); h > 24 {
		t.Fatalf("frame is %d lines on a 24-line terminal:\n%s", h, got)
	}
	for _, want := range []string{"hpctun init", "Gateway", "Apply these changes?", "Yes", "No", "+diff line 01", "of 60", "scroll"} {
		if !strings.Contains(got, want) {
			t.Errorf("frame lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "+diff line 60") {
		t.Errorf("the last line fits a 24-line terminal only if the diff was not trimmed:\n%s", got)
	}
}

// TestReviewScrollsDiff covers PgDn/PgUp and the arrow keys moving the diff
// while the question keeps its answer.
func TestReviewScrollsDiff(t *testing.T) {
	m, _, send := reviewScreen(t, 60, 24)
	for range 10 {
		send(pressCode(tea.KeyPgDown))
	}
	got := m.View().Content
	if !strings.Contains(got, "+diff line 60") || strings.Contains(got, "+diff line 01") {
		t.Fatalf("after PgDn to the end:\n%s", got)
	}
	if !strings.Contains(got, "of 60") || lipgloss.Height(got) > 24 {
		t.Fatalf("status or height after scrolling:\n%s", got)
	}
	send(pressCode(tea.KeyUp))
	if got := m.View().Content; strings.Contains(got, "+diff line 60") {
		t.Fatalf("up did not scroll back one line:\n%s", got)
	}
	for range 10 {
		send(pressCode(tea.KeyPgUp))
	}
	if got := m.View().Content; !strings.Contains(got, "+diff line 01") {
		t.Fatalf("PgUp did not return to the top:\n%s", got)
	}
	if m.done {
		t.Fatal("scrolling answered the question")
	}
}

// TestReviewKeysStillAnswer: scrolling keys do not take the question's own
// keys; y still answers Yes.
func TestReviewKeysStillAnswer(t *testing.T) {
	m, v, send := reviewScreen(t, 60, 24)
	send(pressCode(tea.KeyPgDown))
	send(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if !*v || !m.done {
		t.Fatalf("y after scrolling: value %v done %v", *v, m.done)
	}
}

// TestReviewShortDiffUnchanged: a diff that fits is shown whole, with no
// scroll status or key.
func TestReviewShortDiffUnchanged(t *testing.T) {
	m, _, _ := reviewScreen(t, 5, 40)
	got := m.View().Content
	if !strings.Contains(got, "+diff line 05") || strings.Contains(got, "of 5") || strings.Contains(got, "scroll") {
		t.Fatalf("short diff:\n%s", got)
	}
}

// TestReviewUnknownHeightShowsAll: before the terminal reports its size,
// the whole diff is shown, as before.
func TestReviewUnknownHeightShowsAll(t *testing.T) {
	m, _, _ := reviewScreen(t, 60, 0)
	if got := m.View().Content; !strings.Contains(got, "+diff line 60") {
		t.Fatalf("unknown height:\n%s", got)
	}
}
