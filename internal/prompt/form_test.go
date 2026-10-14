package prompt

import (
	"errors"
	"fmt"
	"testing"

	"hpctun/internal/config"
)

// script is a fake Asker that answers questions in order and checks each
// answer against the validator, as huh would. "" accepts what the question
// shows (its pre-filled value, else its default), "<back>" presses Esc and
// "<abort>" presses Ctrl-C.
type script struct {
	t       *testing.T
	answers []string
	asked   []string
	ctx     Context           // the context the last question was asked under
	shown   []Context         // the context of every question
	selects []string          // the description of every Select
	inputs  map[string]string // the description of each Input, by title (the last one asked)
	outside int               // Outside calls
	// codeQ: the script answers CodeQuestion itself. Otherwise it is taken
	// at its default without using an answer, and passed through on the
	// way back when the answer before it was Esc, so scripts written
	// before the question still read as they did.
	codeQ    bool
	lastBack bool
}

func (s *script) Outside(fn func() error) error { s.outside++; return fn() }
func (s *script) Close()                        {}

func (s *script) next(title string) string {
	s.t.Helper()
	s.asked = append(s.asked, title)
	s.shown = append(s.shown, s.ctx)
	if len(s.answers) == 0 {
		s.t.Fatalf("unexpected question %q", title)
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a
}

func control(a string) error {
	switch a {
	case "<abort>":
		return ErrAborted
	case "<back>":
		return ErrBack
	}
	return nil
}

func (s *script) SetContext(c Context) { s.ctx = c }

func (s *script) Input(q InputQ) (string, error) {
	if s.inputs == nil {
		s.inputs = map[string]string{}
	}
	s.inputs[q.Title] = q.Description
	for {
		a := s.next(q.Title)
		s.lastBack = a == "<back>"
		if err := control(a); err != nil {
			return "", err
		}
		if a == "" {
			a = q.Value
		}
		if a == "" {
			a = q.Default
		}
		if q.Validate == nil || q.Validate(a) == nil {
			return a, nil
		}
		// invalid: huh would re-ask; the script supplies the next answer
	}
}

func (s *script) Select(q SelectQ) (string, error) {
	s.selects = append(s.selects, q.Description)
	a := s.next(q.Title)
	s.lastBack = a == "<back>"
	if err := control(a); err != nil {
		return "", err
	}
	if a == "" {
		a = q.Default
	}
	for _, o := range q.Options {
		if o.Value == a {
			return a, nil
		}
	}
	return "", fmt.Errorf("script answer %q is not an option for %q", a, q.Title)
}

func (s *script) Confirm(title, _ string, def bool) (bool, error) {
	if title == CodeQuestion && !s.codeQ {
		s.asked, s.shown = append(s.asked, title), append(s.shown, s.ctx)
		if s.lastBack {
			return false, ErrBack
		}
		return def, nil
	}
	a := s.next(title)
	s.lastBack = a == "<back>"
	switch a {
	case "y":
		return true, nil
	case "n":
		return false, nil
	case "":
		return def, nil
	default:
		if err := control(a); err != nil {
			return false, err
		}
		return false, fmt.Errorf("bad confirm answer %q", a)
	}
}

func (s *script) done() {
	s.t.Helper()
	if len(s.answers) != 0 {
		s.t.Fatalf("unused answers %q (asked %q)", s.answers, s.asked)
	}
}

func noneTaken(string) bool { return false }
func portsFree(int) error   { return nil }

func TestTunnelFormFullPortForward(t *testing.T) {
	s := &script{t: t, answers: []string{"port", "jup", "4001", "", "8888", "y", ""}}
	tn, err := TunnelForm(s, Preset{}, FormOpts{DefaultHost: "node1", NameTaken: noneTaken, PortTaken: portsFree, Full: true})
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	want := config.Tunnel{Name: "jup", Port: 4001, Host: "node1", RemotePort: 8888, Route: config.RouteJump}
	if tn != want {
		t.Fatalf("got %+v, want %+v", tn, want)
	}
}

func TestTunnelFormSocks(t *testing.T) {
	s := &script{t: t, answers: []string{"socks", "proxy", "5005", ""}}
	tn, err := TunnelForm(s, Preset{}, FormOpts{NameTaken: noneTaken, PortTaken: portsFree, Full: true})
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if tn != (config.Tunnel{Name: "proxy", Port: 5005, Route: config.RouteSocks}) {
		t.Fatalf("got %+v", tn)
	}
}

func TestTunnelFormRejectsBadAnswersThenAccepts(t *testing.T) {
	taken := func(n string) bool { return n == "dash" }
	busy := func(p int) error {
		if p == 8099 {
			return errors.New("used by dash")
		}
		return nil
	}
	s := &script{t: t, answers: []string{"port", "Bad Name", "dash", "web", "0", "8099", "8080", "web1", "", "n", ""}}
	tn, err := TunnelForm(s, Preset{}, FormOpts{NameTaken: taken, PortTaken: busy, Full: true})
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if tn != (config.Tunnel{Name: "web", Port: 8080, Host: "web1", RemotePort: 8080, Route: config.RouteMaster}) {
		t.Fatalf("got %+v", tn)
	}
}

func TestTunnelFormRequiresHostWithoutDefault(t *testing.T) {
	s := &script{t: t, answers: []string{"port", "a", "1234", "", "web1", "", "n", ""}}
	tn, err := TunnelForm(s, Preset{}, FormOpts{NameTaken: noneTaken, PortTaken: portsFree, Full: true})
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if tn.Host != "web1" {
		t.Fatalf("got %+v", tn)
	}
}

// With some fields preset (tun add jup --port 4005), only required-but-missing
// fields are asked; optional ones take their defaults.
func TestTunnelFormPartialPresetAsksOnlyMissingRequired(t *testing.T) {
	s := &script{t: t, answers: []string{"web1"}} // host: no default_host
	tn, err := TunnelForm(s, Preset{Name: "jup", Port: 4005}, FormOpts{NameTaken: noneTaken, PortTaken: portsFree})
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if tn != (config.Tunnel{Name: "jup", Port: 4005, Host: "web1", RemotePort: 4005, Route: config.RouteMaster}) {
		t.Fatalf("got %+v", tn)
	}
}

func TestSaveName(t *testing.T) {
	s := &script{t: t, answers: []string{""}}
	if n, err := SaveName(s, noneTaken); err != nil || n != "" {
		t.Fatalf("blank: %q %v", n, err)
	}
	s = &script{t: t, answers: []string{"dash", "web"}}
	if n, err := SaveName(s, func(n string) bool { return n == "dash" }); err != nil || n != "web" {
		t.Fatalf("got %q %v", n, err)
	}
	s.done()
}

func TestTunnelFormEscRevisitsPreviousAnswers(t *testing.T) {
	s := &script{t: t, answers: []string{
		"port", "jup", "<back>", // Esc at the port returns to the name...
		"",                                // ...pre-filled with jup
		"4001", "node1", "8888", "<back>", // Esc at the loopback question
		"", // remote port pre-filled with 8888
		"y", "",
	}}
	tn, err := TunnelForm(s, Preset{}, FormOpts{NameTaken: noneTaken, PortTaken: portsFree, Full: true})
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	want := config.Tunnel{Name: "jup", Port: 4001, Host: "node1", RemotePort: 8888, Route: config.RouteJump}
	if tn != want {
		t.Fatalf("got %+v, want %+v", tn, want)
	}
	if last := s.shown[len(s.shown)-1].Recap; len(last) != 1 || last[0].Value != "jup  port forward  localhost:4001 → node1" {
		t.Fatalf("recap = %+v", last)
	}
}

func TestTunnelFormBlankRemotePortFollowsLocalPort(t *testing.T) {
	s := &script{t: t, answers: []string{
		"port", "a", "4001", "h", "", "<back>", "<back>", "<back>",
		"4002", "", "", "n", "",
	}}
	tn, err := TunnelForm(s, Preset{}, FormOpts{NameTaken: noneTaken, PortTaken: portsFree, Full: true})
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if tn.Port != 4002 || tn.RemotePort != 4002 || tn.Host != "h" {
		t.Fatalf("got %+v", tn)
	}
}

func TestTunnelFormEscOnFirstQuestion(t *testing.T) {
	s := &script{t: t, answers: []string{"<back>"}}
	if _, err := TunnelForm(s, Preset{}, FormOpts{NameTaken: noneTaken, PortTaken: portsFree, Full: true}); !errors.Is(err, ErrBack) {
		t.Fatalf("err = %v", err)
	}
	// With the name preset, the first question asked is the port.
	s = &script{t: t, answers: []string{"<back>"}}
	if _, err := TunnelForm(s, Preset{Name: "x"}, FormOpts{NameTaken: noneTaken, PortTaken: portsFree}); !errors.Is(err, ErrBack) {
		t.Fatalf("err = %v", err)
	}
}

// The full form ends with an optional page question; blank leaves no url.
func TestTunnelFormAsksForPage(t *testing.T) {
	opts := FormOpts{Full: true, DefaultHost: "node1", NameTaken: noneTaken, PortTaken: portsFree}
	s := &script{t: t, answers: []string{"port", "jup", "8888", "", "", "n", "http://localhost:8888/lab"}}
	tun, err := TunnelForm(s, Preset{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if tun.URL != "http://localhost:8888/lab" {
		t.Fatalf("url = %q", tun.URL)
	}
	s = &script{t: t, answers: []string{"socks", "proxy", "5005", ""}}
	tun, err = TunnelForm(s, Preset{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	s.done()
	if tun.URL != "" || tun.Route != config.RouteSocks {
		t.Fatalf("got %+v", tun)
	}
}

// A preset url is kept and not asked.
func TestTunnelFormPresetURL(t *testing.T) {
	opts := FormOpts{DefaultHost: "node1", NameTaken: noneTaken, PortTaken: portsFree}
	tun, err := TunnelForm(&script{t: t}, Preset{Name: "chat", Port: 1080, Socks: true, URL: "https://chat.example.org"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if tun.URL != "https://chat.example.org" {
		t.Fatalf("got %+v", tun)
	}
}
