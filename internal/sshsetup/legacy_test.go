package sshsetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hpctun/internal/change"
)

func legacyHome(t *testing.T, userConfig string) Paths {
	t.Helper()
	p := DefaultPaths(t.TempDir())
	os.MkdirAll(filepath.Dir(p.UserConfig), 0o700)
	os.WriteFile(p.LegacyTunConfig, []byte("Host old\n"), 0o600)
	os.WriteFile(p.UserConfig, []byte(userConfig), 0o600)
	return p
}

func TestLegacyChangesNone(t *testing.T) {
	p := DefaultPaths(t.TempDir())
	cs, err := LegacyChanges(p)
	if err != nil || len(cs) != 0 {
		t.Fatalf("%v %v", cs, err)
	}
}

func TestLegacyChangesInclude(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Include tun_config\n\nHost a\n", "Include hpctun_config\n\nHost a\n"},
		{"# mine\nInclude ~/.ssh/tun_config\nHost a\n", "# mine\nInclude hpctun_config\nHost a\n"},
		{"Include tun_config other.conf\nHost a\n", "Include hpctun_config other.conf\nHost a\n"},
		{"Include tun_config\r\nHost a\r\n", "Include hpctun_config\r\nHost a\r\n"},
	} {
		p := legacyHome(t, c.in)
		cs, err := LegacyChanges(p)
		if err != nil {
			t.Fatal(err)
		}
		var got string
		var removed bool
		for _, ch := range cs {
			switch ch.Path {
			case p.UserConfig:
				got = ch.New
				if !ch.Backup {
					t.Error("user config change without backup")
				}
			case p.LegacyTunConfig:
				removed = ch.Remove
			}
		}
		if got != c.want || !removed {
			t.Errorf("in %q: got %q removed=%v, want %q", c.in, got, removed, c.want)
		}
	}
}

// TestLegacyChangesIncludeGlob covers an Include argument that is a glob
// pattern: replacing it outright could silently drop other files it also
// names, so it is left alone. findInclude/insertInclude then decide whether
// hpctun_config is already covered.
func TestLegacyChangesIncludeGlob(t *testing.T) {
	// The glob also matches hpctun_config: already covered, so the user's
	// config is untouched (the legacy tun_config is still removed).
	p := legacyHome(t, "Include *_config\nHost a\n")
	cs, err := LegacyChanges(p)
	if err != nil {
		t.Fatal(err)
	}
	var removed bool
	for _, c := range cs {
		if c.Path == p.UserConfig && c.New != c.Old {
			t.Errorf("glob include rewritten: %q -> %q", c.Old, c.New)
		}
		if c.Path == p.LegacyTunConfig {
			removed = c.Remove
		}
	}
	if !removed {
		t.Error("legacy tun_config not removed")
	}

	// The glob does not match hpctun_config: the line is kept as is, and a
	// plain Include is added for hpctun_config.
	p = legacyHome(t, "Include tun_* other\nHost a\n")
	cs, err = LegacyChanges(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Path != p.UserConfig {
			continue
		}
		if !strings.Contains(c.New, "Include tun_* other\n") {
			t.Fatalf("glob line changed: %q", c.New)
		}
		if inc := findInclude(c.New, p); inc.Status != IncludePresent {
			t.Fatalf("Include not added: %q", c.New)
		}
		return
	}
	t.Fatal("no user config change")
}

// TestLegacyChangesIncludePreservesLine covers rewriting an Include
// argument without disturbing anything else on the line: a quoted argument
// alongside it, a trailing comment, and indentation/separator/case before
// any Host block.
func TestLegacyChangesIncludePreservesLine(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Include tun_config \"my hosts.conf\"\nHost a\n", "Include hpctun_config \"my hosts.conf\"\nHost a\n"},
		{"Include tun_config # work\nHost a\n", "Include hpctun_config # work\nHost a\n"},
		{"  include=tun_config\nHost a\n", "  include=hpctun_config\nHost a\n"},
	} {
		p := legacyHome(t, c.in)
		cs, err := LegacyChanges(p)
		if err != nil {
			t.Fatal(err)
		}
		var got string
		for _, ch := range cs {
			if ch.Path == p.UserConfig {
				got = ch.New
			}
		}
		if got != c.want {
			t.Errorf("in %q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLegacyChangesNoOldInclude(t *testing.T) {
	p := legacyHome(t, "Host a\n")
	cs, err := LegacyChanges(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Path == p.UserConfig {
			if inc := findInclude(c.New, p); inc.Status != IncludePresent {
				t.Fatalf("Include not added: %q", c.New)
			}
			return
		}
	}
	t.Fatal("no user config change")
}

func TestLegacyChangesKnownHosts(t *testing.T) {
	p := legacyHome(t, "Include tun_config\n")
	os.WriteFile(p.LegacyKnownHosts, []byte("@cert-authority * ssh-ed25519 AAAA\n"), 0o600)
	cs, _ := LegacyChanges(p)
	var wrote, removed bool
	for _, c := range cs {
		if c.Path == p.KnownHosts && c.New == "@cert-authority * ssh-ed25519 AAAA\n" && c.Perm == 0o600 {
			wrote = true
		}
		if c.Path == p.LegacyKnownHosts && c.Remove {
			removed = true
		}
	}
	if !wrote || !removed {
		t.Fatalf("known hosts not moved: %+v", cs)
	}

	// new file already there: keep both files as they are
	os.WriteFile(p.KnownHosts, []byte("new\n"), 0o600)
	cs, _ = LegacyChanges(p)
	for _, c := range cs {
		if c.Path == p.KnownHosts || c.Path == p.LegacyKnownHosts {
			t.Errorf("touched known hosts although the new file exists: %+v", c)
		}
	}
}

// TestLegacyChangesDanglingSymlink covers ~/.ssh/tun_config left as a
// dangling symlink (its target already gone, as after the state or config
// directory itself moved): LegacyChanges' Lstat check finds it, so sync must
// still remove it instead of leaving Check reporting the problem forever.
func TestLegacyChangesDanglingSymlink(t *testing.T) {
	p := DefaultPaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(p.UserConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p.UserConfig, []byte("Include tun_config\nHost a\n"), 0o600)
	if err := os.Symlink(filepath.Join(filepath.Dir(p.LegacyTunConfig), "gone"), p.LegacyTunConfig); err != nil {
		t.Fatal(err)
	}
	cs, err := LegacyChanges(p)
	if err != nil {
		t.Fatal(err)
	}
	var removed bool
	for _, c := range cs {
		if c.Path == p.LegacyTunConfig {
			removed = c.Remove && !c.Empty()
		}
	}
	if !removed {
		t.Fatalf("dangling tun_config not queued for removal: %+v", cs)
	}
	if err := change.Apply(cs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p.LegacyTunConfig); !os.IsNotExist(err) {
		t.Fatalf("dangling symlink not removed: %v", err)
	}
}
