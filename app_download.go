package main

// `branchkit-cli app download`: fetch one BranchKit release file for this
// machine and prove it is the release's, before anything could install it.
//
// Order matters. The checksums file and GitHub's attestation of it are
// fetched and verified first, so a release GitHub did not attest for the
// releases repository costs a few small downloads and stages nothing. Only
// then is the large file downloaded, to a temporary name, and it takes its
// real name only once its digest matches its line in the verified
// checksums. The file name comes from the contract (release_assets.json)
// and carries the version, so a checksums file from another release cannot
// vouch for it.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// A checksums file lists a handful of files; an attestation bundle is a
	// few kilobytes, more for a release predicate naming every asset.
	maxSumsBytes   = 1 << 20
	maxBundleBytes = 4 << 20
	// A release's API answer lists its assets; an attestations answer may
	// carry its bundles inline.
	maxReleaseJSONBytes = 4 << 20
	maxAttestationsJSON = 16 << 20
	// How many attestations of one digest are read, at most.
	maxAttestations = 30
	// The largest release file the updater will stage.
	maxAppArtifactBytes = 4 << 30
)

// appDownloadResult is what `app download` prints.
type appDownloadResult struct {
	Version  string     `json:"version"`
	Tag      string     `json:"tag"`
	Kind     string     `json:"kind"`
	Path     string     `json:"path"`
	SHA256   string     `json:"sha256"`
	Identity *appSigner `json:"identity"`
}

type appDownloadOptions struct {
	version        string // X.Y.Z, or "" for the latest release
	current        string // X.Y.Z, or "" when unknown
	kind           string // "" = this OS's update file
	allowDowngrade bool
}

func parseAppDownloadArgs(args []string, env appEnv) (appDownloadOptions, error) {
	var o appDownloadOptions
	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(args[i], "=")
		switch name {
		case "--allow-downgrade":
			if hasValue {
				return o, fmt.Errorf("--allow-downgrade takes no value")
			}
			o.allowDowngrade = true
			continue
		case "--version", "--current", "--kind":
		default:
			return o, fmt.Errorf("unknown argument %q", args[i])
		}
		if !hasValue {
			if i+1 >= len(args) {
				return o, fmt.Errorf("%s needs a value", name)
			}
			i++
			value = args[i]
		}
		if value == "" {
			return o, fmt.Errorf("%s needs a value", name)
		}
		switch name {
		case "--version":
			o.version = value
		case "--current":
			o.current = value
		case "--kind":
			o.kind = value
		}
	}
	if o.current == "" {
		o.current = env.getenv("BRANCHKIT_APP_VERSION")
	}
	for _, v := range []*string{&o.version, &o.current} {
		if *v == "" {
			continue
		}
		*v = strings.TrimPrefix(*v, "v")
		if !appVersionPattern.MatchString(*v) {
			return o, fmt.Errorf("version %q is not X.Y.Z", *v)
		}
	}
	return o, nil
}

func runAppDownload(args []string, env appEnv) error {
	opts, err := parseAppDownloadArgs(args, env)
	if err != nil {
		return err
	}
	contract, err := loadReleaseContract()
	if err != nil {
		return err
	}
	artifact, err := contract.artifactFor(env.goos, env.goarch, opts.kind)
	if err != nil {
		return err
	}
	if opts.current == "" && !opts.allowDowngrade {
		return fmt.Errorf("the installed version is unknown: pass --current (or set BRANCHKIT_APP_VERSION), or --allow-downgrade")
	}

	release, err := fetchAppRelease(env, contract, opts.version)
	if err != nil {
		return err
	}
	version := strings.TrimPrefix(release.TagName, contract.TagPrefix)
	if release.TagName != contract.tag(version) || !appVersionPattern.MatchString(version) {
		return fmt.Errorf("release tag %q is not %sX.Y.Z", release.TagName, contract.TagPrefix)
	}
	if opts.version != "" && version != opts.version {
		return fmt.Errorf("asked for %s, GitHub answered with %s", contract.tag(opts.version), release.TagName)
	}
	if err := checkNewer(version, opts); err != nil {
		return err
	}
	fileName, _ := artifact.fileName(version, env.goarch)

	assets := map[string]ghAsset{}
	for _, a := range release.Assets {
		assets[a.Name] = a
	}
	need := func(name, what string) (ghAsset, error) {
		a, ok := assets[name]
		if !ok || a.BrowserDownloadURL == "" {
			return ghAsset{}, fmt.Errorf("release %s publishes no %s (%s)", release.TagName, what, name)
		}
		return a, nil
	}

	// 1. The checksums and GitHub's attestation of them. No attestation, or
	//    none that verifies under the app's identity policy, ends it here.
	sumsAsset, err := need(contract.Checksums, "checksums file")
	if err != nil {
		return err
	}
	if _, err := need(fileName, "file for this machine"); err != nil {
		return err
	}
	fmt.Fprintf(env.stderr, "Verifying %s %s...\n", contract.Repository, release.TagName)
	if env.anchors, err = withFreshGitHubRoot(env.anchors, env.freshGitHubRoot); err != nil {
		return err
	}
	sums, err := appFetch(env, sumsAsset, maxSumsBytes)
	if err != nil {
		return err
	}
	sumsDigest := sha256.Sum256(sums)
	signer, bundleJSON, err := releaseAttestation(env, contract.Repository, sumsDigest[:], release.TagName)
	if err != nil {
		return err
	}
	listed, err := parseSums(sums)
	if err != nil {
		return err
	}
	want, ok := listed[fileName]
	if !ok {
		return fmt.Errorf("%s does not list %s", contract.Checksums, fileName)
	}

	// 2. The file, into updates/<version>/, kept only if it matches.
	dir, err := appUpdateDir(env.appSupport, version)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, fileName)
	if have, err := fileSHA256(path); err == nil && have == want {
		fmt.Fprintf(env.stderr, "%s is already staged.\n", fileName)
	} else {
		fmt.Fprintf(env.stderr, "Downloading %s...\n", fileName)
		if err := stageVerifiedAsset(env, assets[fileName], path, want); err != nil {
			return err
		}
	}
	// The two signed records the file was checked against, beside it.
	for name, data := range map[string][]byte{contract.Checksums: sums, contract.Checksums + ".sigstore.json": bundleJSON} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return err
		}
	}

	out, _ := json.MarshalIndent(appDownloadResult{
		Version:  version,
		Tag:      release.TagName,
		Kind:     artifact.Kind,
		Path:     path,
		SHA256:   want,
		Identity: signer,
	}, "", "  ")
	fmt.Fprintln(env.stdout, string(out))
	return nil
}

