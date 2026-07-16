package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The load-bearing test: a keypair from `registry keygen` signs via the
// maintainer path (localKeySigner) and verifies via the install path
// (verifyRegistryCounterSig) — the two halves of the moat agree end to end.
func TestRegistryKeygenSignVerifyRoundTrip(t *testing.T) {
	privB64, pubB64, err := generateRegistryKeypair()
	if err != nil {
		t.Fatal(err)
	}

	priv, err := parseRegistryPrivateKey(privB64)
	if err != nil {
		t.Fatalf("generated private key doesn't parse: %v", err)
	}
	pub, err := parseRegistryPublicKey(pubB64)
	if err != nil {
		t.Fatalf("generated public key doesn't parse: %v", err)
	}

	signer := localKeySigner{priv: priv}
	sig, err := signer.signCounterSig(testManifestHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyRegistryCounterSig(pub, testManifestHash, sig); err != nil {
		t.Fatalf("install-path verification rejected a maintainer-path signature: %v", err)
	}
}

func TestLoadLocalSignerFromFileAndEnv(t *testing.T) {
	privB64, _, err := generateRegistryKeypair()
	if err != nil {
		t.Fatal(err)
	}

	// From a file (with a trailing newline, as a real key file would have).
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte(privB64+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLocalSigner(keyFile); err != nil {
		t.Errorf("file key source failed: %v", err)
	}

	// From the env var (how CI passes a secret).
	t.Setenv("BRANCHKIT_REGISTRY_KEY", privB64)
	if _, err := loadLocalSigner(""); err != nil {
		t.Errorf("env key source failed: %v", err)
	}

	// Neither → a clear error, not a silent empty signer.
	t.Setenv("BRANCHKIT_REGISTRY_KEY", "")
	if _, err := loadLocalSigner(""); err == nil {
		t.Error("expected an error when no key source is provided")
	}
}

func TestSha256HexFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x")
	if err := os.WriteFile(f, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Known SHA-256 of "abc".
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	got, err := sha256HexFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("sha256HexFile = %s, want %s", got, want)
	}
}

func TestParseRegistryPrivateKeyRejectsBad(t *testing.T) {
	if _, err := parseRegistryPrivateKey("not base64!!"); err == nil {
		t.Error("accepted non-base64 private key")
	}
	if _, err := parseRegistryPrivateKey("YWJj"); err == nil { // "abc" — wrong length
		t.Error("accepted a wrong-length private key")
	}
}
