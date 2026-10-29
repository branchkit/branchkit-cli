package main

// The app updater's download: the asset contract, the release identity
// policy, and the whole command run against a fake GitHub serving releases
// signed by a test Sigstore instance. No network.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
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
		return releaseContract{Repository: "o/r", TagPrefix: "v", Checksums: "SUMS", ChecksumsAttestation: "SUMS.sigstore.json",
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

// signedSums is a SHA256SUMS over files and its bundle, signed as id with
// predicate (nil: a provenance-shaped statement).
func signedSums(t *testing.T, fs *fakeSigstore, id fakeIdentity, files map[string][]byte, predicate map[string]any) (sums, bundle []byte) {
	t.Helper()
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	for _, n := range names {
		buf.WriteString(sha256HexBytes(files[n]) + "  " + n + "\n")
	}
	sums = buf.Bytes()
	predicateType := releasePredicateType
	if predicate == nil {
		predicateType, predicate = "https://slsa.dev/provenance/v1", map[string]any{}
	}
	return sums, fs.attest(t, id, statement("SHA256SUMS", sha256HexBytes(sums), predicateType, predicate))
}

func TestVerifyAppSums(t *testing.T) {
	fs := newFakeSigstore(t)
	files := map[string][]byte{"BranchKit-0.2.0-macos-arm64.zip": []byte("app")}
	const tag = "v0.2.0"
	workflow := actionsIdentity(appReleaseRepo, "release.yml@refs/tags/"+tag)
	cases := []struct {
		name      string
		id        fakeIdentity
		predicate map[string]any
		github    bool // the test instance stands in for GitHub's own
		want      string
	}{
		{"the releases repo's workflow on the tag", workflow, nil, false, "workflow"},
		{"...on GitHub's instance too", workflow, nil, true, "workflow"},
		{"GitHub's release signer for the repo and tag", signerOnly, releasePredicate(appReleaseRepo, tag), true, "release-signer"},

		{"the release signer off GitHub's instance", signerOnly, releasePredicate(appReleaseRepo, tag), false, ""},
		{"the release signer for a fork", signerOnly, releasePredicate("mallory/releases", tag), true, ""},
		{"the release signer for another tag", signerOnly, releasePredicate(appReleaseRepo, "v0.1.0"), true, ""},
		{"a fork's workflow", actionsIdentity("mallory/releases", "release.yml@refs/tags/"+tag), nil, true, ""},
		{"the workflow on a branch", actionsIdentity(appReleaseRepo, "release.yml@refs/heads/main"), nil, false, ""},
		{"the workflow on another tag", actionsIdentity(appReleaseRepo, "release.yml@refs/tags/v0.1.0"), nil, false, ""},
		{"the workflow's SAN from another issuer",
			fakeIdentity{SAN: workflow.SAN, Issuer: "https://oidc.ci.example", SourceRepo: workflow.SourceRepo}, nil, false, ""},
		{"the workflow's SAN with another source repo",
			fakeIdentity{SAN: workflow.SAN, Issuer: actionsIssuer, SourceRepo: "https://github.com/mallory/releases"}, nil, false, ""},
		{"an email identity whose predicate names the repo", emailIdentity, releasePredicate(appReleaseRepo, tag), true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sums, bundle := signedSums(t, fs, c.id, files, c.predicate)
			signer, err := verifyAppSums(bundle, sums, tag, []trustAnchor{fs.anchor(c.github)})
			if c.want == "" {
				if err == nil {
					t.Fatalf("accepted as %+v", signer)
				}
				return
			}
			if err != nil {
				t.Fatalf("verifyAppSums: %v", err)
			}
			if signer.Signer != c.want || signer.Repository != appReleaseRepo {
				t.Errorf("signer = %+v, want %s for %s", signer, c.want, appReleaseRepo)
			}
		})
	}

	t.Run("checksums changed after signing", func(t *testing.T) {
		sums, bundle := signedSums(t, fs, workflow, files, nil)
		tampered := append([]byte(strings.Repeat("0", 64)+"  evil.zip\n"), sums...)
		if _, err := verifyAppSums(bundle, tampered, tag, []trustAnchor{fs.anchor(false)}); err == nil {
			t.Fatal("verified checksums the bundle never signed")
		}
	})

	t.Run("the real roots refuse the test instance", func(t *testing.T) {
		sums, bundle := signedSums(t, fs, workflow, files, nil)
		if _, err := verifyAppSums(bundle, sums, tag, embeddedAnchors(t)); err == nil {
			t.Fatal("a certificate from an untrusted CA verified")
		}
	})
}

// fakeReleases serves one release of branchkit/releases the way GitHub
// does: the API's release JSON and the asset downloads.
func fakeReleases(t *testing.T, tag string, files map[string][]byte) (*http.Client, *[]string) {
	t.Helper()
	setGitHubToken(t, "")
	var requested []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		api := "/repos/" + appReleaseRepo + "/releases/"
		switch {
		case r.URL.Path == api+"latest" || r.URL.Path == api+"tags/"+tag:
			type asset struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			}
			var assets []asset
			for name := range files {
				assets = append(assets, asset{name, "https://github.com/" + appReleaseRepo + "/releases/download/" + tag + "/" + name})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "assets": assets})
		case strings.HasPrefix(r.URL.Path, "/"+appReleaseRepo+"/releases/download/"+tag+"/"):
			data, ok := files[filepath.Base(r.URL.Path)]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write(data)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return asGitHub(srv), &requested
}

