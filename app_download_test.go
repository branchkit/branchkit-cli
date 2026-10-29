package main

// The app updater's download: the asset contract, the release identity
// policy, and the whole command run against a fake GitHub (release JSON,
// assets, the attestations API and its blob storage) serving releases that a
// test Sigstore instance attests. No network.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestReleaseContract(t *testing.T) {
	c, err := loadReleaseContract()
	if err != nil {
		t.Fatal(err)
	}
	if c.Repository != appReleaseRepo {
		t.Errorf("contract repository %q, identity policy %q: the two must name the same repository", c.Repository, appReleaseRepo)
	}
	cases := []struct{ goos, goarch, kind, want string }{
		{"darwin", "arm64", "", "BranchKit-0.2.0-macos-arm64.zip"},
		{"darwin", "arm64", "dmg", "BranchKit-0.2.0-macos-arm64.dmg"},
		{"windows", "amd64", "", "BranchKit-0.2.0-windows-x64.zip"},
		{"windows", "arm64", "", "BranchKit-0.2.0-windows-arm64.zip"},
		{"linux", "amd64", "", "BranchKit-0.2.0-x86_64.AppImage"},
		{"linux", "arm64", "deb", "branchkit_0.2.0_arm64.deb"},
		{"linux", "amd64", "rpm", "branchkit-0.2.0-1.x86_64.rpm"},
	}
	for _, tc := range cases {
		a, err := c.artifactFor(tc.goos, tc.goarch, tc.kind)
		if err != nil {
			t.Errorf("%s/%s %q: %v", tc.goos, tc.goarch, tc.kind, err)
			continue
		}
		if got, _ := a.fileName("0.2.0", tc.goarch); got != tc.want {
			t.Errorf("%s/%s %q = %s, want %s", tc.goos, tc.goarch, tc.kind, got, tc.want)
		}
	}
	for _, bad := range [][3]string{
		{"darwin", "amd64", ""}, // no Intel build
		{"darwin", "arm64", "deb"},
		{"freebsd", "amd64", ""},
	} {
		if _, err := c.artifactFor(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("artifactFor%v found a file", bad)
		}
	}
}

func TestReleaseContractRules(t *testing.T) {
	base := func() releaseContract {
		return releaseContract{Repository: "o/r", TagPrefix: "v", Checksums: "SUMS",
			Artifacts: []releaseArtifact{{OS: "linux", Kind: "appimage", Name: "B-{version}-{arch}.AppImage", Arch: map[string]string{"amd64": "x86_64"}, Use: []string{"update"}}}}
	}
	if c := base(); c.validate() != nil {
		t.Fatalf("base contract invalid: %v", c.validate())
	}
	breaks := map[string]func(*releaseContract){
		"no version in name": func(c *releaseContract) { c.Artifacts[0].Name = "B-{arch}" },
		"two update files": func(c *releaseContract) {
			c.Artifacts = append(c.Artifacts, releaseArtifact{OS: "linux", Kind: "deb", Name: "b_{version}_{arch}.deb", Arch: map[string]string{"amd64": "amd64"}, Use: []string{"update"}})
		},
		"no update file":   func(c *releaseContract) { c.Artifacts[0].Use = []string{"install"} },
		"unknown use":      func(c *releaseContract) { c.Artifacts[0].Use = []string{"update", "sideload"} },
		"name used twice":  func(c *releaseContract) { c.Artifacts[0].Arch["arm64"] = "x86_64" },
		"name is the sums": func(c *releaseContract) { c.Checksums = "B-0.0.0-x86_64.AppImage" },
		"no checksums":     func(c *releaseContract) { c.Checksums = "" },
	}
	for name, breakIt := range breaks {
		c := base()
		breakIt(&c)
		if c.validate() == nil {
			t.Errorf("%s: validate accepted it", name)
		}
	}
}

