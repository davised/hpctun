package prompt

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync/atomic"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// HuhAsker asks questions with huh, one single-field form per question,
// wrapped in a screen that adds a heading, the recap of answers so far and a
// key help footer, and turns Esc into ErrBack. A full-screen asker runs every
// question in one program, so the screen never drops back to the shell
// between them; Close ends it.
type HuhAsker struct {
	// Heading names the flow, for example "hpctun init".
	Heading string
	// FullScreen draws each question on the alternate screen, so a run of
	// questions replaces itself instead of scrolling the terminal. Single
	// questions that follow printed output leave it off.
	FullScreen bool

	ctx    Context
	prog   *tea.Program // full-screen: the program every question shares
	sess   *session
	exited chan error // receives prog's Run result
	// ended is set when prog ended on its own (Ctrl-C with no question open,
	// or a terminal error); every later question returns it, so going back
	// after a failed Outside does not start a new screen.
	ended error
	opts  []tea.ProgramOption // for tests: input and output other than the terminal
}

var _ Asker = (*HuhAsker)(nil)

// Whether the terminal background is dark, and whether the terminal has said
// so. Until it answers, colors are chosen for a dark background, which most
// terminals have; the answer is kept for later questions so they draw right
// from the first frame.
var bgDark, bgKnown atomic.Bool

func init() { bgDark.Store(true) }

// selectHeight caps a Select's visible options; longer lists scroll.
const selectHeight = 10

func (h *HuhAsker) SetContext(c Context) { h.ctx = c }

func (h *HuhAsker) Input(q InputQ) (string, error) {
	v := q.Value
	in := huh.NewInput().Title(q.Title).Description(q.Description).Placeholder(q.Default).Value(&v).
		Validate(func(s string) error {
			if s == "" {
				s = q.Default
			}
			if q.Validate == nil {
				return nil
			}
			return q.Validate(s)
		})
	if len(q.Suggestions) > 0 {
		in.Suggestions(q.Suggestions)
	}
	answer := func() string { return cmp.Or(v, q.Default) }
	if err := h.run(in, q.Title, answer); err != nil {
		return "", err
	}
	if v == "" {
		v = q.Default
	}
	return v, nil
}

func (h *HuhAsker) Select(q SelectQ) (string, error) {
	v := q.Default
	opts := make([]huh.Option[string], len(q.Options))
	for i, o := range q.Options {
		opts[i] = huh.NewOption(o.Label, o.Value)
	}
	s := huh.NewSelect[string]().Title(q.Title).Description(q.Description).Options(opts...).Value(&v)
	if len(opts) > selectHeight {
		// Height counts the title and description lines too.
		extra := 1
		if q.Description != "" {
			extra += lipgloss.Height(q.Description)
		}
		s.Height(selectHeight + extra)
	}
	answer := func() string {
		for _, o := range q.Options {
			if o.Value == v {
				return o.Label
			}
		}
		return v
	}
	if err := h.run(s, q.Title, answer); err != nil {
		return "", err
	}
	return v, nil
}

func (h *HuhAsker) Confirm(title, description string, def bool) (bool, error) {
	v := def
	answer := func() string {
		if v {
			return "Yes"
		}
		return "No"
	}
	if err := h.run(huh.NewConfirm().Title(title).Description(description).Value(&v), title, answer); err != nil {
		return false, err
	}
	return v, nil
}

// run asks f. title and answer describe the question once answered: an
// inline question leaves that one line behind in place of the form.
func (h *HuhAsker) run(f huh.Field, title string, answer func() string) error {
	form := huh.NewForm(huh.NewGroup(f)).
		WithShowHelp(false).
		WithTheme(huh.ThemeFunc(func(bool) *huh.Styles { return theme(bgDark.Load()) })).
		WithKeyMap(keyMap())
	m := &screen{form: form, heading: h.Heading, ctx: h.ctx, full: h.FullScreen, title: title, answer: answer}
	if h.FullScreen {
		return h.ask(m) // SubmitCmd and CancelCmd stay nil: the session keeps running
	}
	form.SubmitCmd = tea.Quit
	form.CancelCmd = tea.Interrupt
	m.finish = tea.Quit
	_, err := tea.NewProgram(m).Run()
	// The cursor is on the last line of the final frame; go back to the line
	// after the answer (or to where the question began) and clear.
	up := m.height - 1
	if !m.back && form.State == huh.StateCompleted {
		up--
	}
	if up > 0 {
		fmt.Fprintf(os.Stdout, "\x1b[%dA", up)
	}
	fmt.Fprint(os.Stdout, "\r\x1b[J")
	switch {
	case m.back:
		return ErrBack
	case form.State == huh.StateAborted || errors.Is(err, tea.ErrInterrupted):
		return ErrAborted
	}
	return err
}

