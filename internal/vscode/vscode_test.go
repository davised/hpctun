package vscode

import (
	"errors"
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestFind(t *testing.T) {
	noPath := func(string) (string, error) { return "", errors.New("not found") }
	onPath := func(name string) (string, error) { return "/usr/local/bin/" + name, nil }
	only := func(p string) func(string) bool { return func(q string) bool { return q == p } }
	if got, ok := Find("darwin", env(nil), onPath, only("")); !ok || got != "/usr/local/bin/code" {
		t.Errorf("PATH: %q %v", got, ok)
	}
	app := "/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code"
	if got, ok := Find("darwin", env(nil), noPath, only(app)); !ok || got != app {
		t.Errorf("mac app: %q %v", got, ok)
	}
	user := "/Users/me/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code"
	if got, ok := Find("darwin", env(map[string]string{"HOME": "/Users/me"}), noPath, only(user)); !ok || got != user {
		t.Errorf("mac user app: %q %v", got, ok)
	}
	win := `C:\Users\me\AppData\Local\Programs\Microsoft VS Code\bin\code.cmd`
	if got, ok := Find("windows", env(map[string]string{"LOCALAPPDATA": `C:\Users\me\AppData\Local`}), noPath, only(win)); !ok || got != win {
		t.Errorf("windows: %q %v", got, ok)
	}
	if got, ok := Find("windows", env(nil), onPath, only("")); !ok || got != "/usr/local/bin/code.cmd" {
		t.Errorf("windows PATH: %q %v", got, ok)
	}
	if _, ok := Find("linux", env(nil), noPath, only("")); ok {
		t.Error("found nothing, reported found")
	}
}

func TestHeadless(t *testing.T) {
	for _, c := range []struct {
		goos string
		env  map[string]string
		want bool
	}{
		{"darwin", nil, false},
		{"darwin", map[string]string{"SSH_CONNECTION": "1 2 3 4"}, true},
		{"linux", nil, true},
		{"linux", map[string]string{"DISPLAY": ":0"}, false},
		{"linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, false},
		{"windows", nil, false},
	} {
		if got := Headless(c.goos, env(c.env)); got != c.want {
			t.Errorf("%s %v: %v", c.goos, c.env, got)
		}
	}
}

func TestSettingsPath(t *testing.T) {
	if got := SettingsPath("darwin", env(nil), "/Users/me"); got != filepath.Join("/Users/me", "Library", "Application Support", "Code", "User", "settings.json") {
		t.Errorf("darwin %q", got)
	}
	if got := SettingsPath("windows", env(map[string]string{"APPDATA": `C:\A`}), "x"); got != filepath.Join(`C:\A`, "Code", "User", "settings.json") {
		t.Errorf("windows %q", got)
	}
	if got := SettingsPath("linux", env(nil), "/home/me"); got != filepath.Join("/home/me", ".config", "Code", "User", "settings.json") {
		t.Errorf("linux %q", got)
	}
	if got := SettingsPath("linux", env(map[string]string{"XDG_CONFIG_HOME": "/x"}), "/home/me"); got != filepath.Join("/x", "Code", "User", "settings.json") {
		t.Errorf("linux xdg %q", got)
	}
}

func TestTunnelFolderURI(t *testing.T) {
	if got := TunnelFolderURI("hpc-me", "/home/me/My Project/a&b"); got != "vscode-remote://tunnel+hpc-me/home/me/My%20Project/a%26b" {
		t.Fatal(got)
	}
	if got := TunnelFolderURI("hpc-me", "/home/me/My Project#2/ü/R&D"); got != "vscode-remote://tunnel+hpc-me/home/me/My%20Project%232/%C3%BC/R%26D" {
		t.Fatal(got)
	}
}

func TestHasExtensionID(t *testing.T) {
	list := "ms-python.python\r\nMS-VSCODE.remote-server\n"
	if !HasExtensionID(list, RemoteTunnels) || HasExtensionID(list, "ms-vscode-remote.remote-ssh") {
		t.Fatal("wrong")
	}
}