func TestParseSums(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	sums, err := parseSums([]byte(a + "  one.zip\n" + b + " *two.dmg\n"))
	if err != nil || sums["one.zip"] != a || sums["two.dmg"] != b || len(sums) != 2 {
		t.Fatalf("parseSums = %v, %v", sums, err)
	}
	for name, bad := range map[string]string{
		"short digest":   "abc  one.zip\n",
		"upper-case hex": strings.ToUpper(a) + "  one.zip\n",
		"a path":         a + "  dir/one.zip\n",
		"listed twice":   a + "  one.zip\n" + b + "  one.zip\n",
		"blank line":     a + "  one.zip\n\n",
		"CRLF":           a + "  one.zip\r\n",
	} {
		if _, err := parseSums([]byte(bad)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// testdata/release_SHA256SUMS.golden is what the release assembler writes
// for every file the contract names at 0.1.0, each digest the SHA-256 of the
// name and a newline. The assembler's own test compares its output with the
// same file, so the writer and this parser are held to one text.
func TestParseSumsReadsTheAssemblersFile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "release_SHA256SUMS.golden"))
	if err != nil {
		t.Fatal(err)
	}
	sums, err := parseSums(data)
	if err != nil {
		t.Fatal(err)
	}
	c, err := loadReleaseContract()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, a := range c.Artifacts {
		for goarch := range a.Arch {
			name, _ := a.fileName("0.1.0", goarch)
			want = append(want, name)
		}
	}
	if len(sums) != len(want) {
		t.Errorf("the golden file lists %d files, the contract names %d", len(sums), len(want))
	}
	for _, name := range want {
		d := sha256.Sum256([]byte(name + "\n"))
		if sums[name] != hex.EncodeToString(d[:]) {
			t.Errorf("%s: digest %q", name, sums[name])
		}
	}
	if !bytes.Equal(data, sumsOfDigests(sums)) {
		t.Errorf("the golden file is not sorted by name, one LF line per file:\n%s", data)
	}
}

// sumsOfDigests writes name → digest in sha256sum's text format, sorted.
func sumsOfDigests(sums map[string]string) []byte {
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", sums[n], n)
	}
	return b.Bytes()
}

// sumsOf is a SHA256SUMS over files, as the release assembler writes it.
func sumsOf(files map[string][]byte) []byte {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	for _, n := range names {
		buf.WriteString(sha256HexBytes(files[n]) + "  " + n + "\n")
	}
	return buf.Bytes()
}

// verifyAppSums is verifyAppAttestation over a checksums file's bytes.
func verifyAppSums(bundleJSON, sums []byte, tag string, anchors []trustAnchor) (*appSigner, error) {
	digest := sha256.Sum256(sums)
	return verifyAppAttestation(bundleJSON, digest[:], tag, anchors)
}

// attestSums signs a statement over sums as id with predicate (nil: a
// provenance-shaped one) and returns the bundle.
func attestSums(t *testing.T, fs *fakeSigstore, id fakeIdentity, sums []byte, predicate map[string]any) []byte {
	t.Helper()
	predicateType := releasePredicateType
	if predicate == nil {
		predicateType, predicate = "https://slsa.dev/provenance/v1", map[string]any{}
	}
	return fs.attest(t, id, statement("SHA256SUMS", sha256HexBytes(sums), predicateType, predicate))
}

