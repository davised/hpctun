// Package askpass is hpctun's SSH_ASKPASS program: it shows ssh's prompt
// in a dialog window when there is no terminal to ask on.
package askpass

import (
	"errors"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Kind is the dialog a prompt needs.
type Kind int

const (
	Secret   Kind = iota // a password, passphrase or code: input hidden
	Question             // yes/no, such as accepting a new host key
)

// Dialog is one question to put on screen.
type Dialog struct {
	Kind  Kind
	Title string
	Text  string
}

// Env names the variables hpctun sets for ssh.
const (
	EnvLog = "HPCTUN_ASKPASS_LOG" // the log ssh writes its stderr to
)

var (
	userHost = regexp.MustCompile(`\(([^()\s@]+@[^()\s]+)\)|^([^\s@']+@[^\s']+)'s password`)
	keyFile  = regexp.MustCompile(`for (?:key '([^']+)'|(\S+):\s*$)`)
)

// ForPrompt picks the dialog for ssh's prompt. extra is text to show above
// the prompt, such as a second-factor menu ssh wrote to its log.
func ForPrompt(prompt, extra string) Dialog {
	d := Dialog{Kind: Secret, Title: "hpctun", Text: strings.TrimSpace(prompt)}
	if strings.Contains(prompt, "(yes/no") {
		d.Kind = Question
	}
	if m := userHost.FindStringSubmatch(prompt); m != nil {
		d.Title = "hpctun: " + m[1] + m[2]
	} else if m := keyFile.FindStringSubmatch(prompt); m != nil {
		d.Title = "hpctun: key " + filepath.Base(m[1]+m[2])
	}
	if extra = strings.TrimSpace(extra); extra != "" {
		d.Text = extra + "\n\n" + d.Text
	}
	return d
}

// SecondFactor reports whether prompt is a keyboard-interactive question
// whose choices the server sent as instructions, which ssh writes to
// stderr rather than to the prompt (Duo's "Passcode or option (1-3)").
func SecondFactor(prompt string) bool {
	p := strings.ToLower(prompt)
	return strings.Contains(p, "option (") || strings.Contains(p, "passcode")
}

// LogTail returns up to n non-empty lines from the end of the log at path,
// leaving out ssh's own warnings, which are not meant for the user.
func LogTail(path string, n int) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var lines []string
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimRight(line, "\r ")
		if line == "" || strings.HasPrefix(line, "Warning: ") || strings.HasPrefix(line, "debug") {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ErrCancelled is returned when the user dismisses the dialog.
var ErrCancelled = errors.New("cancelled")

// ErrNoDialog is returned on Linux when neither zenity nor kdialog is
// installed.
var ErrNoDialog = errors.New("install zenity or kdialog so hpctun can ask for your password in a window")

// Runner runs a dialog program and returns its stdout and exit status.
type Runner func(argv []string) (stdout string, exit int, err error)

// Show puts d on screen and returns the answer: the text typed for a
// Secret, "yes" or "no" for a Question.
func Show(d Dialog, goos string, lookPath func(string) (string, error), run Runner) (string, error) {
	argv, err := command(d, goos, lookPath)
	if err != nil {
		return "", err
	}
	out, exit, err := run(argv)
	if err != nil {
		return "", err
	}
	if d.Kind == Question {
		if exit == 0 {
			return "yes", nil
		}
		return "no", nil
	}
	if exit != 0 {
		return "", ErrCancelled
	}
	return strings.TrimSuffix(out, "\n"), nil
}

// appleScript shows a dialog: argv is title, text, then "secret" or
// "question". It brings itself to the front first, since a dialog from a
// background process otherwise opens behind other windows. Return answers
// No to a question: accepting a new host key takes a click.
const appleScript = `on run argv
	set t to item 1 of argv
	set p to item 2 of argv
	activate
	if item 3 of argv is "question" then
		display dialog p with title t buttons {"No", "Yes"} default button "No" cancel button "No"
		return "yes"
	end if
	set r to display dialog p with title t default answer "" with hidden answer buttons {"Cancel", "OK"} default button "OK" cancel button "Cancel"
	return text returned of r
end run`

// kdialogText shows s as plain text: kdialog renders anything that looks
// like HTML, so a server's prompt could otherwise style itself. Escaped
// inside <qt>, it is shown exactly as sent, line breaks kept.
func kdialogText(s string) string {
	return "<qt>" + strings.ReplaceAll(html.EscapeString(s), "\n", "<br>") + "</qt>"
}

func command(d Dialog, goos string, lookPath func(string) (string, error)) ([]string, error) {
	if goos == "darwin" {
		kind := "secret"
		if d.Kind == Question {
			kind = "question"
		}
		return []string{"osascript", "-e", appleScript, d.Title, d.Text, kind}, nil
	}
	if _, err := lookPath("zenity"); err == nil {
		if d.Kind == Question {
			return []string{"zenity", "--question", "--title", d.Title, "--text", d.Text, "--no-markup"}, nil
		}
		return []string{"zenity", "--entry", "--hide-text", "--title", d.Title, "--text", d.Text, "--no-markup"}, nil
	}
	if _, err := lookPath("kdialog"); err == nil {
		if d.Kind == Question {
			return []string{"kdialog", "--title", d.Title, "--yesno", kdialogText(d.Text)}, nil
		}
		return []string{"kdialog", "--title", d.Title, "--password", kdialogText(d.Text)}, nil
	}
	return nil, ErrNoDialog
}

// Script is the SSH_ASKPASS wrapper for the hpctun binary at exe:
// SSH_ASKPASS names a program and cannot carry arguments.
func Script(exe string) string {
	return "#!/bin/sh\nexec " + shellQuote(exe) + " askpass \"$@\"\n"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
