package main

// Who may sign an app release. A plugin's attestation is judged after
// verification (identityOf, then the install path's repository match); an
// app release is judged during it, by an identity policy pinned here, because
// there is exactly one publisher and its identity is known in advance.
//
// The release's SHA256SUMS carries the attestation, and each file is checked
// against its line there, so one signature covers every file of a release.

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// The identities an app release may be signed by, all bound to the one
// repository releases are published from. Two shapes are accepted because
// both are ways GitHub signs a release, and which one the release workflow
// uses is not settled:
//
//   - a workflow in that repository, run on the release's own tag, signing
//     with its Actions identity (attest-build-provenance and the like);
//   - GitHub's release signer, which signs an immutable release's assets
//     with a release predicate naming the repository and tag. Only its
//     certificates from GitHub's own Sigstore instance count (see
//     releaseSignerSAN in sigstore.go). Such a bundle is made when the
//     release is published, after which its assets cannot change, so it
//     cannot be the release's own SHA256SUMS.sigstore.json asset: GitHub
//     serves it from its attestations API, which `app download` does not
//     read yet. The policy accepts it so that reading it is the only change
//     that shape needs.
const (
	appReleaseRepo    = "branchkit/releases"
	appReleaseRepoURI = "https://github.com/" + appReleaseRepo
	// A workflow file directly under .github/workflows, at refs/tags/<tag>.
	appWorkflowSANPrefix = appReleaseRepoURI + "/.github/workflows/"
	// The release predicate's type, any version of it: its `repository`
	// and `tag` fields are what the binding below reads.
	releasePredicatePrefix = "https://in-toto.io/attestation/release/"
)

// appSigner is who signed a verified app release, for the record the
// download prints.
type appSigner struct {
	Signer     string `json:"signer"` // "workflow" or "release-signer"
	SAN        string `json:"san"`
	Repository string `json:"repository"`
}

// verifyAppSums verifies a release's checksums file against its Sigstore
// bundle and the app's identity policy for one release tag. Any failure is
// an error: an update with no valid attestation is never staged.
func verifyAppSums(bundleJSON, sums []byte, tag string, anchors []trustAnchor) (*appSigner, error) {
	digest := sha256.Sum256(sums)
	return verifyAppAttestation(bundleJSON, digest[:], tag, anchors)
}

// verifyAppAttestation is verifyAppSums for a file known by its SHA-256.
func verifyAppAttestation(bundleJSON, digest []byte, tag string, anchors []trustAnchor) (*appSigner, error) {
	b, err := parseBundle(bundleJSON)
	if err != nil {
		return nil, err
	}

	workflow, err := appWorkflowIdentity(tag)
	if err != nil {
		return nil, err
	}
	releaseSigner, err := verify.NewShortCertificateIdentity(
		// The release signer's certificates carry no OIDC issuer (the
		// committed fixtures); its issuer is not what makes it GitHub's.
		// GitHub's own Fulcio issuing that SAN does, which anchor.github
		// checks.
		"", ".*", releaseSignerSAN, "")
	if err != nil {
		return nil, err
	}

	var errs []error
	for _, anchor := range anchors {
		result, err := verifyEntity(b, digest, anchor, verify.WithCertificateIdentity(workflow))
		if err == nil {
			return &appSigner{Signer: "workflow", SAN: result.Signature.Certificate.SubjectAlternativeName, Repository: appReleaseRepo}, nil
		}
		errs = append(errs, err)
		if !anchor.github {
			continue
		}
		result, err = verifyEntity(b, digest, anchor, verify.WithCertificateIdentity(releaseSigner))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := checkReleasePredicate(result, tag); err != nil {
			return nil, err
		}
		return &appSigner{Signer: "release-signer", SAN: releaseSignerSAN, Repository: appReleaseRepo}, nil
	}
	if len(anchors) == 0 {
		errs = append(errs, errors.New("no trusted roots"))
	}
	return nil, fmt.Errorf("the checksums' attestation is not a signature by %s for %s: %w",
		appReleaseRepo, tag, errors.Join(errs...))
}

// appWorkflowIdentity is the Actions identity of a workflow in the releases
// repository run on exactly this tag.
func appWorkflowIdentity(tag string) (verify.CertificateIdentity, error) {
	san, err := verify.NewSANMatcher("",
		"^"+regexp.QuoteMeta(appWorkflowSANPrefix)+`[^@/]+@refs/tags/`+regexp.QuoteMeta(tag)+"$")
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	issuer, err := verify.NewIssuerMatcher(actionsIssuer, "")
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	return verify.NewCertificateIdentity(san, issuer, certificate.Extensions{SourceRepositoryURI: appReleaseRepoURI})
}

// checkReleasePredicate binds a release-signer attestation to the releases
// repository and this tag. The certificate names neither; the predicate,
// signed by GitHub's release infrastructure, does.
func checkReleasePredicate(result *verify.VerificationResult, tag string) error {
	stmt := result.Statement
	if stmt == nil || !strings.HasPrefix(stmt.GetPredicateType(), releasePredicatePrefix) {
		return fmt.Errorf("the release signer's attestation is not a release predicate")
	}
	fields := stmt.GetPredicate().GetFields()
	repo := fields["repository"].GetStringValue()
	gotTag := fields["tag"].GetStringValue()
	if !strings.EqualFold(repo, appReleaseRepo) || gotTag != tag {
		return fmt.Errorf("the release attestation is for %s %s, not %s %s", repo, gotTag, appReleaseRepo, tag)
	}
	return nil
}