func TestVerifyAppSums(t *testing.T) {
	fs := newFakeSigstore(t)
	sums := sumsOf(map[string][]byte{"BranchKit-0.2.0-macos-arm64.zip": []byte("app")})
	const tag = "v0.2.0"
	release := releasePredicate(appReleaseRepo, tag)
	cases := []struct {
		name      string
		id        fakeIdentity
		predicate map[string]any
		github    bool // the test instance stands in for GitHub's own
		ok        bool
	}{
		{"GitHub's release signer for the repo and tag", signerOnly, release, true, true},

		{"the release signer off GitHub's instance", signerOnly, release, false, false},
		{"the release signer for a fork", signerOnly, releasePredicate("mallory/releases", tag), true, false},
		{"the release signer for another tag", signerOnly, releasePredicate(appReleaseRepo, "v0.1.0"), true, false},
		{"the release signer with no release predicate", signerOnly, nil, true, false},
		{"an email identity whose predicate names the repo", emailIdentity, release, true, false},
		// Not a shape the release workflow uses (an immutable release is
		// attested by GitHub), so not accepted either.
		{"a workflow of the releases repo on the tag", actionsIdentity(appReleaseRepo, "release.yml@refs/tags/"+tag), release, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bundle := attestSums(t, fs, c.id, sums, c.predicate)
			signer, err := verifyAppSums(bundle, sums, tag, []trustAnchor{fs.anchor(c.github)})
			if !c.ok {
				if err == nil {
					t.Fatalf("accepted as %+v", signer)
				}
				return
			}
			if err != nil {
				t.Fatalf("verifyAppSums: %v", err)
			}
			if signer.Signer != "release-signer" || signer.Repository != appReleaseRepo || signer.SAN != releaseSignerSAN {
				t.Errorf("signer = %+v", signer)
			}
		})
	}

	t.Run("checksums changed after signing", func(t *testing.T) {
		bundle := attestSums(t, fs, signerOnly, sums, release)
		tampered := append([]byte(strings.Repeat("0", 64)+"  evil.zip\n"), sums...)
		if _, err := verifyAppSums(bundle, tampered, tag, []trustAnchor{fs.anchor(true)}); err == nil {
			t.Fatal("verified checksums the bundle never signed")
		}
	})

	t.Run("the real roots refuse the test instance", func(t *testing.T) {
		bundle := attestSums(t, fs, signerOnly, sums, release)
		if _, err := verifyAppSums(bundle, sums, tag, embeddedAnchors(t)); err == nil {
			t.Fatal("a certificate from an untrusted CA verified")
		}
	})
}

// A real certificate of GitHub's release signer (cli/cli's v2.96.0
// checksums) passes the release-signer identity on the embedded GitHub root:
// what stops it is only its predicate, which names cli/cli. So the identity
// policy matches the signer as GitHub really issues it (no OIDC issuer, the
// GitHub instance's root), not only as the test instance draws it.
func TestVerifyAppAttestationRealCertificates(t *testing.T) {
	bundleJSON, digestHex, _ := loadSigstoreFixtures(t)
	digest, _ := hex.DecodeString(digestHex)
	_, err := verifyAppAttestation(bundleJSON, digest, "v2.96.0", embeddedAnchors(t))
	if err == nil || !strings.Contains(err.Error(), "is for cli/cli v2.96.0, not "+appReleaseRepo) {
		t.Fatalf("error = %v, want the predicate to be what refuses it", err)
	}

	// A real Actions certificate (the announcements plugin's release
	// workflow) is not the release signer. It was issued through Public
	// Good, which verifyAppAttestation never reads, so on the embedded roots
	// it fails before any identity is looked at. Handed Public Good as if it
	// were GitHub's instance, it verifies, and the identity policy is what
	// refuses it.
	public, _ := os.ReadFile("testdata/sigstore/public_bundle.jsonl")
	publicDigest, _ := os.ReadFile("testdata/sigstore/public_artifact.sha256")
	d, _ := hex.DecodeString(strings.Fields(string(publicDigest))[0])
	var publicGood []trustAnchor
	for _, a := range embeddedAnchors(t) {
		if !a.github {
			a.github = true
			publicGood = append(publicGood, a)
		}
	}
	_, err = verifyAppAttestation(bytes.TrimSpace(public), d, "v0.2.0-rc.1", publicGood)
	if err == nil || !strings.Contains(err.Error(), "is not GitHub's release attestation") || !strings.Contains(err.Error(), releaseSignerSAN) {
		t.Fatalf("error = %v, want the release-signer identity to refuse it", err)
	}
	if _, err := verifyAppAttestation(bytes.TrimSpace(public), d, "v0.2.0-rc.1", embeddedAnchors(t)); err == nil {
		t.Fatal("an Actions bundle passed the app's policy on the embedded roots")
	}
}

