package browser

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
)

type fakeFile struct{ fs.FileInfo }

func (fakeFile) IsDir() bool { return false }

// testEnv: files exist at the given paths, PATH holds the given names,
// commands answer from cmds (by full line; missing: exit 1), and Start records.
func testEnv(t *testing.T, goos string, files, path []string, cmds map[string]string) (Env, *[][]string) {
	var started [][]string
	return Env{
		GOOS: goos, Home: "/home/me",
		Getenv: func(string) string { return "" },
		Stat: func(p string) (fs.FileInfo, error) {
			if slices.Contains(files, p) {
				return fakeFile{}, nil
			}
			return nil, fs.ErrNotExist
		},
		LookPath: func(n string) (string, error) {
			if slices.Contains(path, n) {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
		Runner: &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
			if out, ok := cmds[c.Line()]; ok {
				return sshctl.Result{Stdout: []byte(out)}, nil
			}
			return sshctl.Result{ExitCode: 1}, nil
		}},
		Start: func(argv []string) error { started = append(started, argv); return nil },
	}, &started
}

const (
	macChrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	macBrave  = "/home/me/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"
	lsPlist   = "plutil -convert json -o - /home/me/Library/Preferences/com.apple.LaunchServices/com.apple.launchservices.secure.plist"
)

func lsJSON(httpsHandler string) string {
	return `{"LSHandlers":[{"LSHandlerURLScheme":"mailto","LSHandlerRoleAll":"com.apple.mail"},` +
		`{"LSHandlerURLScheme":"https","LSHandlerRoleAll":"` + httpsHandler + `"}]}`
}

func TestOpenProxied(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "browser", "chat")
	cases := []struct {
		name, goos  string
		files, path []string
		cmds        map[string]string
		env         string
		want        string // binary started
	}{
		{"mac first found", "darwin", []string{macBrave}, nil, nil, "", macBrave},
		{"mac default chrome wins", "darwin", []string{macBrave, macChrome}, nil, map[string]string{lsPlist: lsJSON("com.google.chrome")}, "", macChrome},
		{"mac default not chromium", "darwin", []string{macBrave}, nil, map[string]string{lsPlist: lsJSON("app.zen-browser.zen")}, "", macBrave},
		{"linux path", "linux", nil, []string{"chromium"}, nil, "", "/usr/bin/chromium"},
		{"linux default edge", "linux", nil, []string{"chromium", "microsoft-edge"}, map[string]string{"xdg-settings get default-web-browser": "microsoft-edge.desktop\n"}, "", "/usr/bin/microsoft-edge"},
		{"HPCTUN_BROWSER", "linux", nil, []string{"chromium"}, nil, "/opt/vivaldi/vivaldi", "/opt/vivaldi/vivaldi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, started := testEnv(t, c.goos, c.files, c.path, c.cmds)
			e.Getenv = func(k string) string {
				if k == "HPCTUN_BROWSER" {
					return c.env
				}
				return ""
			}
			b, old, err := e.OpenProxied(context.Background(), profile, 1080, "https://chat.example.org")
			if err != nil || old != 0 {
				t.Fatalf("err %v old %d", err, old)
			}
			want := []string{c.want, "--user-data-dir=" + profile, "--proxy-server=socks5://127.0.0.1:1080",
				"--no-first-run", "--no-default-browser-check", "https://chat.example.org"}
			if len(*started) != 1 || !slices.Equal((*started)[0], want) || b.Path != c.want {
				t.Fatalf("started %q, want %q", *started, want)
			}
			if st, err := os.Stat(profile); err != nil || !st.IsDir() {
				t.Fatalf("profile not created: %v", err)
			}
		})
	}
}

func TestOpenProxiedBlankAndNone(t *testing.T) {
	e, started := testEnv(t, "linux", nil, []string{"chromium"}, nil)
	if _, _, err := e.OpenProxied(context.Background(), t.TempDir(), 1080, ""); err != nil {
		t.Fatal(err)
	}
	if got := (*started)[0]; got[len(got)-1] != "--no-default-browser-check" {
		t.Fatalf("blank window got a url: %q", got)
	}
	e, started = testEnv(t, "linux", nil, nil, nil)
	if _, _, err := e.OpenProxied(context.Background(), t.TempDir(), 1080, ""); !errors.Is(err, ErrNoChromium) || len(*started) != 0 {
		t.Fatalf("err %v started %q", err, *started)
	}
}

