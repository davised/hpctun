package main

import (
	"fmt"
	"regexp"
	"runtime/debug"
	"time"
)

// major is hpctun's major version. It stays 0 until the maintainer releases v1.
const major = 0

// pseudo matches the time and revision at the end of a Go pseudo-version,
// such as v0.0.0-20260924051049-1dac96b98ddc (the time is UTC).
var pseudo = regexp.MustCompile(`(\d{8})\d{6}-([0-9a-f]{12})$`)

// version is hpctun's version for --version: vMAJOR.YYYYMMDD-hash, from the
// commit's date (UTC) and short hash, with -dirty for a build with
// uncommitted changes.
func version() string {
	info, _ := debug.ReadBuildInfo()
	return versionFrom(info)
}

// versionFrom reads the commit from info: the vcs settings go build embeds
// in a checkout, or the pseudo-version go install records. A tagged release
// is shown as tagged; without either, the version is vMAJOR-dev.
func versionFrom(info *debug.BuildInfo) string {
	dev := fmt.Sprintf("v%d-dev", major)
	if info == nil {
		return dev
	}
	var rev, when, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			when = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if t, err := time.Parse(time.RFC3339, when); err == nil && len(rev) >= 7 {
		v := fmt.Sprintf("v%d.%s-%s", major, t.UTC().Format("20060102"), rev[:7])
		if modified == "true" {
			v += "-dirty"
		}
		return v
	}
	mv := info.Main.Version
	if m := pseudo.FindStringSubmatch(mv); m != nil {
		return fmt.Sprintf("v%d.%s-%s", major, m[1], m[2][:7])
	}
	if mv != "" && mv != "(devel)" {
		return mv
	}
	return dev
}
