// Package selfupdate is the part of `hpctun self-update` that needs no
// processes: deciding, verifying, extracting and swapping, and reading
// releases from GitHub. The command lives in cmd/hpctun/selfupdate.go.
package selfupdate

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	validVersion = regexp.MustCompile(`^v[0-9][A-Za-z0-9._-]*$`)
	datedVersion = regexp.MustCompile(`^v\d+\.(\d{8})-[0-9a-f]{7}(-dirty)?$`)
)

// ValidVersion reports whether s may name a release directory.
func ValidVersion(s string) bool { return validVersion.MatchString(s) }

// ParseVersion reads hpctun's version format (cmd/hpctun/version.go):
// date is YYYYMMDD; dev is true for vN-dev and -dirty builds; ok is false
// for anything else, such as a tag.
func ParseVersion(s string) (date string, dev bool, ok bool) {
	if strings.HasSuffix(s, "-dev") {
		return "", true, true
	}
	m := datedVersion.FindStringSubmatch(s)
	if m == nil {
		return "", false, false
	}
	return m[1], m[2] != "", true
}

// Action is what self-update does.
type Action int

const (
	UpToDate Action = iota
	Update
	Downgrade
)

// ErrDevBuild refuses to replace a development build without --force.
var ErrDevBuild = errors.New("this is a development build")

// Decide compares the running version with latest. A latest earlier than
// the running version's date is a downgrade: the release was rolled back.
func Decide(running, latest string, force bool) (Action, error) {
	latestDate, _, ok := ParseVersion(latest)
	if !ok || latestDate == "" {
		return 0, fmt.Errorf("unexpected latest version %q", latest)
	}
	if running == latest && !force {
		return UpToDate, nil
	}
	runDate, dev, _ := ParseVersion(running)
	if dev && !force {
		return 0, ErrDevBuild
	}
	if runDate != "" && latestDate < runDate {
		return Downgrade, nil
	}
	return Update, nil
}
