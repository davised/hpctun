package prompt

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"hpctun/internal/change"
	"hpctun/internal/config"
	"hpctun/internal/gateway"
)

// WizardDeps are the wizard's connections to the outside world.
type WizardDeps struct {
	Asker       Asker
	Out         io.Writer                                // progress printed while no question is showing
	Hosts       []string                                 // Host aliases the user defines (hpctun_config excluded)
	ModeOf      func(alias string) (gateway.Mode, error) // which mode hpctun would use
	TestGateway func(dest string) error                  // start the master to prove it works
	PortInUse   func(port int) bool                      // something listens there now
	Path        string                                   // where the config will be written
	LocalUser   string                                   // default login name
	// Preview is what the ssh setup would write for the answers in nc.
	Preview func(nc config.NewConfig) SSHPreview
	// HostName is the host name ssh connects to for alias (ssh -G); the
	// default host for a separate tunnel connection.
	HostName func(alias string) (string, error)
	// NoControlMaster is set on Windows: the connection test is a prompt-free
	// sign-in check whose failure does not block, the share-or-separate tunnel
	// connection questions are skipped, and the connection mode (ModeOf) is
	// never looked up.
	NoControlMaster bool
}

// SSHPreview is shown in the ssh setup review.
type SSHPreview struct {
	Text    string          // unified diffs of every file the answers would change
	Changes []change.Change // the changes Text shows; returned as approved when accepted
}

// Wizard runs hpctun init's questions. seed, if set, supplies defaults and
// initial tunnels (hpctun init --from); its [ssh] user is never used. Esc goes
// back one question; Esc on the first question, or declining to write the
// config, returns ErrAborted. approved are the Changes of the ssh review the
// user accepted, exactly as shown; nil when the review was not shown or was
// declined.
func Wizard(d WizardDeps, seed *config.NewConfig) (nc config.NewConfig, approved []change.Change, err error) {
	w := newWizard(d, seed, false)
	err = runSteps(w.chooseGateway, w.typeGateway, w.testGateway, w.gatewayAlias,
		w.chooseTransfer, w.typeTransfer, w.transferAlias, w.nodePatterns,
		w.askTunnels, w.tunnelConnection, w.tunnelHost, w.tunnelAlias, w.askCode, w.username, w.sshReview,
		w.defaultHost, w.tunnels, w.review)
	if errors.Is(err, ErrBack) {
		err = ErrAborted
	}
	if err != nil {
		return config.NewConfig{}, nil, err
	}
	if w.skipped() {
		w.nc.Tunnels, w.nc.DefaultHost = nil, "" // a seed's, declined with the question
	}
	switch {
	case !w.codeOn() || w.nc.SSH == nil:
		w.nc.Code = nil // declined, or declined with the ssh changes it needs
	case w.nc.Code == nil:
		w.nc.Code = config.DefaultCode()
	}
	return w.nc, w.approved, nil
}

// skipped reports whether hpctun init's tunnel question was answered No: the
// seed's tunnels and default host are then left out. They stay in w.nc so
// that going back and answering Yes offers them again.
func (w *wizard) skipped() bool { return !w.setup && w.tunAsked && !w.tunnelsOn }

type wizard struct {
	d  WizardDeps
	nc config.NewConfig

	choice  string // gateway alias picked from the list, or "" to type one
	typed   string // what was typed for the gateway: [user@]host
	gwHost  string // host part of typed, when hpctun defines the gateway alias
	gwUser  string // user part of typed
	gwAlias string // the alias hpctun defines for gwHost

	xChoice string // transferSkip, transferType, or an alias
	xHost   string
	xAlias  string
	nodes   []string
	user    string
	setup   bool // SSHSetup: declining the review changes nothing

	codeAsked bool // "Set up VS Code on a compute node?" has been answered
	codeYes   bool // ... with Yes; before it is asked, whether the seed has [code]

	tunAsked  bool   // "Set up tunnels?" has been answered
	tunnelsOn bool   // ... with Yes
	tunChoice string // tunnelShare, tunnelSeparate or tunnelKeep
	tunHost   string // host name for a separate tunnel connection
	tunAlias  string // the alias hpctun defines for tunHost
	tunKeep   string // a tunnel_gateway hpctun does not define, offered to keep
	tunDrop   bool   // a separate connection was chosen but its ssh changes declined

	labels []Option
	mode   string // how hpctun will connect to the gateway, for the recap
	tested bool   // the connection test passed for this gateway
	notice string

	approved []change.Change // the accepted ssh review's changes
}

