package change

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDiffInsertion(t *testing.T) {
	c := Change{Display: "~/.ssh/config", Exists: true,
		Old: "# mine\nInclude /x\n\nHost a\n  User b\n",
		New: "# mine\nInclude /x\n\nInclude tun_config\n\nHost a\n  User b\n"}
	want := "--- ~/.ssh/config\n+++ ~/.ssh/config (proposed)\n@@ -1,5 +1,7 @@\n # mine\n Include /x\n \n+Include tun_config\n+\n Host a\n   User b\n"
	if got := c.Diff(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestDiffNewFile(t *testing.T) {
	c := Change{Display: "~/.ssh/tun_config", New: "a\nb\n"}
	if got := c.Diff(); got != "--- /dev/null\n+++ ~/.ssh/tun_config\n@@ -0,0 +1,2 @@\n+a\n+b\n" {
		t.Fatalf("got\n%s", got)
	}
}

func TestDiffTwoHunks(t *testing.T) {
	var old []string
	for i := 1; i <= 20; i++ {
		old = append(old, fmt.Sprint(i))
	}
	nu := slices.Clone(old)
	nu[1], nu[17] = "two", "eighteen"
	c := Change{Display: "f", Exists: true, Old: strings.Join(old, "\n") + "\n", New: strings.Join(nu, "\n") + "\n"}
	want := "--- f\n+++ f (proposed)\n" +
		"@@ -1,5 +1,5 @@\n 1\n-2\n+two\n 3\n 4\n 5\n" +
		"@@ -15,6 +15,6 @@\n 15\n 16\n 17\n-18\n+eighteen\n 19\n 20\n"
	if got := c.Diff(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestApplyWritesBacksUpOnceSkipsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh", "config")
	c, err := Read(path, "cfg")
	if err != nil || c.Exists {
		t.Fatalf("%+v %v", c, err)
	}
	c.New, c.Perm, c.DirPerm, c.Backup = "one\n", 0o600, 0o700, true
	if err := Apply([]Change{c}); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	di, _ := os.Stat(filepath.Dir(path))
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Fatalf("modes %v %v", fi.Mode().Perm(), di.Mode().Perm())
	}
	if _, err := os.Stat(path + ".bak-hpctun"); err == nil {
		t.Fatal("backup of a file that did not exist")
	}
	for _, next := range []string{"two\n", "three\n"} {
		c, _ = Read(path, "cfg")
		c.New, c.Backup = next, true
		if err := Apply([]Change{c}); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(path + ".bak-hpctun"); string(b) != "one\n" {
		t.Fatalf("backup = %q", b)
	}
	c, _ = Read(path, "cfg")
	if !c.Empty() || c.Diff() != "" {
		t.Fatal("unchanged file is not an empty change")
	}
}

func TestRemove(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "tun_config")
	os.WriteFile(p, []byte("Host a\n"), 0o600)
	c, err := Read(p, "~/.ssh/tun_config")
	if err != nil {
		t.Fatal(err)
	}
	c.New, c.Remove = "", true
	if got, want := c.Diff(), "--- ~/.ssh/tun_config\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-Host a\n"; got != want {
		t.Errorf("diff:\n%q\nwant\n%q", got, want)
	}
	if c.Empty() {
		t.Fatal("removal of an existing file is not empty")
	}
	if err := Apply([]Change{c}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatalf("not removed: %v", err)
	}
	// removing a missing file is empty
	gone, _ := Read(p, "x")
	gone.Remove = true
	if !gone.Empty() {
		t.Error("removal of a missing file should be empty")
	}
}

// TestRemoveDanglingSymlink covers a dangling symlink (its target is gone):
// ReadFile treats it as missing, but it still exists to remove, so Read must
// report it as existing (Lstat) for a Remove change to be non-empty and
// actually applied.
func TestRemoveDanglingSymlink(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "tun_config")
	if err := os.Symlink(filepath.Join(d, "gone"), p); err != nil {
		t.Fatal(err)
	}
	c, err := Read(p, "~/.ssh/tun_config")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Exists || c.Old != "" {
		t.Fatalf("Read(dangling symlink) = %+v", c)
	}
	c.New, c.Remove = "", true
	if c.Empty() {
		t.Fatal("removal of a dangling symlink is not empty")
	}
	if err := Apply([]Change{c}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatalf("not removed: %v", err)
	}
}

func TestBackupSuffix(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config")
	os.WriteFile(p, []byte("a\n"), 0o600)
	c, _ := Read(p, "config")
	c.New, c.Backup = "b\n", true
	if err := Apply([]Change{c}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(p + ".bak-hpctun"); err != nil || string(b) != "a\n" {
		t.Fatalf("backup: %q %v", b, err)
	}
}

// TestApplyRefusesFilesChangedSinceRead covers approving diffs and then
// finding the files changed underneath: nothing is written, not even the
// changes whose files are as they were, so what lands is exactly what was
// approved or nothing.
func TestApplyRefusesFilesChangedSinceRead(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.WriteFile(b, []byte("b\n"), 0o600)
	ca, _ := Read(a, "~/a")
	cb, _ := Read(b, "~/b")
	ca.New, cb.New = "new a\n", "new b\n"

	for _, edit := range []func(){
		func() { os.WriteFile(b, []byte("edited\n"), 0o600) }, // changed
		func() { os.Remove(b) },                               // deleted
	} {
		edit()
		err := Apply([]Change{ca, cb})
		if err == nil || !strings.Contains(err.Error(), "~/b changed since") {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(a); err == nil {
			t.Fatal("wrote a although b had changed")
		}
		os.WriteFile(b, []byte("b\n"), 0o600)
	}
	os.WriteFile(a, []byte("created meanwhile\n"), 0o600)
	if err := Apply([]Change{ca, cb}); err == nil || !strings.Contains(err.Error(), "~/a changed since") {
		t.Fatalf("created: err = %v", err)
	}
	os.Remove(a)
	if err := Apply([]Change{ca, cb}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(b); string(got) != "new b\n" {
		t.Fatalf("b = %q", got)
	}
}