// fetchAppRelease reads one release of the contract's repository (the latest
// when version is empty) from GitHub's API.
func fetchAppRelease(env appEnv, contract *releaseContract, version string) (*ghRelease, error) {
	url := "https://api.github.com/repos/" + contract.Repository + "/releases/latest"
	if version != "" {
		url = "https://api.github.com/repos/" + contract.Repository + "/releases/tags/" + contract.tag(version)
	}
	req, err := newGitHubRequest(url)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := appGet(env, req)
	if err != nil {
		return nil, fmt.Errorf("could not reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		if version != "" {
			return nil, fmt.Errorf("%s has no release %s", contract.Repository, contract.tag(version))
		}
		return nil, fmt.Errorf("%s has no release yet", contract.Repository)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub answered %d for %s", resp.StatusCode, url)
	}
	var release ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseJSONBytes)).Decode(&release); err != nil {
		return nil, fmt.Errorf("read release: %w", err)
	}
	return &release, nil
}

// releaseAttestation finds GitHub's release attestation of a file among the
// attestations GitHub's API lists for its digest in repo, and verifies it
// (verifyAppAttestation). It returns the bundle that verified, to keep beside
// the staged file. An immutable release's attestation cannot be one of its
// assets (it is made at publication, when the assets freeze), so this API is
// the one place it is read from.
func releaseAttestation(env appEnv, repo string, digest []byte, tag string) (*appSigner, []byte, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/attestations/sha256:%s?per_page=%d",
		repo, hex.EncodeToString(digest), maxAttestations)
	req, err := newGitHubRequest(url)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := appGet(env, req)
	if err != nil {
		return nil, nil, fmt.Errorf("could not reach GitHub's attestations: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil, fmt.Errorf("GitHub holds no attestation of %s's checksums; an update is never staged without one", tag)
	}
	if resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("GitHub's attestations answered %d", resp.StatusCode)
	}
	var list struct {
		Attestations []struct {
			Bundle    json.RawMessage `json:"bundle"`
			BundleURL string          `json:"bundle_url"`
		} `json:"attestations"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAttestationsJSON)).Decode(&list); err != nil {
		return nil, nil, fmt.Errorf("read attestations: %w", err)
	}
	if len(list.Attestations) == 0 {
		return nil, nil, fmt.Errorf("GitHub holds no attestation of %s's checksums; an update is never staged without one", tag)
	}

	var errs []error
	for i, a := range list.Attestations {
		if i == maxAttestations {
			break
		}
		bundleJSON, err := attestationBundle(env, a.Bundle, a.BundleURL)
		if err == nil {
			var signer *appSigner
			if signer, err = verifyAppAttestation(bundleJSON, digest, tag, env.anchors); err == nil {
				return signer, bundleJSON, nil
			}
		}
		errs = append(errs, err)
	}
	return nil, nil, fmt.Errorf("none of GitHub's attestations of %s's checksums is its release attestation: %w",
		tag, errors.Join(errs...))
}

// attestationBundle is one listed attestation's bundle: inline, or fetched
// from its bundle_url, a Snappy-compressed blob on GitHub's storage. The
// request carries no credentials (githubDo sends a token only to GitHub's own
// API hosts), and the blob is verified like any other bundle.
func attestationBundle(env appEnv, inline json.RawMessage, bundleURL string) ([]byte, error) {
	if len(inline) > 0 && string(inline) != "null" {
		return inline, nil
	}
	if bundleURL == "" {
		return nil, fmt.Errorf("an attestation carries neither a bundle nor a bundle_url")
	}
	if !strings.HasPrefix(bundleURL, "https://") {
		return nil, fmt.Errorf("an attestation's bundle_url is not https")
	}
	req, err := newGitHubRequest(bundleURL)
	if err != nil {
		return nil, err
	}
	resp, err := appGet(env, req)
	if err != nil {
		return nil, fmt.Errorf("download an attestation bundle: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("an attestation bundle answered %d", resp.StatusCode)
	}
	compressed, err := io.ReadAll(io.LimitReader(resp.Body, maxBundleBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download an attestation bundle: %w", err)
	}
	if len(compressed) > maxBundleBytes {
		return nil, fmt.Errorf("an attestation bundle is larger than %d MB", maxBundleBytes>>20)
	}
	return snappyDecode(compressed, 4*maxBundleBytes)
}

// checkNewer refuses a release that is not newer than the installed one,
// unless the person asked for a downgrade.
func checkNewer(version string, opts appDownloadOptions) error {
	if opts.allowDowngrade {
		return nil
	}
	target, _ := parseSemver(version)
	current, _ := parseSemver(opts.current)
	if !current.Less(target) {
		return fmt.Errorf("release %s is not newer than the installed %s (pass --allow-downgrade to fetch it anyway)", version, opts.current)
	}
	return nil
}

// appUpdateDir is <app support>/updates/<version>, created owner-only. It is
// the only place `app download` writes. A partial download a killed run left
// anywhere under updates/ is removed first: a run that is stopped cannot
// clean up after itself, and each run's temporary name is new. On Unix a
// concurrent run whose file this removes fails at its rename and stages
// nothing; on Windows an open file cannot be removed, so the sweep skips it
// and both runs stage the same verified file, the later rename replacing the
// earlier.
func appUpdateDir(appSupport, version string) (string, error) {
	updates := filepath.Join(appSupport, "updates")
	dir := filepath.Join(updates, version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// MkdirAll leaves an existing folder's mode alone.
	for _, d := range []string{updates, dir} {
		if err := os.Chmod(d, 0o700); err != nil {
			return "", err
		}
	}
	parts, _ := filepath.Glob(filepath.Join(updates, "*", ".*"+partSuffix))
	for _, p := range parts {
		os.Remove(p)
	}
	return dir, nil
}

// partSuffix ends the temporary name a file downloads under.
const partSuffix = ".part"

// appGet sends a request through githubDo and guards its body against a
// connection that stops sending (stallGuard).
func appGet(env appEnv, req *http.Request) (*http.Response, error) {
	resp, err := githubDo(env.client, req)
	if err != nil {
		return nil, err
	}
	idle := env.stall
	if idle == 0 {
		idle = appStallTimeout
	}
	resp.Body = guardStalls(resp.Body, idle)
	return resp, nil
}

// appFetch reads a small release asset whole, refusing one over limit bytes.
func appFetch(env appEnv, asset ghAsset, limit int64) ([]byte, error) {
	req, err := newAssetRequest(asset)
	if err != nil {
		return nil, err
	}
	resp, err := appGet(env, req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download %s answered %d", asset.Name, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset.Name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d MB", asset.Name, limit>>20)
	}
	return data, nil
}

// stallGuard fails a response body that has sent nothing for idle, by
// closing it, so the read in progress returns. Only silence counts: a slow
// body that keeps arriving runs as long as it needs.
type stallGuard struct {
	body    io.ReadCloser
	idle    time.Duration
	timer   *time.Timer
	stalled atomic.Bool
}

func guardStalls(body io.ReadCloser, idle time.Duration) *stallGuard {
	g := &stallGuard{body: body, idle: idle}
	g.timer = time.AfterFunc(idle, func() {
		g.stalled.Store(true)
		body.Close()
	})
	return g
}

func (g *stallGuard) Read(p []byte) (int, error) {
	n, err := g.body.Read(p)
	if n > 0 {
		g.timer.Reset(g.idle)
	}
	if err != nil && g.stalled.Load() {
		return n, fmt.Errorf("nothing arrived for %s, so the download was stopped", g.idle)
	}
	return n, err
}

func (g *stallGuard) Close() error {
	g.timer.Stop()
	return g.body.Close()
}

// stageVerifiedAsset streams an asset to a temporary file beside path and
// renames it to path only when its SHA-256 is want. A file that does not
// match is deleted, never left where an installer could find it.
func stageVerifiedAsset(env appEnv, asset ghAsset, path, want string) (err error) {
	req, err := newAssetRequest(asset)
	if err != nil {
		return err
	}
	resp, err := appGet(env, req)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("download %s answered %d", asset.Name, resp.StatusCode)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*"+partSuffix)
	if err != nil {
		return err
	}
	defer func() {
		tmp.Close()
		if err != nil {
			os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxAppArtifactBytes+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", asset.Name, err)
	}
	if n > maxAppArtifactBytes {
		return fmt.Errorf("%s is larger than %d GB", asset.Name, maxAppArtifactBytes>>30)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s does not match the signed checksums: got %s, want %s", asset.Name, got, want)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// fileSHA256 is a file's SHA-256 as lowercase hex.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
