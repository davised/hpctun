package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"hpctun/internal/selfupdate"
)

func TestKeygen(t *testing.T) {
	out := filepath.Join(t.TempDir(), "key")
	var stdout bytes.Buffer
	if err := keygen(out, &stdout, rand.Reader); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("seed file %q", b)
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if strings.TrimSpace(stdout.String()) != base64.StdEncoding.EncodeToString(pub) {
		t.Fatalf("printed %q", stdout.String())
	}
	if strings.Contains(stdout.String(), strings.TrimSpace(string(b))) {
		t.Fatal("printed the private key")
	}
	if fi, _ := os.Stat(out); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if err := keygen(out, &stdout, rand.Reader); err == nil {
		t.Fatal("replaced an existing key file")
	}
	if err := keygen("", &stdout, rand.Reader); err == nil {
		t.Fatal("no -out accepted")
	}
}

func testSeed(t *testing.T) (seed, pub string) {
	t.Helper()
	p, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(priv.Seed()), base64.StdEncoding.EncodeToString(p)
}

func TestSignAndVerify(t *testing.T) {
	seed, pub := testSeed(t)
	path := filepath.Join(t.TempDir(), "SHA256SUMS")
	os.WriteFile(path, []byte("abc  hpctun-v1-linux-amd64.tar.gz\n"), 0o644)
	if err := sign(path, seed, pub); err != nil {
		t.Fatal(err)
	}
	if err := verify(path, pub); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("abd  hpctun-v1-linux-amd64.tar.gz\n"), 0o644)
	if err := verify(path, pub); !errors.Is(err, selfupdate.ErrBadSignature) {
		t.Fatalf("changed file verified: %v", err)
	}
}

func TestSignRefusesWrongOrMissingKey(t *testing.T) {
	seed, _ := testSeed(t)
	_, otherPub := testSeed(t)
	path := filepath.Join(t.TempDir(), "SHA256SUMS")
	os.WriteFile(path, []byte("x\n"), 0o644)
	if err := sign(path, seed, otherPub); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong key: %v", err)
	}
	if err := sign(path, seed, selfupdate.PlaceholderKey); !errors.Is(err, selfupdate.ErrNoReleaseKey) {
		t.Fatalf("placeholder: %v", err)
	}
	if err := sign(path, "", otherPub); err == nil {
		t.Fatal("empty HPCTUN_SIGNING_KEY accepted")
	}
	if _, err := os.Stat(path + ".sig"); err == nil {
		t.Fatal("wrote a signature after refusing")
	}
}
