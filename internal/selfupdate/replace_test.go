package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceFile(t *testing.T) {
	for _, windows := range []bool{false, true} {
		dir := t.TempDir()
		old, nu := filepath.Join(dir, "hpctun"), filepath.Join(dir, ".new", "hpctun")
		write(t, old, "old")
		write(t, nu, "new")
		if err := Replace(old, nu, windows); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, old); got != "new" {
			t.Fatalf("windows=%v: %q", windows, got)
		}
		_, err := os.Stat(old + ".old")
		if windows != (err == nil) {
			t.Fatalf("windows=%v: .old present = %v", windows, err == nil)
		}
		CleanOld(old)
		if _, err := os.Stat(old + ".old"); err == nil {
			t.Fatal("CleanOld left .old")
		}
	}
}

func TestReplaceBundle(t *testing.T) {
	dir := t.TempDir()
	old, nu := filepath.Join(dir, "hpctun-tray.app"), filepath.Join(dir, ".new", "hpctun-tray.app")
	write(t, filepath.Join(old, "Contents", "MacOS", "hpctun"), "old")
	write(t, filepath.Join(nu, "Contents", "MacOS", "hpctun"), "new")
	if err := Replace(old, nu, false); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(old, "Contents", "MacOS", "hpctun")); got != "new" {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(old + ".old"); err == nil {
		t.Fatal(".old left behind")
	}
}

// When the new copy cannot be renamed in, the old one is put back.
func TestReplaceRestores(t *testing.T) {
	for name, windows := range map[string]bool{"bundle": false, "windows file": true} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			old, nu := filepath.Join(dir, "x"), filepath.Join(dir, ".new", "x")
			if name == "bundle" {
				write(t, filepath.Join(old, "f"), "old")
				write(t, filepath.Join(nu, "f"), "new")
			} else {
				write(t, old, "old")
				write(t, nu, "new")
			}
			calls := 0
			rename = func(a, b string) error {
				calls++
				if calls == 2 {
					return errors.New("disk on fire")
				}
				return os.Rename(a, b)
			}
			t.Cleanup(func() { rename = os.Rename })
			err := Replace(old, nu, windows)
			if err == nil || !strings.HasPrefix(err.Error(), "can't replace "+old+": ") {
				t.Fatalf("err %v", err)
			}
			if _, err := os.Stat(old); err != nil {
				t.Fatalf("old not restored: %v", err)
			}
			if _, err := os.Stat(old + ".old"); err == nil {
				t.Fatal(".old left behind")
			}
		})
	}
}

func TestReplaceUnwritable(t *testing.T) {
	dir := t.TempDir()
	err := Replace(filepath.Join(dir, "missing", "hpctun"), filepath.Join(dir, "nope"), false)
	if err == nil || !strings.HasPrefix(err.Error(), "can't replace ") {
		t.Fatalf("err %v", err)
	}
}
