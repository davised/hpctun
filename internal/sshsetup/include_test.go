package sshsetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hpctun/internal/change"
)

func setupHome(t *testing.T, userConfig string) Paths {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := DefaultPaths(home)
	if userConfig != "" {
		if err := os.MkdirAll(filepath.Dir(p.UserConfig), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p.UserConfig, []byte(userConfig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// addInclude proposes the Include change and applies it, as an approved
// `tun ssh-config sync` would.
func addInclude(t *testing.T, p Paths) (bool, error) {
	t.Helper()
	c, err := IncludeChange(p)
	if err != nil {
		return false, err
	}
	if c.Empty() {
		return false, nil
	}
	return true, change.Apply([]change.Change{c})
}

func TestAddInclude(t *testing.T) {
	cases := []struct{ name, before, after string }{
		{"above first host", "# mine\nInclude /x/colima\n\nHost a\n  User b\n",
			"# mine\nInclude /x/colima\n\nInclude hpctun_config\n\nHost a\n  User b\n"},
		{"no host lines, no trailing newline", "ServerAliveInterval 30",
			"ServerAliveInterval 30\n\nInclude hpctun_config\n"},
		{"crlf", "Host a\r\n  User b\r\n", "Include hpctun_config\r\n\r\nHost a\r\n  User b\r\n"},
		{"indented, capitals, equals", "# c\n  HOST=a\n", "# c\nInclude hpctun_config\n\n  HOST=a\n"},
		{"match first", "match host x\n  User y\nHost a\n", "Include hpctun_config\n\nmatch host x\n  User y\nHost a\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := setupHome(t, c.before)
			added, err := addInclude(t, p)
			if err != nil || !added {
				t.Fatalf("added=%v err=%v", added, err)
			}
			if got := read(t, p.UserConfig); got != c.after {
				t.Fatalf("got %q\nwant %q", got, c.after)
			}
			if got := read(t, p.UserConfig+".bak-hpctun"); got != c.before {
				t.Fatalf("backup = %q", got)
			}
		})
	}
}

func TestIncludeChangeDiff(t *testing.T) {
	p := setupHome(t, "Host a\n")
	c, err := IncludeChange(p)
	if err != nil {
		t.Fatal(err)
	}
	c.Display = "~/.ssh/config"
	want := "--- ~/.ssh/config\n+++ ~/.ssh/config (proposed)\n@@ -1,1 +1,3 @@\n+Include hpctun_config\n+\n Host a\n"
	if got := c.Diff(); got != want {
		t.Fatalf("got\n%s", got)
	}
	if read(t, p.UserConfig) != "Host a\n" {
		t.Fatal("proposing a change wrote the file")
	}
}

func TestAddIncludeCreatesFile(t *testing.T) {
	p := setupHome(t, "")
	if added, err := addInclude(t, p); err != nil || !added {
		t.Fatal(added, err)
	}
	if got := read(t, p.UserConfig); got != "Include hpctun_config\n" {
		t.Fatalf("got %q", got)
	}
	fi, _ := os.Stat(p.UserConfig)
	di, _ := os.Stat(filepath.Dir(p.UserConfig))
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Fatalf("modes %v %v", fi.Mode().Perm(), di.Mode().Perm())
	}
	if _, err := os.Stat(p.UserConfig + ".bak-hpctun"); err == nil {
		t.Fatal("backup of a file that did not exist")
	}
}

func TestAddIncludePresentIsNoOp(t *testing.T) {
	for _, line := range []string{"Include hpctun_config", "Include ~/.ssh/hpctun_config", "Include ~/.ssh/*_config"} {
		p := setupHome(t, line+"\nHost a\n")
		added, err := addInclude(t, p)
		if err != nil || added || read(t, p.UserConfig) != line+"\nHost a\n" {
			t.Fatalf("%s: added=%v err=%v", line, added, err)
		}
		inc, _ := FindInclude(p)
		if inc.Status != IncludePresent || inc.Line != 1 {
			t.Fatalf("%s: %+v", line, inc)
		}
	}
}

func TestAddIncludeMisplacedRefuses(t *testing.T) {
	p := setupHome(t, "Host a\n  Include hpctun_config\n")
	_, err := addInclude(t, p)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v", err)
	}
	if inc, _ := FindInclude(p); inc.Status != IncludeMisplaced {
		t.Fatalf("%+v", inc)
	}
}

func TestAddIncludeKeepsExistingBackup(t *testing.T) {
	p := setupHome(t, "Host a\n")
	os.WriteFile(p.UserConfig+".bak-hpctun", []byte("older"), 0o600)
	if _, err := addInclude(t, p); err != nil {
		t.Fatal(err)
	}
	if read(t, p.UserConfig+".bak-hpctun") != "older" {
		t.Fatal("backup overwritten")
	}
}

func TestAddIncludeThroughSymlink(t *testing.T) {
	p := setupHome(t, "")
	target := filepath.Join(filepath.Dir(p.UserConfig), "..", "dotfiles", "ssh_config")
	os.MkdirAll(filepath.Dir(target), 0o700)
	os.MkdirAll(filepath.Dir(p.UserConfig), 0o700)
	os.WriteFile(target, []byte("Host a\n"), 0o600)
	if err := os.Symlink(target, p.UserConfig); err != nil {
		t.Fatal(err)
	}
	if _, err := addInclude(t, p); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(p.UserConfig); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	if read(t, target) != "Include hpctun_config\n\nHost a\n" {
		t.Fatalf("target = %q", read(t, target))
	}
}

func TestIncludeChangeShowsPathsWithTilde(t *testing.T) {
	p := setupHome(t, "Host a\n  Include hpctun_config\n")
	_, err := IncludeChange(p)
	if err == nil || !strings.HasPrefix(err.Error(), "~/.ssh/config line 2 ") {
		t.Fatalf("err = %v", err)
	}
	p = setupHome(t, "Host a\n")
	c, err := IncludeChange(p)
	if err != nil || c.Display != "~/.ssh/config" {
		t.Fatalf("Display = %q, err = %v", c.Display, err)
	}
	if c, err := TunConfigChange(p, "x"); err != nil || c.Display != "~/.ssh/hpctun_config" {
		t.Fatalf("Display = %q, err = %v", c.Display, err)
	}
}

func TestPathsShow(t *testing.T) {
	p := DefaultPaths("/home/me")
	for in, want := range map[string]string{
		"/home/me/.ssh/config": "~/.ssh/config",
		"/home/meow/x":         "/home/meow/x",
		"/etc/ssh/ssh_config":  "/etc/ssh/ssh_config",
	} {
		if got := p.Show(in); got != want {
			t.Errorf("Show(%q) = %q, want %q", in, got, want)
		}
	}
	if got := (Paths{}).Show("/a/b"); got != "/a/b" {
		t.Errorf("no Home: %q", got)
	}
}

// TestIncludesUsesPathsHome: ~/ in an Include is the home tun was given
// (USERPROFILE on Windows), not $HOME.
func TestIncludesUsesPathsHome(t *testing.T) {
	p := setupHome(t, "Include ~/.ssh/hpctun_config\nHost a\n")
	t.Setenv("HOME", t.TempDir())
	if inc, err := FindInclude(p); err != nil || inc.Status != IncludePresent {
		t.Fatalf("%+v %v", inc, err)
	}
}

// TestMatchPathWindows: on Windows an Include may use either slash and any
// letter case (C:/Users/me/... as VS Code users often write it).
func TestMatchPathWindows(t *testing.T) {
	name := `C:\Users\me\.ssh\hpctun_config`
	for _, pat := range []string{`C:/Users/me/.ssh/hpctun_config`, `c:\users\ME\.ssh\hpctun_config`, `C:/Users/me/.ssh/*_config`, name} {
		if !matchPath(pat, name, true) {
			t.Errorf("%s does not match", pat)
		}
	}
	if matchPath(`C:/Users/me/.ssh/other`, name, true) {
		t.Error("other matched")
	}
	if matchPath("/home/ME/.ssh/hpctun_config", "/home/me/.ssh/hpctun_config", false) {
		t.Error("case-insensitive off Windows")
	}
}
