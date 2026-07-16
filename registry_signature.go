package main

// Registry counter-signature — the fork-resilience half of the signing chain
// (notes/DESIGN_PLUGIN_SIGNING_CHAIN.md step 5). Where the author signature
// (sigstore.go) proves "who built this", the registry counter-signature
// proves "BranchKit's canonical registry admitted this exact listing". Only
// BranchKit holds the private key, so a cloned registry cannot mint a
// counter-signature that a real install accepts — that's the moat.
//
// This file is the verification core: a domain-separated, versioned message
// over (manifest hash + author-attestation digest), signed with Ed25519
// (Go stdlib — no dependency). The install path recomputes the two digests
// from what it downloaded and verifies the registry's signature over them
// with the embedded registry public key. Signing (the registry-side CI step)
// uses signRegistryCounterSig with the private key held as a CI secret.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

// registryCounterSigVersion domain-separates this signature so a registry
// counter-signature can never be confused with, or replayed as, any other
// Ed25519 signature in the system. Bump it only with a deliberate format
// change (and dual-accept during any rollout).
const registryCounterSigVersion = "branchkit-registry-countersig-v1"

// embeddedRegistryPublicKeyB64 is BranchKit's registry signing public key
// (base64 raw Ed25519). This is the "what the seal looks like" half — safe to
// distribute; the private key lives only in the registry CI secret store.
// Rotating it is a new app/CLI release with a new value here (the small
// catalog is re-counter-signed under the new key). Generated 2026-07-16.
const embeddedRegistryPublicKeyB64 = "bGBpeL/bmGY+UxJh0e8OJQOqVOIyE6LdTndMoTvCXA4="

// registryPublicKey returns the embedded registry verification key.
func registryPublicKey() (ed25519.PublicKey, error) {
	return parseRegistryPublicKey(embeddedRegistryPublicKeyB64)
}

// registryCounterSigMessage is the exact byte string both sides sign/verify.
// manifestHash and attestationDigest are lowercase hex SHA-256 strings:
//   - manifestHash: SHA-256 of the plugin's canonical plugin.json bytes.
//   - attestationDigest: SHA-256 of the author Sigstore bundle
//     (<artifact>.sigstore.json) — binds the counter-sig to a specific,
//     already-author-verified release, so an approved listing can't be
//     retargeted at a different (or unsigned) artifact.
func registryCounterSigMessage(manifestHash, attestationDigest string) []byte {
	return []byte(strings.Join([]string{
		registryCounterSigVersion,
		strings.ToLower(strings.TrimSpace(manifestHash)),
		strings.ToLower(strings.TrimSpace(attestationDigest)),
	}, "\n") + "\n")
}

// signRegistryCounterSig produces the base64 signature the registry records
// in the catalog entry. Registry-side only (needs the private key).
func signRegistryCounterSig(priv ed25519.PrivateKey, manifestHash, attestationDigest string) string {
	sig := ed25519.Sign(priv, registryCounterSigMessage(manifestHash, attestationDigest))
	return base64.StdEncoding.EncodeToString(sig)
}

// verifyRegistryCounterSig checks a registry counter-signature at install
// time. Returns nil iff the signature is valid for (manifestHash,
// attestationDigest) under pub. A malformed or mismatched signature is an
// error — the caller treats a present-but-invalid counter-sig as a hard
// failure (a forged canonical listing), and its absence as "not registry-
// signed" (Community rather than RegistrySigned), never as a pass.
func verifyRegistryCounterSig(pub ed25519.PublicKey, manifestHash, attestationDigest, sigB64 string) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("registry public key is not a valid Ed25519 key")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil {
		return fmt.Errorf("registry signature is not valid base64: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("registry signature has wrong length %d", len(sig))
	}
	if !ed25519.Verify(pub, registryCounterSigMessage(manifestHash, attestationDigest), sig) {
		return fmt.Errorf("registry counter-signature does not verify")
	}
	return nil
}

// sha256HexBytes returns the lowercase hex SHA-256 of in-memory bytes — the
// digest form the counter-sig payload uses (files go through sha256HexFile).
func sha256HexBytes(b []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// verifyCatalogCounterSig checks a plugin's registry counter-signature at
// install time, recomputing the digests from the bytes actually downloaded.
//
// Returns (true, nil) when a valid counter-signature is present — the listing
// is the canonical registry one (RegistrySigned). (false, nil) when the entry
// carries no counter-signature: during rollout most entries won't, and a
// sideloaded/community plugin never will — that's not a failure, just "not
// registry-signed". (false, err) when a counter-signature is PRESENT but the
// download doesn't match what was signed or the signature is invalid — a
// forged or retargeted canonical listing, which the caller treats as a hard
// install failure.
func verifyCatalogCounterSig(pub ed25519.PublicKey, entry catalogEntry, manifestBytes, bundleBytes []byte) (bool, error) {
	if strings.TrimSpace(entry.RegistrySignature) == "" {
		return false, nil
	}
	manifestHash := sha256HexBytes(manifestBytes)
	attestDigest := sha256HexBytes(bundleBytes)

	// If the entry declares the digests it signed, the download must match
	// them — a clear error if the release was swapped after admission.
	if entry.ManifestSHA256 != "" && !strings.EqualFold(entry.ManifestSHA256, manifestHash) {
		return false, fmt.Errorf("manifest hash mismatch: downloaded %s, catalog counter-signed %s", manifestHash, entry.ManifestSHA256)
	}
	if entry.AttestationSHA256 != "" && !strings.EqualFold(entry.AttestationSHA256, attestDigest) {
		return false, fmt.Errorf("attestation digest mismatch: downloaded %s, catalog counter-signed %s", attestDigest, entry.AttestationSHA256)
	}
	if err := verifyRegistryCounterSig(pub, manifestHash, attestDigest, entry.RegistrySignature); err != nil {
		return false, err
	}
	return true, nil
}

// parseRegistryPublicKey decodes a base64 Ed25519 public key (the form the
// registry key is distributed and embedded in).
func parseRegistryPublicKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("registry public key is not valid base64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("registry public key has wrong length %d (want %d)", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}
