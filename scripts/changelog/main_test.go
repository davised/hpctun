package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

const sample = `# Changelog

Intro text.

## Unreleased

### Fixed

- down no longer hangs.

## v0.20261001-abc1234 (2026-10-01)

- First entry.
`

func TestNotes(t *testing.T) {
	got, err := notes(sample)
	if err != nil {
		t.Fatal(err)
	}
	if want := "### Fixed\n\n- down no longer hangs.\n"; got != want {
		t.Fatalf("notes = %q, want %q", got, want)
	}
}

func TestNotesAtEndOfFile(t *testing.T) {
	got, err := notes("# Changelog\n\n## Unreleased\n\n- Only change.\n")
	if err != nil || got != "- Only change.\n" {
		t.Fatalf("notes = %q, %v", got, err)
	}
}

func TestNotesRefusesEmpty(t *testing.T) {
	for _, text := range []string{
		"# Changelog\n\n## Unreleased\n\n## v1 (2026-10-01)\n\n- Old.\n",
		"# Changelog\n\n## Unreleased\n\n### Fixed\n\n### Added\n",
		"# Changelog\n\n## v1 (2026-10-01)\n\n- Old.\n",
	} {
		if got, err := notes(text); err == nil {
			t.Errorf("notes(%q) = %q, want an error", text, got)
		}
	}
}

func TestStamp(t *testing.T) {
	now := time.Date(2026, 10, 14, 23, 0, 0, 0, time.UTC)
	got, err := stamp(sample, "v0.20261014-2dbba6b", now)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(sample, "## Unreleased\n",
		"## Unreleased\n\n## v0.20261014-2dbba6b (2026-10-14)\n", 1)
	if got != want {
		t.Fatalf("stamp =\n%s\nwant\n%s", got, want)
	}
	// The new Unreleased section is empty, so the next release needs entries.
	if _, err := notes(got); err == nil {
		t.Fatal("a freshly stamped changelog has notes")
	}
}

func TestStampRefuses(t *testing.T) {
	now := time.Now()
	if _, err := stamp("# Changelog\n\n## Unreleased\n", "v1", now); err == nil {
		t.Error("stamped an empty Unreleased section")
	}
	if _, err := stamp(sample, "v1 x", now); err == nil {
		t.Error("stamped a version with a space")
	}
}

// The repository's own changelog must always have a well-formed Unreleased
// section (entries or not), or the release cannot read it.
func TestRepositoryChangelog(t *testing.T) {
	data, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := section(strings.Split(string(data), "\n")); err != nil {
		t.Fatal(err)
	}
}
