package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

const testManifestHash = "1111111111111111111111111111111111111111111111111111111111111111"

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
	sig := signRegistryCounterSig(priv, testManifestHash)
	if err := verifyRegistryCounterSig(pub, testManifestHash, sig); err != nil {
		t.Fatalf("valid signature failed to verify: %v", err)
	}
}

func TestRegistryCounterSigRejectsTamperedManifest(t *testing.T) {
	pub, priv := testKeypair(t)
	sig := signRegistryCounterSig(priv, testManifestHash)
	// A different manifest hash — the listing was retargeted at other code.
	if err := verifyRegistryCounterSig(pub, "deadbeef"+testManifestHash[8:], sig); err == nil {
		t.Error("verification passed for a tampered manifest hash")
	}
}

func TestRegistryCounterSigRejectsWrongKey(t *testing.T) {
	_, priv := testKeypair(t)
	otherPub, _ := testKeypair(t)
	sig := signRegistryCounterSig(priv, testManifestHash)

	// A clone with its own key cannot produce a signature our embedded key
	// accepts — the whole moat in one assertion.
	if err := verifyRegistryCounterSig(otherPub, testManifestHash, sig); err == nil {
		t.Fatal("verification passed under a key that did not sign it")
	}
}

func TestRegistryCounterSigCaseAndSpaceNormalized(t *testing.T) {
	pub, priv := testKeypair(t)
	// Signed with lowercase/trimmed; verifying with upper/whitespace must still
	// pass (both sides normalize) — hex-case drift between tools can't break it.
	sig := signRegistryCounterSig(priv, testManifestHash)
	if err := verifyRegistryCounterSig(pub, "  "+strings.ToUpper(testManifestHash)+" ", sig); err != nil {
		t.Errorf("normalization mismatch: %v", err)
	}
}

func TestRegistryCounterSigRejectsMalformed(t *testing.T) {
	pub, _ := testKeypair(t)
	if err := verifyRegistryCounterSig(pub, testManifestHash, "not-base64!!"); err == nil {
		t.Error("accepted non-base64 signature")
	}
	if err := verifyRegistryCounterSig(pub, testManifestHash,
		base64.StdEncoding.EncodeToString([]byte("too short"))); err == nil {
		t.Error("accepted a wrong-length signature")
	}
	if err := verifyRegistryCounterSig(ed25519.PublicKey{1, 2, 3}, testManifestHash, "AAAA"); err == nil {
		t.Error("accepted an invalid public key")
	}
}

func TestEmbeddedRegistryPublicKeyParses(t *testing.T) {
	// The real embedded key (the one the user generated) must be a valid
	// Ed25519 public key — a typo would silently disable all counter-sig
	// verification.
	pub, err := registryPublicKey()
	if err != nil {
		t.Fatalf("embedded registry public key is invalid: %v", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Fatalf("embedded key wrong size: %d", len(pub))
	}
}

func TestVerifyCatalogCounterSig(t *testing.T) {
	pub, priv := testKeypair(t)
	manifest := []byte(`{"id":"demo","run":"./demo"}`)
	manifestHash := sha256HexBytes(manifest)

	signed := catalogEntry{
		ID:                "demo",
		ManifestSHA256:    manifestHash,
		RegistrySignature: signRegistryCounterSig(priv, manifestHash),
	}

	// Valid counter-sig over the downloaded manifest → registry-signed.
	ok, err := verifyCatalogCounterSig(pub, signed, manifest)
	if err != nil || !ok {
		t.Fatalf("valid counter-sig: ok=%v err=%v", ok, err)
	}

	// No signature in the entry → not registry-signed, but NOT an error
	// (rollout / community plugins).
	ok, err = verifyCatalogCounterSig(pub, catalogEntry{ID: "demo"}, manifest)
	if err != nil || ok {
		t.Errorf("absent counter-sig should be (false, nil), got ok=%v err=%v", ok, err)
	}

	// A different manifest than the one signed (another release of the
	// repository: the entry records no tag to tell it from a swap) → not
	// registry-signed, NOT an error: a genuine older release still installs.
	ok, err = verifyCatalogCounterSig(pub, signed, []byte(`{"id":"demo","version":"0.9.0"}`))
	if err != nil || ok {
		t.Errorf("another release should be (false, nil), got ok=%v err=%v", ok, err)
	}
	// A signature from a different (clone's) key → hard error, whichever
	// release is being installed.
	otherPub, _ := testKeypair(t)
	if _, err := verifyCatalogCounterSig(otherPub, signed, manifest); err == nil {
		t.Error("signature under a foreign key must be rejected")
	}
	if _, err := verifyCatalogCounterSig(otherPub, signed, []byte(`{"id":"demo","version":"0.9.0"}`)); err == nil {
		t.Error("a forged listing must be rejected for another release too")
	}
	// A recorded hash the signature does not cover (the listing retargeted
	// at another manifest) → hard error.
	retargeted := signed
	retargeted.ManifestSHA256 = sha256HexBytes([]byte(`{"id":"evil"}`))
	if _, err := verifyCatalogCounterSig(pub, retargeted, []byte(`{"id":"evil"}`)); err == nil {
		t.Error("a hash the signature does not cover must be rejected")
	}
	// No recorded hash: the signature is checked over the download, and a
	// miss is not registry-signed, never refused.
	bare := catalogEntry{ID: "demo", RegistrySignature: signed.RegistrySignature}
	if ok, err := verifyCatalogCounterSig(pub, bare, manifest); err != nil || !ok {
		t.Errorf("unrecorded hash, signed manifest: ok=%v err=%v", ok, err)
	}
	if ok, err := verifyCatalogCounterSig(pub, bare, []byte(`{"id":"demo","version":"0.9.0"}`)); err != nil || ok {
		t.Errorf("unrecorded hash, other manifest: ok=%v err=%v, want (false, nil)", ok, err)
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
