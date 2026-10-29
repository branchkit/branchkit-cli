package main

// `branchkit-cli app download`: fetch one BranchKit release file for this
// machine and prove it is the release's, before anything could install it.
//
// Order matters. The checksums file and its attestation are fetched and
// verified first, so a release that is not signed by the releases repository
// costs two small downloads and stages nothing. Only then is the large file
// downloaded, to a temporary name, and it takes its real name only once its
// digest matches its line in the verified checksums. The file name comes
// from the contract (release_assets.json) and carries the version, so a
// checksums file from another release cannot vouch for it.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	// A checksums file lists a handful of files; an attestation bundle is a
	// few kilobytes, more for a release predicate naming every asset.
	maxSumsBytes   = 1 << 20
	maxBundleBytes = 4 << 20
	// A release's API answer lists its assets.
	maxReleaseJSONBytes = 4 << 20
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

	// 1. The checksums and their attestation. No attestation, or one that
	//    does not verify under the app's identity policy, ends it here.
	sumsAsset, err := need(contract.Checksums, "checksums file")
	if err != nil {
		return err
	}
	bundleAsset, err := need(contract.ChecksumsAttestation, "attestation of its checksums")
	if err != nil {
		return err
	}
	if _, err := need(fileName, "file for this machine"); err != nil {
		return err
	}
	fmt.Fprintf(env.stderr, "Verifying %s %s...\n", contract.Repository, release.TagName)
	sums, err := download(env.client, sumsAsset, maxSumsBytes)
	if err != nil {
		return fmt.Errorf("download %s: %w", contract.Checksums, err)
	}
	bundleJSON, err := download(env.client, bundleAsset, maxBundleBytes)
	if err != nil {
		return fmt.Errorf("download %s: %w", contract.ChecksumsAttestation, err)
	}
	signer, err := verifyAppSums(bundleJSON, sums, release.TagName, env.anchors)
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
		if err := stageVerifiedAsset(env.client, assets[fileName], path, want); err != nil {
			return err
		}
	}
	for name, data := range map[string][]byte{contract.Checksums: sums, contract.ChecksumsAttestation: bundleJSON} {
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
	resp, err := githubDo(env.client, req)
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
// the only place `app download` writes.
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
	return dir, nil
}

// stageVerifiedAsset streams an asset to a temporary file beside path and
// renames it to path only when its SHA-256 is want. A file that does not
// match is deleted, never left where an installer could find it.
func stageVerifiedAsset(client *http.Client, asset ghAsset, path, want string) (err error) {
	req, err := newAssetRequest(asset)
	if err != nil {
		return err
	}
	resp, err := githubDo(client, req)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("download %s answered %d", asset.Name, resp.StatusCode)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.part")
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