// fakeAttestation is one entry of the attestations API's answer.
type fakeAttestation struct {
	Bundle    json.RawMessage `json:"bundle,omitempty"`
	BundleURL string          `json:"bundle_url,omitempty"`
}

// fakeGitHubRelease is what the fake GitHub serves: one release of
// branchkit/releases, its assets, the attestations the API lists per digest,
// and the blobs a bundle_url points at.
type fakeGitHubRelease struct {
	tag    string
	files  map[string][]byte
	attest map[string][]fakeAttestation // sha256 hex → the API's list
	blobs  map[string][]byte            // path on the blob host → body
	stall  string                       // an asset that sends 4 bytes, then nothing
}

const fakeBlobHost = "https://tmaproduction.blob.core.windows.net"

func (f *fakeGitHubRelease) serve(t *testing.T) (*http.Client, *[]string) {
	t.Helper()
	setGitHubToken(t, "")
	var requested []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		api := "/repos/" + appReleaseRepo + "/"
		switch {
		case r.URL.Path == api+"releases/latest" || r.URL.Path == api+"releases/tags/"+f.tag:
			type asset struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			}
			var assets []asset
			for name := range f.files {
				assets = append(assets, asset{name, "https://github.com/" + appReleaseRepo + "/releases/download/" + f.tag + "/" + name})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": f.tag, "assets": assets, "immutable": true})
		case strings.HasPrefix(r.URL.Path, api+"attestations/sha256:"):
			list, ok := f.attest[strings.TrimPrefix(r.URL.Path, api+"attestations/sha256:")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"attestations": list})
		case strings.HasPrefix(r.URL.Path, "/"+appReleaseRepo+"/releases/download/"+f.tag+"/"):
			name := filepath.Base(r.URL.Path)
			data, ok := f.files[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if name == f.stall {
				w.Write(data[:4])
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				return
			}
			w.Write(data)
		default:
			if data, ok := f.blobs[r.URL.Path]; ok {
				w.Header().Set("Content-Type", "application/x-snappy")
				w.Write(data)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return asGitHub(srv), &requested
}

// publish lists bundle as the attestation of sums, the way GitHub answers
// today: no inline bundle, a bundle_url to a Snappy blob.
func (f *fakeGitHubRelease) publish(sums, bundle []byte) {
	path := fmt.Sprintf("/attestations/%d.json.sn", len(f.blobs)+1)
	f.blobs[path] = snappyLiterals(bundle)
	digest := sha256HexBytes(sums)
	f.attest[digest] = append(f.attest[digest], fakeAttestation{BundleURL: fakeBlobHost + path})
}

// appRelease is a v0.2.0 release with every contract file, attested by
// GitHub's release signer, after whatever mutate changes.
type appRelease struct {
	fs     *fakeSigstore
	feed   *fakeGitHubRelease
	zip    string
	zipped []byte
}

func newAppRelease(t *testing.T, mutate func(files, signed map[string][]byte)) *appRelease {
	t.Helper()
	r := &appRelease{
		fs:     newFakeSigstore(t),
		zip:    "BranchKit-0.2.0-macos-arm64.zip",
		zipped: []byte("BranchKit.app, zipped"),
	}
	signed := map[string][]byte{
		r.zip:                               r.zipped,
		"BranchKit-0.2.0-macos-arm64.dmg":   []byte("disk image"),
		"BranchKit-0.2.0-windows-x64.zip":   []byte("windows"),
		"BranchKit-0.2.0-windows-arm64.zip": []byte("windows arm"),
		"BranchKit-0.2.0-x86_64.AppImage":   []byte("appimage"),
		"branchkit_0.2.0_amd64.deb":         []byte("deb"),
		"branchkit-0.2.0-1.x86_64.rpm":      []byte("rpm"),
	}
	r.feed = &fakeGitHubRelease{tag: "v0.2.0", files: map[string][]byte{},
		attest: map[string][]fakeAttestation{}, blobs: map[string][]byte{}}
	for n, d := range signed {
		r.feed.files[n] = d
	}
	if mutate != nil {
		mutate(r.feed.files, signed)
	}
	sums := sumsOf(signed)
	r.feed.files["SHA256SUMS"] = sums
	r.feed.publish(sums, attestSums(t, r.fs, signerOnly, sums, releasePredicate(appReleaseRepo, "v0.2.0")))
	return r
}

func (r *appRelease) sums() []byte { return r.feed.files["SHA256SUMS"] }

// env is a Mac on Apple silicon talking to the fake GitHub, with the test
// instance as GitHub's Sigstore.
func (r *appRelease) env(t *testing.T, client *http.Client, support string, out io.Writer) appEnv {
	return appEnv{
		client:     client,
		anchors:    []trustAnchor{r.fs.anchor(true)},
		appSupport: support,
		goos:       "darwin",
		goarch:     "arm64",
		getenv:     func(string) string { return "" },
		stdout:     out,
		stderr:     io.Discard,
	}
}

// run runs `app download args...` and returns the app-support folder, what
// it printed and its error.
func (r *appRelease) run(t *testing.T, args ...string) (string, appDownloadResult, error) {
	t.Helper()
	client, _ := r.feed.serve(t)
	support := t.TempDir()
	var out bytes.Buffer
	err := runAppDownload(args, r.env(t, client, support, &out))
	var res appDownloadResult
	if err == nil {
		if jerr := json.Unmarshal(out.Bytes(), &res); jerr != nil {
			t.Fatalf("output is not JSON: %v\n%s", jerr, out.String())
		}
	}
	return support, res, err
}

func TestAppDownloadStagesAVerifiedUpdate(t *testing.T) {
	r := newAppRelease(t, nil)
	support, res, err := r.run(t, "--current", "v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(support, "updates", "0.2.0")
	want := appDownloadResult{
		Version: "0.2.0", Tag: "v0.2.0", Kind: "zip",
		Path:   filepath.Join(dir, r.zip),
		SHA256: sha256HexBytes(r.zipped),
	}
	if res.Version != want.Version || res.Tag != want.Tag || res.Kind != want.Kind || res.Path != want.Path || res.SHA256 != want.SHA256 {
		t.Errorf("result = %+v, want %+v", res, want)
	}
	if res.Identity == nil || res.Identity.Signer != "release-signer" || res.Identity.Repository != appReleaseRepo {
		t.Errorf("identity = %+v", res.Identity)
	}
	if got, err := os.ReadFile(res.Path); err != nil || !bytes.Equal(got, r.zipped) {
		t.Errorf("staged file = %q, %v", got, err)
	}

	// Only updates/ was written, owner-only, holding the file and the two
	// signed records it was checked against.
	top, _ := os.ReadDir(support)
	if len(top) != 1 || top[0].Name() != "updates" {
		t.Errorf("app support holds %v, want only updates/", top)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "BranchKit-0.2.0-macos-arm64.zip SHA256SUMS SHA256SUMS.sigstore.json" {
		t.Errorf("updates/0.2.0 holds %v", names)
	}
	if kept, _ := os.ReadFile(filepath.Join(dir, "SHA256SUMS.sigstore.json")); !bytes.HasPrefix(kept, []byte(`{"mediaType"`)) {
		t.Errorf("the kept attestation is not the decoded bundle: %.40q", kept)
	}
	if runtime.GOOS != "windows" {
		for _, d := range []string{filepath.Dir(dir), dir} {
			if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
				t.Errorf("%s mode = %v, %v; want 0700", d, fi.Mode().Perm(), err)
			}
		}
	}
}

// The attestations API may also carry a bundle inline, and may list other
// attestations of the same file first; the release attestation is found
// among them.
func TestAppDownloadFindsTheReleaseAttestationAmongOthers(t *testing.T) {
	r := newAppRelease(t, nil)
	sums := r.sums()
	digest := sha256HexBytes(sums)
	release := r.feed.attest[digest]
	other := attestSums(t, r.fs, emailIdentity, sums, releasePredicate(appReleaseRepo, "v0.2.0"))
	inline := attestSums(t, r.fs, signerOnly, sums, releasePredicate(appReleaseRepo, "v0.2.0"))
	r.feed.attest[digest] = []fakeAttestation{{Bundle: other}, {}, {Bundle: inline}}
	if _, res, err := r.run(t, "--current", "0.1.0"); err != nil || res.Identity == nil {
		t.Fatalf("inline: %v", err)
	}
	r.feed.attest[digest] = append([]fakeAttestation{{Bundle: other}}, release...)
	if _, _, err := r.run(t, "--current", "0.1.0"); err != nil {
		t.Fatalf("bundle_url after another: %v", err)
	}
}

func TestAppDownloadRefusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(files, signed map[string][]byte)
		after  func(r *appRelease) // after signing
		args   []string
		want   string // in the error
	}{
		{"no attestation", nil, func(r *appRelease) { r.feed.attest = map[string][]fakeAttestation{} },
			[]string{"--current", "0.1.0"}, "holds no attestation"},
		{"an empty attestation list", nil, func(r *appRelease) {
			r.feed.attest[sha256HexBytes(r.sums())] = []fakeAttestation{}
		}, []string{"--current", "0.1.0"}, "holds no attestation"},
		{"only someone else's attestation", nil, func(r *appRelease) {
			r.feed.attest = map[string][]fakeAttestation{}
			r.feed.publish(r.sums(), attestSums(t, r.fs, emailIdentity, r.sums(), releasePredicate(appReleaseRepo, "v0.2.0")))
		}, []string{"--current", "0.1.0"}, "is its release attestation"},
		{"a bundle blob that is not Snappy", nil, func(r *appRelease) {
			for p := range r.feed.blobs {
				r.feed.blobs[p] = []byte("{not snappy")
			}
		}, []string{"--current", "0.1.0"}, "snappy"},
		{"a bundle_url that is not https", nil, func(r *appRelease) {
			r.feed.attest[sha256HexBytes(r.sums())] = []fakeAttestation{{BundleURL: "http://tmaproduction.blob.core.windows.net/x.json.sn"}}
		}, []string{"--current", "0.1.0"}, "not https"},
		{"no checksums file", nil, func(r *appRelease) { delete(r.feed.files, "SHA256SUMS") },
			[]string{"--current", "0.1.0"}, "publishes no checksums file"},
		{"the file is missing from the checksums", func(files, signed map[string][]byte) {
			delete(signed, "BranchKit-0.2.0-macos-arm64.zip")
		}, nil, []string{"--current", "0.1.0"}, "does not list"},
		{"the file is not the one the checksums name", func(files, _ map[string][]byte) {
			files["BranchKit-0.2.0-macos-arm64.zip"] = []byte("swapped after signing")
		}, nil, []string{"--current", "0.1.0"}, "does not match"},
		{"the release is older", nil, nil, []string{"--current", "0.3.0"}, "not newer"},
		{"the release is the installed one", nil, nil, []string{"--current", "v0.2.0"}, "not newer"},
		{"the installed version is unknown", nil, nil, nil, "installed version is unknown"},
		{"no such file for the kind", nil, nil, []string{"--current", "0.1.0", "--kind", "deb"}, "no \"deb\" file"},
		{"a version that is not X.Y.Z", nil, nil, []string{"--version", "0.2", "--current", "0.1.0"}, "not X.Y.Z"},
		{"an empty version", nil, nil, []string{"--version=", "--current", "0.1.0"}, "needs a value"},
		{"an unknown flag", nil, nil, []string{"--current", "0.1.0", "--force"}, "unknown argument"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newAppRelease(t, c.mutate)
			if c.after != nil {
				c.after(r)
			}
			support, _, err := r.run(t, c.args...)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want one containing %q", err, c.want)
			}
			// Nothing an installer could pick up, and no partial download.
			matches, _ := filepath.Glob(filepath.Join(support, "updates", "*", "*.zip*"))
			if len(matches) != 0 {
				t.Errorf("left behind %v", matches)
			}
		})
	}
}

