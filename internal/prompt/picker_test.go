package prompt

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// pickScreen is init's full-screen host question with n hosts, sized to a
// terminal of rows lines (0: the size is not known yet).
func pickScreen(t *testing.T, n, rows int) (*screen, func(tea.Msg)) {
	t.Helper()
	q := SelectQ{Title: "Which host is the gateway?", Description: "These are the hosts in your ssh config."}
	for i := 1; i <= n; i++ {
		q.Options = append(q.Options, Option{Label: fmt.Sprintf("host%02d", i), Value: fmt.Sprintf("h%d", i)})
	}
	v := new(string)
	p := newPicker(q, v)
	form := huh.NewForm(huh.NewGroup(p.sel)).WithShowHelp(false).WithKeyMap(keyMap())
	m := &screen{form: form, pick: p, heading: "hpctun init", full: true, title: q.Title, answer: func() string { return "" },
		ctx: Context{Recap: []Line{{"Cluster", "osu"}}}}
	sess := &session{done: make(chan struct{}, 1)}
	send := func(msg tea.Msg) { feed(t, sess, msg) }
	if rows > 0 {
		send(tea.WindowSizeMsg{Width: 100, Height: rows})
	}
	send(askMsg{m})
	return m, send
}

// TestPickerShowsAllThatFit is GitHub issue 1: more than nine hosts, and a
// terminal with room for them all, shows them all.
func TestPickerShowsAllThatFit(t *testing.T) {
	m, _ := pickScreen(t, 14, 40)
	got := m.View().Content
	for _, want := range []string{"host01", "host14"} {
		if !strings.Contains(got, want) {
			t.Errorf("frame lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "of 14") {
		t.Errorf("a list shown whole has no count:\n%s", got)
	}
}

// TestPickerSaysThereAreMore: a list taller than the terminal fills it and
// says where the cursor is and that there are more.
func TestPickerSaysThereAreMore(t *testing.T) {
	m, send := pickScreen(t, 40, 24)
	got := m.View().Content
	if h := lipgloss.Height(got); h > 24 {
		t.Fatalf("frame is %d lines on a 24-line terminal:\n%s", h, got)
	}
	if !strings.Contains(got, "host12") || strings.Contains(got, "host40") {
		t.Errorf("a 24-line terminal should show more than ten hosts but not all forty:\n%s", got)
	}
	if !strings.Contains(got, "1 of 40") || !strings.Contains(got, "for more") {
		t.Errorf("frame does not say there are more:\n%s", got)
	}
	for range 3 {
		send(pressCode(tea.KeyDown))
	}
	if got := m.View().Content; !strings.Contains(got, "4 of 40") {
		t.Errorf("after three downs:\n%s", got)
	}
}

// TestPickerUnknownHeightSaysThereAreMore: before the terminal reports its
// size the list keeps its usual height, and still says there are more.
func TestPickerUnknownHeightSaysThereAreMore(t *testing.T) {
	m, _ := pickScreen(t, 40, 0)
	got := m.View().Content
	if strings.Contains(got, "host40") || !strings.Contains(got, "1 of 40") {
		t.Errorf("unknown height:\n%s", got)
	}
}
