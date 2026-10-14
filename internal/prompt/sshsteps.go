package prompt

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"hpctun/internal/change"
	"hpctun/internal/config"
)

const (
	transferSkip = "\x00skip"
	transferType = "\x00type"
)

const (
	tunnelShare    = "\x00share"
	tunnelSeparate = "\x00separate"
	tunnelKeep     = "\x00keep"
)

// SSHSetup asks the ssh questions from Wizard for an existing config,
// pre-filled from it (including the user name), and returns it with Gateway
// and SSH updated, and the approved changes as Wizard does. ErrAborted means
// nothing should change.
func SSHSetup(d WizardDeps, current config.NewConfig) (config.NewConfig, []change.Change, error) {
	w := newWizard(d, &current, true)
	err := runSteps(w.chooseGateway, w.typeGateway, w.gatewayAlias,
		w.chooseTransfer, w.typeTransfer, w.transferAlias, w.nodePatterns,
		w.askTunnels, w.tunnelConnection, w.tunnelHost, w.tunnelAlias, w.username, w.sshReview)
	if errors.Is(err, ErrBack) {
		err = ErrAborted
	}
	if err != nil {
		return config.NewConfig{}, nil, err
	}
	return w.nc, w.approved, nil
}

func splitUserHost(s string) (user, host string) {
	if i := strings.LastIndexByte(s, '@'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

func firstLabel(host string) string {
	l, _, _ := strings.Cut(host, ".")
	return l
}

// gatewayName is the gateway alias the finished config will use.
func (w *wizard) gatewayName() string {
	if w.gwHost != "" {
		return w.gwAlias
	}
	return cmp.Or(w.choice, w.typed)
}

func (w *wizard) transferName() string {
	switch w.xChoice {
	case transferSkip:
		return ""
	case transferType:
		if w.xHost == "" {
			return ""
		}
		return cmp.Or(w.xAlias, firstLabel(w.xHost)) + "  (" + w.xHost + ")"
	default:
		return w.xChoice
	}
}

// newAlias validates a name for hpctun to define.
func (w *wizard) newAlias(other string) func(string) error {
	return func(s string) error {
		if err := config.ValidateAlias(s); err != nil {
			return err
		}
		if slices.Contains(w.d.Hosts, s) {
			return fmt.Errorf("%s is already a host in your ssh config; pick another name, or go back and choose it from the list", s)
		}
		if other != "" && s == other {
			return fmt.Errorf("%s is already the gateway's name", s)
		}
		return nil
	}
}

func (w *wizard) gatewayAlias() (bool, error) {
	if w.gwHost == "" {
		return false, nil
	}
	w.show()
	v, err := w.d.Asker.Input(InputQ{
		Title:       "Short name for " + w.gwHost,
		Description: "hpctun adds this name to your ssh config: `ssh <name>` logs in, and tunnels use it.",
		Default:     firstLabel(w.gwHost),
		Value:       w.gwAlias,
		Validate:    w.newAlias(""),
	})
	if err == nil {
		w.gwAlias = v
	}
	return true, err
}

func (w *wizard) chooseTransfer() (bool, error) {
	gw := w.gatewayName()
	opts := []Option{{Label: "Skip", Value: transferSkip}, {Label: "Enter a hostname", Value: transferType}}
	for _, h := range w.d.Hosts {
		if h != gw {
			opts = append(opts, Option{Label: h, Value: h})
		}
	}
	w.show()
	c, err := w.d.Asker.Select(SelectQ{
		Title: "Transfer host (optional)",
		Description: "A host for copying files, such as a data-transfer node: `scp <name>:path .` copies from it,\n" +
			"and `hpctun cp` hops through it to reach nodes. Skip it to copy through the gateway.",
		Options: opts,
		Default: w.xChoice,
	})
	if err == nil {
		w.xChoice = c
	}
	return true, err
}

func (w *wizard) typeTransfer() (bool, error) {
	if w.xChoice != transferType {
		return false, nil
	}
	w.show()
	v, err := w.d.Asker.Input(InputQ{
		Title:       "Transfer hostname",
		Description: "For example files.example.edu. Your username there is the same as on the gateway.",
		Value:       w.xHost,
		Validate:    config.ValidateHostName,
	})
	if err == nil {
		w.xHost = v
	}
	return true, err
}

func (w *wizard) transferAlias() (bool, error) {
	if w.xChoice != transferType {
		return false, nil
	}
	w.show()
	v, err := w.d.Asker.Input(InputQ{
		Title:       "Short name for " + w.xHost,
		Description: "hpctun adds this name to your ssh config: `scp <name>:path .` copies from it.",
		Default:     firstLabel(w.xHost),
		Value:       w.xAlias,
		Validate:    w.newAlias(w.gatewayName()),
	})
	if err == nil {
		w.xAlias = v
	}
	return true, err
}

func patterns(s string) []string {
	if s == "" {
		return nil // strings.FieldsFunc("") returns a non-nil empty slice
	}
	return strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' })
}

func (w *wizard) nodePatterns() (bool, error) {
	via := "`ssh <host>` then goes through " + w.gatewayName()
	if t := w.transferName(); t != "" {
		via += ", and `hpctun cp` through " + strings.Fields(t)[0]
	}
	w.show()
	v, err := w.d.Asker.Input(InputQ{
		Title:       "Hosts you log in to through the gateway (optional)",
		Description: "ssh patterns such as web* or node??, separated by spaces. " + via + ".\nAdd more later with `hpctun ssh-config setup`.",
		Value:       strings.Join(w.nodes, " "),
		Suggestions: hostChoices(w.nc.DefaultHost, w.nc.Tunnels),
		Validate: func(s string) error {
			for _, p := range patterns(s) {
				if err := config.ValidatePattern(p); err != nil {
					return err
				}
			}
			return nil
		},
	})
	if err == nil {
		w.nodes = patterns(v)
	}
	return true, err
}

func (w *wizard) askTunnels() (bool, error) {
	desc := "Tunnels make a port on a remote host (Jupyter, a web dashboard) reachable on this machine,\n" +
		"or send a browser's traffic through the gateway. Most people only need logins and copies\n" +
		"and can skip this; `hpctun add` adds tunnels any time."
	if w.setup {
		desc = "Whether tunnels share the gateway's connection or have their own.\n" +
			"Most people only need logins and copies and can skip this."
	}
	w.show()
	v, err := w.d.Asker.Confirm("Set up tunnels? (advanced)", desc, w.tunnelsOn)
	if err == nil {
		w.tunAsked, w.tunnelsOn = true, v
	}
	return true, err
}

func (w *wizard) tunnelConnection() (bool, error) {
	if w.d.NoControlMaster || !w.tunnelsOn {
		return false, nil
	}
	opts := []Option{{Label: "Share the gateway's connection", Value: tunnelShare}, {Label: "Use a separate connection", Value: tunnelSeparate}}
	def := w.tunChoice
	if w.keepable() {
		opts = append(opts, Option{Label: "Keep " + w.tunKeep, Value: tunnelKeep})
	} else if def == tunnelKeep {
		def = tunnelShare
	}
	w.show()
	c, err := w.d.Asker.Select(SelectQ{
		Title: "Tunnel connection",
		Description: "Tunnels can share the gateway's connection with your logins, or have their own,\n" +
			"so logging in and out never disturbs them. A separate one may ask you to log in once more.",
		Options: opts,
		Default: def,
	})
	if err == nil {
		w.tunChoice = c
	}
	return true, err
}

// keepable reports whether the seed's tunnel_gateway can be kept: not once
// the gateway or transfer host is that alias, which the config would refuse
// after the ssh changes were written.
func (w *wizard) keepable() bool {
	return w.tunKeep != "" && w.tunKeep != w.gatewayName() && w.tunKeep != w.sshTable().Transfer
}

// separate reports whether tunnels get a connection hpctun defines.
func (w *wizard) separate() bool { return w.tunnelsOn && w.tunChoice == tunnelSeparate }

func (w *wizard) tunnelHost() (bool, error) {
	if w.d.NoControlMaster || !w.separate() {
		return false, nil
	}
	desc := "The host tunnels connect to. Blank means the gateway's host, on a connection of its own."
	def := w.gwHost
	if def == "" {
		// The gateway is the user's own alias; hpctun's block copies none of it.
		gw := w.gatewayName()
		desc += "\nhpctun defines it with only HostName and User, so other settings in your ssh config for " + gw +
			"\n(such as Port, ProxyJump or IdentityFile) are not copied; if it needs them, share the gateway's connection."
		if w.d.HostName != nil {
			def, _ = w.d.HostName(gw)
		}
	}
	w.show()
	v, err := w.d.Asker.Input(InputQ{
		Title:       "Tunnel host",
		Description: desc,
		Default:     def,
		Value:       w.tunHost,
		Validate:    config.ValidateHostName,
	})
	if err == nil {
		w.tunHost = v
	}
	return true, err
}

func (w *wizard) tunnelAlias() (bool, error) {
	if w.d.NoControlMaster || !w.separate() {
		return false, nil
	}
	gw := w.gatewayName()
	w.show()
	v, err := w.d.Asker.Input(InputQ{
		Title:       "Short name for the tunnel connection",
		Description: "hpctun adds this name to your ssh config for tunnels only; `ssh " + gw + "` stays your login.",
		Default:     gw + "-tunnels",
		Value:       w.tunAlias,
		Validate: func(s string) error {
			if err := w.newAlias(gw)(s); err != nil {
				return err
			}
			if x := w.sshTable().Transfer; x != "" && s == x {
				return fmt.Errorf("%s is already the transfer host's name", s)
			}
			return nil
		},
	})
	if err == nil {
		w.tunAlias = v
	}
	return true, err
}

// tunnelGateway is the tunnel_gateway the answers give, if the ssh review
// is accepted. A Keep that is no longer keepable counts as Share.
func (w *wizard) tunnelGateway() string {
	switch {
	case !w.tunnelsOn:
		return ""
	case w.tunChoice == tunnelSeparate:
		return w.tunAlias
	case w.tunChoice == tunnelKeep && w.keepable():
		return w.tunKeep
	}
	return ""
}

func (w *wizard) tunnelRecap() string {
	switch {
	case !w.tunnelsOn:
		return "skipped (add them later with `hpctun add`)"
	case w.tunChoice == tunnelKeep && w.keepable():
		return w.tunKeep
	case w.tunChoice == tunnelSeparate && w.tunDrop:
		return "shared with the gateway (the separate one needs the ssh changes)"
	case w.tunChoice == tunnelSeparate && w.tunHost != "":
		return cmp.Or(w.tunAlias, "…") + "  (" + w.tunHost + ", a connection of its own)"
	}
	return "shared with the gateway"
}

// defines reports whether hpctun will write any ssh entries or needs the
// user's login name: [code] always does (ssh.user).
func (w *wizard) defines() bool {
	return w.gwHost != "" || w.xChoice == transferType || len(w.nodes) > 0 || w.separate() || w.codeOn()
}

// codeOn reports whether the config gets a [code] table: hpctun init's
// answer, or, where the question is not asked (ssh-config setup), whether
// the config has one.
func (w *wizard) codeOn() bool {
	if w.codeAsked {
		return w.codeYes
	}
	return w.nc.Code != nil
}

// CodeQuestion is the title of hpctun init's hpctun code question.
const CodeQuestion = "Set up VS Code on a compute node? (hpctun code)"

// askCode offers hpctun code (hpctun init only): default Yes when the site
// file has [code], No otherwise. No leaves the seed's [code] in w.nc, so
// going back and answering Yes offers it again. Asked on Windows too:
// hpctun code needs no ControlMaster.
func (w *wizard) askCode() (bool, error) {
	if w.setup {
		return false, nil
	}
	desc := "hpctun code starts a Slurm job of your own and opens VS Code on its node, through\n" +
		"VS Code's Remote - Tunnels. It needs Slurm on the cluster and an ssh key for the gateway."
	if w.nc.Code != nil {
		desc += "\nThe site file sets it up; choose No to leave it out."
	}
	w.show()
	v, err := w.d.Asker.Confirm(CodeQuestion, desc, w.codeYes)
	if err == nil {
		w.codeAsked, w.codeYes = true, v
	}
	return true, err
}

func (w *wizard) username() (bool, error) {
	if !w.defines() {
		return false, nil
	}
	w.show()
	v, err := w.d.Asker.Input(InputQ{
		Title:       "Your username on these hosts",
		Description: "Written into each host hpctun defines.",
		Default:     cmp.Or(w.gwUser, w.d.LocalUser),
		Value:       w.user,
		Validate:    config.ValidateUser,
	})
	if err == nil {
		w.user = v
	}
	return true, err
}

func (w *wizard) sshTable() config.SSH {
	s := config.SSH{GatewayHost: w.gwHost, Nodes: slices.Clone(w.nodes)}
	if w.defines() {
		s.User = w.user
	}
	switch w.xChoice {
	case transferSkip:
	case transferType:
		s.Transfer, s.TransferHost = cmp.Or(w.xAlias, firstLabel(w.xHost)), w.xHost
	default:
		s.Transfer = w.xChoice
	}
	if w.separate() {
		s.TunnelGatewayHost = w.tunHost
	}
	return s
}

func (w *wizard) sshReview() (bool, error) {
	w.approved = nil
	if !w.defines() {
		w.finishSSH(false)
		return false, nil
	}
	s := w.sshTable()
	pv := w.d.Preview(config.NewConfig{Gateway: w.gatewayName(), TunnelGateway: w.tunnelGateway(), SSH: &s})
	c := w.recap()
	c.Body = pv.Text
	c.Notice, w.notice = w.notice, ""
	w.d.Asker.SetContext(c)
	desc := "Above are the changes to your files. Nothing is written until the end."
	if !w.setup {
		desc += "\nChoose No to leave your ssh config alone; hpctun still works, logging in on its own."
	}
	ok, err := w.d.Asker.Confirm("Apply these changes?", desc, false)
	if err != nil {
		return true, err
	}
	if !ok && w.setup {
		return true, ErrAborted
	}
	if ok {
		w.approved = pv.Changes
	} else if w.codeOn() {
		w.notice = "Without these changes hpctun has no username for the cluster, so the [code] table is left out.\n" +
			"Add it later and run `hpctun ssh-config sync`."
	}
	w.finishSSH(ok)
	return true, nil
}

// finishSSH settles the config's gateway and [ssh] table from the answers.
func (w *wizard) finishSSH(accepted bool) {
	s := w.sshTable()
	switch {
	case accepted:
		w.nc.Gateway, w.nc.SSH = w.gatewayName(), &s
		if w.gwHost != "" {
			w.mode = "defined by hpctun in ~/.ssh/hpctun_config"
		}
	case w.gwHost == "" && s.Transfer != "" && s.TransferHost == "":
		w.nc.Gateway, w.nc.SSH = w.gatewayName(), &config.SSH{Transfer: s.Transfer}
	default:
		w.nc.Gateway, w.nc.SSH = cmp.Or(w.choice, w.typed), nil
	}
	w.nc.TunnelGateway, w.tunDrop = w.tunnelGateway(), false
	if !accepted && w.separate() {
		w.nc.TunnelGateway, w.tunDrop = "", true // nothing defines the alias
	}
}
