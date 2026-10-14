package main

import (
	"archive/zip"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// appName is the bundle the release's app zip holds; self-update expects this name.
const appName = "hpctun-tray.app"

// epoch is every entry's time, so the same inputs give the same zip.
var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// appInputs are the thin darwin builds (arm64 and amd64, in any order)
// and the files that go in Contents/ (LICENSE, README.md, site files).
type appInputs struct {
	Version      string
	HPCtun, Tray [2]string
	Extras       []string
}

var versionRE = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)-`)

// bundleVersion turns a release version (v0.20261008-abc1234) into the
// numbers-and-dots form Info.plist needs (0.20261008).
func bundleVersion(v string) (string, error) {
	m := versionRE.FindStringSubmatch(v)
	if m == nil {
		return "", fmt.Errorf("version %q is not v<number>.<number>-<commit>", v)
	}
	return m[1] + "." + m[2], nil
}

// infoPlist is the bundle's Info.plist: no Dock icon (LSUIElement).
func infoPlist(version string) ([]byte, error) {
	short, err := bundleVersion(version)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key>
	<string>edu.oregonstate.hpctun-tray</string>
	<key>CFBundleName</key>
	<string>hpctun</string>
	<key>CFBundleDisplayName</key>
	<string>hpctun</string>
	<key>CFBundleExecutable</key>
	<string>hpctun-tray</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>%s</string>
	<key>CFBundleVersion</key>
	<string>%s</string>
	<key>LSMinimumSystemVersion</key>
	<string>12.0</string>
	<key>LSUIElement</key>
	<true/>
	<key>NSHighResolutionCapable</key>
	<true/>
</dict>
</plist>
`, short, short)), nil
}

// writeApp writes out, a zip of hpctun-tray.app with universal
// hpctun-tray and hpctun, Info.plist and the extras. On any error it
// removes out.
func writeApp(out string, in appInputs) (err error) {
	plist, err := infoPlist(in.Version)
	if err != nil {
		return err
	}
	tray, err := makeFat(in.Tray[:]...)
	if err != nil {
		return err
	}
	hpctun, err := makeFat(in.HPCtun[:]...)
	if err != nil {
		return err
	}
	type entry struct {
		name string
		data []byte
		mode fs.FileMode
	}
	contents := appName + "/Contents/"
	entries := []entry{
		{appName + "/", nil, fs.ModeDir | 0o755},
		{contents, nil, fs.ModeDir | 0o755},
		{contents + "MacOS/", nil, fs.ModeDir | 0o755},
		{contents + "Info.plist", plist, 0o644},
		{contents + "MacOS/hpctun-tray", tray, 0o755},
		{contents + "MacOS/hpctun", hpctun, 0o755},
	}
	seen := map[string]bool{}
	for _, p := range in.Extras {
		base := filepath.Base(p)
		if seen[base] {
			return fmt.Errorf("two extra files are named %s", base)
		}
		seen[base] = true
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		entries = append(entries, entry{contents + base, data, 0o644})
	}

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(out)
		}
	}()
	zw := zip.NewWriter(f)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Modified: epoch}
		if !e.mode.IsDir() {
			h.Method = zip.Deflate
		}
		h.SetMode(e.mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if _, err := w.Write(e.data); err != nil {
			return err
		}
	}
	return zw.Close()
}