func newWizard(d WizardDeps, seed *config.NewConfig, setup bool) *wizard {
	w := &wizard{d: d, xChoice: transferSkip, setup: setup, tunChoice: tunnelShare}
	if seed == nil {
		return w
	}
	w.nc = *seed
	w.nc.Tunnels = slices.Clone(seed.Tunnels)
	w.codeYes = seed.Code != nil
	w.nc.SSH = nil
	s := seed.SSH
	switch {
	case s != nil && s.GatewayHost != "":
		w.typed, w.gwHost, w.gwAlias = s.GatewayHost, s.GatewayHost, seed.Gateway
		if setup && s.User != "" {
			w.typed, w.gwUser = s.User+"@"+s.GatewayHost, s.User
		}
	case slices.Contains(d.Hosts, seed.Gateway):
		w.choice = seed.Gateway
	default:
		w.typed = seed.Gateway
		w.splitTyped()
	}
	w.nc.Gateway = cmp.Or(w.choice, w.typed)
	if s != nil {
		switch {
		case s.TransferHost != "":
			w.xChoice, w.xHost, w.xAlias = transferType, s.TransferHost, s.Transfer
		case s.Transfer != "" && slices.Contains(d.Hosts, s.Transfer):
			w.xChoice = s.Transfer
		case s.Transfer != "":
			w.xChoice, w.xAlias = transferType, s.Transfer
		}
		w.nodes = slices.Clone(s.Nodes)
		if setup {
			w.user = s.User
		}
	}
	if tg := seed.TunnelGateway; tg != "" {
		w.tunnelsOn = true
		if s != nil && s.TunnelGatewayHost != "" {
			w.tunChoice, w.tunHost, w.tunAlias = tunnelSeparate, s.TunnelGatewayHost, tg
		} else {
			w.tunChoice, w.tunKeep = tunnelKeep, tg
		}
	}
	return w
}

// recap is the answers so far.
func (w *wizard) recap() Context {
	var c Context
	if w.nc.Gateway != "" {
		v := w.nc.Gateway
		if w.gwHost != "" && w.gwAlias != "" && v != w.gwAlias {
			v = w.gwAlias + "  (" + w.typed + ")"
		}
		var notes []string
		if w.mode != "" {
			notes = append(notes, w.mode)
		}
		if w.tested {
			notes = append(notes, "connected ✓")
		}
		if len(notes) > 0 {
			v += "  (" + strings.Join(notes, "; ") + ")"
		}
		c.Recap = append(c.Recap, Line{"Gateway", v})
	}
	if t := w.transferName(); t != "" {
		c.Recap = append(c.Recap, Line{"Transfer", t})
	}
	if len(w.nodes) > 0 {
		c.Recap = append(c.Recap, Line{"Nodes", strings.Join(w.nodes, " ")})
	}
	if w.tunAsked {
		label := "Tunnel connection"
		if !w.tunnelsOn {
			label = "Tunnels"
		}
		c.Recap = append(c.Recap, Line{label, w.tunnelRecap()})
	}
	if w.codeAsked {
		v := "not set up (add a [code] table later)"
		if w.codeYes {
			v = "hpctun code, in a Slurm job on a compute node"
		}
		c.Recap = append(c.Recap, Line{"VS Code", v})
	}
	if w.skipped() {
		return c
	}
	if w.nc.DefaultHost != "" {
		c.Recap = append(c.Recap, Line{"Default host", w.nc.DefaultHost})
	}
	for i, t := range w.nc.Tunnels {
		l := Line{Value: summary(t)}
		if i == 0 {
			l.Label = "Tunnels"
		}
		c.Recap = append(c.Recap, l)
	}
	return c
}

// show sets the context for the next question: the recap, and the pending
// notice, which is shown once.
func (w *wizard) show() {
	c := w.recap()
	c.Notice, w.notice = w.notice, ""
	w.d.Asker.SetContext(c)
}

