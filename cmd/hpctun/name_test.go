package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// oldName finds the tool's name before the rename as a word. Tunnels
// ("tunnel", "tunnels") do not match.
var oldName = regexp.MustCompile(`\btun\b`)

// allowOld are string literals that must keep the old name: the migration
// reads them.
var allowOld = map[string]bool{
	"tun":                                  true, // config.LegacyName, state.LegacyDir
	"TUN_CONFIG":                           true,
	"tun_config":                           true,
	"tun_known_hosts":                      true,
	"tun-code":                             true,
	"moved %s to %s (tun is now hpctun)\n": true,
	"~/.ssh/tun_config is from before the rename; run `hpctun ssh-config sync`": true,
	"%s is from before the rename; run `hpctun ssh-config sync`":                true,
}

// skipOldNameDir reports whether a directory named name should not be
// descended into when scanning for the old name: dot and underscore
// directories (like .claude/worktrees, which can hold pre-rename branches
// checked out in the main checkout, and the ./... convention for excluding
// packages), plus the generated docs and dist.
func skipOldNameDir(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "docs" || name == "dist"
}

// findOldNameViolations walks root's .go files (excluding _test.go, which
// may legitimately need the old name in fixtures) and returns one
// "position: literal" string per string literal that still says the old
// name outside allowOld.
func findOldNameViolations(root string) ([]string, error) {
	fset := token.NewFileSet()
	var violations []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipOldNameDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil || allowOld[s] {
				return true
			}
			if oldName.MatchString(s) {
				violations = append(violations, fmt.Sprintf("%s: %q still says tun", fset.Position(lit.Pos()), s))
			}
			return true
		})
		return nil
	})
	return violations, err
}

func TestNoOldNameInStrings(t *testing.T) {
	violations, err := findOldNameViolations(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// TestFindOldNameViolationsSkipsDotAndUnderscoreDirs covers why the scan
// must not descend into dot or underscore directories: .claude/worktrees can
// hold other pre-rename branches checked out under the main checkout, whose
// old-named source would otherwise fail this repo's own scan.
func TestFindOldNameViolationsSkipsDotAndUnderscoreDirs(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	violation := "package %s\n\nvar s = \"run tun now\"\n"
	write(".claude/worktrees/old/main.go", fmt.Sprintf(violation, "old"))
	write("_generated/main.go", fmt.Sprintf(violation, "gen"))
	write("docs/main.go", fmt.Sprintf(violation, "docs"))
	write("dist/main.go", fmt.Sprintf(violation, "dist"))
	write("real/main.go", fmt.Sprintf(violation, "real"))

	violations, err := findOldNameViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || !strings.Contains(violations[0], filepath.Join(root, "real", "main.go")) {
		t.Fatalf("violations = %v", violations)
	}
}

// TestFindOldNameViolationsReturnsWalkError covers a walk that cannot even
// start: the caller must see the error instead of a silent, incomplete scan.
func TestFindOldNameViolationsReturnsWalkError(t *testing.T) {
	if _, err := findOldNameViolations(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("want an error for a missing root")
	}
}
