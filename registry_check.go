package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// The registry's per-release check: everything that can be decided about one
// release of a plugin without a person, run the same way by the registry's
// workflow and by an author before they submit. It fails closed: a check
// that cannot run (a download that fails, a file it cannot read) is a failed
// check, never a skipped one.

// releaseArtifactRE is the tarball layout release-action publishes:
// branchkit-plugin-<name>-<os>-<arch>.tar.gz.
var releaseArtifactRE = regexp.MustCompile(`^branchkit-plugin-(.+)-(darwin|linux|windows)-(arm64|x86_64)\.tar\.gz$`)

// maxArtifactBytes bounds one download: a plugin tarball larger than this is
// refused rather than read into memory.
const maxArtifactBytes = 256 << 20

// maxExtractedBytes bounds what one tarball may extract to, checked from its
// headers before anything is written, so a compressed bomb cannot fill the
// runner's disk. Every extracted file is scanned in full (scanTree).
const maxExtractedBytes = 1 << 30

// tarballExtractedSize sums the sizes a gzipped tarball declares, stopping
// as soon as the sum passes limit.
func tarballExtractedSize(path string, limit int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	tr := tar.NewReader(gz)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return 0, err
		}
		total += hdr.Size
		if total > limit {
			return total, nil
		}
	}
}

type releaseCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// powerLineResult is crossesPowerLine's answer as the report carries it.
type powerLineResult struct {
	Crosses bool     `json:"crosses"`
	Reasons []string `json:"reasons"`
}

type releaseReport struct {
	Source    string   `json:"source"`
	Tag       string   `json:"tag"`
	OK        bool     `json:"ok"`
	Platforms []string `json:"platforms"`
	// ManifestSHA256 is over the manifest's canonical JSON (canonicalJSON),
	// the same on every platform whatever its line endings.
	ManifestSHA256 string          `json:"manifest_sha256,omitempty"`
	ConsentDigest  string          `json:"consent_digest,omitempty"`
	PowerLine      powerLineResult `json:"power_line"`
	Checks         []releaseCheck  `json:"checks"`
}

func (r *releaseReport) check(name string, ok bool, detail string) {
	r.Checks = append(r.Checks, releaseCheck{Name: name, OK: ok, Detail: detail})
	if !ok {
		r.OK = false
	}
}

// checkedArtifact is one platform's tarball after download and verification.
type checkedArtifact struct {
	platform      string // "darwin-arm64"
	manifestBytes []byte
	manifest      PluginManifest
	dir           string // extracted contents
}

func cmdRegistryCheck(args []string) {
	if len(args) != 1 || !strings.Contains(args[0], "@") {
		fmt.Fprintln(os.Stderr, "Usage: branchkit-cli registry check github:owner/repo@tag")
		os.Exit(1)
	}
	// Progress lines from the shared download and verify helpers go to
	// stderr, so stdout carries the report alone.
	stdout := os.Stdout
	os.Stdout = os.Stderr
	report := checkRelease(args[0])
	os.Stdout = stdout
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(data))
	if !report.OK {
		os.Exit(1)
	}
}

func checkRelease(sourceArg string) releaseReport {
	report := releaseReport{Source: sourceArg, OK: true, Platforms: []string{}, Checks: []releaseCheck{}}
	report.PowerLine = powerLineResult{Reasons: []string{}}
	source, err := parseGitHubSource(sourceArg)
	if err != nil || source.Version == "" {
		report.check("source", false, "need github:owner/repo@tag")
		return report
	}
	report.Tag = source.Version
	client := &http.Client{Timeout: 120 * time.Second}
	release, err := fetchRelease(client, source)
	if err != nil {
		report.check("release", false, err.Error())
		return report
	}

	tmp, err := os.MkdirTemp("", "branchkit-registry-check-")
	if err != nil {
		report.check("workspace", false, err.Error())
		return report
	}
	defer os.RemoveAll(tmp)

	name := pluginNameFromRepo(source.Repo)
	wantRepo := source.Owner + "/" + source.Repo
	var artifacts []checkedArtifact
	for _, asset := range release.Assets {
		m := releaseArtifactRE.FindStringSubmatch(asset.Name)
		if m == nil || m[1] != name {
			continue
		}
		platform := m[2] + "-" + m[3]
		a, err := checkArtifact(client, release.Assets, asset, wantRepo, filepath.Join(tmp, platform))
		if err != nil {
			report.check("artifact "+platform, false, err.Error())
			continue
		}
		a.platform = platform
		report.check("artifact "+platform, true, "checksum and author signature verified")
		artifacts = append(artifacts, a)
	}
	assessRelease(&report, name, source.Version, artifacts)
	return report
}