// A partial download a killed run left behind, in any version's folder, is
// gone after the next run; the staged file is untouched by that sweep.
func TestAppDownloadRemovesPartialDownloads(t *testing.T) {
	r := newAppRelease(t, nil)
	client, _ := r.feed.serve(t)
	support := t.TempDir()
	stale := []string{
		filepath.Join(support, "updates", "0.1.0", ".BranchKit-0.1.0-macos-arm64.zip.111.part"),
		filepath.Join(support, "updates", "0.2.0", ".BranchKit-0.2.0-macos-arm64.zip.222.part"),
	}
	for _, p := range stale {
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte("half"), 0o600)
	}
	if err := runAppDownload([]string{"--current", "0.1.0"}, r.env(t, client, support, io.Discard)); err != nil {
		t.Fatal(err)
	}
	for _, p := range stale {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived: %v", p, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(support, "updates", "0.2.0", r.zip)); !bytes.Equal(got, r.zipped) {
		t.Errorf("staged file = %q", got)
	}
}

// A download that stops sending fails after the stall limit, however long it
// has run, and leaves nothing behind. (A slow one that keeps sending is
// bounded by nothing: there is no total timeout to test.)
func TestAppDownloadStallFails(t *testing.T) {
	r := newAppRelease(t, nil)
	r.feed.stall = r.zip
	client, _ := r.feed.serve(t)
	support := t.TempDir()
	env := r.env(t, client, support, io.Discard)
	env.stall = 200 * time.Millisecond
	err := runAppDownload([]string{"--current", "0.1.0"}, env)
	if err == nil || !strings.Contains(err.Error(), "nothing arrived") {
		t.Fatalf("error = %v, want the stall named", err)
	}
	left, _ := filepath.Glob(filepath.Join(support, "updates", "*", "*"))
	for _, p := range left {
		if strings.Contains(filepath.Base(p), r.zip) {
			t.Errorf("left behind %s", p)
		}
	}
}

