// Package selfupdatetest signs test releases with a fixed key that only
// tests use; hpctun's real key is selfupdate.ReleasePublicKey.
package selfupdatetest

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"

	"hpctun/internal/selfupdate"
)

var priv = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))

// PublicKey is the base64 public key matching Sign.
func PublicKey() string {
	return base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
}

// Sign is the SHA256SUMS.sig content for sums under the test key.
func Sign(sums []byte) []byte { return selfupdate.SignSums(priv, sums) }
