package main

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// fakeTUF is a TUF repository made up for a test: one version of each
// role, signed by keys it generates, whose one target is trusted_root.json.
type fakeTUF struct {
	root  []byte            // the root metadata a client starts from
	files map[string][]byte // path → body, as the client asks for it
}

func newFakeTUF(t *testing.T, trustedRoot []byte) *fakeTUF {
	t.Helper()
	expires := time.Now().Add(24 * time.Hour)
	root := metadata.Root(expires)
	targets := metadata.Targets(expires)
	snapshot := metadata.Snapshot(expires)
	timestamp := metadata.Timestamp(expires)

	target, err := metadata.TargetFile().FromBytes("trusted_root.json", trustedRoot, "sha256")
	if err != nil {
		t.Fatal(err)
	}
	targets.Signed.Targets["trusted_root.json"] = target

	signers := map[string]signature.Signer{}
	for _, role := range []string{metadata.ROOT, metadata.TARGETS, metadata.SNAPSHOT, metadata.TIMESTAMP} {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key, err := metadata.KeyFromPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		if err := root.Signed.AddKey(key, role); err != nil {
			t.Fatal(err)
		}
		if signers[role], err = signature.LoadSigner(priv, crypto.Hash(0)); err != nil {
			t.Fatal(err)
		}
	}
	encode := func(sign func(signature.Signer) (*metadata.Signature, error), role string, toBytes func(bool) ([]byte, error)) []byte {
		if _, err := sign(signers[role]); err != nil {
			t.Fatal(err)
		}
		b, err := toBytes(false)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	f := &fakeTUF{files: map[string][]byte{}}
	f.root = encode(root.Sign, metadata.ROOT, root.ToBytes)
	f.files["/1.root.json"] = f.root
	f.files["/1.targets.json"] = encode(targets.Sign, metadata.TARGETS, targets.ToBytes)
	f.files["/1.snapshot.json"] = encode(snapshot.Sign, metadata.SNAPSHOT, snapshot.ToBytes)
	f.files["/timestamp.json"] = encode(timestamp.Sign, metadata.TIMESTAMP, timestamp.ToBytes)
	f.files["/targets/"+hex.EncodeToString(target.Hashes["sha256"])+".trusted_root.json"] = trustedRoot
	return f
}

// serve answers the client's requests from files; anything else (the next
// root version among them) is a 404, which ends TUF's walk.
func (f *fakeTUF) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := f.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// embeddedRootLine is one line of the embedded trusted roots: GitHub's
// instance or the other one.
func embeddedRootLine(t *testing.T, github bool) []byte {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(embeddedTrustedRoots), "\n") {
		anchors, err := parseTrustAnchors([][]byte{[]byte(line)})
		if err != nil {
			t.Fatal(err)
		}
		if anchors[0].github == github {
			return []byte(line)
		}
	}
	t.Fatalf("no embedded root with github=%v", github)
	return nil
}

// A trusted root read over TUF is a GitHub anchor that verifies GitHub's
// real release attestation (cli/cli's v2.96.0 checksums).
func TestRefreshGitHubAnchor(t *testing.T) {
	repo := newFakeTUF(t, embeddedRootLine(t, true))
	srv := repo.serve(t)
	anchor, err := refreshGitHubAnchor(srv.Client(), srv.URL, repo.root)
	if err != nil {
		t.Fatal(err)
	}
	if !anchor.github {
		t.Fatal("GitHub's trusted root read over TUF is not a GitHub anchor")
	}
	bundleJSON, digestHex, _ := loadSigstoreFixtures(t)
	id, err := verifyBundle(bundleJSON, digestHex, []trustAnchor{anchor})
	if err != nil || id.RepoSlug != "cli/cli" {
		t.Fatalf("the refreshed root verifies cli/cli's release attestation as %+v, %v", id, err)
	}
}

func TestRefreshGitHubAnchorRefusals(t *testing.T) {
	github := embeddedRootLine(t, true)
	cases := []struct {
		name  string
		setup func(t *testing.T) (repo *fakeTUF, startFrom []byte)
		want  string // in the error
	}{
		{"a trusted root changed after it was signed", func(t *testing.T) (*fakeTUF, []byte) {
			repo := newFakeTUF(t, github)
			for p := range repo.files {
				if strings.HasPrefix(p, "/targets/") {
					repo.files[p] = bytes.Replace(github, []byte("GitHub, Inc."), []byte("GitHub, Inx."), 1)
				}
			}
			return repo, repo.root
		}, "hash verification failed"},
		{"a repository signed by keys the client does not trust", func(t *testing.T) (*fakeTUF, []byte) {
			return newFakeTUF(t, github), newFakeTUF(t, github).root
		}, "not enough signatures"},
		{"a trusted root that is not GitHub's instance", func(t *testing.T) (*fakeTUF, []byte) {
			repo := newFakeTUF(t, embeddedRootLine(t, false))
			return repo, repo.root
		}, "not GitHub's instance"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, startFrom := c.setup(t)
			srv := repo.serve(t)
			_, err := refreshGitHubAnchor(srv.Client(), srv.URL, startFrom)
			t.Logf("refused: %v", err)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want one containing %q", err, c.want)
			}
		})
	}

	// Nothing answering at all.
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	if _, err := refreshGitHubAnchor(srv.Client(), srv.URL, newFakeTUF(t, github).root); err == nil {
		t.Fatal("a repository that is not there answered")
	}
}

