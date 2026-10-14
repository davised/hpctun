package legacy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoveDir(t *testing.T) {
	d := t.TempDir()
	old, nu := filepath.Join(d, "tun"), filepath.Join(d, "hpctun")

	// neither: nothing to do
	if moved, err := MoveDir(old, nu); moved || err != nil {
		t.Fatalf("neither: %v %v", moved, err)
	}
	// old only: moved, contents kept
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "config.toml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if moved, err := MoveDir(old, nu); !moved || err != nil {
		t.Fatalf("old only: %v %v", moved, err)
	}
	if b, err := os.ReadFile(filepath.Join(nu, "config.toml")); err != nil || string(b) != "x" {
		t.Fatalf("contents: %q %v", b, err)
	}
	if _, err := os.Lstat(old); !os.IsNotExist(err) {
		t.Fatalf("old still there: %v", err)
	}
	// both: new wins, old untouched
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if moved, err := MoveDir(old, nu); moved || err != nil {
		t.Fatalf("both: %v %v", moved, err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("old removed: %v", err)
	}
}

func TestMoveDirParentMissing(t *testing.T) {
	d := t.TempDir()
	old := filepath.Join(d, "a", "tun")
	nu := filepath.Join(d, "b", "hpctun") // parent b does not exist
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if moved, err := MoveDir(old, nu); !moved || err != nil {
		t.Fatalf("%v %v", moved, err)
	}
}
