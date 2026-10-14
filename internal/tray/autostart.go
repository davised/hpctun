package tray

import (
	"errors"
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"hpctun/internal/atomicfile"
)

// Autostart is the "Start at login" file for the tray at Exe: a
// LaunchAgent on macOS, an XDG autostart entry on Linux, a Run value in
// the registry on Windows.
type Autostart struct {
	GOOS       string
	Home       string
	ConfigHome string // $XDG_CONFIG_HOME; "" means ~/.config
	Exe        string
}

const launchAgentLabel = "edu.oregonstate.hpctun-tray"

// runKey holds the programs Windows starts when the user logs in.
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

const runValue = "hpctun-tray"

// Path is where the file goes.
func (s Autostart) Path() string {
	if s.GOOS == "windows" {
		return `HKCU\` + runKey + `\` + runValue
	}
	if s.GOOS == "darwin" {
		return filepath.Join(s.Home, "Library", "LaunchAgents", launchAgentLabel+".plist")
	}
	dir := s.ConfigHome
	if dir == "" {
		dir = filepath.Join(s.Home, ".config")
	}
	return filepath.Join(dir, "autostart", "hpctun-tray.desktop")
}

// Content is the file.
func (s Autostart) Content() string {
	if s.GOOS == "windows" {
		return `"` + s.Exe + `"`
	}
	if s.GOOS == "darwin" {
		return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchAgentLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + html.EscapeString(s.Exe) + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`
	}
	return `[Desktop Entry]
Type=Application
Name=hpctun tray
Comment=ssh tunnels through the cluster gateway
Exec=` + desktopQuote(s.Exe) + `
Terminal=false
X-GNOME-Autostart-enabled=true
`
}

// desktopQuote quotes an Exec argument as the Desktop Entry spec asks:
// in double quotes, with ", `, $ and \ escaped by a backslash; % is
// doubled, as Exec reserves it for field codes.
func desktopQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`, `%`, `%%`)
	return `"` + r.Replace(s) + `"`
}

// Enabled reports whether the file is there.
func (s Autostart) Enabled() bool {
	if s.GOOS == "windows" {
		v, ok := readRunValue()
		return ok && v != ""
	}
	_, err := os.Stat(s.Path())
	return err == nil
}

// Set writes or removes the file.
func (s Autostart) Set(on bool) error {
	if s.GOOS == "windows" {
		if !on {
			return deleteRunValue()
		}
		return writeRunValue(s.Content())
	}
	p := s.Path()
	if !on {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(p, []byte(s.Content()), 0o644)
}

// OpenFileCommand is the command that opens path in the user's editor.
func OpenFileCommand(goos, path string) []string {
	switch goos {
	case "darwin":
		return []string{"open", "-t", path}
	case "windows":
		return []string{"notepad.exe", path} // .toml usually has no program of its own
	}
	return []string{"xdg-open", path}
}
