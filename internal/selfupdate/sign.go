package selfupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
)

// SigName is the release asset that holds the signature of SHA256SUMS:
// one line, the base64 of an Ed25519 signature over SHA256SUMS's bytes.
const SigName = "SHA256SUMS.sig"

// PlaceholderKey is ReleasePublicKey before the release maintainer sets it.
// A build carrying it refuses every release: with no key there is nothing
// to check a signature against.
const PlaceholderKey = "UNSET-RELEASE-SIGNING-KEY"

// ReleasePublicKey is the base64 Ed25519 public key whose private half
// signs every release's SHA256SUMS (scripts/sign -keygen prints it). The
// private key lives only in the GitHub "release" environment's secret
// HPCTUN_SIGNING_KEY, used by the release workflow's publish job.
const ReleasePublicKey = "Rjj3TaWlfhZKA5YWGukYy6//raHX5sTAR14y0k5peI4="

// ErrNoReleaseKey: this build cannot check releases at all.
var ErrNoReleaseKey = errors.New("this build of hpctun has no release signing key, so it cannot check that a release is genuine; download the new release from " + DefaultReleases)

// ErrBadSignature: SHA256SUMS was not signed with the release key, or was
// changed after it was signed.
var ErrBadSignature = errors.New("SHA256SUMS is not signed with hpctun's release key")

// ParsePublicKey decodes a base64 Ed25519 public key; the placeholder is
// ErrNoReleaseKey.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	if s == PlaceholderKey {
		return nil, ErrNoReleaseKey
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("the release signing key is not a base64 Ed25519 public key")
	}
	return ed25519.PublicKey(b), nil
}

// SignSums is the SHA256SUMS.sig content for sums.
func SignSums(priv ed25519.PrivateKey, sums []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums)) + "\n")
}

// VerifySums checks sig (SHA256SUMS.sig's content) over sums with pub.
func VerifySums(pub ed25519.PublicKey, sums, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize {
		return ErrNoReleaseKey
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize || !ed25519.Verify(pub, sums, raw) {
		return ErrBadSignature
	}
	return nil
}
