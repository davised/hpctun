// Package examples holds tests for the example files and scripts in this
// directory, which sites hand out; hpctun itself does not use them.
package examples

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hpctun/internal/vscode"
)

// merge runs merge-vscode-settings.py with args and returns its combined
// output and whether it exited 0.
func merge(t *testing.T, args ...string) (string, bool) {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	out, err := exec.Command(py, append([]string{"merge-vscode-settings.py"}, args...)...).CombinedOutput()
	return string(out), err == nil
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// parse reads path as plain JSON: merged output must need no JSONC support.
func parse(t *testing.T, path string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(read(t, path)), &m); err != nil {
		t.Fatalf("%s is not plain JSON: %v\n%s", path, err, read(t, path))
	}
	return m
}

func backups(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "settings.json.bak-*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

const defaults = `// defaults
{
	"search.maxResults": 1000,
	"search.followSymlinks": false,
	"files.watcherExclude": {"**/.git/objects/**": true, "**/work/**": true,},
}
`

func TestShippedExampleParses(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Machine", "settings.json")
	out, ok := merge(t, target, "../internal/vscode/defaults.jsonc")
	if !ok {
		t.Fatalf("merge failed: %s", out)
	}
	m := parse(t, target)
	if m["terminal.integrated.enableFileLinks"] != "notRemote" {
		t.Errorf("example not merged: %v", m)
	}
	if b := backups(t, filepath.Dir(target)); len(b) != 0 {
		t.Errorf("backup of a file that did not exist: %v", b)
	}
}

func TestUserValuesWinAndCommentsAreBackedUp(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "defaults.jsonc")
	write(t, src, defaults)
	target := filepath.Join(dir, "settings.json")
	user := "\uFEFF{\n\t// mine\n\t\"search.maxResults\": 50, /* keep */\n" +
		"\t\"http.proxy\": \"http://proxy:3128\", // a URL is not a comment\n" +
		"\t\"note\": \"a /* b */ c\",\n" +
		"\t\"files.watcherExclude\": {\"**/data/**\": true, \"**/work/**\": false,},\n}\n"
	write(t, target, user)
	out, ok := merge(t, target, src)
	if !ok {
		t.Fatalf("merge failed: %s", out)
	}
	m := parse(t, target)
	if m["search.maxResults"] != 50.0 {
		t.Errorf("user's search.maxResults replaced: %v", m["search.maxResults"])
	}
	if m["search.followSymlinks"] != false {
		t.Errorf("missing key not added: %v", m)
	}
	if m["http.proxy"] != "http://proxy:3128" || m["note"] != "a /* b */ c" {
		t.Errorf("strings damaged: %q %q", m["http.proxy"], m["note"])
	}
	w := m["files.watcherExclude"].(map[string]any)
	if w["**/data/**"] != true || w["**/work/**"] != false || w["**/.git/objects/**"] != true {
		t.Errorf("watcherExclude merge = %v", w)
	}
	b := backups(t, dir)
	if len(b) != 1 || read(t, b[0]) != user {
		t.Fatalf("backup = %v, want one with the original bytes", b)
	}
	if !strings.Contains(out, "comments") {
		t.Errorf("output does not warn that comments were dropped:\n%s", out)
	}
}

func TestLaterSourceWinsOverEarlier(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "defaults.jsonc")
	write(t, src, defaults)
	site := filepath.Join(dir, "site.jsonc")
	write(t, site, `{"search.maxResults": 200, "http.noProxy": ["localhost", ".example.edu"]}`)
	target := filepath.Join(dir, "settings.json")
	if out, ok := merge(t, target, src, site); !ok {
		t.Fatalf("merge failed: %s", out)
	}
	m := parse(t, target)
	if m["search.maxResults"] != 200.0 {
		t.Errorf("site value did not win over defaults: %v", m["search.maxResults"])
	}
	if np, _ := m["http.noProxy"].([]any); len(np) != 2 {
		t.Errorf("http.noProxy = %v", m["http.noProxy"])
	}
}

