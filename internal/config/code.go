package config

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"hpctun/internal/slurmjob"
)

// Defaults for the [code] table.
const (
	DefaultCodeCPUs    = 4
	DefaultCodeTime    = "24:00:00"
	DefaultStartWithin = 30 * time.Minute
)

// DefaultServerDirs are the node-local bases tried for VS Code's server
// cache (spec: "Reused from tun-vt5").
var DefaultServerDirs = []string{"/scratch", "/data"}

var serverDirRE = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// Code is the [code] table with defaults applied: the Slurm job `hpctun code`
// starts.
type Code struct {
	CPUs        int
	Time        string        // sbatch --time
	StartWithin time.Duration // how long the job may wait to start
	Partition   string        // sbatch -p; empty: Slurm's default partition
	Sbatch      []string      // extra sbatch arguments
	ServerDirs  []string      // node-local bases for VS Code's server cache; empty: home only
	Obsolete    []string      // Remote-SSH keys the file still sets (alias, host_key_alias, host_ca)
}

type rawCode struct {
	CPUs        int      `toml:"cpus"`
	Time        string   `toml:"time"`
	StartWithin string   `toml:"start_within"`
	Partition   string   `toml:"partition"`
	Sbatch      []string `toml:"sbatch"`
	ServerDirs  []string `toml:"server_dirs"`
	// No longer used (Remote-SSH); kept so old files still load.
	Alias        string `toml:"alias"`
	HostKeyAlias string `toml:"host_key_alias"`
	HostCA       string `toml:"host_ca"`
}

var partitionRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// DefaultCode is an empty [code] table: hpctun code with every default.
func DefaultCode() *Code {
	return &Code{CPUs: DefaultCodeCPUs, Time: DefaultCodeTime, StartWithin: DefaultStartWithin, ServerDirs: slices.Clone(DefaultServerDirs)}
}

// codeFromRaw validates [code]; present reports that the table exists (an
// empty [code] enables hpctun code with every default) and dirsSet that the
// file sets server_dirs (an empty list means home only). A seed (LoadSeed)
// may leave out ssh.user: the wizard asks for it.
func codeFromRaw(r *rawCode, present, dirsSet bool, s *SSH, seed bool) (*Code, error) {
	if !present {
		return nil, nil
	}
	if r == nil {
		r = &rawCode{}
	}
	bad := func(field, msg string) (*Code, error) {
		return nil, &ValidationError{Field: field, Msg: msg}
	}
	if s == nil {
		return bad("code", "requires the [ssh] table: hpctun code reaches nodes through the gateway")
	}
	if s.User == "" && !seed {
		return bad("ssh.user", "required when [code] is set")
	}
	c := &Code{
		CPUs:        cmp.Or(r.CPUs, DefaultCodeCPUs),
		Time:        cmp.Or(r.Time, DefaultCodeTime),
		StartWithin: DefaultStartWithin,
		Partition:   r.Partition,
		Sbatch:      slices.Clone(r.Sbatch),
		ServerDirs:  slices.Clone(DefaultServerDirs),
	}
	if dirsSet {
		c.ServerDirs = slices.Clone(r.ServerDirs)
	}
	if r.CPUs < 0 {
		return bad("code.cpus", "must be 1 or more")
	}
	if _, err := slurmjob.ParseTime(c.Time); err != nil {
		return bad("code.time", err.Error())
	}
	if r.StartWithin != "" {
		d, err := time.ParseDuration(r.StartWithin)
		if err != nil || d < time.Minute {
			return bad("code.start_within", "must be a duration of at least a minute, such as 30m or 2h")
		}
		c.StartWithin = d
	}
	if c.Partition != "" && !partitionRE.MatchString(c.Partition) {
		return bad("code.partition", "must be a partition name: letters, digits, . _ -")
	}
	for _, a := range c.Sbatch {
		if strings.ContainsAny(a, "\r\n") {
			return bad("code.sbatch", "arguments cannot contain line breaks")
		}
	}
	for _, d := range c.ServerDirs {
		if !serverDirRE.MatchString(d) || d == "/tmp" || strings.HasPrefix(d, "/tmp/") {
			return bad("code.server_dirs", "each must be an absolute path of letters, digits, . _ - and / (not /tmp)")
		}
	}
	for _, k := range []struct{ key, v string }{{"alias", r.Alias}, {"host_key_alias", r.HostKeyAlias}, {"host_ca", r.HostCA}} {
		if k.v != "" {
			c.Obsolete = append(c.Obsolete, k.key)
		}
	}
	return c, nil
}

// FormatCode renders c as a [code] table, leaving out fields at their
// defaults.
func FormatCode(c *Code) string {
	var b strings.Builder
	b.WriteString("[code]\n")
	if c.CPUs != DefaultCodeCPUs {
		fmt.Fprintf(&b, "cpus = %d\n", c.CPUs)
	}
	kv := func(k, v, def string) {
		if v != def {
			fmt.Fprintf(&b, "%s = %s\n", k, strconv.Quote(v))
		}
	}
	kv("time", c.Time, DefaultCodeTime)
	if c.StartWithin != DefaultStartWithin {
		fmt.Fprintf(&b, "start_within = %s\n", strconv.Quote(formatDuration(c.StartWithin)))
	}
	kv("partition", c.Partition, "")
	if len(c.Sbatch) > 0 {
		q := make([]string, len(c.Sbatch))
		for i, a := range c.Sbatch {
			q[i] = strconv.Quote(a)
		}
		fmt.Fprintf(&b, "sbatch = [%s]\n", strings.Join(q, ", "))
	}
	if !slices.Equal(c.ServerDirs, DefaultServerDirs) {
		q := make([]string, len(c.ServerDirs))
		for i, d := range c.ServerDirs {
			q[i] = strconv.Quote(d)
		}
		fmt.Fprintf(&b, "server_dirs = [%s]\n", strings.Join(q, ", "))
	}
	return b.String()
}

func formatDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

func cloneCode(c *Code) *Code {
	if c == nil {
		return nil
	}
	d := *c
	d.Sbatch = slices.Clone(c.Sbatch)
	d.ServerDirs = slices.Clone(c.ServerDirs)
	d.Obsolete = slices.Clone(c.Obsolete)
	return &d
}