// ask shows m in the shared program, starting it on the first question, and
// waits for the answer.
func (h *HuhAsker) ask(m *screen) error {
	if h.ended != nil {
		return h.ended
	}
	if h.prog == nil {
		h.sess = &session{done: make(chan struct{}, 1)}
		h.prog = tea.NewProgram(h.sess, h.opts...)
		h.exited = make(chan error, 1)
		go func() { _, err := h.prog.Run(); h.exited <- err }()
	}
	// Send returns at once if the program has already ended; exited then
	// has its result.
	h.prog.Send(askMsg{m})
	select {
	case <-h.sess.done:
	case err := <-h.exited:
		return h.end(err)
	}
	switch {
	case m.back:
		return ErrBack
	case m.form.State == huh.StateAborted:
		return ErrAborted
	}
	return nil
}

// end records that the shared program ended on its own with err (from Run)
// and returns what the question that noticed should: ErrAborted for Ctrl-C
// while hpctun was working between questions, else the terminal error.
func (h *HuhAsker) end(err error) error {
	h.prog = nil
	if err == nil || errors.Is(err, tea.ErrInterrupted) {
		err = ErrAborted
	}
	h.ended = err
	return err
}

// Outside runs fn with the terminal released. The program does the release
// and restore itself (tea.Exec), so a Ctrl-C that ends it in the meantime
// cannot race with them; fn then does not run.
func (h *HuhAsker) Outside(fn func() error) error {
	if h.ended != nil {
		return h.ended
	}
	if h.prog == nil {
		return fn()
	}
	res := make(chan error, 1)
	h.prog.Send(outsideMsg{fn: fn, res: res})
	select {
	case err := <-res:
		return err
	case err := <-h.exited:
		return h.end(err)
	}
}

// Close ends the shared program, leaving the alternate screen.
func (h *HuhAsker) Close() {
	if h.prog == nil {
		return
	}
	h.prog.Quit() // a no-op if it has already ended
	<-h.exited
	h.prog = nil
}

// session is the model of a full-screen HuhAsker's one program. Each
// question arrives as an askMsg; when it finishes, the session signals done
// and keeps drawing it until the next one arrives, so the screen never goes
// blank while hpctun works between questions.
type session struct {
	cur      *screen
	size     tea.WindowSizeMsg
	done     chan struct{} // one value per finished question; buffered
	reported bool
}

// askMsg hands the session its next question.
type askMsg struct{ s *screen }

// outsideMsg asks the session to run fn with the terminal released and to
// send the result on res (buffered): fn's error, else releasing or restoring
// the terminal's.
type outsideMsg struct {
	fn  func() error
	res chan error
}

// execFunc runs a func as a tea.ExecCommand. The func uses the terminal
// directly, so the streams the program offers are ignored.
type execFunc func() error

func (f execFunc) Run() error        { return f() }
func (execFunc) SetStdin(io.Reader)  {}
func (execFunc) SetStdout(io.Writer) {}
func (execFunc) SetStderr(io.Writer) {}

func (s *session) Init() tea.Cmd {
	if bgKnown.Load() {
		return nil
	}
	return tea.RequestBackgroundColor
}

func (s *session) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case askMsg:
		s.cur, s.reported = msg.s, false
		s.cur.width = s.size.Width
		cmds := []tea.Cmd{s.cur.form.Init()}
		if s.size.Width > 0 {
			size := s.size
			cmds = append(cmds, func() tea.Msg { return size })
		}
		return s, tea.Batch(cmds...)
	case outsideMsg:
		// The callback runs on the program's goroutine right after the
		// terminal is restored.
		return s, tea.Exec(execFunc(msg.fn), func(err error) tea.Msg {
			msg.res <- err
			return nil
		})
	case tea.WindowSizeMsg:
		s.size = msg
	case tea.BackgroundColorMsg:
		bgDark.Store(msg.IsDark())
		bgKnown.Store(true)
		return s, nil
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" && (s.cur == nil || s.cur.done) {
			return s, tea.Interrupt // no question open: hpctun is working; Ctrl-C still quits
		}
	}
	if s.cur == nil || s.cur.done {
		return s, nil
	}
	_, cmd := s.cur.Update(msg)
	if s.cur.done && !s.reported {
		s.reported = true
		s.done <- struct{}{}
	}
	return s, cmd
}

