package main

// Author-attestation verification in the install path — step 3 of
// docs/design/PLAN_SIGNING_CHAIN_IMPL.md, built on verifyBundle (sigstore.go).
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
	// SAN for display + the audit trail. Issuer sat here too, under the same
	// comment, and nothing ever displayed or recorded it.
	SAN string
	// Reason explains a non-verified outcome (absent bundle, digest mismatch,
	// repo mismatch, crypto failure) — surfaced to the user, never swallowed.
	Reason string
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
		Verified: true,
		RepoSlug: id.RepoSlug,
		SAN:      id.SAN,
	}, nil
}

// checkPublisherClaim cross-checks the manifest's declared publisher against
// the verified attestation identity. This closes the gap where a fork could
// declare `publisher: "github:someone-else"` and still install verified — the
// bundle honestly attests the fork's repo, so repo-vs-source matching alone
// never catches a false ownership claim. The manifest travels inside the
// signed tarball, so when the attestation verifies, the claim it contains is
// exactly what the named repo's workflow shipped: comparing the two is sound.
//
// Rules, in order:
//   - No publisher, or a malformed one → nothing to check. Malformed is
//     treated as no provenance claim (matching the actuator validator's
//     posture); a note is printed, never an error.
//   - Publisher declared but no verified attestation → the claim is unproven.
//     Noted, not fatal: unsigned installs stay on the community posture.
//   - Publisher names a provider a GitHub attestation cannot corroborate
//     (gitlab:, self-hosted, …) → noted as unproven, not fatal. The registry
//     counter-signature is the layer that can vouch for those.
//   - Publisher is `github:<owner>` and the attestation is verified → the
//     attested repo's owner MUST equal <owner> (case-insensitive). Mismatch is
//     a HARD error, same class as a present-but-invalid bundle: it is the
//     impersonation signal this whole mechanism exists to catch.
func checkPublisherClaim(publisher string, attestation *AuthorAttestation) error {
	if publisher == "" {
		return nil
	}

	provider, identity, ok := splitPublisher(publisher)
	if !ok {
		fmt.Printf("Note: malformed publisher %q — treated as no provenance claim.\n", publisher)
		return nil
	}

	if attestation == nil || !attestation.Verified {
		fmt.Printf("Note: publisher %q is declared but unproven (no verified attestation).\n", publisher)
		return nil
	}

	if provider != "github" {
		fmt.Printf("Note: publisher %q cannot be corroborated by a GitHub attestation; claim recorded as unproven.\n", publisher)
		return nil
	}

	owner, _, found := strings.Cut(attestation.RepoSlug, "/")
	if !found || !strings.EqualFold(owner, identity) {
		return fmt.Errorf(
			"publisher mismatch: manifest declares %q but the attestation was issued to %q — refusing to install an artifact whose verified identity contradicts its ownership claim",
			publisher, attestation.RepoSlug)
	}
	return nil
}

// splitPublisher parses `provider:identity` (hosted) or rejects anything it
// does not positively recognize. `provider=host:identity` (self-hosted) parses
// too — the host is carried in the provider slot's suffix and can never equal
// "github", so self-hosted claims route to the unproven path above. This is a
// deliberately narrow parser: the actuator's publisher.rs owns the full
// taxonomy; the CLI only needs to answer "is this a bare github claim, and
// for whom?"
func splitPublisher(s string) (provider, identity string, ok bool) {
	provider, identity, found := strings.Cut(s, ":")
	if !found || provider == "" || identity == "" {
		return "", "", false
	}
	if strings.ContainsAny(identity, ":/ ") {
		return "", "", false
	}
	return strings.ToLower(provider), identity, true
}
