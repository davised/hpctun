// Package browser finds web browsers and starts them: a Chromium browser on
// a profile of its own behind a SOCKS proxy, or the user's default browser
// for a page on localhost.
package browser

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"hpctun/internal/detach"
	"hpctun/internal/sshctl"
)

// Browser is a Chromium-family browser hpctun can start.
type Browser struct {
	Name string // for messages: "Google Chrome"
	Path string // the executable
}

// ErrNoChromium means no Chromium-family browser was found.
var ErrNoChromium = errors.New("no Chrome, Edge, Brave or Chromium found")

// Env is everything the package touches outside itself. Tests fake each.
type Env struct {
	GOOS     string
	Getenv   func(string) string
	Home     string
	Stat     func(string) (fs.FileInfo, error)
	LookPath func(string) (string, error)
	Runner   sshctl.Runner             // plutil, xdg-settings and ps: programs that exit
	Start    func(argv []string) error // starts a program without waiting for it

	// Windows only, where another process's command line cannot be read
	// cheaply: whether a file is held open (Chromium's profile lockfile),
	// and the port file hpctun writes beside it.
	InUse     func(path string) bool
	ReadFile  func(string) ([]byte, error)
	WriteFile func(string, []byte) error
}

// chromium is one Chromium-family browser and how each platform names it.
type chromium struct {
	name     string
	app      string   // macOS bundle in /Applications
	bundle   string   // macOS bundle ID
	desktops []string // Linux .desktop names
	bins     []string // Linux executables on PATH
	winPaths []string // Windows: path under %ProgramFiles%, %ProgramFiles(x86)% or %LOCALAPPDATA%
	progIDs  []string // Windows: the https UserChoice ProgId's prefix
}

// chromiums, in the order a browser is picked when the default is not one.
var chromiums = []chromium{
	{
		name: "Google Chrome", app: "Google Chrome.app", bundle: "com.google.chrome",
		desktops: []string{"google-chrome.desktop"},
		bins:     []string{"google-chrome", "google-chrome-stable"},
		winPaths: []string{filepath.Join("Google", "Chrome", "Application", "chrome.exe")},
		progIDs:  []string{"ChromeHTML"},
	},
	{
		name: "Microsoft Edge", app: "Microsoft Edge.app", bundle: "com.microsoft.edgemac",
		desktops: []string{"microsoft-edge.desktop"},
		bins:     []string{"microsoft-edge", "microsoft-edge-stable"},
		winPaths: []string{filepath.Join("Microsoft", "Edge", "Application", "msedge.exe")},
		progIDs:  []string{"MSEdgeHTM"},
	},
	{
		name: "Brave", app: "Brave Browser.app", bundle: "com.brave.browser",
		desktops: []string{"brave-browser.desktop"},
		bins:     []string{"brave-browser"},
		winPaths: []string{filepath.Join("BraveSoftware", "Brave-Browser", "Application", "brave.exe")},
		progIDs:  []string{"BraveHTML"},
	},
	{
		name: "Chromium", app: "Chromium.app", bundle: "org.chromium.chromium",
		desktops: []string{"chromium.desktop", "chromium-browser.desktop"},
		bins:     []string{"chromium", "chromium-browser"},
		winPaths: []string{filepath.Join("Chromium", "Application", "chrome.exe")},
		progIDs:  []string{"ChromiumHTM"},
	},
}

// locate finds c's executable.
func (e Env) locate(c chromium) (Browser, bool) {
	if e.GOOS == "darwin" {
		bin := strings.TrimSuffix(c.app, ".app")
		for _, dir := range []string{"/Applications", filepath.Join(e.Home, "Applications")} {
			p := filepath.Join(dir, c.app, "Contents", "MacOS", bin)
			if st, err := e.Stat(p); err == nil && !st.IsDir() {
				return Browser{Name: c.name, Path: p}, true
			}
		}
		return Browser{}, false
	}
	if e.GOOS == "windows" {
		for _, root := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
			dir := e.Getenv(root)
			if dir == "" {
				continue
			}
			for _, rel := range c.winPaths {
				p := filepath.Join(dir, rel)
				if st, err := e.Stat(p); err == nil && !st.IsDir() {
					return Browser{Name: c.name, Path: p}, true
				}
			}
		}
		return Browser{}, false
	}
	for _, n := range c.bins {
		if p, err := e.LookPath(n); err == nil {
			return Browser{Name: c.name, Path: p}, true
		}
	}
	return Browser{}, false
}

