package main

// Sigstore bundle verification — the author-signature half of the plugin
// signing chain. Keyless: the author holds no key; identity is the provider
// OIDC claim in the Fulcio cert.
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
	// or statement predicate). The normalized publisher-matching key. Empty
	// when the signer is neither of the two GitHub identities, so it matches
	// no repository.
	RepoSlug string
}

// The two GitHub identities whose repository binding the CLI reads, and the
// one place each is defined.
const (
	// actionsIssuer is the OIDC issuer of a GitHub Actions workflow. Only
	// its certificates carry a SourceRepositoryURI the CLI believes: Fulcio
	// copies that extension from the issuer's token, and some other CI
	// issuers fill it from a claim their user can point at any repository.
	actionsIssuer = "https://token.actions.githubusercontent.com"
	// releaseSignerSAN is the certificate identity of GitHub's release
	// infrastructure. Its certificates name no repository; the repository is
	// in the release predicate it signs. Anyone can write such a predicate,
	// so it counts only from this signer, chained to GitHub's own instance.
	releaseSignerSAN = "https://dotcom.releases.github.com"
	// githubFulcioURI is the Fulcio of GitHub's own Sigstore instance, as
	// its trusted root names it. The committed release-signer fixtures
	// verify against that root and not against Sigstore's Public Good root
	// (TestVerifyBundleRejectsWithoutMatchingRoot).
	githubFulcioURI = "fulcio.githubapp.com"
)

// trustAnchor is one Sigstore instance's trusted material, and whether it is
// GitHub's own instance: the only one whose release-signer certificates the
// CLI accepts.
type trustAnchor struct {
	material root.TrustedMaterial
	github   bool
}

// parseTrustAnchors reads serialized TrustedRoot JSONs, one per instance, as
// `gh attestation trusted-root` prints them. A root that does not parse is an
// error: the set is embedded, so a bad one is a build defect, not input.
func parseTrustAnchors(rootJSONs [][]byte) ([]trustAnchor, error) {
	anchors := make([]trustAnchor, 0, len(rootJSONs))
	for _, rootJSON := range rootJSONs {
		tr, err := root.NewTrustedRootFromJSON(rootJSON)
		if err != nil {
			return nil, fmt.Errorf("parse trusted root: %w", err)
		}
		anchors = append(anchors, trustAnchor{material: tr, github: isGitHubInstance(tr)})
	}
	return anchors, nil
}

// isGitHubInstance reports whether every certificate authority of a trusted
// root is GitHub's Fulcio. The URI comes from the CLI's own embedded root
// file, so it is as trusted as the keys beside it.
func isGitHubInstance(tr root.TrustedMaterial) bool {
	cas := tr.FulcioCertificateAuthorities()
	if len(cas) == 0 {
		return false
	}
	for _, ca := range cas {
		f, ok := ca.(*root.FulcioCertificateAuthority)
		if !ok || f.URI != githubFulcioURI {
			return false
		}
	}
	return true
}

// parseBundle reads a Sigstore bundle's JSON.
func parseBundle(bundleJSON []byte) (*bundle.Bundle, error) {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bytes.TrimSpace(bundleJSON)); err != nil {
		return nil, fmt.Errorf("parse sigstore bundle: %w", err)
	}
	return &b, nil
}

// verifyEntity verifies a signed entity against an artifact's SHA-256 under
// one trust anchor and one identity policy: verify.WithCertificateIdentity,
// or verify.WithoutIdentitiesUnsafe when the caller judges the identity
// itself. Verification is offline: the timestamp or the transparency-log
// inclusion proof travels inside the bundle.
func verifyEntity(entity verify.SignedEntity, digest []byte, anchor trustAnchor, identity verify.PolicyOption) (*verify.VerificationResult, error) {
	entries, err := entity.TlogEntries()
	if err != nil {
		return nil, fmt.Errorf("read transparency-log entries: %w", err)
	}

	// Require the signature to be anchored in time by at least one trusted
	// observer. Two bundle shapes reach here. A PRIVATE repository's
	// GitHub attestation is signed by GitHub's own instance: a
	// TSA-signed timestamp and no transparency-log entry. A PUBLIC
	// repository's goes through Sigstore's Public Good instance: a Rekor
	// entry and no TSA timestamp. A log entry's integrated time only
	// counts as an observer timestamp once the entry itself is verified,
	// so when the bundle carries entries they are verified too; without
	// that, every public plugin's bundle failed ("integrated timestamps:
	// 0 < 1"). Requiring a log entry unconditionally would instead reject
	// every private-repo attestation. Identity + digest binding remain the
	// load-bearing checks.
	opts := []verify.VerifierOption{verify.WithObserverTimestamps(1)}
	if len(entries) > 0 {
		opts = append(opts, verify.WithTransparencyLog(1))
	}
	verifier, err := verify.NewVerifier(anchor.material, opts...)
	if err != nil {
		return nil, fmt.Errorf("build verifier: %w", err)
	}
	return verifier.Verify(entity, verify.NewPolicy(verify.WithArtifactDigest("sha256", digest), identity))
}

// verifyBundle verifies a plugin's Sigstore bundle against an artifact digest
// under any of the trust anchors (Sigstore Public Good and GitHub's
// instance; the bundle's certificate decides which one applies).
//
// digestHex is the artifact's SHA-256 as lowercase hex.
func verifyBundle(bundleJSON []byte, digestHex string, anchors []trustAnchor) (*SigstoreIdentity, error) {
	digest, err := hex.DecodeString(strings.TrimSpace(digestHex))
	if err != nil {
		return nil, fmt.Errorf("artifact digest is not hex: %w", err)
	}
	b, err := parseBundle(bundleJSON)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, anchor := range anchors {
		// Identity POLICY is deliberately not enforced here (the unsafe-named
		// option means "no identity constraint at verify time") — identity is
		// extracted and returned; the install path matches it against the
		// manifest's declared publisher and resolves the trust tier.
		result, err := verifyEntity(b, digest, anchor, verify.WithoutIdentitiesUnsafe())
		if err != nil {
			lastErr = err
			continue
		}
		return identityOf(result, anchor)
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no trusted roots provided")
	}
	return nil, fmt.Errorf("bundle did not verify against any trusted root: %w", lastErr)
}

// identityOf reads who a verified bundle speaks for. RepoSlug comes only from
// a source the signer cannot choose: the SourceRepositoryURI of a GitHub
// Actions certificate (build provenance), or the release predicate of
// GitHub's release signer on GitHub's own instance (release attestation).
// Any other signer gets an empty RepoSlug, which matches no repository, so
// the install fails closed.
func identityOf(result *verify.VerificationResult, anchor trustAnchor) (*SigstoreIdentity, error) {
	id := &SigstoreIdentity{}
	issuer := ""
	if result.Signature != nil && result.Signature.Certificate != nil {
		cert := result.Signature.Certificate
		id.SAN = cert.SubjectAlternativeName
		id.SourceRepo = cert.SourceRepositoryURI // OID .1.12; empty for release-infra certs
		issuer = cert.Issuer
	}
	if id.SAN == "" {
		return nil, fmt.Errorf("bundle verified but certificate carries no subject identity")
	}

	switch {
	case issuer == actionsIssuer:
		id.RepoSlug = repoSlugFromURI(id.SourceRepo)
	case id.SAN == releaseSignerSAN && anchor.github && result.Statement != nil:
		id.RepoSlug = repoSlugFromStatement(result.Statement)
	}
	return id, nil
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
// to a `pkg:github/owner/name@...` subject PURI. Only identityOf's
// release-signer case may call it: for any other signer the statement says
// whatever its author wrote.
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
