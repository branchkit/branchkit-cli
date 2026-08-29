package main

// Sigstore bundle verification — the author-signature half of the plugin
// signing chain (docs/design/DESIGN_PLUGIN_SIGNING_CHAIN.md, step 0 of
// docs/design/PLAN_SIGNING_CHAIN_IMPL.md).
//
// verifyBundle answers: "was this exact artifact signed by an
// OIDC-identified author, with the signature recorded in a transparency
// log?" It verifies cryptographically (Fulcio cert chain, Rekor inclusion,
// artifact digest) and RETURNS the identity — enforcement of "is that
// identity the manifest's declared publisher?" is the caller's policy,
// applied in the install path where the tier is resolved.
//
// sigstore-go is used as a library (not a cosign binary shell-out): users
// won't have cosign installed, and the CLI is the install path.

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"

	intoto "github.com/in-toto/attestation/go/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"google.golang.org/protobuf/types/known/structpb"
)

// SigstoreIdentity is the verified author identity extracted from a
// successfully verified bundle. It carries BOTH identity sources because
// GitHub emits two attestation shapes and the repo binding lives in
// different places (discovered during the step-0 spike, 2026-07-15):
//
//   - build-provenance (`attest-build-provenance` with the caller's own
//     workflow OIDC): cert SAN is the workflow URI and the Fulcio
//     SourceRepositoryURI extension is populated.
//   - release attestation (GitHub-hosted release infra): cert SAN is the
//     generic `dotcom.releases.github.com`, no repo extension — the repo
//     is in the in-toto statement predicate (`repository: owner/name`).
//
// `RepoSlug` normalizes both to "owner/name" for publisher matching; it's
// the field tier resolution should compare against the manifest's
// `publisher` ("github:owner"). BranchKit's own author-tooling snippet
// (step 2) will pin ONE model so first-party/registry plugins have a
// predictable cert shape — but consuming both keeps sideloaded community
// releases verifiable regardless of how the author signed.
type SigstoreIdentity struct {
	// Certificate subject alternative name (workflow URI, or the generic
	// release-infra identity).
	SAN string
	// Fulcio SourceRepositoryURI extension when present
	// ("https://github.com/OWNER/REPO"). Empty for release attestations.
	SourceRepo string
	// "owner/name" resolved from whichever source carried it (cert extension
	// or statement predicate). The normalized publisher-matching key.
	RepoSlug string
}

// verifyBundle verifies a Sigstore bundle against an artifact digest using
// one of the provided trusted roots (each a serialized TrustedRoot JSON —
// callers may hold several, e.g. Sigstore Public Good + GitHub's instance,
// and the bundle's certificate decides which one applies).
//
// digestHex is the artifact's SHA-256 as lowercase hex. Verification is
// fully offline given the bundle + trusted root: the Rekor inclusion proof
// travels inside the bundle.
func verifyBundle(bundleJSON []byte, digestHex string, trustedRootJSONs [][]byte) (*SigstoreIdentity, error) {
	digest, err := hex.DecodeString(strings.TrimSpace(digestHex))
	if err != nil {
		return nil, fmt.Errorf("artifact digest is not hex: %w", err)
	}

	var b bundle.Bundle
	if err := b.UnmarshalJSON(bytes.TrimSpace(bundleJSON)); err != nil {
		return nil, fmt.Errorf("parse sigstore bundle: %w", err)
	}

	var lastErr error
	for _, rootJSON := range trustedRootJSONs {
		trustedRoot, err := root.NewTrustedRootFromJSON(rootJSON)
		if err != nil {
			lastErr = fmt.Errorf("parse trusted root: %w", err)
			continue
		}

		// Require the signature to be anchored in time by at least one trusted
		// observer. "Observer" covers BOTH shapes we'll encounter: GitHub's
		// artifact attestations carry a TSA-signed timestamp (no embedded
		// Rekor entry), while cosign-with-Rekor bundles carry a tlog entry
		// whose integrated timestamp counts. We deliberately do NOT hard-
		// require `WithTransparencyLog` — it would reject every GitHub
		// attestation (they have zero tlog entries). Public-log discoverability
		// is a stronger property worth revisiting as an author-tooling policy
		// (have the signing snippet also upload to Rekor), tracked in
		// PLAN_SIGNING_CHAIN_IMPL; for v1, observer-timestamp anchoring is the
		// bar, and the identity + digest binding are the load-bearing checks.
		verifier, err := verify.NewVerifier(trustedRoot,
			verify.WithObserverTimestamps(1),
		)
		if err != nil {
			lastErr = fmt.Errorf("build verifier: %w", err)
			continue
		}

		// Identity POLICY is deliberately not enforced here (the unsafe-named
		// option means "no identity constraint at verify time") — identity is
		// extracted and returned; the install path matches it against the
		// manifest's declared publisher and resolves the trust tier.
		result, err := verifier.Verify(&b, verify.NewPolicy(
			verify.WithArtifactDigest("sha256", digest),
			verify.WithoutIdentitiesUnsafe(),
		))
		if err != nil {
			lastErr = err
			continue
		}

		id := &SigstoreIdentity{}
		if result.Signature != nil && result.Signature.Certificate != nil {
			cert := result.Signature.Certificate
			id.SAN = cert.SubjectAlternativeName
			id.SourceRepo = cert.SourceRepositoryURI // OID .1.12; empty for release-infra certs
		}
		if id.SAN == "" {
			return nil, fmt.Errorf("bundle verified but certificate carries no subject identity")
		}

		// Prefer the cert's source-repo extension (build-provenance model);
		// fall back to the in-toto statement predicate (release-attestation
		// model). Both normalize to "owner/name".
		id.RepoSlug = repoSlugFromURI(id.SourceRepo)
		if result.Statement != nil {
			if id.RepoSlug == "" {
				id.RepoSlug = repoSlugFromStatement(result.Statement)
			}
		}
		return id, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no trusted roots provided")
	}
	return nil, fmt.Errorf("bundle did not verify against any trusted root: %w", lastErr)
}

// repoSlugFromURI normalizes a "https://github.com/OWNER/REPO[/...]" URI to
// "OWNER/REPO". Returns "" for anything that isn't a github.com repo URI
// (e.g. the generic release-infra SAN).
func repoSlugFromURI(uri string) string {
	const gh = "https://github.com/"
	if !strings.HasPrefix(uri, gh) {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(uri, gh), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// repoSlugFromStatement pulls "owner/name" out of an in-toto statement — the
// release-attestation path where the repo isn't in the cert. Tries the
// predicate's `repository` field (GitHub release predicate), then falls back
// to a `pkg:github/owner/name@...` subject PURI.
func repoSlugFromStatement(stmt interface {
	GetPredicate() *structpb.Struct
	GetSubject() []*intoto.ResourceDescriptor
}) string {
	if pred := stmt.GetPredicate(); pred != nil {
		if v, ok := pred.GetFields()["repository"]; ok {
			if s := v.GetStringValue(); s != "" {
				return s
			}
		}
	}
	for _, sub := range stmt.GetSubject() {
		if slug := repoSlugFromPurl(sub.GetUri()); slug != "" {
			return slug
		}
	}
	return ""
}

// repoSlugFromPurl extracts "owner/name" from a "pkg:github/owner/name@ver" PURL.
func repoSlugFromPurl(uri string) string {
	const p = "pkg:github/"
	if !strings.HasPrefix(uri, p) {
		return ""
	}
	rest := strings.TrimPrefix(uri, p)
	if at := strings.IndexByte(rest, '@'); at >= 0 {
		rest = rest[:at]
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}
