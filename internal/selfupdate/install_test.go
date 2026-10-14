package selfupdate

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDetect(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	plain := filepath.Join(root, "plain", "hpctun")
	touch(t, plain)
	withTray := filepath.Join(root, "tray", "hpctun")
	touch(t, withTray)
	touch(t, filepath.Join(root, "tray", "hpctun-tray"))
	app := filepath.Join(root, "Applications", "hpctun-tray.app")
	inApp := filepath.Join(app, "Contents", "MacOS", "hpctun")
	touch(t, inApp)

	cases := map[string]struct {
		exe  string
		want Install
	}{
		"plain":     {plain, Install{Kind: Plain, Exe: plain}},
		"with tray": {withTray, Install{Kind: PlainWithTray, Exe: withTray, Tray: filepath.Join(root, "tray", "hpctun-tray")}},
		"app":       {inApp, Install{Kind: App, Exe: inApp, Bundle: app}},
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "bin", "hpctun")
		os.MkdirAll(filepath.Dir(link), 0o755)
		if err := os.Symlink(inApp, link); err != nil {
			t.Fatal(err)
		}
		cases["symlink into app"] = struct {
			exe  string
			want Install
		}{link, Install{Kind: App, Exe: inApp, Bundle: app}}
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Detect(c.exe)
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Detect = %+v, %v; want %+v", got, err, c.want)
			}
		})
	}
	if _, err := Detect(filepath.Join(root, "nope")); err == nil {
		t.Fatal("missing executable accepted")
	}
}

func TestAsset(t *testing.T) {
	const v = "v0.20261007-ab131b6"
	cases := []struct {
		name         string
		in           Install
		goos, goarch string
		archive      string
		members      []string
	}{
		{"mac", Install{Kind: Plain}, "darwin", "arm64", "hpctun-" + v + "-darwin-arm64.tar.gz", []string{"hpctun-" + v + "-darwin-arm64/hpctun"}},
		{"windows", Install{Kind: Plain}, "windows", "amd64", "hpctun-" + v + "-windows-amd64.zip", []string{"hpctun-" + v + "-windows-amd64/hpctun.exe"}},
		{"linux tray", Install{Kind: PlainWithTray}, "linux", "amd64", "hpctun-" + v + "-linux-amd64.tar.gz",
			[]string{"hpctun-" + v + "-linux-amd64/hpctun", "hpctun-" + v + "-linux-amd64/hpctun-tray"}},
		{"app", Install{Kind: App}, "darwin", "amd64", "hpctun-tray-" + v + "-darwin-universal.zip", nil},
	}
	for _, c := range cases {
		archive, members, err := c.in.Asset(v, c.goos, c.goarch)
		if err != nil || archive != c.archive || !reflect.DeepEqual(members, c.members) {
			t.Errorf("%s: %q %q %v", c.name, archive, members, err)
		}
	}
	if _, _, err := (Install{Kind: Plain}).Asset(v, "linux", "arm64"); err == nil || err.Error() != "no release for linux/arm64" {
		t.Fatalf("linux/arm64: %v", err)
	}
	for in, want := range map[Kind]string{Plain: "hpctun", PlainWithTray: "hpctun and hpctun-tray"} {
		if got := (Install{Kind: in}).Describe(); got != want {
			t.Errorf("Describe %v = %q", in, got)
		}
	}
	if got := (Install{Kind: App, Bundle: "/Applications/hpctun-tray.app"}).Describe(); got != "hpctun-tray.app" {
		t.Errorf("Describe app = %q", got)
	}
}
