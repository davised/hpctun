package main

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "// runs on BUILD1\nfine line\n")
	write(t, dir, "b.md", "See GitLab.\n")
	write(t, dir, "c.txt", "ok: files.hpc.oregonstate.edu\n")
	write(t, dir, "d.bin", "\x00\x01gitlab\x00")
	write(t, dir, "scripts/publiccheck/main.go", "\"gitlab\",\n") // its own pattern list
	write(t, dir, ".gitignore", "/.beads/\n")
	hits, err := scan(dir, []string{"a.go", "b.md", "c.txt", "d.bin", "scripts/publiccheck/main.go", ".gitignore"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, h := range hits {
		got[h.File+":"+h.Pattern] = h.Line
	}
	want := map[string]int{"a.go:build1": 1, "b.md:gitlab": 1}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: line %d, got %v", k, v, got)
		}
	}
}

// TestScanBeadsHook checks that the beads hook command name used by the
// .codex hooks is reported, so agent scaffolding cannot reach the public
// repository.
func TestScanBeadsHook(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "hooks.json", "{\n  \"command\": \"bd codex-hook SessionStart\",\n}\n")
	hits, err := scan(dir, []string{"hooks.json"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %v, want exactly one hit", hits)
	}
	h := hits[0]
	if h.File != "hooks.json" || h.Line != 2 || h.Pattern != "bd codex-hook" {
		t.Errorf("got %+v, want hooks.json line 2 pattern bd codex-hook", h)
	}
}

// TestScanGitignoreLine checks that only the .beads entry of .gitignore is
// exempt: a forbidden line anywhere in .gitignore is still reported.
func TestScanGitignoreLine(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".gitignore", "/.beads/\n.beads-credential-key\n")
	hits, err := scan(dir, []string{".gitignore"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %v, want exactly one hit", hits)
	}
	h := hits[0]
	if h.File != ".gitignore" || h.Line != 2 || h.Pattern != ".beads" {
		t.Errorf("got %+v, want .gitignore line 2 pattern .beads", h)
	}
}

// TestScanBarringer checks that the restricted host name is reported, so a
// config that names it cannot reach the public repository. Matching ignores
// case, so the capitalised spelling in a real config is caught too.
func TestScanBarringer(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "config.toml", "gateway = \"shell\"\ndefault_host = \"Barringer\"\n")
	hits, err := scan(dir, []string{"config.toml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %v, want exactly one hit", hits)
	}
	h := hits[0]
	if h.File != "config.toml" || h.Line != 2 || h.Pattern != "barringer" {
		t.Errorf("got %+v, want config.toml line 2 pattern barringer", h)
	}
}
