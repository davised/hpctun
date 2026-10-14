// Package vscode finds VS Code's command-line launcher, builds the URIs that
// open a folder through a tunnel, and adds hpctun's recommended settings.
package vscode

import (
	"net/url"
	"path/filepath"
	"strings"
)

// RemoteTunnels is the Remote - Tunnels extension's id.
const RemoteTunnels = "ms-vscode.remote-server"

// Find returns VS Code's `code` launcher: from PATH, else from where the
// installer puts it (most Mac users never add it to PATH).
func Find(goos string, getenv func(string) string, lookPath func(string) (string, error), exists func(string) bool) (string, bool) {
	name := "code"
	if goos == "windows" {
		name = "code.cmd"
	}
	if p, err := lookPath(name); err == nil {
		return p, true
	}
	var cands []string
	switch goos {
	case "darwin":
		app := "Applications/Visual Studio Code.app/Contents/Resources/app/bin/code"
		cands = append(cands, "/"+app)
		if h := getenv("HOME"); h != "" {
			cands = append(cands, h+"/"+app)
		}
	case "windows":
		if l := getenv("LOCALAPPDATA"); l != "" {
			cands = append(cands, l+`\Programs\Microsoft VS Code\bin\code.cmd`)
		}
		if p := getenv("ProgramFiles"); p != "" {
			cands = append(cands, p+`\Microsoft VS Code\bin\code.cmd`)
		}
	}
	for _, c := range cands {
		if exists(c) {
			return c, true
		}
	}
	return "", false
}

// HasExtensionID reports whether `code --list-extensions` output lists id.
func HasExtensionID(list, id string) bool {
	for _, l := range strings.Split(list, "\n") {
		if strings.EqualFold(strings.TrimSpace(l), id) {
			return true
		}
	}
	return false
}

// Headless reports that there is no screen to open VS Code on: hpctun runs
// over ssh, or on Linux without a display.
func Headless(goos string, getenv func(string) string) bool {
	if getenv("SSH_CONNECTION") != "" {
		return true
	}
	return goos == "linux" && getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == ""
}

// escapePath escapes each segment of a path for use in a vscode-remote URI.
// It handles both URL encoding and the special case of & which cmd.exe treats
// as a command separator.
func escapePath(path string) string {
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, s := range segs {
		// url.PathEscape leaves & unescaped; on Windows code.cmd runs through
		// cmd.exe, which treats it as a command separator.
		segs[i] = strings.ReplaceAll(url.PathEscape(s), "&", "%26")
	}
	return strings.Join(segs, "/")
}

// TunnelFolderURI opens path (absolute, on the remote host) through the
// tunnel name.
func TunnelFolderURI(name, path string) string {
	return "vscode-remote://tunnel+" + name + "/" + escapePath(path)
}

// SettingsPath is VS Code's user settings.json.
func SettingsPath(goos string, getenv func(string) string, home string) string {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Code", "User", "settings.json")
	case "windows":
		return filepath.Join(getenv("APPDATA"), "Code", "User", "settings.json")
	}
	base := getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "Code", "User", "settings.json")
}
