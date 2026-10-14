package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// sumFor is name's checksum in a SHA256SUMS file (`<hex>  <name>`, or
// `<hex> *<name>` from shasum -b).
func sumFor(sums []byte, name string) (string, bool) {
	for line := range strings.SplitSeq(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

// Listed reports whether SHA256SUMS has a line for name.
func Listed(sums []byte, name string) bool {
	_, ok := sumFor(sums, name)
	return ok
}

// CheckSum verifies that file is the name SHA256SUMS lists.
func CheckSum(sums []byte, name string, file io.Reader) error {
	want, ok := sumFor(sums, name)
	if !ok {
		return fmt.Errorf("SHA256SUMS has no line for %s; nothing was changed", name)
	}
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return fmt.Errorf("checksum mismatch for %s; nothing was changed", name)
	}
	return nil
}