func (w *wizard) chooseGateway() (bool, error) {
	if len(w.d.Hosts) == 0 {
		w.choice = ""
		return false, nil
	}
	if w.labels == nil {
		w.labels = []Option{{Label: "Enter a hostname", Value: ""}}
		for _, h := range w.d.Hosts {
			label := h
			if !w.d.NoControlMaster {
				if m, err := w.d.ModeOf(h); err == nil && m == gateway.ModeSSHConfig {
					label += "  (ControlMaster already configured)"
				}
			}
			w.labels = append(w.labels, Option{Label: label, Value: h})
		}
	}
	w.show()
	c, err := w.d.Asker.Select(SelectQ{
		Title: "Gateway",
		Description: "The ssh host every tunnel and login goes through, such as a login or bastion node.\n" +
			"These are the hosts in ~/.ssh/config; press / to filter.",
		Options: w.labels,
		Default: w.choice,
	})
	if err != nil {
		return true, err
	}
	w.choice = c
	if c != "" {
		w.gwHost = ""
		w.setGateway(c)
	}
	return true, nil
}

func (w *wizard) typeGateway() (bool, error) {
	if w.choice != "" {
		return false, nil
	}
	w.show()
	v, err := w.d.Asker.Input(InputQ{
		Title: "Gateway hostname",
		Description: "The login host, such as login.example.edu. Write user@host if your username there is not " +
			cmp.Or(w.d.LocalUser, "your local one") + ".\nTab completes aliases from ~/.ssh/config.",
		Value:       w.typed,
		Suggestions: w.d.Hosts,
		Validate:    w.validateTyped,
	})
	if err != nil {
		return true, err
	}
	w.typed = v
	w.splitTyped()
	w.setGateway(v)
	return true, nil
}

// validateTyped checks a typed gateway: a hostname, user@hostname, or an
// alias from ~/.ssh/config. user@alias is refused: hpctun would define a new
// alias whose HostName is the other alias, which ssh looks up in DNS.
func (w *wizard) validateTyped(s string) error {
	if s == "" {
		return errors.New("a gateway is required")
	}
	if err := config.ValidateDest(s); err != nil {
		return err
	}
	user, host := splitUserHost(s)
	if user != "" && slices.Contains(w.d.Hosts, host) {
		return fmt.Errorf("%s is already a host in your ssh config; enter just %s, "+
			"and to log in there as %s, add `User %s` under `Host %s` in ~/.ssh/config", host, host, user, user, host)
	}
	return config.ValidateHostName(host)
}

// splitTyped sets gwUser and gwHost from the typed gateway. An alias the
// user already has, with or without user@, is not a host for hpctun to define:
// ssh reads the alias's own settings (with that user), whereas an alias hpctun
// defined would have the other alias as its HostName, which ssh looks up in
// DNS.
func (w *wizard) splitTyped() {
	w.gwUser, w.gwHost = splitUserHost(w.typed)
	if slices.Contains(w.d.Hosts, w.gwHost) {
		w.gwHost = ""
	}
}

func (w *wizard) setGateway(gw string) {
	if gw == w.nc.Gateway && w.mode != "" {
		return
	}
	w.nc.Gateway, w.mode, w.tested = gw, "", false
	if w.d.NoControlMaster {
		return
	}
	if m, err := w.d.ModeOf(gw); err == nil {
		w.mode = "hpctun will open its own shared connection"
		if m == gateway.ModeSSHConfig {
			w.mode = "uses the ControlMaster from your ssh config"
		}
	}
}

func (w *wizard) testGateway() (bool, error) {
	if w.d.NoControlMaster {
		return w.testSignIn()
	}
	w.show()
	test, err := w.d.Asker.Confirm("Test the connection now?",
		"Opens the shared connection to "+w.nc.Gateway+"; ssh may ask you to log in.", true)
	if err != nil || !test {
		return true, err
	}
	err = w.d.Asker.Outside(func() error {
		fmt.Fprintf(w.d.Out, "Connecting to %s...\n", w.nc.Gateway)
		return w.d.TestGateway(w.nc.Gateway)
	})
	if err != nil {
		w.notice = fmt.Sprintf("Connection to %s failed:\n%v\nFix the gateway and try again, or skip the test.", w.nc.Gateway, err)
		return true, ErrBack
	}
	w.tested = true
	return true, nil
}

// testSignIn is the connection test on Windows: tunnels there sign in on
// their own with no terminal, so the question is whether ssh needs to ask
// anything. A failure is shown and the wizard goes on; a key can be set up
// later in it.
func (w *wizard) testSignIn() (bool, error) {
	w.show()
	test, err := w.d.Asker.Confirm("Test signing in without a prompt?",
		"Signs in to "+w.nc.Gateway+" without letting ssh ask for anything. Tunnels need that: on Windows each\n"+
			"tunnel signs in by itself, with a key in the ssh agent.", true)
	if err != nil || !test {
		return true, err
	}
	err = w.d.Asker.Outside(func() error {
		fmt.Fprintf(w.d.Out, "Signing in to %s...\n", w.nc.Gateway)
		return w.d.TestGateway(w.nc.Gateway)
	})
	if err != nil {
		w.notice = fmt.Sprintf("Signing in to %s needs a prompt, so tunnels cannot open yet:\n%v", w.nc.Gateway, err)
		return true, nil
	}
	w.tested = true
	return true, nil
}

