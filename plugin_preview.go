package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type previewResult struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Version      string              `json:"version"`
	Description  string              `json:"description"`
	Author       string              `json:"author"`
	Privileges   []string            `json:"privileges"`
	DependsOn    []previewDep        `json:"depends_on"`
	Conformance  string              `json:"conformance"`
	Tier         string              `json:"tier"`
	Blocklisted  bool                `json:"blocklisted"`
	BlockReason  string              `json:"block_reason,omitempty"`
	Source       string              `json:"source"`
	Tag          string              `json:"tag"`
}

type previewDep struct {
	Plugin           string `json:"plugin"`
	Version          string `json:"version,omitempty"`
	Installed        bool   `json:"installed"`
	InstalledVersion string `json:"installed_version,omitempty"`
	Satisfied        *bool  `json:"satisfied,omitempty"`
}

func cmdPreview(source string) {
	if isShortName(source) {
		resolved, err := resolveShortName(source)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		source = resolved
	}

	parsed, err := parseGitHubSource(source)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	tag := parsed.Version
	if tag == "" {
		t, err := fetchLatestTag(parsed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		tag = t
	}

	manifest, err := fetchManifestFromRepo(parsed, tag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Check blocklist
	bl := fetchBlocklist()
	var blocklisted bool
	var blockReason string
	if bl != nil {
		for _, entry := range bl.Blocked {
			if matchesBlockedSource(source, entry.Source) {
				blocklisted = true
				blockReason = entry.Reason
				break
			}
		}
	}

	// Check conformance
	cs := fetchConformanceStatus(parsed, tag)

	// Check catalog tier
	tier := lookupCatalogTier(manifest.ID)

	// Check dependencies against installed plugins
	installedVersions := map[string]string{}
	for _, dp := range discoverPlugins() {
		installedVersions[dp.Manifest.ID] = dp.Manifest.Version
	}

	deps := make([]previewDep, 0, len(manifest.DependsOn))
	for _, dep := range manifest.DependsOn {
		pd := previewDep{
			Plugin:  dep.Plugin,
			Version: dep.Version,
		}
		if v, found := installedVersions[dep.Plugin]; found {
			pd.Installed = true
			pd.InstalledVersion = v
			if dep.Version != "" && v != "" {
				ok, err := satisfiesConstraint(v, dep.Version)
				if err == nil {
					pd.Satisfied = &ok
				}
			}
		}
		deps = append(deps, pd)
	}

	result := previewResult{
		ID:          manifest.ID,
		Name:        manifest.Name,
		Version:     manifest.Version,
		Description: manifest.Description,
		Author:      manifest.Author,
		Privileges:  manifest.Capabilities,
		DependsOn:   deps,
		Conformance: cs.Status,
		Tier:        tier,
		Blocklisted: blocklisted,
		BlockReason: blockReason,
		Source:      source,
		Tag:         tag,
	}
	if result.Privileges == nil {
		result.Privileges = []string{}
	}

	data, _ := json.Marshal(result)
	fmt.Println(string(data))
}

func fetchManifestFromRepo(source ResolvedSource, tag string) (PluginManifest, error) {
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/plugin.json",
		source.Owner, source.Repo, tag)

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return PluginManifest{}, err
	}
	req.Header.Set("User-Agent", "branchkit-cli")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return PluginManifest{}, fmt.Errorf("failed to fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return PluginManifest{}, fmt.Errorf("no plugin.json found at %s/%s@%s", source.Owner, source.Repo, tag)
	}
	if resp.StatusCode >= 300 {
		return PluginManifest{}, fmt.Errorf("GitHub returned %d fetching manifest", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return PluginManifest{}, fmt.Errorf("failed to read manifest: %w", err)
	}

	var m PluginManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return PluginManifest{}, fmt.Errorf("invalid plugin.json: %w", err)
	}
	return m, nil
}
