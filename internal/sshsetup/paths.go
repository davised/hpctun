package sshsetup

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"hpctun/internal/change"
	"hpctun/internal/config"
	"hpctun/internal/sshconfig"
)

// Paths are the files sshsetup reads and writes.
type Paths struct {
	Home             string // the home directory, shown as ~
	UserConfig       string // ~/.ssh/config
	TunConfig        string // ~/.ssh/hpctun_config
	Sockets          string // ~/.ssh/sockets, for ControlPath
	KnownHosts       string // ~/.ssh/hpctun_known_hosts, left from hpctun code's Remote-SSH alias; only the legacy migration writes it
	LegacyTunConfig  string // ~/.ssh/tun_config, from before the rename
	LegacyKnownHosts string // ~/.ssh/tun_known_hosts, from before the rename
}

// DefaultPaths are the paths under home.
func DefaultPaths(home string) Paths {
	d := filepath.Join(home, ".ssh")
	return Paths{
		Home:             home,
		UserConfig:       filepath.Join(d, "config"),
		TunConfig:        filepath.Join(d, "hpctun_config"),
		Sockets:          filepath.Join(d, "sockets"),
		KnownHosts:       filepath.Join(d, "hpctun_known_hosts"),
		LegacyTunConfig:  filepath.Join(d, "tun_config"),
		LegacyKnownHosts: filepath.Join(d, "tun_known_hosts"),
	}
}

// Show names path for messages and diffs, with Home as ~.
func (p Paths) Show(path string) string {
	if p.Home != "" && strings.HasPrefix(path, p.Home+string(filepath.Separator)) {
		return "~" + path[len(p.Home):]
	}
	return path
}

// UserAliases lists the concrete Host names in the user's own ssh config,
// not counting hpctun_config or a tun_config left from before the rename.
func UserAliases(p Paths) ([]string, error) {
	return sshconfig.ListHosts(p.UserConfig, p.TunConfig, p.LegacyTunConfig)
}

// NewInput builds the generator input for cfg, which must have [ssh],
// asking sshG for the host names of aliases the user defines.
func NewInput(ctx context.Context, sshG SSHG, p Paths, cfg *config.Config, source string) (Input, error) {
	if cfg.SSH == nil {
		return Input{}, errors.New("the config has no [ssh] table")
	}
	aliases, err := UserAliases(p)
	if err != nil {
		return Input{}, err
	}
	in := Input{Gateway: cfg.Gateway, TunnelGateway: cfg.TunnelGateway, SSH: *cfg.SSH, UserAliases: aliases, Source: source}
	in.ResolveHostNames(ctx, sshG)
	return in, nil
}

// TunConfigChange proposes writing text to TunConfig (mode 0600, in a 0700
// directory). Nothing is written here.
func TunConfigChange(p Paths, text string) (change.Change, error) {
	c, err := change.Read(p.TunConfig, p.Show(p.TunConfig))
	if err != nil {
		return change.Change{}, err
	}
	c.New, c.Perm, c.DirPerm = text, 0o600, 0o700
	return c, nil
}

// EnsureSockets creates the ControlPath directory, mode 0700.
func EnsureSockets(p Paths) error { return os.MkdirAll(p.Sockets, 0o700) }

// Stale reports whether TunConfig does not hold exactly text. A missing file
// is stale.
func Stale(p Paths, text string) (bool, error) {
	b, err := os.ReadFile(p.TunConfig)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return string(b) != text, nil
}