func (s *session) View() tea.View {
	if s.cur == nil {
		return tea.View{AltScreen: true}
	}
	return s.cur.View()
}

// screen is the Bubble Tea model around one huh form.
type screen struct {
	form    *huh.Form
	heading string
	ctx     Context
	full    bool
	title   string
	answer  func() string
	width   int
	rows    int // the terminal's height; 0 until it is known
	height  int // of the last frame drawn
	offset  int // first body line shown when the body is taller than its room
	room    int // body lines the last frame had room for; 0 when all of it fit
	back    bool
	done    bool
	finish  tea.Cmd // what Esc returns: tea.Quit inline, nil in a session
	body    string  // the full-screen frame above the footer, kept for after the answer
}

func (m *screen) Init() tea.Cmd {
	if bgKnown.Load() {
		return m.form.Init()
	}
	return tea.Batch(m.form.Init(), tea.RequestBackgroundColor)
}

func (m *screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		bgDark.Store(msg.IsDark())
		bgKnown.Store(true)
	case tea.WindowSizeMsg:
		m.width, m.rows = msg.Width, msg.Height
	case tea.KeyPressMsg:
		if msg.String() == "esc" && !fieldUsesEsc(m.form) {
			m.back, m.done = true, true
			return m, m.finish
		}
		if m.scroll(msg.String()) {
			return m, nil
		}
	}
	f, cmd := m.form.Update(msg)
	m.form = f.(*huh.Form)
	if m.form.State != huh.StateNormal {
		m.done = true
	}
	return m, cmd
}

// fieldUsesEsc reports whether the focused field has its own use for Esc
// right now, such as a Select setting or clearing its filter.
func fieldUsesEsc(f *huh.Form) bool { return fieldUses(f, "esc") }

// fieldUses reports whether the focused field binds k right now.
func fieldUses(f *huh.Form, k string) bool {
	return slices.ContainsFunc(f.KeyBinds(), func(b key.Binding) bool {
		return b.Enabled() && slices.Contains(b.Keys(), k)
	})
}

// scroll moves a body taller than its room (the ssh review's diff) for k:
// PgUp/PgDn by a page, and the arrows by a line when the field does not use
// them. It reports whether k was a scroll key.
func (m *screen) scroll(k string) bool {
	if m.room == 0 {
		return false
	}
	step := 0
	switch k {
	case "pgup":
		step = -max(m.room-1, 1)
	case "pgdown":
		step = max(m.room-1, 1)
	case "up", "down":
		if fieldUses(m.form, k) {
			return false
		}
		step = map[string]int{"up": -1, "down": 1}[k]
	default:
		return false
	}
	m.offset += step // View clamps it
	return true
}

// bodyView is the body as it fits in rows lines: whole, or a window at
// offset with a status line. It sets room (0 when the body fits).
func (m *screen) bodyView(st chrome, rows int) string {
	lines := strings.Split(m.ctx.Body, "\n")
	if m.rows == 0 || len(lines) <= rows {
		m.room, m.offset = 0, 0
		return st.body.Render(m.ctx.Body)
	}
	m.room = max(rows-1, 3) // one line for the status
	m.offset = min(max(m.offset, 0), len(lines)-m.room)
	end := m.offset + m.room
	status := fmt.Sprintf("lines %d-%d of %d · pgup/pgdn or ↑/↓ to scroll", m.offset+1, end, len(lines))
	return st.body.Render(strings.Join(lines[m.offset:end], "\n")) + "\n" + st.label.Render("  "+status)
}

func (m *screen) View() tea.View {
	var v tea.View
	v.AltScreen = m.full
	st := newChrome(bgDark.Load())
	if m.done {
		if m.full {
			// The session keeps showing the question while hpctun works on
			// the answer; the footer's keys no longer apply.
			v.SetContent(m.body + "\n\n" + st.label.Render(" …"))
			return v
		}
		// The inline renderer only redraws a final frame correctly when it
		// is as tall as the one before; run removes the blank lines after
		// the program ends.
		lines := make([]string, max(m.height, 1))
		if !m.back && m.form.State == huh.StateCompleted {
			lines[0] = st.label.Render(m.title) + " " + m.answer()
		}
		v.SetContent(strings.Join(lines, "\n"))
		return v
	}
	var top, bottom []string
	if m.full {
		if m.heading != "" {
			top = append(top, st.heading.Render(m.heading), "")
		}
		if r := m.recap(st); r != "" {
			top = append(top, r, "")
		}
	}
	if m.ctx.Notice != "" {
		bottom = append(bottom, st.notice.Width(max(m.width-2, 20)).Render(m.ctx.Notice), "")
	}
	bottom = append(bottom, m.form.View())
	parts := top
	if m.full && m.ctx.Body != "" {
		// The body gets what the rest of the frame leaves: the recap and
		// question above and below it, its blank line, and the footer.
		used := lipgloss.Height(strings.Join(slices.Concat(top, bottom), "\n")) + 1 + 2 + lipgloss.Height(m.footer(st))
		parts = append(parts, m.bodyView(st, m.rows-used), "")
	}
	parts = append(parts, bottom...)
	body := strings.Join(parts, "\n")
	if m.full {
		m.body = body
	}
	content := body + "\n\n" + m.footer(st)
	m.height = lipgloss.Height(content)
	v.SetContent(content)
	return v
}

