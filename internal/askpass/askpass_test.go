package askpass

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestForPrompt(t *testing.T) {
	cases := []struct {
		prompt, extra string
		want          Dialog
	}{
		{"(me@shell.example.org) Password: ", "", Dialog{Secret, "hpctun: me@shell.example.org", "(me@shell.example.org) Password:"}},
		{"Enter passphrase for key '/Users/me/.ssh/id_ed25519': ", "", Dialog{Secret, "hpctun: key id_ed25519", "Enter passphrase for key '/Users/me/.ssh/id_ed25519':"}},
		{"Enter passphrase for /home/me/.ssh/id_rsa: ", "", Dialog{Secret, "hpctun: key id_rsa", "Enter passphrase for /home/me/.ssh/id_rsa:"}},
		{"me@127.0.0.1's password: ", "", Dialog{Secret, "hpctun: me@127.0.0.1", "me@127.0.0.1's password:"}},
		{"Are you sure you want to continue connecting (yes/no/[fingerprint])? ", "", Dialog{Question, "hpctun", "Are you sure you want to continue connecting (yes/no/[fingerprint])?"}},
		{"Passcode or option (1-2): ", "Duo two-factor login for me\n 1. Duo Push to XXX-XXX-1234", Dialog{Secret, "hpctun", "Duo two-factor login for me\n 1. Duo Push to XXX-XXX-1234\n\nPasscode or option (1-2):"}},
	}
	for _, c := range cases {
		if got := ForPrompt(c.prompt, c.extra); got != c.want {
			t.Errorf("ForPrompt(%q) = %+v\nwant %+v", c.prompt, got, c.want)
		}
	}
}

func TestSecondFactor(t *testing.T) {
	for p, want := range map[string]bool{
		"Passcode or option (1-3): ":       true,
		"Enter a passcode or select one: ": true,
		"(me@gw) Password: ":               false,
	} {
		if SecondFactor(p) != want {
			t.Errorf("SecondFactor(%q) = %v", p, !want)
		}
	}
}

func TestLogTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.log")
	log := "Warning: Permanently added 'gw' (ED25519) to the list of known hosts.\r\n" +
		"Duo two-factor login for me\n\nEnter a passcode or select one of the following options:\n\n 1. Duo Push to XXX-XXX-1234\n 2. Phone call\n\n"
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	want := "Enter a passcode or select one of the following options:\n 1. Duo Push to XXX-XXX-1234\n 2. Phone call"
	if got := LogTail(path, 3); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := LogTail(filepath.Join(t.TempDir(), "none"), 3); got != "" {
		t.Errorf("missing log: %q", got)
	}
}

func TestShow(t *testing.T) {
	has := func(names ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			for _, h := range names {
				if h == n {
					return "/usr/bin/" + n, nil
				}
			}
			return "", exec.ErrNotFound
		}
	}
	secret, question := Dialog{Secret, "T", "P"}, Dialog{Question, "T", "Q?"}
	cases := []struct {
		name     string
		d        Dialog
		goos     string
		look     func(string) (string, error)
		out      string
		exit     int
		wantArgv []string
		want     string
		wantErr  error
	}{
		{"zenity secret", secret, "linux", has("zenity", "kdialog"), "s3cret 'x'\n", 0,
			[]string{"zenity", "--entry", "--hide-text", "--title", "T", "--text", "P", "--no-markup"}, "s3cret 'x'", nil},
		{"zenity cancel", secret, "linux", has("zenity"), "", 1, nil, "", ErrCancelled},
		{"zenity question no", question, "linux", has("zenity"), "", 1,
			[]string{"zenity", "--question", "--title", "T", "--text", "Q?", "--no-markup"}, "no", nil},
		{"kdialog secret", secret, "linux", has("kdialog"), "pw\n", 0,
			[]string{"kdialog", "--title", "T", "--password", "<qt>P</qt>"}, "pw", nil},
		{"kdialog question yes", question, "linux", has("kdialog"), "", 0,
			[]string{"kdialog", "--title", "T", "--yesno", "<qt>Q?</qt>"}, "yes", nil},
		{"no dialog", secret, "linux", has(), "", 0, nil, "", ErrNoDialog},
		{"macOS secret", secret, "darwin", has(), "pw\n", 0,
			[]string{"osascript", "-e", appleScript, "T", "P", "secret"}, "pw", nil},
		{"macOS question", question, "darwin", has(), "yes\n", 0,
			[]string{"osascript", "-e", appleScript, "T", "Q?", "question"}, "yes", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var argv []string
			got, err := Show(c.d, c.goos, c.look, func(a []string) (string, int, error) {
				argv = a
				return c.out, c.exit, nil
			})
			if !errors.Is(err, c.wantErr) || got != c.want {
				t.Fatalf("got %q, %v; want %q, %v", got, err, c.want, c.wantErr)
			}
			if c.wantArgv != nil && !reflect.DeepEqual(argv, c.wantArgv) {
				t.Fatalf("argv %q\nwant %q", argv, c.wantArgv)
			}
		})
	}
}

// kdialog renders text that looks like HTML; a server's prompt is shown
// exactly as sent.
func TestKdialogTextEscaped(t *testing.T) {
	argv, err := command(Dialog{Secret, "T", "<a href=\"x\">Duo</a>\n1. Push"}, "linux", func(n string) (string, error) {
		if n == "kdialog" {
			return "/usr/bin/kdialog", nil
		}
		return "", exec.ErrNotFound
	})
	if err != nil || argv[len(argv)-1] != "<qt>&lt;a href=&#34;x&#34;&gt;Duo&lt;/a&gt;<br>1. Push</qt>" {
		t.Fatalf("%q %v", argv, err)
	}
}

// The wrapper runs the binary with askpass and the prompt, whatever the
// binary's path contains.
func TestScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	dir := filepath.Join(t.TempDir(), "it's a dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "hpctun $x")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s|' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "askpass.sh")
	if err := os.WriteFile(script, []byte(Script(exe)), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(script, "(me@gw) Password: ").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "askpass|(me@gw) Password: |" {
		t.Fatalf("got %q", got)
	}
	if !strings.HasPrefix(Script(exe), "#!/bin/sh\n") {
		t.Fatal("no shebang")
	}
}

// On a Mac, Return on the host-key question answers No: accepting a key
// takes a deliberate click.
func TestAppleScriptQuestionDefaultsToNo(t *testing.T) {
	if !strings.Contains(appleScript, `buttons {"No", "Yes"} default button "No" cancel button "No"`) {
		t.Fatalf("question dialog:\n%s", appleScript)
	}
}