// defaultChromium is the default browser when it is a Chromium browser
// that can be found. Any doubt (no answer, an error) means it is not.
func (e Env) defaultChromium(ctx context.Context) (Browser, bool) {
	match := func(c chromium) bool { return false }
	switch e.GOOS {
	case "darwin":
		plist := filepath.Join(e.Home, "Library", "Preferences", "com.apple.LaunchServices", "com.apple.launchservices.secure.plist")
		res, err := e.Runner.Run(ctx, "plutil", "-convert", "json", "-o", "-", plist)
		if err != nil || res.ExitCode != 0 {
			return Browser{}, false
		}
		var doc struct {
			Handlers []struct {
				Scheme string `json:"LSHandlerURLScheme"`
				Role   string `json:"LSHandlerRoleAll"`
			} `json:"LSHandlers"`
		}
		if json.Unmarshal(res.Stdout, &doc) != nil {
			return Browser{}, false
		}
		id := ""
		for _, h := range doc.Handlers {
			if h.Scheme == "https" {
				id = h.Role
			}
		}
		match = func(c chromium) bool { return strings.EqualFold(c.bundle, id) }
	case "windows":
		res, err := e.Runner.Run(ctx, "reg", "query", `HKCU\Software\Microsoft\Windows\Shell\Associations\UrlAssociations\https\UserChoice`, "/v", "ProgId")
		if err != nil || res.ExitCode != 0 {
			return Browser{}, false
		}
		id := ""
		for line := range strings.SplitSeq(string(res.Stdout), "\n") {
			if f := strings.Fields(line); len(f) >= 3 && f[0] == "ProgId" {
				id = f[2]
			}
		}
		match = func(c chromium) bool {
			for _, p := range c.progIDs {
				if id != "" && strings.HasPrefix(id, p) {
					return true
				}
			}
			return false
		}
	default:
		res, err := e.Runner.Run(ctx, "xdg-settings", "get", "default-web-browser")
		if err != nil || res.ExitCode != 0 {
			return Browser{}, false
		}
		name := strings.TrimSpace(string(res.Stdout))
		match = func(c chromium) bool {
			for _, d := range c.desktops {
				if d == name {
					return true
				}
			}
			return false
		}
	}
	for _, c := range chromiums {
		if match(c) {
			return e.locate(c)
		}
	}
	return Browser{}, false
}

// pick is the browser for a proxied window: HPCTUN_BROWSER, else the default
// browser if it is Chromium, else the first one found.
func (e Env) pick(ctx context.Context) (Browser, error) {
	if p := e.Getenv("HPCTUN_BROWSER"); p != "" {
		return Browser{Name: filepath.Base(p), Path: p}, nil
	}
	if b, ok := e.defaultChromium(ctx); ok {
		return b, nil
	}
	for _, c := range chromiums {
		if b, ok := e.locate(c); ok {
			return b, nil
		}
	}
	return Browser{}, ErrNoChromium
}

// ProxyArgs is the command line for a window on profile that sends
// everything, names included, through the SOCKS proxy on port. url "" opens
// a blank window.
func ProxyArgs(b Browser, profile string, port int, url string) []string {
	argv := []string{b.Path, "--user-data-dir=" + profile,
		fmt.Sprintf("--proxy-server=socks5://127.0.0.1:%d", port),
		"--no-first-run", "--no-default-browser-check"}
	if url != "" {
		argv = append(argv, url)
	}
	return argv
}

// OpenProxied opens url (or a blank window) in a Chromium browser on
// profile, behind the SOCKS proxy on port. A window already running on
// profile gets the url and keeps its own proxy: oldPort reports that
// proxy's port when it differs.
func (e Env) OpenProxied(ctx context.Context, profile string, port int, url string) (Browser, int, error) {
	return e.openProxied(ctx, profile, port, url, func(dir string) error { return os.MkdirAll(dir, 0o700) })
}

