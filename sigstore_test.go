package main

// Offline verification tests against committed fixtures — no network.
//
// Fixtures (testdata/sigstore/, captured 2026-07-15):
//   bundle.jsonl        — GitHub artifact attestation for cli/cli's
//                         gh_2.96.0_checksums.txt (a real, public Sigstore
//                         v0.3 bundle; first line is the bundle JSON)
//   trusted_roots.jsonl — `gh attestation trusted-root` output: one JSON
//                         trusted root per line (Sigstore Public Good +
//                         GitHub's instance; the bundle verifies against
//                         the GitHub one — multi-root selection is part of
//                         what's under test)
//   artifact.sha256     — the artifact's digest (the artifact itself is
//                         not committed; verification is digest-based)
//
// Refresh recipe if the fixtures ever age out (trusted root key rotation):
//   gh release download --repo cli/cli --pattern "gh_*_checksums.txt"
//   shasum -a 256 gh_*_checksums.txt > artifact.sha256   (hex only)
//   gh attestation download <file> --repo cli/cli        (→ bundle.jsonl)
//   gh attestation trusted-root > trusted_roots.jsonl

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func loadSigstoreFixtures(t *testing.T) (bundleJSON []byte, digestHex string, roots []trustAnchor) {
	t.Helper()
	raw, err := os.ReadFile("testdata/sigstore/bundle.jsonl")
	if err != nil {
		t.Fatalf("read bundle fixture: %v", err)
	}
	bundleJSON = bytes.SplitN(bytes.TrimSpace(raw), []byte("\n"), 2)[0]

	digestRaw, err := os.ReadFile("testdata/sigstore/artifact.sha256")
	if err != nil {
		t.Fatalf("read digest fixture: %v", err)
	}
	digestHex = strings.Fields(string(digestRaw))[0]

	rootsRaw, err := os.ReadFile("testdata/sigstore/trusted_roots.jsonl")
	if err != nil {
		t.Fatalf("read trusted roots fixture: %v", err)
	}
	var rootJSONs [][]byte
	for _, line := range bytes.Split(bytes.TrimSpace(rootsRaw), []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			rootJSONs = append(rootJSONs, line)
		}
	}
	roots, err = parseTrustAnchors(rootJSONs)
	if err != nil {
		t.Fatal(err)
	}
	return
}

// The embedded roots parse, and exactly one of them (GitHub's instance) is
// the one the release signer counts from: a refresh that dropped it would
// fail every release-attestation install, and one that marked Public Good as
// GitHub's would drop the release signer's root restriction.
func TestEmbeddedTrustAnchors(t *testing.T) {
	anchors, err := trustAnchors()
	if err != nil {
		t.Fatal(err)
	}
	github := 0
	for _, a := range anchors {
		if a.github {
			github++
		}
	}
	if len(anchors) != 2 || github != 1 {
		t.Fatalf("%d roots, %d of them GitHub's instance; want 2 and 1", len(anchors), github)
	}
}

func TestVerifyBundleAgainstRealAttestation(t *testing.T) {
	bundleJSON, digestHex, roots := loadSigstoreFixtures(t)

	id, err := verifyBundle(bundleJSON, digestHex, roots)
	if err != nil {
		t.Fatalf("verifyBundle: %v", err)
	}
	// This artifact (the release checksums file) is signed by GitHub's
	// release infrastructure, so its verified identity is that signer.
	// The property under test is that verification SUCCEEDS against the
	// right trusted root and the identity is extracted — the workflow-shape
	// identity (source repo, OIDC issuer) is covered by the workflow fixture.
	if id.SAN != "https://dotcom.releases.github.com" {
		t.Errorf("SAN = %q, want the GitHub release signer identity", id.SAN)
	}
	if id.RepoSlug != "cli/cli" {
		t.Errorf("RepoSlug = %q, want cli/cli (the release signer's predicate)", id.RepoSlug)
	}
}

func TestVerifyBundleRejectsWrongDigest(t *testing.T) {
	bundleJSON, _, roots := loadSigstoreFixtures(t)

	// Same bundle, different artifact — a tampered download MUST fail even
	// though the bundle itself is authentic.
	wrong := strings.Repeat("ab", 32)
	if _, err := verifyBundle(bundleJSON, wrong, roots); err == nil {
		t.Fatal("verification succeeded for a digest the bundle never signed")
	}
}

func TestVerifyBundleResolvesRepoFromStatement(t *testing.T) {
	// A real GitHub RELEASE attestation (astral-sh/uv's release asset). In this
	// model the cert SAN is the generic release-infra identity and carries no
	// source-repo extension — the repo binding lives in the in-toto statement
	// predicate. verifyBundle must still resolve "owner/name" (RepoSlug), which
	// is the publisher-matching key tier resolution compares against the
	// manifest. This exercises the second of the two GitHub attestation shapes
	// the step-0 spike surfaced (the first — build-provenance with a workflow
	// SAN + source-repo extension — is the cert path).
	bundleRaw, err := os.ReadFile("testdata/sigstore/workflow_bundle.jsonl")
	if err != nil {
		t.Fatalf("read release-attestation fixture: %v", err)
	}
	bundleJSON := bytes.SplitN(bytes.TrimSpace(bundleRaw), []byte("\n"), 2)[0]
	digestRaw, err := os.ReadFile("testdata/sigstore/workflow_artifact.sha256")
	if err != nil {
		t.Fatalf("read release-attestation digest fixture: %v", err)
	}
	_, _, roots := loadSigstoreFixtures(t)

	id, err := verifyBundle(bundleJSON, strings.Fields(string(digestRaw))[0], roots)
	if err != nil {
		t.Fatalf("verifyBundle: %v", err)
	}
	if id.RepoSlug != "astral-sh/uv" {
		t.Errorf("RepoSlug = %q, want astral-sh/uv (from statement predicate)", id.RepoSlug)
	}
}

