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

func loadSigstoreFixtures(t *testing.T) (bundleJSON []byte, digestHex string, roots [][]byte) {
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
	for _, line := range bytes.Split(bytes.TrimSpace(rootsRaw), []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			roots = append(roots, line)
		}
	}
	return
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
