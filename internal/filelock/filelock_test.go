package filelock

import (
	"os"
	"path/filepath"
	"testing"
)

// Two handles on one file stand in for two processes: flock and LockFileEx
// both treat separately opened handles as separate owners.
func TestTryLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	a, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if busy, err := TryLock(a); busy || err != nil {
		t.Fatalf("first lock: busy %v err %v", busy, err)
	}
	b, _ := os.OpenFile(p, os.O_RDWR, 0o600)
	defer b.Close()
	if busy, err := TryLock(b); !busy || err != nil {
		t.Fatalf("second lock: busy %v err %v", busy, err)
	}
	a.Close()
	if busy, err := TryLock(b); busy || err != nil {
		t.Fatalf("after release: busy %v err %v", busy, err)
	}
}