// A window already running on this profile with another port is reported;
// one on a profile whose path merely starts with ours is not.
func TestOpenProxiedOldPort(t *testing.T) {
	profile := "/s/browser/chat"
	ps := "/usr/bin/chromium --user-data-dir=/s/browser/chat2 --proxy-server=socks5://127.0.0.1:9999\n" +
		"/usr/bin/chromium --user-data-dir=/s/browser/chat --proxy-server=socks5://127.0.0.1:1081 --no-first-run\n"
	e, _ := testEnv(t, "linux", nil, []string{"chromium"}, map[string]string{"ps -ww -eo args": ps})
	e.Start = func([]string) error { return nil }
	_, old, err := e.openProxied(context.Background(), profile, 1080, "", func(string) error { return nil })
	if err != nil || old != 1081 {
		t.Fatalf("old %d err %v", old, err)
	}
	e, _ = testEnv(t, "linux", nil, []string{"chromium"}, map[string]string{"ps -ww -eo args": ps})
	_, old, _ = e.openProxied(context.Background(), "/s/browser/cha", 1080, "", func(string) error { return nil })
	if old != 0 {
		t.Fatalf("prefix matched: old %d", old)
	}
}

func TestOpenPage(t *testing.T) {
	cases := []struct {
		name, goos  string
		files, path []string
		cmds        map[string]string
		want        []string
		desc        string
	}{
		{"mac chrome default: its binary, no profile", "darwin", []string{macChrome}, nil, map[string]string{lsPlist: lsJSON("com.google.chrome")}, []string{macChrome, "http://localhost:4001/"}, "Google Chrome"},
		{"mac zen default", "darwin", []string{macChrome}, nil, map[string]string{lsPlist: lsJSON("app.zen-browser.zen")}, []string{"open", "http://localhost:4001/"}, "the default browser"},
		{"mac no https entry: Safari", "darwin", []string{macChrome}, nil, map[string]string{lsPlist: `{"LSHandlers":[]}`}, []string{"open", "http://localhost:4001/"}, "the default browser"},
		{"mac plutil fails", "darwin", []string{macChrome}, nil, nil, []string{"open", "http://localhost:4001/"}, "the default browser"},
		{"mac plist garbage", "darwin", []string{macChrome}, nil, map[string]string{lsPlist: "not json"}, []string{"open", "http://localhost:4001/"}, "the default browser"},
		{"linux chromium default", "linux", nil, []string{"chromium"}, map[string]string{"xdg-settings get default-web-browser": "chromium.desktop\n"}, []string{"/usr/bin/chromium", "http://localhost:4001/"}, "Chromium"},
		{"linux firefox default", "linux", nil, []string{"chromium"}, map[string]string{"xdg-settings get default-web-browser": "firefox.desktop\n"}, []string{"xdg-open", "http://localhost:4001/"}, "the default browser"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, started := testEnv(t, c.goos, c.files, c.path, c.cmds)
			desc, err := e.OpenPage(context.Background(), "http://localhost:4001/")
			if err != nil || desc != c.desc {
				t.Fatalf("desc %q err %v", desc, err)
			}
			if len(*started) != 1 || !slices.Equal((*started)[0], c.want) {
				t.Fatalf("started %q, want %q", *started, c.want)
			}
		})
	}
}

var winRoots = map[string]string{"ProgramFiles": `C:\PF`, "ProgramFiles(x86)": `C:\PF86`, "LOCALAPPDATA": `C:\LA`}

func winEnv(t *testing.T, files []string, progID string) (Env, *[][]string) {
	cmds := map[string]string{}
	if progID != "" {
		cmds[`reg query HKCU\Software\Microsoft\Windows\Shell\Associations\UrlAssociations\https\UserChoice /v ProgId`] =
			"\r\nHKEY_CURRENT_USER\\Software\\...\\UserChoice\r\n    ProgId    REG_SZ    " + progID + "\r\n\r\n"
	}
	e, started := testEnv(t, "windows", files, nil, cmds)
	e.Getenv = func(k string) string { return winRoots[k] }
	return e, started
}

