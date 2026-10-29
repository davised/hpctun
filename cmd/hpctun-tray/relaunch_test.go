package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRelaunchArgv(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	bundle := filepath.Join(dir, "hpctun-tray.app")
	macos := filepath.Join(bundle, "Contents", "MacOS")
	os.MkdirAll(macos, 0o755)
	tray := filepath.Join(macos, "hpctun-tray")
	os.WriteFile(tray, []byte("x"), 0o755)

	got, err := relaunchArgv("darwin", tray, nil)
	if err != nil || !slices.Equal(got, []string{"open", "-n", bundle}) {
		t.Fatalf("darwin: %q, %v", got, err)
	}
	if _, err := relaunchArgv("darwin", filepath.Join(dir, "hpctun-tray"), nil); err == nil {
		t.Fatal("darwin outside an app: no error")
	}
	got, err = relaunchArgv("linux", "/opt/hpctun/hpctun-tray", []string{"-hpctun", "/x"})
	if err != nil || !slices.Equal(got, []string{"/opt/hpctun/hpctun-tray", "-hpctun", "/x"}) {
		t.Fatalf("linux: %q, %v", got, err)
	}
	got, err = relaunchArgv("windows", `C:\hpctun\hpctun-tray.exe`, nil)
	if err != nil || !slices.Equal(got, []string{`C:\hpctun\hpctun-tray.exe`}) {
		t.Fatalf("windows: %q, %v", got, err)
	}
}
