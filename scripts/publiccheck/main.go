// Command publiccheck lists lines in tracked files that name private
// infrastructure or documents and must not reach the public repository.
// Matching ignores case; binary files are skipped.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var patterns = []string{
	"gitlab", "/local/cqls", "cqls-hpcman", "cgrb-biocomputing", "build1",
	"10.224.", "/scratch/gitlab", "docs/specs", "docs/plans", "spec amendment",
	"private-token", ".beads", "bd codex-hook", "barringer",
}

// allowed are files that may name the patterns: this checker itself, whose
// pattern list and tests must spell the names out.
var allowed = map[string]bool{
	"scripts/publiccheck/main.go":      true,
	"scripts/publiccheck/main_test.go": true,
}

// gitignoreOK is the one line .gitignore may carry that names a pattern: the
// entry keeping the private .beads directory out of the repository. Every
// other line is scanned like any file, so a private path added there is
// reported.
const gitignoreOK = "/.beads/"

type hit struct {
	File    string
	Line    int
	Pattern string
	Text    string
}

func scan(root string, files []string) ([]hit, error) {
	var hits []hit
	for _, f := range files {
		name := filepath.ToSlash(f)
		if allowed[name] {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			return nil, err
		}
		if bytes.IndexByte(b, 0) >= 0 {
			continue // binary
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for n := 1; sc.Scan(); n++ {
			text := sc.Text()
			if name == ".gitignore" && strings.TrimSpace(text) == gitignoreOK {
				continue
			}
			low := strings.ToLower(text)
			for _, p := range patterns {
				if strings.Contains(low, p) {
					hits = append(hits, hit{f, n, p, strings.TrimSpace(text)})
				}
			}
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
	}
	return hits, nil
}

func main() {
	out, err := exec.Command("git", "ls-files", "-z").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "publiccheck: git ls-files:", err)
		os.Exit(2)
	}
	var files []string
	for _, f := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	hits, err := scan(".", files)
	if err != nil {
		fmt.Fprintln(os.Stderr, "publiccheck:", err)
		os.Exit(2)
	}
	for _, h := range hits {
		fmt.Printf("%s:%d: %s: %s\n", h.File, h.Line, h.Pattern, h.Text)
	}
	if len(hits) > 0 {
		fmt.Fprintf(os.Stderr, "publiccheck: %d lines name private things\n", len(hits))
		os.Exit(1)
	}
	fmt.Println("publiccheck: ok")
}
