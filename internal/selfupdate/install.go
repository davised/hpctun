package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// AppName is the macOS bundle a release's app zip holds.
const AppName = "hpctun-tray.app"

// Kind is how hpctun was installed.
type Kind int

const (
	Plain         Kind = iota // hpctun on its own
	PlainWithTray             // hpctun with hpctun-tray next to it (the Linux archive)
	App                       // inside a macOS .app bundle, which holds both
)

// Install is what self-update replaces.
type Install struct {
	Kind   Kind
	Exe    string // the running hpctun, symlinks resolved
	Tray   string // hpctun-tray next to it, for PlainWithTray
	Bundle string // the .app, for App
}

// Detect classifies the install exe (the running hpctun) belongs to.
func Detect(exe string) (Install, error) {
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return Install{}, err
	}
	macos := filepath.Dir(real)
	contents := filepath.Dir(macos)
	if filepath.Base(macos) == "MacOS" && filepath.Base(contents) == "Contents" && strings.HasSuffix(filepath.Dir(contents), ".app") {
		return Install{Kind: App, Exe: real, Bundle: filepath.Dir(contents)}, nil
	}
	tray := filepath.Join(macos, "hpctun-tray")
	if fi, err := os.Lstat(tray); err == nil && fi.Mode().IsRegular() {
		return Install{Kind: PlainWithTray, Exe: real, Tray: tray}, nil
	}
	return Install{Kind: Plain, Exe: real}, nil
}

// Describe names what is replaced, for messages.
func (i Install) Describe() string {
	switch i.Kind {
	case PlainWithTray:
		return "hpctun and hpctun-tray"
	case App:
		return filepath.Base(i.Bundle)
	}
	return "hpctun"
}

// Target is the path the swap replaces first.
func (i Install) Target() string {
	if i.Kind == App {
		return i.Bundle
	}
	return i.Exe
}

var platforms = []string{"windows/amd64", "windows/arm64", "darwin/arm64", "darwin/amd64", "linux/amd64"}

// Asset names the release file this install needs, and for a plain
// install the members to take from it.
func (i Install) Asset(ver, goos, goarch string) (archive string, members []string, err error) {
	if i.Kind == App {
		return "hpctun-tray-" + ver + "-darwin-universal.zip", nil, nil
	}
	if !slices.Contains(platforms, goos+"/"+goarch) {
		return "", nil, fmt.Errorf("no release for %s/%s", goos, goarch)
	}
	base := "hpctun-" + ver + "-" + goos + "-" + goarch
	archive, exe := base+".tar.gz", "hpctun"
	if goos == "windows" {
		archive, exe = base+".zip", "hpctun.exe"
	}
	members = []string{base + "/" + exe}
	if i.Kind == PlainWithTray {
		members = append(members, base+"/hpctun-tray")
	}
	return archive, members, nil
}
