package main

// Install-path attestation outcomes, driven by the committed uv release
// bundle (testdata/sigstore/workflow_*) served from a local test server —
// no network. Proves the three postures the install path must distinguish:
// verified, absent (soft), and present-but-wrong (hard).

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func loadWorkflowFixture(t *testing.T) (bundle []byte, digestHex string) {
	t.Helper()
	b, err := os.ReadFile("testdata/sigstore/workflow_bundle.jsonl")
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	// The bundle asset is a single JSON object (first line of the download).
	if i := strings.IndexByte(string(b), '\n'); i >= 0 {
		b = b[:i]
	}
	d, err := os.ReadFile("testdata/sigstore/workflow_artifact.sha256")
	if err != nil {
		t.Fatalf("read digest: %v", err)
	}
	return b, strings.Fields(string(d))[0]
}

// serveAssets returns a test server and the ghAsset list pointing at it.
func serveAssets(t *testing.T, files map[string][]byte) (*httptest.Server, []ghAsset) {
	t.Helper()
	mux := http.NewServeMux()
	var assets []ghAsset
	srv := httptest.NewServer(mux)
	for name, data := range files {
		data := data
		path := "/" + name
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) { w.Write(data) })
		assets = append(assets, ghAsset{Name: name, BrowserDownloadURL: srv.URL + path})
	}
	return srv, assets
}

func TestReleaseAttestationVerified(t *testing.T) {
	bundle, digest := loadWorkflowFixture(t)
	const artifact = "branchkit-plugin-demo-linux-x86_64.tar.gz"
	srv, assets := serveAssets(t, map[string][]byte{
		artifact:                       []byte("ignored — verification is digest-based"),
		attestationAssetName(artifact): bundle,
	})
	defer srv.Close()

	att, err := verifyReleaseAttestation(srv.Client(), assets, artifact, digest, "astral-sh/uv")
	if err != nil {
		t.Fatalf("verifyReleaseAttestation: %v", err)
	}
	if !att.Verified || att.RepoSlug != "astral-sh/uv" {
		t.Errorf("got verified=%v repo=%q, want true astral-sh/uv", att.Verified, att.RepoSlug)
	}
}

func TestReleaseAttestationAbsentIsSoft(t *testing.T) {
	_, digest := loadWorkflowFixture(t)
	const artifact = "branchkit-plugin-demo-linux-x86_64.tar.gz"
	// No .sigstore.json asset published.
	srv, assets := serveAssets(t, map[string][]byte{artifact: []byte("x")})
	defer srv.Close()

	att, err := verifyReleaseAttestation(srv.Client(), assets, artifact, digest, "astral-sh/uv")
	if err != nil {
		t.Fatalf("absent attestation must be a soft outcome, got error: %v", err)
	}
	if att.Verified {
		t.Error("Verified should be false when no bundle is published")
	}
	if att.Reason == "" {
		t.Error("a non-verified outcome must carry a Reason")
	}
}

func TestReleaseAttestationRepoMismatchIsHard(t *testing.T) {
	bundle, digest := loadWorkflowFixture(t)
	const artifact = "branchkit-plugin-demo-linux-x86_64.tar.gz"
	srv, assets := serveAssets(t, map[string][]byte{
		artifact:                       []byte("x"),
		attestationAssetName(artifact): bundle,
	})
	defer srv.Close()

	// The bundle really attests astral-sh/uv; installing it as someone else's
	// plugin must be rejected — a valid signature for the WRONG repo is an
	// attack, not a pass.
	_, err := verifyReleaseAttestation(srv.Client(), assets, artifact, digest, "evil/impostor")
	if err == nil {
		t.Fatal("repo mismatch must be a hard error")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error should name the mismatch, got: %v", err)
	}
}

func TestReleaseAttestationBadDigestIsHard(t *testing.T) {
	bundle, _ := loadWorkflowFixture(t)
	const artifact = "branchkit-plugin-demo-linux-x86_64.tar.gz"
	srv, assets := serveAssets(t, map[string][]byte{
		artifact:                       []byte("x"),
		attestationAssetName(artifact): bundle,
	})
	defer srv.Close()

	// Bundle present and authentic, but for a different artifact digest — a
	// swapped tarball. Must fail closed.
	wrong := strings.Repeat("00", 32)
	if _, err := verifyReleaseAttestation(srv.Client(), assets, artifact, wrong, "astral-sh/uv"); err == nil {
		t.Fatal("a bundle that doesn't cover the downloaded bytes must be a hard error")
	}
}

// checkPublisherClaim is the ownership-vs-identity cross-check. The one hard
// outcome is a bare github: claim contradicted by a verified attestation;
// every other combination must stay soft, or unsigned/community installs
// would start failing on a field that promises nothing.
func TestCheckPublisherClaim(t *testing.T) {
	verified := func(slug string) *AuthorAttestation {
		return &AuthorAttestation{Verified: true, RepoSlug: slug}
	}
	cases := []struct {
		name        string
		publisher   string
		attestation *AuthorAttestation
		wantErr     bool
	}{
		{"no claim, no attestation", "", nil, false},
		{"no claim, verified", "", verified("branchkit/branchkit-plugin-keyboard"), false},
		{"claim matches attested owner", "github:branchkit", verified("branchkit/branchkit-plugin-keyboard"), false},
		{"claim matches, case-insensitive", "github:BranchKit", verified("branchkit/x"), false},
		{"claim contradicted by attestation", "github:branchkit", verified("attacker/branchkit-plugin-keyboard"), true},
		{"claim without attestation is unproven, soft", "github:branchkit", nil, false},
		{"claim with unverified attestation is unproven, soft", "github:branchkit", &AuthorAttestation{Verified: false, Reason: "no attestation"}, false},
		{"non-github provider cannot be corroborated, soft", "gitlab:janedoe", verified("attacker/x"), false},
		{"self-hosted provider routes to unproven, soft", "gitea=git.janedoe.dev:janedoe", verified("attacker/x"), false},
		{"malformed claim is no claim, soft", "just-a-name", verified("attacker/x"), false},
		{"empty identity is malformed, soft", "github:", verified("attacker/x"), false},
		{"identity with slash is malformed, soft", "github:owner/repo", verified("attacker/x"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPublisherClaim(tc.publisher, tc.attestation)
			if (err != nil) != tc.wantErr {
				t.Fatalf("publisher=%q attestation=%+v: err=%v, wantErr=%v",
					tc.publisher, tc.attestation, err, tc.wantErr)
			}
		})
	}
}