func TestAppDownloadAllowDowngrade(t *testing.T) {
	r := newAppRelease(t, nil)
	_, res, err := r.run(t, "--version", "v0.2.0", "--current", "0.3.0", "--allow-downgrade")
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "0.2.0" {
		t.Errorf("version = %s", res.Version)
	}
}

func TestAppDownloadFetchesTheAskedTag(t *testing.T) {
	r := newAppRelease(t, nil)
	client, requested := r.feed.serve(t)
	if err := runAppDownload([]string{"--version=0.2.0", "--current=0.1.0"}, r.env(t, client, t.TempDir(), io.Discard)); err != nil {
		t.Fatal(err)
	}
	if len(*requested) == 0 || (*requested)[0] != "/repos/branchkit/releases/releases/tags/v0.2.0" {
		t.Errorf("first request %v, want the release by tag", *requested)
	}
}

func TestAppDownloadReadsTheVersionFromTheEnvironment(t *testing.T) {
	r := newAppRelease(t, nil)
	client, _ := r.feed.serve(t)
	env := r.env(t, client, t.TempDir(), io.Discard)
	env.getenv = func(k string) string {
		if k == "BRANCHKIT_APP_VERSION" {
			return "0.3.0"
		}
		return ""
	}
	if err := runAppDownload(nil, env); err == nil || !strings.Contains(err.Error(), "not newer") {
		t.Fatalf("error = %v, want the installed 0.3.0 to refuse 0.2.0", err)
	}
}