func (m *screen) recap(st chrome) string {
	w := 0
	for _, l := range m.ctx.Recap {
		w = max(w, lipgloss.Width(l.Label))
	}
	var b strings.Builder
	for i, l := range m.ctx.Recap {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(" " + st.label.Width(w+2).Render(l.Label) + st.value.Render(l.Value))
	}
	return b.String()
}

func (m *screen) footer(st chrome) string {
	binds := m.form.KeyBinds()
	if !fieldUsesEsc(m.form) {
		binds = append(binds, key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")))
	}
	if m.room > 0 {
		binds = append(binds, key.NewBinding(key.WithKeys("pgup", "pgdown"), key.WithHelp("pgup/pgdn", "scroll")))
	}
	binds = append(binds, key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")))
	hm := help.New()
	hm.Styles = st.help
	hm.SetWidth(m.width)
	return hm.ShortHelpView(binds)
}

// chrome holds the styles for what screen draws around the form.
type chrome struct {
	heading, label, value, notice, body lipgloss.Style
	help                                help.Styles
}

func newChrome(dark bool) chrome {
	ld := lipgloss.LightDark(dark)
	muted := ld(lipgloss.Color("240"), lipgloss.Color("250"))
	var c chrome
	c.heading = lipgloss.NewStyle().Bold(true).Padding(0, 1).
		Foreground(lipgloss.Color("#FFFDF5")).Background(ld(lipgloss.Color("#5A56E0"), lipgloss.Color("#7571F9")))
	c.label = lipgloss.NewStyle().Foreground(muted)
	c.value = lipgloss.NewStyle()
	c.notice = lipgloss.NewStyle().Foreground(ld(lipgloss.Color("#C4153D"), lipgloss.Color("#FF7A99"))).PaddingLeft(1)
	c.body = lipgloss.NewStyle().PaddingLeft(2)
	c.help = help.Styles{
		ShortKey:       lipgloss.NewStyle().Bold(true),
		ShortDesc:      lipgloss.NewStyle().Foreground(muted),
		ShortSeparator: lipgloss.NewStyle().Foreground(muted),
		Ellipsis:       lipgloss.NewStyle().Foreground(muted),
	}
	return c
}

// theme is huh's Charm theme with brighter descriptions and placeholders,
// which are hard to read in the default.
func theme(dark bool) *huh.Styles {
	t := huh.ThemeCharm(dark)
	ld := lipgloss.LightDark(dark)
	t.Focused.Description = t.Focused.Description.Foreground(ld(lipgloss.Color("240"), lipgloss.Color("250")))
	t.Focused.TextInput.Placeholder = t.Focused.TextInput.Placeholder.Foreground(ld(lipgloss.Color("245"), lipgloss.Color("244")))
	// The Charm theme swaps its light and dark option colors, which leaves
	// unselected options unreadable on either background.
	t.Focused.Option = lipgloss.NewStyle()
	t.Focused.UnselectedOption = lipgloss.NewStyle()
	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Card = t.Blurred.Base
	t.Blurred.NextIndicator = lipgloss.NewStyle()
	t.Blurred.PrevIndicator = lipgloss.NewStyle()
	return t
}

// keyMap is huh's default with the keys that make sense for one question per
// form: Tab completes a suggestion instead of moving on, and the shift+tab
// "back" binding, which has no previous field to go to, is off (Esc goes
// back instead).
func keyMap() *huh.KeyMap {
	k := huh.NewDefaultKeyMap()
	k.Input.AcceptSuggestion = key.NewBinding(key.WithKeys("tab", "ctrl+e"), key.WithHelp("tab", "complete"))
	k.Input.Next = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "next"))
	k.Input.Prev.SetEnabled(false)
	k.Select.Next = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select"))
	k.Select.Prev.SetEnabled(false)
	k.Confirm.Prev.SetEnabled(false)
	return k
}