func (w *wizard) defaultHost() (bool, error) {
	if !w.tunnelsOn {
		return false, nil
	}
	w.show()
	dh, err := w.d.Asker.Input(InputQ{
		Title: "Default target host (optional)",
		Description: "The host your tunnels usually point at, as the gateway reaches it (for example a compute node).\n" +
			"Tunnels that name no host use it. Leave blank to name the host in every tunnel.",
		Value:       w.nc.DefaultHost,
		Suggestions: tunnelHosts(w.nc.Tunnels),
	})
	if err != nil {
		return true, err
	}
	w.nc.DefaultHost = dh
	return true, nil
}

func (w *wizard) tunnels() (bool, error) {
	if !w.tunnelsOn {
		return false, nil
	}
	for {
		w.show()
		def := "add"
		if len(w.nc.Tunnels) > 0 {
			def = "done"
		}
		next, err := w.d.Asker.Select(SelectQ{
			Title: "Add a tunnel?",
			Description: "Save the tunnels you open often, so `hpctun up <name>` or `hpctun up --all` opens them.\n" +
				"This is optional: add more any time with `hpctun add`, or open one without saving it with `hpctun up <port> --host <host>`.",
			Options: []Option{{Label: "Add a tunnel", Value: "add"}, {Label: "Done", Value: "done"}},
			Default: def,
		})
		if err != nil || next == "done" {
			return true, err
		}
		t, err := TunnelForm(w.d.Asker, Preset{}, FormOpts{
			DefaultHost: w.nc.DefaultHost,
			Full:        true,
			Context:     w.recap(),
			Hosts:       hostChoices(w.nc.DefaultHost, w.nc.Tunnels),
			NameTaken: func(n string) bool {
				return slices.ContainsFunc(w.nc.Tunnels, func(t config.Tunnel) bool { return t.Name == n })
			},
			PortTaken: func(p int) error {
				if i := slices.IndexFunc(w.nc.Tunnels, func(t config.Tunnel) bool { return t.Port == p }); i >= 0 {
					return fmt.Errorf("port %d is already used by tunnel %q", p, w.nc.Tunnels[i].Name)
				}
				if w.d.PortInUse(p) {
					return fmt.Errorf("something is already listening on port %d", p)
				}
				return nil
			},
		})
		if errors.Is(err, ErrBack) {
			continue
		}
		if err != nil {
			return true, err
		}
		w.nc.Tunnels = append(w.nc.Tunnels, t)
	}
}

func (w *wizard) review() (bool, error) {
	w.show()
	ok, err := w.d.Asker.Confirm(fmt.Sprintf("Write this config to %s?", w.d.Path),
		"Esc goes back to change it.", true)
	if err != nil {
		return true, err
	}
	if !ok {
		return true, ErrAborted
	}
	return true, nil
}

// summary describes a finished tunnel in one line.
func summary(t config.Tunnel) string {
	if t.Route == config.RouteSocks {
		return fmt.Sprintf("%s  SOCKS proxy on localhost:%d", t.Name, t.Port)
	}
	s := fmt.Sprintf("%s  localhost:%d → %s:%d", t.Name, t.Port, t.Host, t.RemotePort)
	if t.Route == config.RouteJump {
		s += "  (localhost-only service)"
	}
	return s
}

// tunnelHosts lists the distinct target hosts of ts, in order.
func tunnelHosts(ts []config.Tunnel) []string {
	var hs []string
	for _, t := range ts {
		if t.Host != "" && !slices.Contains(hs, t.Host) {
			hs = append(hs, t.Host)
		}
	}
	return hs
}

// hostChoices are the completions for a tunnel's target host.
func hostChoices(defaultHost string, ts []config.Tunnel) []string {
	hs := tunnelHosts(ts)
	if defaultHost != "" && !slices.Contains(hs, defaultHost) {
		hs = append([]string{defaultHost}, hs...)
	}
	return hs
}

// HostChoices are the completions for a tunnel's target host in cfg.
func HostChoices(cfg *config.Config) []string {
	return hostChoices(cfg.DefaultHost, cfg.Tunnels)
}
