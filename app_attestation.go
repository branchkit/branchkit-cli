package main

// Who may sign an app release. A plugin's attestation is judged after
// verification (identityOf, then the install path's repository match); an
// app release is judged during it, by an identity policy pinned here, because
// there is exactly one publisher and its identity is known in advance.
//
// A release is published on branchkit/releases as an immutable release, and
// GitHub's release signer attests it: one in-toto statement whose subjects
// are every asset with its digest, and whose release predicate names the
// repository and tag. The updater verifies that attestation over the
// release's SHA256SUMS and checks each file against its line there.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/verify"
)

// The one identity an app release may be signed by. GitHub's release signer
// (releaseSignerSAN in sigstore.go) counts only from GitHub's own Sigstore
// instance, and only with a release predicate for exactly this repository
// and the release's tag.
//
// This is how BranchKit publishes a release: built elsewhere, uploaded to a
// draft, then published immutable, so GitHub attests it and no signing key
// exists to keep. Two other shapes were weighed and are not accepted: a
// workflow in the releases repository attesting files it did not build, and
// a separate signing key. Should immutable releases prove unavailable, the
// shape that replaces them is added here, and nowhere else.
const (
	appReleaseRepo = "branchkit/releases"
	// The release predicate's type, any version of it: its `repository`
	// and `tag` fields are what the binding below reads.
	releasePredicatePrefix = "https://in-toto.io/attestation/release/"
)

// appSigner is who signed a verified app release, for the record the
// download prints.
type appSigner struct {
	Signer     string `json:"signer"` // "release-signer"
	SAN        string `json:"san"`
	Repository string `json:"repository"`
}

// verifyAppAttestation verifies one Sigstore bundle over a release file's
// SHA-256 (the release's SHA256SUMS) under the app's identity policy for one
// release tag. Any failure is an error: an update with no valid attestation
// is never staged.
func verifyAppAttestation(bundleJSON, digest []byte, tag string, anchors []trustAnchor) (*appSigner, error) {
	b, err := parseBundle(bundleJSON)
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
		if !anchor.github {
			continue
		}
		result, err := verifyEntity(b, digest, anchor, verify.WithCertificateIdentity(releaseSigner))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := checkReleasePredicate(result, tag); err != nil {
			return nil, err
		}
		return &appSigner{Signer: "release-signer", SAN: releaseSignerSAN, Repository: appReleaseRepo}, nil
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no GitHub trusted root"))
	}
	return nil, fmt.Errorf("the attestation is not GitHub's release attestation of %s %s: %w",
		appReleaseRepo, tag, errors.Join(errs...))
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
