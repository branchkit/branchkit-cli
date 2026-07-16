package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

const (
	testManifestHash = "1111111111111111111111111111111111111111111111111111111111111111"
	testAttestDigest = "2222222222222222222222222222222222222222222222222222222222222222"
)

func testKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestRegistryCounterSigRoundTrip(t *testing.T) {
	pub, priv := testKeypair(t)
	sig := signRegistryCounterSig(priv, testManifestHash, testAttestDigest)
	if err := verifyRegistryCounterSig(pub, testManifestHash, testAttestDigest, sig); err != nil {
		t.Fatalf("valid signature failed to verify: %v", err)
	}
}

func TestRegistryCounterSigRejectsTamperedDigests(t *testing.T) {
	pub, priv := testKeypair(t)
	sig := signRegistryCounterSig(priv, testManifestHash, testAttestDigest)

	// A different manifest — the listing was retargeted at other code.
	if err := verifyRegistryCounterSig(pub, "deadbeef"+testManifestHash[8:], testAttestDigest, sig); err == nil {
		t.Error("verification passed for a tampered manifest hash")
	}
	// A different attestation — the listing was retargeted at a different
	// (or unsigned) artifact.
	if err := verifyRegistryCounterSig(pub, testManifestHash, "deadbeef"+testAttestDigest[8:], sig); err == nil {
		t.Error("verification passed for a tampered attestation digest")
	}
}

func TestRegistryCounterSigRejectsWrongKey(t *testing.T) {
	_, priv := testKeypair(t)
	otherPub, _ := testKeypair(t)
	sig := signRegistryCounterSig(priv, testManifestHash, testAttestDigest)

	// A clone with its own key cannot produce a signature our embedded key
	// accepts — the whole moat in one assertion.
	if err := verifyRegistryCounterSig(otherPub, testManifestHash, testAttestDigest, sig); err == nil {
		t.Fatal("verification passed under a key that did not sign it")
	}
}

func TestRegistryCounterSigCaseAndSpaceNormalized(t *testing.T) {
	pub, priv := testKeypair(t)
	// Signed with lowercase/trimmed; verifying with upper/whitespace must still
	// pass (both sides normalize) — hex-case drift between tools can't break it.
	sig := signRegistryCounterSig(priv, testManifestHash, testAttestDigest)
	if err := verifyRegistryCounterSig(pub, "  "+strings.ToUpper(testManifestHash)+" ", testAttestDigest, sig); err != nil {
		t.Errorf("normalization mismatch: %v", err)
	}
}

func TestRegistryCounterSigRejectsMalformed(t *testing.T) {
	pub, _ := testKeypair(t)
	if err := verifyRegistryCounterSig(pub, testManifestHash, testAttestDigest, "not-base64!!"); err == nil {
		t.Error("accepted non-base64 signature")
	}
	if err := verifyRegistryCounterSig(pub, testManifestHash, testAttestDigest,
		base64.StdEncoding.EncodeToString([]byte("too short"))); err == nil {
		t.Error("accepted a wrong-length signature")
	}
	if err := verifyRegistryCounterSig(ed25519.PublicKey{1, 2, 3}, testManifestHash, testAttestDigest, "AAAA"); err == nil {
		t.Error("accepted an invalid public key")
	}
}

func TestParseRegistryPublicKey(t *testing.T) {
	pub, _ := testKeypair(t)
	b64 := base64.StdEncoding.EncodeToString(pub)
	got, err := parseRegistryPublicKey(b64)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(pub) {
		t.Error("round-tripped key differs")
	}
	if _, err := parseRegistryPublicKey("short"); err == nil {
		t.Error("accepted a wrong-length key")
	}
}
