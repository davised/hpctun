package selfupdate

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func sumsFor(files map[string]string) []byte {
	var b strings.Builder
	b.WriteString("not a sums line\n\n")
	for name, body := range files {
		fmt.Fprintf(&b, "%x  %s\n", sha256.Sum256([]byte(body)), name)
	}
	return []byte(b.String())
}

func TestCheckSum(t *testing.T) {
	sums := sumsFor(map[string]string{"a.tar.gz": "good", "b.zip": "other"})
	if err := CheckSum(sums, "a.tar.gz", strings.NewReader("good")); err != nil {
		t.Fatal(err)
	}
	err := CheckSum(sums, "a.tar.gz", strings.NewReader("bad"))
	if err == nil || err.Error() != "checksum mismatch for a.tar.gz; nothing was changed" {
		t.Fatalf("mismatch: %v", err)
	}
	err = CheckSum(sums, "c.zip", strings.NewReader("good"))
	if err == nil || err.Error() != "SHA256SUMS has no line for c.zip; nothing was changed" {
		t.Fatalf("missing: %v", err)
	}
	// shasum's binary-mode marker
	star := []byte(fmt.Sprintf("%x *a.tar.gz\n", sha256.Sum256([]byte("good"))))
	if err := CheckSum(star, "a.tar.gz", strings.NewReader("good")); err != nil {
		t.Fatalf("star: %v", err)
	}
	if !Listed(sums, "b.zip") || Listed(sums, "c.zip") || Listed(sums, "a.tar") {
		t.Fatal("Listed")
	}
}
