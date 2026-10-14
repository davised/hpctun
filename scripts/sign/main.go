// Command sign makes and uses hpctun's release signing key (Ed25519, from
// Go's standard library). The release workflow's publish job signs
// SHA256SUMS with it; hpctun self-update checks that signature against
// selfupdate.ReleasePublicKey.
//
//	go run ./scripts/sign -keygen -out <file>
//	    writes a new private key (a base64 seed, mode 0600) to <file>,
//	    which must not exist, and prints the public key for
//	    ReleasePublicKey. The private key is never printed.
//	HPCTUN_SIGNING_KEY=<seed> go run ./scripts/sign -sign dist/SHA256SUMS
//	    writes dist/SHA256SUMS.sig, refusing a key that does not match
//	    ReleasePublicKey, so a release is never signed with the wrong key.
//	go run ./scripts/sign -verify SHA256SUMS
//	    checks SHA256SUMS.sig next to it against ReleasePublicKey.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"hpctun/internal/selfupdate"
)

func main() {
	gen := flag.Bool("keygen", false, "make a new key pair")
	out := flag.String("out", "", "with -keygen: file for the private key (must not exist)")
	signPath := flag.String("sign", "", "file to sign; the private key comes from $HPCTUN_SIGNING_KEY")
	verifyPath := flag.String("verify", "", "file whose .sig to check against the built-in public key")
	flag.Parse()
	var err error
	switch {
	case *gen && *signPath == "" && *verifyPath == "":
		err = keygen(*out, os.Stdout, rand.Reader)
	case *signPath != "" && !*gen && *verifyPath == "":
		err = sign(*signPath, os.Getenv("HPCTUN_SIGNING_KEY"), selfupdate.ReleasePublicKey)
	case *verifyPath != "" && !*gen && *signPath == "":
		if err = verify(*verifyPath, selfupdate.ReleasePublicKey); err == nil {
			fmt.Printf("%s is signed with hpctun's release key\n", *verifyPath)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: sign -keygen -out <file> | -sign <file> | -verify <file>")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
}

// keygen writes a new private seed to out (created, never replaced, 0600)
// and prints the matching public key on stdout.
func keygen(out string, stdout io.Writer, random io.Reader) error {
	if out == "" {
		return errors.New("-keygen needs -out <file> for the private key")
	}
	pub, priv, err := ed25519.GenerateKey(random)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(f, base64.StdEncoding.EncodeToString(priv.Seed())); err != nil {
		f.Close()
		os.Remove(out)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(out)
		return err
	}
	_, err = fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(pub))
	return err
}

// sign writes path+".sig" with the private seed (base64), once the seed's
// public key is known to be wantPub.
func sign(path, seed, wantPub string) error {
	pub, err := selfupdate.ParsePublicKey(wantPub)
	if err != nil {
		return fmt.Errorf("refusing to sign: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(seed))
	if err != nil || len(raw) != ed25519.SeedSize {
		return errors.New("HPCTUN_SIGNING_KEY is not a base64 Ed25519 seed, as -keygen writes")
	}
	priv := ed25519.NewKeyFromSeed(raw)
	if !pub.Equal(priv.Public()) {
		return errors.New("HPCTUN_SIGNING_KEY does not match ReleasePublicKey in internal/selfupdate; refusing to sign")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sig := selfupdate.SignSums(priv, data)
	if err := selfupdate.VerifySums(pub, data, sig); err != nil {
		return err
	}
	return os.WriteFile(path+".sig", sig, 0o644)
}

// verify checks path+".sig" over path with pubKey.
func verify(path, pubKey string) error {
	pub, err := selfupdate.ParsePublicKey(pubKey)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(path + ".sig")
	if err != nil {
		return err
	}
	return selfupdate.VerifySums(pub, data, sig)
}
