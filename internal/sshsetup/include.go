package sshsetup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"hpctun/internal/change"
	"hpctun/internal/sshconfig"
)

// IncludeLine is the one line hpctun adds to ~/.ssh/config.
const IncludeLine = "Include hpctun_config"

// IncludeStatus says whether ~/.ssh/config includes hpctun_config.
type IncludeStatus int

const (
	IncludeMissing   IncludeStatus = iota
	IncludePresent                 // at top level, before any Host or Match
	IncludeMisplaced               // inside a Host or Match block: applies only there
)

// Include is where the Include is, or would go.
type Include struct {
	Status IncludeStatus
	// Line is the 1-based line of the Include. For IncludeMissing it is the
	// first Host or Match line, which the Include would go before; 0 means
	// the end of the file.
	Line int
}

// FindInclude looks for a top-level Include of TunConfig in UserConfig.
func FindInclude(p Paths) (Include, error) {
	data, err := os.ReadFile(p.UserConfig)
	if errors.Is(err, fs.ErrNotExist) {
		return Include{Status: IncludeMissing}, nil
	}
	if err != nil {
		return Include{}, err
	}
	return findInclude(string(data), p), nil
}

func findInclude(text string, p Paths) Include {
	firstBlock := 0
	for i, line := range strings.Split(text, "\n") {
		key, args := sshconfig.Split(strings.TrimSuffix(line, "\r"))
		switch key {
		case "host", "match":
			if firstBlock == 0 {
				firstBlock = i + 1
			}
		case "include":
			for _, a := range args {
				if !includes(a, p) {
					continue
				}
				if firstBlock != 0 {
					return Include{Status: IncludeMisplaced, Line: i + 1}
				}
				return Include{Status: IncludePresent, Line: i + 1}
			}
		}
	}
	return Include{Status: IncludeMissing, Line: firstBlock}
}

// includes reports whether an Include argument in UserConfig names
// TunConfig. ~/ is the home directory, relative paths resolve against
// UserConfig's directory (~/.ssh), and glob patterns are matched.
func includes(arg string, p Paths) bool {
	pat := arg
	switch {
	case strings.HasPrefix(pat, "~/"):
		if p.Home == "" {
			return false
		}
		pat = filepath.Join(p.Home, pat[2:])
	case !filepath.IsAbs(pat):
		pat = filepath.Join(filepath.Dir(p.UserConfig), pat)
	}
	return matchPath(pat, p.TunConfig, runtime.GOOS == "windows")
}

// matchPath is filepath.Match for an Include pattern. Windows paths take
// either slash and ignore letter case, so there both sides are compared
// lower-cased with forward slashes.
func matchPath(pat, name string, windows bool) bool {
	if windows {
		norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, `\`, "/")) }
		ok, _ := path.Match(norm(pat), norm(name))
		return ok
	}
	ok, _ := filepath.Match(pat, name)
	return ok
}

// IncludeChange proposes putting IncludeLine into UserConfig before its
// first Host or Match line (or at the end), backing the file up to
// UserConfig.bak-hpctun first. A missing UserConfig would be created with mode
// 0600 in a 0700 directory. The change is empty when the Include is already
// present; a misplaced Include is an error. Nothing is written here.
func IncludeChange(p Paths) (change.Change, error) {
	c, err := change.Read(p.UserConfig, p.Show(p.UserConfig))
	if err != nil {
		return change.Change{}, err
	}
	c.Perm, c.DirPerm, c.Backup = 0o600, 0o700, true
	inc := findInclude(c.Old, p)
	switch inc.Status {
	case IncludePresent:
		return c, nil
	case IncludeMisplaced:
		return change.Change{}, fmt.Errorf("%s line %d includes hpctun_config inside a Host or Match block, where it applies only to that block; move it above the first Host line", p.Show(p.UserConfig), inc.Line)
	}
	c.New = insertInclude(c.Old, inc.Line)
	return c, nil
}

func insertInclude(text string, before int) string {
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	if before == 0 {
		if text != "" {
			if !strings.HasSuffix(text, "\n") {
				text += nl
			}
			text += nl
		}
		return text + IncludeLine + nl
	}
	var b strings.Builder
	for i, l := range strings.SplitAfter(text, "\n") {
		if i == before-1 {
			b.WriteString(IncludeLine + nl + nl)
		}
		b.WriteString(l)
	}
	return b.String()
}
