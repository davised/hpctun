package sshsetup

import (
	"errors"
	"io/fs"
	"os"
	"strings"

	"hpctun/internal/change"
)

// LegacyChanges are the edits that finish moving ~/.ssh from tun's names
// before the rename: the Include of tun_config in UserConfig names
// hpctun_config instead (in place, keeping other arguments), tun_known_hosts
// moves to hpctun_known_hosts unless that exists, and tun_config is
// removed. The caller writes hpctun_config itself. With no tun_config, there
// is nothing to do.
func LegacyChanges(p Paths) ([]change.Change, error) {
	if _, err := os.Lstat(p.LegacyTunConfig); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var cs []change.Change

	uc, err := change.Read(p.UserConfig, p.Show(p.UserConfig))
	if err != nil {
		return nil, err
	}
	uc.Perm, uc.DirPerm, uc.Backup = 0o600, 0o700, true
	old := p
	old.TunConfig = p.LegacyTunConfig
	uc.New = replaceInclude(uc.Old, old)
	if inc := findInclude(uc.New, p); inc.Status == IncludeMissing {
		uc.New = insertInclude(uc.New, inc.Line) // tun_config was never included
	}
	cs = append(cs, uc)

	if _, err := os.Lstat(p.KnownHosts); errors.Is(err, fs.ErrNotExist) {
		kh, err := change.Read(p.LegacyKnownHosts, p.Show(p.LegacyKnownHosts))
		if err != nil {
			return nil, err
		}
		if kh.Exists {
			nu, err := change.Read(p.KnownHosts, p.Show(p.KnownHosts))
			if err != nil {
				return nil, err
			}
			nu.New, nu.Perm, nu.DirPerm = kh.Old, 0o600, 0o700
			kh.New, kh.Remove = "", true
			cs = append(cs, nu, kh)
		}
	} else if err != nil {
		return nil, err
	}

	tc, err := change.Read(p.LegacyTunConfig, p.Show(p.LegacyTunConfig))
	if err != nil {
		return nil, err
	}
	tc.New, tc.Remove = "", true
	return append(cs, tc), nil
}

// replaceInclude rewrites each Include argument in text that names
// old.TunConfig to hpctun_config, editing only that argument's own bytes so
// every other byte of the line (indentation, the = separator, quoting,
// letter case, a trailing comment) survives untouched. A glob argument
// (containing *, ? or [) is left alone even when it matches old.TunConfig,
// since replacing it could silently drop other files it also names; the
// caller's findInclude/insertInclude fallback then either finds it already
// covers hpctun_config or adds a plain Include for it.
func replaceInclude(text string, old Paths) string {
	ls := strings.SplitAfter(text, "\n")
	for i, l := range ls {
		body := strings.TrimRight(l, "\r\n")
		ls[i] = replaceIncludeLine(body, old) + l[len(body):]
	}
	return strings.Join(ls, "")
}

// replaceIncludeLine is replaceInclude for one line, without its ending.
func replaceIncludeLine(body string, old Paths) string {
	valueStart, ok := findIncludeValue(body)
	if !ok {
		return body
	}
	args := splitIncludeArgs(body[valueStart:], valueStart)
	// Replace back to front: earlier arguments' byte ranges, taken from the
	// unmodified body, stay valid as later ones (to their right) are edited.
	for i := len(args) - 1; i >= 0; i-- {
		a := args[i]
		if hasGlobMeta(a.value) || !includes(a.value, old) {
			continue
		}
		body = body[:a.start] + "hpctun_config" + body[a.end:]
	}
	return body
}

// findIncludeValue returns the byte offset in line (which has no line
// ending) where an Include directive's arguments start, right after the
// keyword and its separator (any run of spaces, tabs and '=', as ssh
// allows). ok is false when line has no "include" keyword, matching
// sshconfig.Split's rules for finding one.
func findIncludeValue(line string) (offset int, ok bool) {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i >= len(line) || line[i] == '#' {
		return 0, false
	}
	keyStart := i
	for i < len(line) && line[i] != ' ' && line[i] != '\t' && line[i] != '=' {
		i++
	}
	if strings.ToLower(line[keyStart:i]) != "include" {
		return 0, false
	}
	for i < len(line) && (line[i] == ' ' || line[i] == '\t' || line[i] == '=') {
		i++
	}
	return i, true
}

// includeArg is one Include argument: its unquoted value, and the byte
// range in the original line it came from (quotes included), so it can be
// edited without disturbing the rest of the line.
type includeArg struct {
	value      string
	start, end int
}

// splitIncludeArgs tokenizes value, an Include line's arguments, the way
// ssh does: double quotes may hold spaces, and an unquoted # starts a
// trailing comment that ends tokenizing. offset is value's own position in
// the line it came from, so the returned ranges are relative to that line.
func splitIncludeArgs(value string, offset int) []includeArg {
	var args []includeArg
	i := 0
	for i < len(value) {
		for i < len(value) && (value[i] == ' ' || value[i] == '\t') {
			i++
		}
		if i >= len(value) || value[i] == '#' {
			break
		}
		start := i
		var b strings.Builder
		quoted := false
		for i < len(value) {
			c := value[i]
			if c == '"' {
				quoted = !quoted
				i++
				continue
			}
			if !quoted && (c == ' ' || c == '\t') {
				break
			}
			b.WriteByte(c)
			i++
		}
		args = append(args, includeArg{b.String(), offset + start, offset + i})
	}
	return args
}

// hasGlobMeta reports whether s uses filepath.Match's pattern syntax.
func hasGlobMeta(s string) bool { return strings.ContainsAny(s, "*?[") }
