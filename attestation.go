package main

// Author-attestation verification in the install path — step 3 of
// notes/PLAN_SIGNING_CHAIN_IMPL.md, built on verifyBundle (sigstore.go).
//
// When a GitHub release publishes a Sigstore bundle beside the plugin
// tarball (`<artifact>.sigstore.json`, the pinned attestation layout — see
// the author snippet in the plugin template), the CLI verifies it: the
// bundle must cover the exact bytes downloaded AND resolve to the repo the
// plugin is being installed from. That upgrades the existing same-origin
// SHA-256 (integrity only) to real provenance (authenticity).
//
// This is the CLI half. The resolved AuthorVerified/Publisher get recorded
// in .branchkit-source.json; the actuator's trust-tier resolution
// (resolve_trust_tier) reads them in a later step, and the registry
// counter-signature (the fork moat) is step 5.

import (
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Sigstore trusted roots for offline verification, embedded so the install
// path needs no network TUF fetch. This is `gh attestation trusted-root`
// output: the Sigstore Public Good root plus GitHub's attestation instance
// (one JSON per line). Refresh on trust-root key rotation with:
//
//	gh attestation trusted-root > branchkit-cli/trusted_roots.jsonl
//
// (Rotation is rare and pre-announced; a stale root fails closed — verified
// installs error rather than silently trusting — so this is safe to embed.)
//
//go:embed trusted_roots.jsonl
var embeddedTrustedRoots string

func trustedRoots() [][]byte {
	var roots [][]byte
	for _, line := range strings.Split(strings.TrimSpace(embeddedTrustedRoots), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			roots = append(roots, []byte(s))
		}
	}
	return roots
}

// AuthorAttestation is the outcome of verifying a release's Sigstore bundle.
type AuthorAttestation struct {
	// Verified is true only when a bundle was present AND cryptographically
	// verified AND bound to the expected repo. Absent bundle => Verified
	// false with Reason "no attestation" (community/unsigned posture).
	Verified bool
	// RepoSlug the attestation resolved to ("owner/name"). Empty when unverified.
	RepoSlug string
	// SAN / Issuer for display + the audit trail.
	SAN    string
	Issuer string
	// Reason explains a non-verified outcome (absent bundle, digest mismatch,
	// repo mismatch, crypto failure) — surfaced to the user, never swallowed.
	Reason string
	// BundleBytes is the raw Sigstore bundle, retained so the registry
	// counter-signature (which signs over the attestation's digest) can be
	// verified at install without re-downloading. Nil when unverified.
	BundleBytes []byte
}

// attestationAssetName is the pinned release-asset layout: the bundle sits
// beside the artifact, same convention as the optional `.sha256` sibling.
func attestationAssetName(artifactName string) string {
	return artifactName + ".sigstore.json"
}

// verifyReleaseAttestation looks for the artifact's Sigstore bundle among the
// release assets, verifies it against the downloaded bytes, and checks the
// resolved repo matches wantOwnerRepo ("owner/repo"). A missing bundle is a
// soft outcome (Verified=false, no error) — unsigned plugins still install
// under the restricted posture. A PRESENT-but-invalid bundle is a hard error:
// a tampered or mis-signed artifact must never install as if unsigned.
func verifyReleaseAttestation(
	client *http.Client,
	assets []ghAsset,
	artifactName string,
	artifactDigestHex string,
	wantOwnerRepo string,
) (*AuthorAttestation, error) {
	bundleName := attestationAssetName(artifactName)
	var bundleURL string
	for _, a := range assets {
		if a.Name == bundleName {
			bundleURL = a.BrowserDownloadURL
			break
		}
	}
	if bundleURL == "" {
		return &AuthorAttestation{Verified: false, Reason: "no attestation published"}, nil
	}

	fmt.Println("Verifying author attestation...")
	req, err := http.NewRequest("GET", bundleURL, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid attestation URL: %w", err)
	}
	req.Header.Set("User-Agent", "branchkit-cli")
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("failed to download attestation bundle %q", bundleName)
	}
	bundleJSON, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	id, err := verifyBundle(bundleJSON, artifactDigestHex, trustedRoots())
	if err != nil {
		// Present but unverifiable — fail closed.
		return nil, fmt.Errorf("author attestation did not verify: %w", err)
	}

	if !strings.EqualFold(id.RepoSlug, wantOwnerRepo) {
		return nil, fmt.Errorf(
			"attestation identity mismatch: bundle attests %q but installing from %q",
			id.RepoSlug, wantOwnerRepo)
	}

	return &AuthorAttestation{
		Verified:    true,
		RepoSlug:    id.RepoSlug,
		SAN:         id.SAN,
		Issuer:      id.Issuer,
		BundleBytes: bundleJSON,
	}, nil
}
