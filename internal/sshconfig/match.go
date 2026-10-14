package sshconfig

import "strings"

// Match reports whether host matches an ssh Host pattern: '*' matches any
// run of characters and '?' exactly one. ssh lowercases both the pattern and
// the host name before matching, so this does too.
func Match(pattern, host string) bool {
	return match(strings.ToLower(pattern), strings.ToLower(host))
}

// MatchAny reports whether host matches any of patterns.
func MatchAny(patterns []string, host string) bool {
	for _, p := range patterns {
		if Match(p, host) {
			return true
		}
	}
	return false
}

func match(p, s string) bool {
	for p != "" {
		switch p[0] {
		case '*':
			for i := len(s); i >= 0; i-- {
				if match(p[1:], s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if s == "" {
				return false
			}
		default:
			if s == "" || p[0] != s[0] {
				return false
			}
		}
		p, s = p[1:], s[1:]
	}
	return s == ""
}

// MatchList reports whether s matches the comma-separated pattern list, as
// ssh matches IgnoreUnknown and similar lists: a pattern starting with '!'
// that matches makes the whole list fail, and negations alone match nothing.
func MatchList(list, s string) bool {
	found := false
	for _, p := range strings.Split(list, ",") {
		p = strings.TrimSpace(p)
		if neg, ok := strings.CutPrefix(p, "!"); ok {
			if Match(neg, s) {
				return false
			}
			continue
		}
		if p != "" && Match(p, s) {
			found = true
		}
	}
	return found
}