func (e Env) openProxied(ctx context.Context, profile string, port int, url string, mkdir func(string) error) (Browser, int, error) {
	b, err := e.pick(ctx)
	if err != nil {
		return Browser{}, 0, err
	}
	if err := mkdir(profile); err != nil {
		return Browser{}, 0, err
	}
	old, fresh := 0, true
	if p, ok := e.runningPort(ctx, profile); ok {
		fresh = false
		if p != port {
			old = p
		}
	}
	if err := e.Start(ProxyArgs(b, profile, port, url)); err != nil {
		return Browser{}, 0, fmt.Errorf("start %s: %w", b.Name, err)
	}
	if e.GOOS == "windows" && fresh {
		// Best effort: without it, the next browse misses only the
		// "keeps its old proxy" warning. A window already running
		// keeps its old proxy, so its port file must stay as it is.
		_ = e.WriteFile(filepath.Join(profile, portFile), []byte(strconv.Itoa(port)+"\n"))
	}
	return b, old, nil
}

var proxyPort = regexp.MustCompile(`--proxy-server=socks5://127\.0\.0\.1:(\d+)`)

// portFile, in a proxied window's profile on Windows, holds the SOCKS port
// the window was started with.
const portFile = "hpctun-proxy-port"

// runningPort finds a browser already running on profile and the SOCKS port
// it was started with.
func (e Env) runningPort(ctx context.Context, profile string) (int, bool) {
	if e.GOOS == "windows" {
		if e.InUse == nil || !e.InUse(filepath.Join(profile, "lockfile")) {
			return 0, false
		}
		b, err := e.ReadFile(filepath.Join(profile, portFile))
		if err != nil {
			return 0, false
		}
		p, err := strconv.Atoi(strings.TrimSpace(string(b)))
		return p, err == nil
	}
	res, err := e.Runner.Run(ctx, "ps", "-ww", "-eo", "args")
	if err != nil || res.ExitCode != 0 {
		return 0, false
	}
	flag := "--user-data-dir=" + profile
	sc := bufio.NewScanner(bytes.NewReader(res.Stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		i := strings.Index(line, flag)
		if i < 0 {
			continue
		}
		if rest := line[i+len(flag):]; rest != "" && rest[0] != ' ' {
			continue // another profile whose path starts with ours
		}
		if m := proxyPort.FindStringSubmatch(line); m != nil {
			p, _ := strconv.Atoi(m[1])
			return p, true
		}
	}
	return 0, false
}

// OpenPage opens url in the user's own browser, never in a proxied window:
// a Chromium default is started by its executable, which hands the url to
// the instance on its default profile; anything else goes to the system's
// opener. desc names the browser for messages.
func (e Env) OpenPage(ctx context.Context, url string) (string, error) {
	if b, ok := e.defaultChromium(ctx); ok {
		if err := e.Start([]string{b.Path, url}); err != nil {
			return "", fmt.Errorf("start %s: %w", b.Name, err)
		}
		return b.Name, nil
	}
	var argv []string
	switch e.GOOS {
	case "darwin":
		argv = []string{"open", url}
	case "windows":
		// Not cmd /c start, which splits a URL at '&'.
		argv = []string{"rundll32", "url.dll,FileProtocolHandler", url}
	default:
		argv = []string{"xdg-open", url}
	}
	if err := e.Start(argv); err != nil {
		return "", fmt.Errorf("%s: %w", argv[0], err)
	}
	return "the default browser", nil
}

// StartDetached starts argv with no input or output and does not wait for
// it: a browser outlives hpctun, in a session (process group, on Windows) of
// its own so a Ctrl-C at the terminal or a kill of hpctun's process group
// does not reach it.
func StartDetached(argv []string) error {
	cmd, err := detach.Start(func() *exec.Cmd { return exec.Command(argv[0], argv[1:]...) })
	if err != nil {
		return err
	}
	return cmd.Process.Release()
}