func fetchRelease(client *http.Client, source ResolvedSource) (ghRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", source.Owner, source.Repo, source.Version)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return ghRelease{}, err
	}
	req.Header.Set("User-Agent", "branchkit-cli")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return ghRelease{}, fmt.Errorf("could not reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return ghRelease{}, fmt.Errorf("no release %s (GitHub answered %d)", source.Version, resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return ghRelease{}, fmt.Errorf("unreadable release: %w", err)
	}
	return rel, nil
}

func download(client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "branchkit-cli")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download answered %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("larger than %d MB", limit>>20)
	}
	return data, nil
}

// checkArtifact downloads one tarball and requires what the install path
// only prefers: a published checksum that matches, and an author signature
// that verifies and names the declared repository. Then it extracts the
// tarball and reads its manifest.
func checkArtifact(client *http.Client, assets []ghAsset, asset ghAsset, wantRepo, dir string) (checkedArtifact, error) {
	body, err := download(client, asset.BrowserDownloadURL, maxArtifactBytes)
	if err != nil {
		return checkedArtifact{}, fmt.Errorf("download: %w", err)
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])

	var checksumURL string
	for _, a := range assets {
		if a.Name == asset.Name+".sha256" {
			checksumURL = a.BrowserDownloadURL
		}
	}
	if checksumURL == "" {
		return checkedArtifact{}, fmt.Errorf("no %s.sha256 published", asset.Name)
	}
	cs, err := download(client, checksumURL, 4096)
	if err != nil {
		return checkedArtifact{}, fmt.Errorf("checksum: %w", err)
	}
	if want := strings.TrimSpace(strings.SplitN(string(cs), " ", 2)[0]); want != digest {
		return checkedArtifact{}, fmt.Errorf("checksum mismatch: published %s, downloaded %s", want, digest)
	}

	att, err := verifyReleaseAttestation(client, assets, asset.Name, digest, wantRepo)
	if err != nil {
		return checkedArtifact{}, err
	}
	if att == nil || !att.Verified {
		reason := "no author signature"
		if att != nil && att.Reason != "" {
			reason = att.Reason
		}
		return checkedArtifact{}, fmt.Errorf("author signature required: %s", reason)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return checkedArtifact{}, err
	}
	tarball := filepath.Join(dir, "artifact.tar.gz")
	if err := os.WriteFile(tarball, body, 0o644); err != nil {
		return checkedArtifact{}, err
	}
	if n, err := tarballExtractedSize(tarball, maxExtractedBytes); err != nil {
		return checkedArtifact{}, fmt.Errorf("unreadable tarball: %w", err)
	} else if n > maxExtractedBytes {
		return checkedArtifact{}, fmt.Errorf("extracts to more than %d MB", maxExtractedBytes>>20)
	}
	extracted := filepath.Join(dir, "x")
	if err := extractTarball(tarball, extracted); err != nil {
		return checkedArtifact{}, fmt.Errorf("extract: %w", err)
	}
	manifestPath, err := findManifest(extracted)
	if err != nil {
		return checkedArtifact{}, err
	}
	mb, err := os.ReadFile(manifestPath)
	if err != nil {
		return checkedArtifact{}, err
	}
	m, err := readManifest(manifestPath)
	if err != nil {
		return checkedArtifact{}, err
	}
	// The same claim check install makes: a release whose manifest names a
	// publisher its signature disproves would list and then fail every
	// install.
	if err := checkPublisherClaim(m.Publisher, att); err != nil {
		return checkedArtifact{}, err
	}
	return checkedArtifact{manifestBytes: mb, manifest: m, dir: extracted}, nil
}

