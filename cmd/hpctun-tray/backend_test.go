package main

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The darwin tray must build without cgo (in CI, on Linux), so it must not use
// fyne.io/systray there, which needs cgo on macOS.
func TestBackends(t *testing.T) {
	for _, tc := range []struct{ goos, want, not string }{
		{"darwin", "hpctun/internal/mactray", "fyne.io/systray"},
		{"linux", "fyne.io/systray", "hpctun/internal/mactray"},
	} {
		cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".")
		cmd.Env = append(os.Environ(), "GOOS="+tc.goos, "GOARCH=amd64", "CGO_ENABLED=0")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list for %s: %v", tc.goos, err)
		}
		deps := strings.Fields(string(out))
		if !slices.Contains(deps, tc.want) {
			t.Errorf("%s: hpctun-tray does not use %s", tc.goos, tc.want)
		}
		if slices.Contains(deps, tc.not) {
			t.Errorf("%s: hpctun-tray uses %s", tc.goos, tc.not)
		}
	}
}