func TestRepoSlugHelpers(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://github.com/astral-sh/uv", "astral-sh/uv"},
		{"https://github.com/cli/cli/.github/workflows/release.yml@refs/tags/v1", "cli/cli"},
		{"https://gitlab.com/x/y", ""},
		{"https://dotcom.releases.github.com", ""},
	}
	for _, c := range cases {
		if got := repoSlugFromURI(c.in); got != c.want {
			t.Errorf("repoSlugFromURI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := repoSlugFromPurl("pkg:github/astral-sh/uv@0.11.29"); got != "astral-sh/uv" {
		t.Errorf("repoSlugFromPurl = %q, want astral-sh/uv", got)
	}
}

func TestVerifyBundleRejectsWithoutMatchingRoot(t *testing.T) {
	bundleJSON, digestHex, roots := loadSigstoreFixtures(t)

	// Only the FIRST root (Sigstore Public Good) — the bundle was signed via
	// GitHub's instance, so it must NOT verify. This is the fork-resilience
	// property in miniature: trust roots are not interchangeable.
	if _, err := verifyBundle(bundleJSON, digestHex, roots[:1]); err == nil {
		t.Fatal("verification succeeded against a trusted root that never issued the certificate")
	}
}

// A public repository's GitHub attestation is signed through Sigstore's
// Public Good instance: its bundle carries a Rekor transparency-log entry
// and no timestamp-authority timestamp — the opposite shape from the
// GitHub-instance bundles above. Verification failed on every such bundle
// ("integrated timestamps: 0 < 1") because the log entry was never
// verified, so its integrated time never counted.
//
// Fixture (captured 2026-10-05): the darwin-arm64 archive of
// branchkit/branchkit-plugin-announcements v0.2.0-rc.1, its bundle and its
// digest (public_bundle.jsonl, public_artifact.sha256).
func TestVerifyBundlePublicGoodTransparencyLog(t *testing.T) {
	_, _, roots := loadSigstoreFixtures(t)
	bundleJSON, err := os.ReadFile("testdata/sigstore/public_bundle.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	digestRaw, err := os.ReadFile("testdata/sigstore/public_artifact.sha256")
	if err != nil {
		t.Fatal(err)
	}
	digestHex := strings.Fields(string(digestRaw))[0]

	id, err := verifyBundle(bytes.TrimSpace(bundleJSON), digestHex, roots)
	if err != nil {
		t.Fatalf("verifyBundle: %v", err)
	}
	if id.RepoSlug != "branchkit/branchkit-plugin-announcements" {
		t.Errorf("RepoSlug = %q", id.RepoSlug)
	}

	wrong := strings.Repeat("0", 64)
	if _, err := verifyBundle(bytes.TrimSpace(bundleJSON), wrong, roots); err == nil {
		t.Error("verified against the wrong digest")
	}
}

// Who a verified bundle speaks for depends on who signed it, never on what
// the signed statement claims: anyone can sign a statement naming any
// repository. The statement's repository counts only from GitHub's release
// signer on GitHub's own instance; a certificate's source repository only
// from a GitHub Actions workflow.
func TestVerifyBundleRepoComesFromTheSignerNotTheStatement(t *testing.T) {
	fs := newFakeSigstore(t)
	artifact := []byte("plugin tarball")
	digest := sha256HexBytes(artifact)
	claimsVictim := statement("plugin.tar.gz", digest, releasePredicateType, releasePredicate("victim/plugin", "v1.0.0"))

	cases := []struct {
		name   string
		id     fakeIdentity
		github bool // the anchor is GitHub's instance
		want   string
	}{
		{"an email identity's predicate names no repo", emailIdentity, false, ""},
		{"nor on GitHub's instance", emailIdentity, true, ""},
		{"the release signer on another instance names none", signerOnly, false, ""},
		{"the release signer on GitHub's instance names its predicate's", signerOnly, true, "victim/plugin"},
		{"an Actions workflow names its own repo, whatever the predicate says",
			actionsIdentity("mallory/fork", "release.yml@refs/tags/v1.0.0"), false, "mallory/fork"},
		{"another issuer's source-repository extension names no repo",
			fakeIdentity{SAN: "https://ci.example/pipelines/1", Issuer: "https://oidc.ci.example", SourceRepo: "https://github.com/victim/plugin"}, false, ""},
		{"an enterprise's own Actions issuer names its repo",
			fakeIdentity{SAN: "https://github.com/acme/plugin/.github/workflows/release.yml@refs/tags/v1.0.0", Issuer: actionsIssuer + "/acme", SourceRepo: "https://github.com/acme/plugin"}, false, "acme/plugin"},
		{"a host that only starts like GitHub's issuer names none",
			fakeIdentity{SAN: "https://ci.example/pipelines/1", Issuer: actionsIssuer + ".evil.example", SourceRepo: "https://github.com/victim/plugin"}, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bundleJSON := fs.attest(t, c.id, claimsVictim)
			id, err := verifyBundle(bundleJSON, digest, []trustAnchor{fs.anchor(c.github)})
			if err != nil {
				t.Fatalf("verifyBundle: %v", err)
			}
			if id.RepoSlug != c.want {
				t.Errorf("RepoSlug = %q, want %q", id.RepoSlug, c.want)
			}
		})
	}
}
