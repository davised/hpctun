// Package sshconfig lists Host aliases from an OpenSSH client config, for
// offering gateway choices in hpctun init.
package sshconfig

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ListHosts returns the concrete Host aliases in the config at path and the
// files it includes, in order of first appearance. Patterns containing *, ?
// or ! are skipped. Relative Include paths resolve against the directory of
// path, as they do against ~/.ssh for the user config. Files named in skip
// are not read, even when included.
func ListHosts(path string, skip ...string) ([]string, error) {
	l := &lister{base: filepath.Dir(path), seenFile: map[string]bool{}, seenHost: map[string]bool{}}
	for _, s := range skip {
		l.seenFile[filepath.Clean(s)] = true
	}
	if err := l.file(path, 0); err != nil {
		return nil, err
	}
	return l.hosts, nil
}

type lister struct {
	base     string
	seenFile map[string]bool
	seenHost map[string]bool
	hosts    []string
}

func (l *lister) file(path string, depth int) error {
	path = filepath.Clean(path)
	if depth > 16 || l.seenFile[path] {
		return nil
	}
	l.seenFile[path] = true
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, args := Split(sc.Text())
		switch key {
		case "host":
			for _, h := range args {
				if strings.ContainsAny(h, "*?!") || l.seenHost[h] {
					continue
				}
				l.seenHost[h] = true
				l.hosts = append(l.hosts, h)
			}
		case "include":
			for _, pat := range args {
				matches, _ := filepath.Glob(l.resolve(pat))
				for _, m := range matches {
					if err := l.file(m, depth+1); err != nil {
						return err
					}
				}
			}
		}
	}
	return sc.Err()
}

func (l *lister) resolve(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(l.base, p)
}

// Split returns the lowercased keyword and its arguments. The keyword may be
// separated from its arguments by whitespace or '='. Double quotes around an
// argument are removed.
func Split(line string) (string, []string) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", nil
	}
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return strings.ToLower(line), nil
	}
	key := strings.ToLower(line[:i])
	rest := strings.TrimLeft(line[i:], " \t=")
	var args []string
	for _, a := range strings.Fields(rest) {
		if strings.HasPrefix(a, "#") {
			break // a trailing comment, not another argument
		}
		args = append(args, strings.Trim(a, `"`))
	}
	return key, args
}
