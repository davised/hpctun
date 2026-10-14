// Command changelog reads and updates CHANGELOG.md for releases. Changes
// collect under "## Unreleased" as they land; a release publishes that
// section as its notes, and afterwards the section is stamped with the
// release's version. (The version names the release commit, so it cannot be
// written into CHANGELOG.md before the release.)
//
//	go run ./scripts/changelog -notes
//	    prints the Unreleased section's body: the changes since the last
//	    release, with wrapped lines joined for GitHub's release page. Fails
//	    if the section is missing or has no entries, so a release never
//	    goes out without notes.
//	go run ./scripts/changelog -stamp v0.20261014-2dbba6b
//	    renames "## Unreleased" to "## <version> (<today, UTC>)" and opens
//	    a new, empty Unreleased section above it.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

const unreleased = "## Unreleased"

func main() {
	file := flag.String("file", "CHANGELOG.md", "the changelog")
	printNotes := flag.Bool("notes", false, "print the Unreleased section")
	version := flag.String("stamp", "", "version to give the Unreleased section")
	flag.Parse()
	if *printNotes == (*version != "") {
		fmt.Fprintln(os.Stderr, "usage: changelog [-file CHANGELOG.md] -notes | -stamp <version>")
		os.Exit(2)
	}
	data, err := os.ReadFile(*file)
	if err == nil {
		if *printNotes {
			var body string
			if body, err = notes(string(data)); err == nil {
				fmt.Print(body)
			}
		} else {
			var out string
			if out, err = stamp(string(data), *version, time.Now().UTC()); err == nil {
				err = os.WriteFile(*file, []byte(out), 0o644)
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "changelog:", err)
		os.Exit(1)
	}
}

// section finds the Unreleased section: the line index of its heading and
// the index of the next "## " heading (or the end).
func section(lines []string) (start, end int, err error) {
	start = -1
	for i, l := range lines {
		if strings.TrimRight(l, " \t\r") == unreleased {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, 0, errors.New(`no "` + unreleased + `" section`)
	}
	end = len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return start, end, nil
}

// notes returns the Unreleased section's body, without its heading and
// surrounding blank lines. Group headings alone ("### Fixed") are not
// entries: the section must hold at least one line of text.
func notes(text string) (string, error) {
	lines := strings.Split(text, "\n")
	start, end, err := section(lines)
	if err != nil {
		return "", err
	}
	body := lines[start+1 : end]
	entries := false
	for _, l := range body {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			entries = true
		}
	}
	if !entries {
		return "", errors.New("the Unreleased section has no entries; add the changes since the last release")
	}
	return strings.TrimSpace(strings.Join(unwrap(body), "\n")) + "\n", nil
}

// unwrap joins each paragraph's and list item's wrapped lines into one
// line: GitHub shows every line break in release notes as a break, so the
// file's wrapping at 72 columns would show up ragged there. Headings, new
// list items, blank lines and fenced code blocks are left as they are.
func unwrap(lines []string) []string {
	var out []string
	fenced, joinable := false, false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "```"):
			fenced = !fenced
			out, joinable = append(out, l), false
		case fenced || t == "" || strings.HasPrefix(t, "#"):
			out, joinable = append(out, l), false
		case joinable && !startsItem(t):
			out[len(out)-1] += " " + t
		default:
			out, joinable = append(out, l), true
		}
	}
	return out
}

// startsItem reports whether a trimmed line begins a list item.
func startsItem(t string) bool {
	if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || strings.HasPrefix(t, "+ ") {
		return true
	}
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(t) && (t[i] == '.' || t[i] == ')') && t[i+1] == ' '
}

// stamp gives the Unreleased section the version's heading and opens a new,
// empty Unreleased section above it.
func stamp(text, version string, now time.Time) (string, error) {
	if strings.ContainsAny(version, " \t\r\n") {
		return "", fmt.Errorf("version %q has spaces in it", version)
	}
	if _, err := notes(text); err != nil {
		return "", err
	}
	lines := strings.Split(text, "\n")
	start, _, _ := section(lines)
	heading := fmt.Sprintf("## %s (%s)", version, now.Format("2006-01-02"))
	out := append([]string{}, lines[:start]...)
	out = append(out, unreleased, "", heading)
	out = append(out, lines[start+1:]...)
	return strings.Join(out, "\n"), nil
}