// The embedded TUF root is TUF root metadata of a repository whose roles
// all have keys, and is the version its comment names.
func TestEmbeddedGitHubTUFRoot(t *testing.T) {
	root, err := metadata.Root().FromBytes(githubTUFRoot)
	if err != nil {
		t.Fatal(err)
	}
	if root.Signed.Version != 9 {
		t.Errorf("version = %d; the comment on githubTUFRoot names 9", root.Signed.Version)
	}
	for _, role := range []string{metadata.ROOT, metadata.TARGETS, metadata.SNAPSHOT, metadata.TIMESTAMP} {
		r, ok := root.Signed.Roles[role]
		if !ok || len(r.KeyIDs) == 0 || r.Threshold < 1 {
			t.Errorf("role %s: %+v", role, r)
		}
	}
	// TUF checks the root's own signatures when a client starts from it.
	if err := root.VerifyDelegate(metadata.ROOT, root); err != nil {
		t.Errorf("the embedded root is not signed by a threshold of its own root keys: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(githubTUFRoot, &raw); err != nil || raw["signed"] == nil {
		t.Errorf("not TUF metadata: %v", err)
	}
}

func TestWithFreshGitHubRoot(t *testing.T) {
	stale, fresh, other := newFakeSigstore(t), newFakeSigstore(t), newFakeSigstore(t)
	anchors := []trustAnchor{other.anchor(false), stale.anchor(true)}

	var notice bytes.Buffer
	got := withFreshGitHubRoot(anchors, func() (trustAnchor, error) { return fresh.anchor(true), nil }, &notice)
	if len(got) != 2 || got[0].material != fresh || !got[0].github || got[1].material != other {
		t.Errorf("anchors = %+v, want the fresh GitHub root in place of the stale one, the other kept", got)
	}
	if notice.Len() != 0 {
		t.Errorf("a refresh that worked printed %q", notice.String())
	}

	got = withFreshGitHubRoot(anchors, func() (trustAnchor, error) { return trustAnchor{}, errors.New("offline") }, &notice)
	if len(got) != 2 || got[1].material != stale {
		t.Errorf("anchors = %+v, want them unchanged when the refresh fails", got)
	}
	if !strings.Contains(notice.String(), "Could not read GitHub's current trusted root (offline)") {
		t.Errorf("notice = %q", notice.String())
	}

	if got := withFreshGitHubRoot(anchors, nil, &notice); len(got) != 2 || got[1].material != stale {
		t.Errorf("no refresh changed the anchors: %+v", got)
	}
}

// A release GitHub attests after its root moved on verifies with the root
// read fresh, and fails, closed, with only the CLI's own snapshot.
func TestAppDownloadUsesGitHubsCurrentRoot(t *testing.T) {
	r := newAppRelease(t, nil)
	client, _ := r.feed.serve(t)
	var out, notice bytes.Buffer
	env := r.env(t, client, t.TempDir(), &out)
	env.stderr = &notice
	env.anchors = []trustAnchor{newFakeSigstore(t).anchor(true)} // a root from before the move

	env.freshGitHubRoot = func() (trustAnchor, error) { return r.fs.anchor(true), nil }
	if err := runAppDownload([]string{"--current", "0.1.0"}, env); err != nil {
		t.Fatalf("with GitHub's current root: %v", err)
	}

	env.appSupport = t.TempDir()
	env.freshGitHubRoot = func() (trustAnchor, error) { return trustAnchor{}, errors.New("offline") }
	err := runAppDownload([]string{"--current", "0.1.0"}, env)
	if err == nil || !strings.Contains(err.Error(), "is not GitHub's release attestation") {
		t.Fatalf("error = %v, want the old root to refuse the release", err)
	}
	if !strings.Contains(notice.String(), "Could not read GitHub's current trusted root") {
		t.Errorf("stderr = %q, want the failed refresh named", notice.String())
	}
	if _, err := os.Stat(env.appSupport + "/updates/0.2.0/" + r.zip); err == nil {
		t.Error("a release the snapshot could not verify was staged")
	}
}