func TestWindowsLocate(t *testing.T) {
	edge := filepath.Join(`C:\PF86`, "Microsoft", "Edge", "Application", "msedge.exe")
	chrome := filepath.Join(`C:\LA`, "Google", "Chrome", "Application", "chrome.exe")
	e, _ := winEnv(t, []string{edge, chrome}, "")
	b, err := e.pick(context.Background())
	if err != nil || b.Path != chrome || b.Name != "Google Chrome" {
		t.Fatalf("pick = %+v, %v (no default: first in order)", b, err)
	}
	e, _ = winEnv(t, []string{edge, chrome}, "MSEdgeHTM")
	if b, _ := e.pick(context.Background()); b.Path != edge {
		t.Fatalf("Edge default: %+v", b)
	}
	e, _ = winEnv(t, []string{edge}, "FirefoxURL-308046B0AF4A39CB")
	if b, _ := e.pick(context.Background()); b.Path != edge {
		t.Fatalf("Firefox default: %+v", b)
	}
	e, _ = winEnv(t, nil, "")
	if _, err := e.pick(context.Background()); !errors.Is(err, ErrNoChromium) {
		t.Fatalf("nothing installed: %v", err)
	}
}

func TestWindowsOpenPage(t *testing.T) {
	chrome := filepath.Join(`C:\PF`, "Google", "Chrome", "Application", "chrome.exe")
	e, started := winEnv(t, []string{chrome}, "ChromeHTML")
	if desc, err := e.OpenPage(context.Background(), "http://localhost:8888/?a=1&b=2"); err != nil || desc != "Google Chrome" {
		t.Fatalf("%q %v", desc, err)
	}
	e, started = winEnv(t, []string{chrome}, "FirefoxURL-308046B0AF4A39CB")
	if _, err := e.OpenPage(context.Background(), "http://localhost:8888/?a=1&b=2"); err != nil {
		t.Fatal(err)
	}
	want := []string{"rundll32", "url.dll,FileProtocolHandler", "http://localhost:8888/?a=1&b=2"}
	if got := (*started)[len(*started)-1]; !slices.Equal(got, want) {
		t.Fatalf("started %q", got)
	}
}

func TestWindowsRunningPort(t *testing.T) {
	edge := filepath.Join(`C:\PF86`, "Microsoft", "Edge", "Application", "msedge.exe")
	e, started := winEnv(t, []string{edge}, "")
	files := map[string]string{}
	inUse := map[string]bool{}
	e.ReadFile = func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, fs.ErrNotExist
	}
	e.WriteFile = func(p string, b []byte) error { files[p] = string(b); return nil }
	e.InUse = func(p string) bool { return inUse[p] }
	profile := filepath.Join(`C:\LA`, "hpctun", "browser", "proxy")
	mkdir := func(string) error { return nil }

	_, old, err := e.openProxied(context.Background(), profile, 5005, "", mkdir)
	if err != nil || old != 0 || files[filepath.Join(profile, portFile)] != "5005\n" || len(*started) != 1 {
		t.Fatalf("first: old %d err %v files %v", old, err, files)
	}
	// The window is running (its lockfile is held) on 5005; now 5006.
	inUse[filepath.Join(profile, "lockfile")] = true
	if _, old, _ := e.openProxied(context.Background(), profile, 5006, "", mkdir); old != 5005 {
		t.Fatalf("running: old %d", old)
	}
	// It keeps its old proxy: the port file must stay as it is, and a
	// second browse must still warn.
	if files[filepath.Join(profile, portFile)] != "5005\n" {
		t.Fatalf("running: port file rewritten %v", files)
	}
	if _, old, _ := e.openProxied(context.Background(), profile, 5006, "", mkdir); old != 5005 {
		t.Fatalf("running again: old %d", old)
	}
	if files[filepath.Join(profile, portFile)] != "5005\n" {
		t.Fatalf("running again: port file rewritten %v", files)
	}
	// Not running: a stale port file means nothing.
	inUse = map[string]bool{}
	files[filepath.Join(profile, portFile)] = "5005\n"
	if _, old, _ := e.openProxied(context.Background(), profile, 5006, "", mkdir); old != 0 {
		t.Fatalf("stale port file: old %d", old)
	}
}

// StartDetached does not wait for the program.
func TestStartDetached(t *testing.T) {
	if _, err := os.Stat("/bin/sleep"); err != nil {
		t.Skip("no /bin/sleep")
	}
	start := time.Now()
	if err := StartDetached([]string{"/bin/sleep", "5"}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("waited for the program")
	}
	if err := StartDetached([]string{"/nonexistent/browser"}); err == nil || !strings.Contains(err.Error(), "nonexistent") {
		t.Fatalf("err = %v", err)
	}
}