// assessRelease is everything decided from the verified artifacts, kept
// apart from the downloads so it can be tested without a network.
func assessRelease(r *releaseReport, name, tag string, artifacts []checkedArtifact) {
	if len(artifacts) == 0 {
		r.check("platforms", false, "no verified branchkit-plugin-"+name+"-<os>-<arch>.tar.gz in the release")
		return
	}
	for _, a := range artifacts {
		r.Platforms = append(r.Platforms, a.platform)
	}
	sort.Strings(r.Platforms)
	r.check("platforms", true, strings.Join(r.Platforms, ", "))

	// One release asks for one thing: every platform's manifest the same
	// content, so a listing's consent cannot differ by operating system.
	// Compared as canonical JSON, not bytes: a Windows build checks the
	// repository out with CRLF line endings, and that is not a difference.
	first := artifacts[0]
	firstCanon, err := canonicalJSON(first.manifestBytes)
	if err != nil {
		r.check("one manifest", false, first.platform+"'s plugin.json is not JSON: "+err.Error())
		return
	}
	same := true
	for _, a := range artifacts[1:] {
		c, err := canonicalJSON(a.manifestBytes)
		if err != nil || string(c) != string(firstCanon) {
			same = false
			r.check("one manifest", false, a.platform+"'s plugin.json differs from "+first.platform+"'s")
		}
	}
	if same {
		r.check("one manifest", true, "")
	}
	m := first.manifest
	sum := sha256.Sum256(firstCanon)
	r.ManifestSHA256 = hex.EncodeToString(sum[:])
	r.ConsentDigest = consentDigest(m)

	r.check("id matches repository", m.ID == name, fmt.Sprintf("plugin.json id %q, repository branchkit-plugin-%s", m.ID, name))
	wantVersion := strings.TrimPrefix(tag, "v")
	r.check("version matches tag", m.Version == wantVersion, fmt.Sprintf("plugin.json version %q, tag %q", m.Version, tag))

	if bad := rawIPHosts(m); len(bad) > 0 {
		r.check("network hosts are names", false, "raw IP addresses: "+strings.Join(bad, ", "))
	} else {
		r.check("network hosts are names", true, "")
	}

	var secrets, hidden []string
	for _, a := range artifacts {
		s, h := scanTree(a.dir)
		secrets = append(secrets, prefixAll(a.platform+": ", s)...)
		hidden = append(hidden, prefixAll(a.platform+": ", h)...)
	}
	r.check("no secrets", len(secrets) == 0, strings.Join(firstN(secrets, 10), "; "))
	r.check("no hidden characters", len(hidden) == 0, strings.Join(firstN(hidden, 10), "; "))

	crosses, reasons := crossesPowerLine(m)
	r.PowerLine = powerLineResult{Crosses: crosses, Reasons: reasons}
}

// canonicalJSON re-encodes a JSON document with sorted keys and no
// insignificant whitespace, so two encodings of one manifest compare equal.
func canonicalJSON(data []byte) ([]byte, error) {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// rawIPHosts are declared network hosts written as an IP address (other
// than this computer's): a listing names who it talks to, and an address
// hides that.
func rawIPHosts(m PluginManifest) []string {
	var out []string
	for _, member := range networkSet(m) {
		host := member
		if h, _, err := net.SplitHostPort(member); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
			out = append(out, member)
		}
	}
	return out
}

// secretPatterns are credentials that should never ship inside a plugin.
var secretPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"private key", regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY`)},
	{"AWS access key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\b(ghp|gho|ghs|ghu|ghr)_[A-Za-z0-9]{36}\b|\bgithub_pat_[A-Za-z0-9_]{60,}`)},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{"Anthropic or OpenAI key", regexp.MustCompile(`\bsk-(ant-)?[A-Za-z0-9_-]{32,}`)},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
}

// hiddenRunes are characters that change how source reads without showing:
// bidirectional overrides (Trojan Source), zero-width characters, and the
// Unicode tag block used to smuggle invisible text.
func isHiddenRune(r rune) bool {
	switch {
	case r >= 0x200B && r <= 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		return true
	case r == 0x2060 || r == 0xFEFF:
		return true
	case r >= 0xE0000 && r <= 0xE007F:
		return true
	}
	return false
}

// textExts are the files read as source for hidden characters. Binaries are
// scanned for secrets only.
var textExts = map[string]bool{
	".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".py": true, ".lua": true,
	".json": true, ".html": true, ".css": true, ".sh": true, ".go": true, ".rs": true,
}

func scanTree(root string) (secrets, hidden []string) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			secrets = append(secrets, "unreadable: "+path)
			return nil
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			secrets = append(secrets, "unreadable: "+path)
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		for _, p := range secretPatterns {
			if p.re.Match(data) {
				secrets = append(secrets, p.name+" in "+rel)
			}
		}
		// Every source file, valid UTF-8 or not: a runtime that replaces a
		// bad byte still runs the rest, so one stray byte must not exempt a
		// file from the scan.
		if textExts[strings.ToLower(filepath.Ext(path))] {
			for i := 0; i < len(data); {
				r, n := utf8.DecodeRune(data[i:])
				if isHiddenRune(r) && !(r == 0xFEFF && i == 0) {
					hidden = append(hidden, fmt.Sprintf("U+%04X in %s", r, rel))
					break
				}
				i += n
			}
		}
		return nil
	})
	return secrets, hidden
}

func prefixAll(p string, xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = p + x
	}
	return out
}

func firstN(xs []string, n int) []string {
	if len(xs) > n {
		return append(xs[:n:n], fmt.Sprintf("and %d more", len(xs)-n))
	}
	return xs
}