func TestNothingToAddLeavesFileAlone(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "defaults.jsonc")
	write(t, src, `{"a": 1, "o": {"x": true}}`)
	target := filepath.Join(dir, "settings.json")
	user := "{ // already set\n\"a\": 2, \"o\": {\"x\": false}}\n"
	write(t, target, user)
	out, ok := merge(t, target, src)
	if !ok {
		t.Fatalf("merge failed: %s", out)
	}
	if read(t, target) != user || len(backups(t, dir)) != 0 {
		t.Errorf("file rewritten or backed up with nothing to add")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "defaults.jsonc")
	write(t, src, defaults)
	target := filepath.Join(dir, "settings.json")
	write(t, target, "{}")
	out, ok := merge(t, "--dry-run", target, src)
	if !ok {
		t.Fatalf("merge failed: %s", out)
	}
	if read(t, target) != "{}" || len(backups(t, dir)) != 0 {
		t.Errorf("--dry-run changed files")
	}
	if !strings.Contains(out, "search.followSymlinks") {
		t.Errorf("--dry-run does not list what it would add:\n%s", out)
	}
}

func TestBadInputChangesNothing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "defaults.jsonc")
	write(t, src, defaults)
	target := filepath.Join(dir, "settings.json")
	write(t, target, `{"a": 1 "b": 2}`)
	if out, ok := merge(t, target, src); ok {
		t.Fatalf("merge of a broken settings.json succeeded: %s", out)
	}
	bad := filepath.Join(dir, "bad.jsonc")
	write(t, bad, `["not", "an", "object"]`)
	write(t, target, "{}")
	if out, ok := merge(t, target, src, bad); ok {
		t.Fatalf("merge with a non-object source succeeded: %s", out)
	}
	if read(t, target) != "{}" || len(backups(t, dir)) != 0 {
		t.Errorf("a failed merge changed files")
	}
}

type mergeCase struct {
	Name    string           `json:"name"`
	Sources []map[string]any `json:"sources"`
	User    string           `json:"user"`
	Want    map[string]any   `json:"want"`
}

func loadCases(t *testing.T) []mergeCase {
	t.Helper()
	var cs []mergeCase
	if err := json.Unmarshal([]byte(read(t, filepath.Join("testdata", "cases.json"))), &cs); err != nil {
		t.Fatal(err)
	}
	return cs
}

// TestSharedCasesGo and TestSharedCasesPython run the same cases, so
// hpctun vscode-settings and merge-vscode-settings.py cannot drift apart.
func TestSharedCasesGo(t *testing.T) {
	for _, c := range loadCases(t) {
		out, _, _, err := vscode.AddSettings(c.User, vscode.Layer(c.Sources...))
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		got, err := vscode.ParseObject(out)
		if err != nil || !reflect.DeepEqual(got, c.Want) {
			t.Errorf("%s: got %v %v, want %v", c.Name, got, err, c.Want)
		}
	}
}

func TestSharedCasesPython(t *testing.T) {
	for _, c := range loadCases(t) {
		dir := t.TempDir()
		target := filepath.Join(dir, "settings.json")
		if c.User != "" {
			write(t, target, c.User)
		}
		args := []string{target}
		for i, s := range c.Sources {
			b, _ := json.Marshal(s)
			p := filepath.Join(dir, fmt.Sprintf("src%d.jsonc", i))
			write(t, p, string(b))
			args = append(args, p)
		}
		if out, ok := merge(t, args...); !ok {
			t.Errorf("%s: %s", c.Name, out)
			continue
		}
		var got map[string]any
		text := "{}"
		if _, err := os.Stat(target); err == nil {
			text = read(t, target)
		}
		if err := json.Unmarshal([]byte(text), &got); err != nil || !reflect.DeepEqual(got, c.Want) {
			t.Errorf("%s: got %v %v, want %v", c.Name, got, err, c.Want)
		}
	}
}
