package main

import (
	"context"
	"os/user"
	"path/filepath"

	"hpctun/internal/config"
	"hpctun/internal/gateway"
	"hpctun/internal/prompt"
	"hpctun/internal/sshsetup"
)

// wizardDeps are the wizard's dependencies shared by init and ssh-config
// setup. PortInUse reports nothing in use; init replaces it. The review's
// preview is the diffs of every file the answers would change; editConfig
// adds the config.toml edit (setup edits an existing file).
func (a *app) wizardDeps(ctx context.Context, heading, path, dir string, editConfig bool, code *config.Code) prompt.WizardDeps {
	r := a.runner()
	hosts, _ := sshsetup.UserAliases(a.sshPaths())
	d := prompt.WizardDeps{
		Asker:     a.asker(heading, true),
		Out:       a.stdout,
		Hosts:     hosts,
		Path:      path,
		LocalUser: a.localUser(),
		ModeOf: func(alias string) (gateway.Mode, error) {
			g, err := gateway.Resolve(ctx, r, alias, dir)
			if err != nil {
				return "", err
			}
			return g.Mode, nil
		},
		TestGateway: func(dest string) error {
			g, err := gateway.Resolve(ctx, r, dest, dir)
			if err != nil {
				return err
			}
			_, err = g.EnsureStarted(ctx, r, filepath.Join(dir, "master.log"))
			return err
		},
		PortInUse:       func(int) bool { return false },
		NoControlMaster: a.windows(),
		HostName: func(alias string) (string, error) {
			g, err := sshsetup.RunnerSSHG(r)(ctx, alias)
			if err != nil {
				return "", err
			}
			return g["hostname"], nil
		},
		Preview: func(nc config.NewConfig) prompt.SSHPreview {
			nc.Code = code // the wizard does not ask about [code]; keep its alias in the preview
			cs, err := a.sshChanges(ctx, nc, path)
			if err != nil {
				return prompt.SSHPreview{Text: "Cannot prepare the changes: " + err.Error()}
			}
			if editConfig {
				cc, err := a.configChange(path, nc)
				if err != nil {
					return prompt.SSHPreview{Text: "Cannot prepare the changes: " + err.Error()}
				}
				cs = append(cs, cc)
			}
			return prompt.SSHPreview{Text: diffs(cs), Changes: cs}
		},
	}
	if a.windows() {
		// Windows has no ControlMaster: the connection test is whether ssh
		// can sign in at all without a prompt.
		d.TestGateway = func(dest string) error { return a.signIn(ctx, dest) }
	}
	return d
}

func (a *app) localUser() string {
	if u := a.getenv("USER"); u != "" {
		return u
	}
	if u := a.getenv("USERNAME"); a.windows() && u != "" {
		return u // user.Current would say DOMAIN\user
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}
