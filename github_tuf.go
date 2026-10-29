package main

// GitHub's current Sigstore trusted root, read fresh for each `app download`.
//
// The trusted roots embedded in this CLI (trusted_roots.jsonl) are a
// snapshot. GitHub's instance moves to a new timestamp authority and Fulcio
// certificate about every six months (its root lists one per period since
// 2023), and a bundle verifies only against the authority that timestamped
// it, so a release GitHub attests after its next move fails against an older
// snapshot. The updater cannot wait for a CLI carrying a newer one: that CLI
// would arrive only through the updater. So `app download` reads GitHub's
// trusted root from the TUF repository GitHub publishes it in (the one
// `gh attestation` reads), verified by TUF from the root metadata embedded
// here, and uses it in place of the snapshot's GitHub entry. A download that
// cannot read it stops (withFreshGitHubRoot).
//
// Root metadata rotates too, but an old copy keeps working: TUF walks forward
// from it through every later root, each signed by a threshold of the keys of
// the one before, so this file only ever needs refreshing to shorten that
// walk.

import (
	_ "embed"
	"fmt"
	"net/http"

	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/theupdateframework/go-tuf/v2/metadata/fetcher"
)

// githubTUFURL is the TUF repository of GitHub's Sigstore instance.
const githubTUFURL = "https://tuf-repo.github.com"

// githubTUFRoot is version 9 of that repository's root metadata (3 of 7 root
// keys sign it). Where it came from, 2026-10-29: gh 2.83.1 embeds version 3;
// TUF walked from that copy to version 9, and the trusted_root.json version
// 9 then served was the same document as trusted_roots.jsonl's GitHub line.
//
//go:embed github_tuf_root.json
var githubTUFRoot []byte

// refreshGitHubAnchor reads GitHub's current trusted root from the TUF
// repository at repoURL, verified from tufRoot, and returns it as a trust
// anchor. A root that is not GitHub's instance (isGitHubInstance) is an
// error: only GitHub's own instance may vouch for its release signer.
func refreshGitHubAnchor(client *http.Client, repoURL string, tufRoot []byte) (trustAnchor, error) {
	f := fetcher.NewDefaultFetcher()
	f.SetHTTPClient(client)
	f.SetHTTPUserAgent("branchkit-cli")
	c, err := tuf.New(&tuf.Options{
		Root:              tufRoot,
		RepositoryBaseURL: repoURL,
		// Nothing is written to disk, so there is no cache for another
		// process to replace; every run walks from the embedded root.
		DisableLocalCache: true,
		Fetcher:           f,
	})
	if err != nil {
		return trustAnchor{}, err
	}
	rootJSON, err := c.GetTarget("trusted_root.json")
	if err != nil {
		return trustAnchor{}, err
	}
	anchors, err := parseTrustAnchors([][]byte{rootJSON})
	if err != nil {
		return trustAnchor{}, err
	}
	if !anchors[0].github {
		return trustAnchor{}, fmt.Errorf("the trusted root %s serves is not GitHub's instance", repoURL)
	}
	return anchors[0], nil
}

// withFreshGitHubRoot is anchors with its GitHub instance replaced by what
// fresh returns (nil fresh: anchors as they are). When fresh fails, so does
// this, and the download stops: TUF is where GitHub revokes a key, so
// verifying against the embedded snapshot instead would let anyone who can
// make that read fail, by blocking it or by tampering with it, bring a
// revoked key back. A download that stops here is tried again by the next
// update check.
func withFreshGitHubRoot(anchors []trustAnchor, fresh func() (trustAnchor, error)) ([]trustAnchor, error) {
	if fresh == nil {
		return anchors, nil
	}
	gh, err := fresh()
	if err != nil {
		return nil, fmt.Errorf("could not read GitHub's current trusted root from %s, so no release can be verified: %w", githubTUFURL, err)
	}
	out := []trustAnchor{gh}
	for _, a := range anchors {
		if !a.github {
			out = append(out, a)
		}
	}
	return out, nil
}
