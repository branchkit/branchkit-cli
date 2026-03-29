package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ResolvedSource is a parsed GitHub source reference.
type ResolvedSource struct {
	Owner   string
	Repo    string
	Version string // empty = latest
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// parseGitHubSource parses "owner/repo" or "owner/repo@version".
func parseGitHubSource(source string) (ResolvedSource, error) {
	var version string
	repoPart := source
	if idx := strings.Index(source, "@"); idx != -1 {
		repoPart = source[:idx]
		version = source[idx+1:]
	}

	parts := strings.Split(repoPart, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ResolvedSource{}, fmt.Errorf("invalid GitHub source '%s' — expected format: owner/repo", source)
	}

	return ResolvedSource{
		Owner:   parts[0],
		Repo:    parts[1],
		Version: version,
	}, nil
}

// pluginNameFromRepo derives the plugin name from a repo following branchkit-plugin-{name}.
func pluginNameFromRepo(repo string) string {
	if after, ok := strings.CutPrefix(repo, "branchkit-plugin-"); ok {
		return after
	}
	return repo
}

// downloadRelease fetches a GitHub release and downloads the platform artifact.
// Returns the path to the downloaded tarball and the release tag.
func downloadRelease(source ResolvedSource, destDir string) (string, string, error) {
	client := &http.Client{Timeout: 60 * time.Second}

	// Fetch release
	var url string
	if source.Version != "" {
		url = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", source.Owner, source.Repo, source.Version)
	} else {
		url = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", source.Owner, source.Repo)
	}

	fmt.Printf("Fetching release from %s/%s...\n", source.Owner, source.Repo)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", "", fmt.Errorf("invalid request URL: %w", err)
	}
	req.Header.Set("User-Agent", "branchkit-cli")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("failed to reach GitHub API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		suffix := ""
		if source.Version != "" {
			suffix = "@" + source.Version
		}
		return "", "", fmt.Errorf(
			"no release found for %s/%s%s\n\n"+
				"To install from source instead:\n"+
				"  branchkit-cli plugin install %s/%s --build",
			source.Owner, source.Repo, suffix, source.Owner, source.Repo,
		)
	}
	if resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", "", fmt.Errorf("failed to parse release JSON: %w", err)
	}

	// Find platform artifact
	archLabel := runtime.GOARCH
	if archLabel == "arm64" {
		// Already correct
	} else if archLabel == "amd64" {
		archLabel = "x86_64"
	}
	pluginName := pluginNameFromRepo(source.Repo)
	expected := fmt.Sprintf("branchkit-plugin-%s-%s-%s.tar.gz", pluginName, runtime.GOOS, archLabel)

	var asset *ghAsset
	for i := range release.Assets {
		if release.Assets[i].Name == expected {
			asset = &release.Assets[i]
			break
		}
	}
	if asset == nil {
		names := make([]string, len(release.Assets))
		for i, a := range release.Assets {
			names[i] = a.Name
		}
		return "", "", fmt.Errorf("no artifact '%s' in release %s — available: %v", expected, release.TagName, names)
	}

	// Download artifact
	fmt.Printf("Downloading %s...\n", asset.Name)
	dlReq, err := http.NewRequest("GET", asset.BrowserDownloadURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("invalid download URL: %w", err)
	}
	dlReq.Header.Set("User-Agent", "branchkit-cli")
	dlResp, err := client.Do(dlReq)
	if err != nil {
		return "", "", fmt.Errorf("failed to download artifact: %w", err)
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode >= 300 {
		return "", "", fmt.Errorf("download failed with status %d", dlResp.StatusCode)
	}

	tarballPath := filepath.Join(destDir, asset.Name)
	outFile, err := os.Create(tarballPath)
	if err != nil {
		return "", "", err
	}
	bodyBytes, err := io.ReadAll(dlResp.Body)
	if err != nil {
		outFile.Close()
		return "", "", err
	}
	outFile.Write(bodyBytes)
	outFile.Close()

	// Verify checksum if available
	checksumName := asset.Name + ".sha256"
	for _, a := range release.Assets {
		if a.Name != checksumName {
			continue
		}
		fmt.Println("Verifying checksum...")
		csReq, err := http.NewRequest("GET", a.BrowserDownloadURL, nil)
		if err != nil {
			return "", "", fmt.Errorf("invalid checksum URL: %w", err)
		}
		csReq.Header.Set("User-Agent", "branchkit-cli")
		csResp, err := client.Do(csReq)
		if err != nil || csResp.StatusCode >= 300 {
			return "", "", fmt.Errorf("failed to download checksum file")
		}
		csData, _ := io.ReadAll(csResp.Body)
		csResp.Body.Close()

		expectedHash := strings.TrimSpace(strings.SplitN(string(csData), " ", 2)[0])
		if expectedHash == "" {
			return "", "", fmt.Errorf("checksum file is empty or malformed")
		}

		actualHash := fmt.Sprintf("%x", sha256.Sum256(bodyBytes))
		if actualHash != expectedHash {
			return "", "", fmt.Errorf("checksum mismatch!\n  Expected: %s\n  Actual:   %s", expectedHash, actualHash)
		}
		fmt.Println("Checksum verified.")
		break
	}

	return tarballPath, release.TagName, nil
}
