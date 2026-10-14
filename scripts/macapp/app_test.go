package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"hpctun/internal/selfupdate"
)

func TestBundleVersion(t *testing.T) {
	for in, want := range map[string]string{
		"v0.20261008-abc1234":       "0.20261008",
		"v1.2-x":                    "1.2",
		"v0.20261008-abc1234-dirty": "0.20261008",
	} {
		if got, err := bundleVersion(in); err != nil || got != want {
			t.Errorf("bundleVersion(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "v0-dev", "0.20261008-abc", "v0.2026"} {
		if got, err := bundleVersion(bad); err == nil {
			t.Errorf("bundleVersion(%q) = %q, want an error", bad, got)
		}
	}
}

func TestInfoPlist(t *testing.T) {
	b, err := infoPlist("v0.20261008-abc1234")
	if err != nil {
		t.Fatal(err)
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		if _, err := d.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("not well-formed XML: %v", err)
		}
	}
	s := string(b)
	for _, want := range []string{
		"<key>CFBundleIdentifier</key>\n\t<string>edu.oregonstate.hpctun-tray</string>",
		"<key>CFBundleExecutable</key>\n\t<string>hpctun-tray</string>",
		"<key>CFBundleShortVersionString</key>\n\t<string>0.20261008</string>",
		"<key>CFBundleVersion</key>\n\t<string>0.20261008</string>",
		"<key>LSMinimumSystemVersion</key>\n\t<string>12.0</string>",
		"<key>LSUIElement</key>\n\t<true/>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Info.plist lacks %q", want)
		}
	}
}

func testInputs(t *testing.T) appInputs {
	arm, amd := buildHello(t, "arm64"), buildHello(t, "amd64")
	dir := t.TempDir()
	lic, site := filepath.Join(dir, "LICENSE"), filepath.Join(dir, "osu.toml")
	os.WriteFile(lic, []byte("bsd"), 0o600)
	os.WriteFile(site, []byte("[ssh]\n"), 0o600)
	return appInputs{Version: "v0.20261008-abc1234", HPCtun: [2]string{arm, amd}, Tray: [2]string{amd, arm}, Extras: []string{lic, site}}
}

func TestWriteApp(t *testing.T) {
	in := testInputs(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.zip"), filepath.Join(dir, "b.zip")
	if err := writeApp(a, in); err != nil {
		t.Fatal(err)
	}
	if err := writeApp(b, in); err != nil {
		t.Fatal(err)
	}
	ab, _ := os.ReadFile(a)
	bb, _ := os.ReadFile(b)
	if !bytes.Equal(ab, bb) {
		t.Error("two runs on the same inputs gave different zips")
	}
	zr, err := zip.OpenReader(a)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	modes := map[string]fs.FileMode{}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		modes[f.Name] = f.Mode()
	}
	want := []string{
		"hpctun-tray.app/",
		"hpctun-tray.app/Contents/",
		"hpctun-tray.app/Contents/MacOS/",
		"hpctun-tray.app/Contents/Info.plist",
		"hpctun-tray.app/Contents/MacOS/hpctun-tray",
		"hpctun-tray.app/Contents/MacOS/hpctun",
		"hpctun-tray.app/Contents/LICENSE",
		"hpctun-tray.app/Contents/osu.toml",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("entries:\n%q\nwant\n%q", names, want)
	}
	for name, mode := range map[string]fs.FileMode{
		"hpctun-tray.app/":                           fs.ModeDir | 0o755,
		"hpctun-tray.app/Contents/MacOS/hpctun-tray": 0o755,
		"hpctun-tray.app/Contents/MacOS/hpctun":      0o755,
		"hpctun-tray.app/Contents/Info.plist":        0o644,
		"hpctun-tray.app/Contents/LICENSE":           0o644,
	} {
		if modes[name] != mode {
			t.Errorf("%s: mode %v, want %v", name, modes[name], mode)
		}
	}
	dest := t.TempDir()
	if err := selfupdate.ExtractTree(a, appName, dest); err != nil {
		t.Fatalf("ExtractTree: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dest, appName, "Contents", "MacOS", "hpctun-tray"))
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("extracted hpctun-tray: %v, %v", fi, err)
	}
}

func TestWriteAppRefusesBadInputsAndWritesNothing(t *testing.T) {
	for name, change := range map[string]func(*appInputs){
		"bad version":     func(in *appInputs) { in.Version = "v0-dev" },
		"not a program":   func(in *appInputs) { in.Tray[0] = in.Extras[0] },
		"duplicate extra": func(in *appInputs) { in.Extras = append(in.Extras, in.Extras[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			in := testInputs(t)
			change(&in)
			out := filepath.Join(t.TempDir(), "app.zip")
			if err := writeApp(out, in); err == nil {
				t.Fatal("writeApp succeeded")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Errorf("%s was left behind (%v)", out, err)
			}
		})
	}
}
