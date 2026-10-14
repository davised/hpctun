package selfupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv := newKey(t)
	sums := []byte("abc  hpctun-v1-linux-amd64.tar.gz\n")
	if err := VerifySums(pub, sums, SignSums(priv, sums)); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePublicKey(base64.StdEncoding.EncodeToString(pub))
	if err != nil || !parsed.Equal(pub) {
		t.Fatalf("parse: %v", err)
	}
}

func TestVerifySumsRefuses(t *testing.T) {
	pub, priv := newKey(t)
	other, _ := newKey(t)
	sums := []byte("abc  a.tar.gz\n")
	sig := SignSums(priv, sums)
	cases := map[string]error{
		"changed sums": VerifySums(pub, []byte("abd  a.tar.gz\n"), sig),
		"other key":    VerifySums(other, sums, sig),
		"empty sig":    VerifySums(pub, sums, nil),
		"garbage":      VerifySums(pub, sums, []byte("not base64!\n")),
		"short":        VerifySums(pub, sums, []byte(base64.StdEncoding.EncodeToString([]byte("short"))+"\n")),
	}
	for name, err := range cases {
		if !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A build whose key is still the placeholder must refuse everything, so
// an unsigned build can never accept a release by accident.
func TestPlaceholderKeyRefusesEverything(t *testing.T) {
	if _, err := ParsePublicKey(PlaceholderKey); !errors.Is(err, ErrNoReleaseKey) {
		t.Fatalf("parse placeholder: %v", err)
	}
	_, priv := newKey(t)
	sums := []byte("abc  a.tar.gz\n")
	if err := VerifySums(nil, sums, SignSums(priv, sums)); !errors.Is(err, ErrNoReleaseKey) {
		t.Fatalf("verify without key: %v", err)
	}
	if _, err := ParsePublicKey(base64.StdEncoding.EncodeToString(make([]byte, 31))); err == nil {
		t.Fatal("accepted a 31-byte key")
	}
}

// Once the maintainer sets ReleasePublicKey, it must parse.
func TestReleasePublicKey(t *testing.T) {
	if ReleasePublicKey == PlaceholderKey {
		t.Skip("ReleasePublicKey is not set yet; self-update refuses every release until it is")
	}
	if _, err := ParsePublicKey(ReleasePublicKey); err != nil {
		t.Fatal(err)
	}
}