// appRelease is a v0.2.0 release with a macOS update zip, signed by the
// releases repo's workflow, plus whatever mutate changes.
type appRelease struct {
	fs     *fakeSigstore
	files  map[string][]byte
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
		"BranchKit-0.2.0-x86_64.AppImage":   []byte("appimage"),
		"branchkit_0.2.0_amd64.deb":         []byte("deb"),
		"branchkit-0.2.0-1.x86_64.rpm":      []byte("rpm"),
		"BranchKit-0.2.0-windows-arm64.zip": []byte("windows arm"),
	}
	r.files = map[string][]byte{}
	for n, d := range signed {
		r.files[n] = d
	}
	if mutate != nil {
		mutate(r.files, signed)
	}
	sums, bundle := signedSums(t, r.fs, actionsIdentity(appReleaseRepo, "release.yml@refs/tags/v0.2.0"), signed, nil)
	r.files["SHA256SUMS"] = sums
	r.files["SHA256SUMS.sigstore.json"] = bundle
	return r
}

// run runs `app download args...` as a Mac on Apple silicon would, and
// returns the app-support folder, what it printed and its error.
func (r *appRelease) run(t *testing.T, args ...string) (string, appDownloadResult, error) {
	t.Helper()
	client, _ := fakeReleases(t, "v0.2.0", r.files)
	support := t.TempDir()
	var out bytes.Buffer
	err := runAppDownload(args, appEnv{
		client:     client,
		anchors:    []trustAnchor{r.fs.anchor(false)},
		appSupport: support,
		goos:       "darwin",
		goarch:     "arm64",
		getenv:     func(string) string { return "" },
		stdout:     &out,
		stderr:     io.Discard,
	})
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
	if res.Identity == nil || res.Identity.Signer != "workflow" || res.Identity.Repository != appReleaseRepo ||
		!strings.HasPrefix(res.Identity.SAN, appWorkflowSANPrefix) {
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
	if runtime.GOOS != "windows" {
		for _, d := range []string{filepath.Dir(dir), dir} {
			if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
				t.Errorf("%s mode = %v, %v; want 0700", d, fi.Mode().Perm(), err)
			}
		}
	}
}

func TestAppDownloadRefusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(files, signed map[string][]byte)
		args   []string
		want   string // in the error
	}{
		{"no attestation", nil, []string{"--current", "0.1.0"}, "publishes no attestation"},
		{"the file is missing from the checksums", func(files, signed map[string][]byte) {
			delete(signed, "BranchKit-0.2.0-macos-arm64.zip")
		}, []string{"--current", "0.1.0"}, "does not list"},
		{"the file is not the one the checksums name", func(files, _ map[string][]byte) {
			files["BranchKit-0.2.0-macos-arm64.zip"] = []byte("swapped after signing")
		}, []string{"--current", "0.1.0"}, "does not match"},
		{"the release is older", nil, []string{"--current", "0.3.0"}, "not newer"},
		{"the release is the installed one", nil, []string{"--current", "v0.2.0"}, "not newer"},
		{"the installed version is unknown", nil, nil, "installed version is unknown"},
		{"no such file for the kind", nil, []string{"--current", "0.1.0", "--kind", "deb"}, "no \"deb\" file"},
		{"a version that is not X.Y.Z", nil, []string{"--version", "0.2", "--current", "0.1.0"}, "not X.Y.Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newAppRelease(t, c.mutate)
			if c.name == "no attestation" {
				delete(r.files, "SHA256SUMS.sigstore.json") // newAppRelease always signs
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
	client, requested := fakeReleases(t, "v0.2.0", r.files)
	err := runAppDownload([]string{"--version=0.2.0", "--current=0.1.0"}, appEnv{
		client: client, anchors: []trustAnchor{r.fs.anchor(false)}, appSupport: t.TempDir(),
		goos: "darwin", goarch: "arm64", getenv: func(string) string { return "" },
		stdout: io.Discard, stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(*requested) == 0 || (*requested)[0] != "/repos/branchkit/releases/releases/tags/v0.2.0" {
		t.Errorf("first request %v, want the release by tag", *requested)
	}
}

func TestAppDownloadReadsTheVersionFromTheEnvironment(t *testing.T) {
	r := newAppRelease(t, nil)
	client, _ := fakeReleases(t, "v0.2.0", r.files)
	err := runAppDownload(nil, appEnv{
		client: client, anchors: []trustAnchor{r.fs.anchor(false)}, appSupport: t.TempDir(),
		goos: "darwin", goarch: "arm64",
		getenv: func(k string) string {
			if k == "BRANCHKIT_APP_VERSION" {
				return "0.3.0"
			}
			return ""
		},
		stdout: io.Discard, stderr: io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "not newer") {
		t.Fatalf("error = %v, want the installed 0.3.0 to refuse 0.2.0", err)
	}
}
