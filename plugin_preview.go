package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type previewResult struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Version            string          `json:"version"`
	Description        string          `json:"description"`
	Author             string          `json:"author"`
	Privileges         []string        `json:"privileges"`
	OptionalPrivileges []string        `json:"optional_privileges"`
	Effects            []previewEffect `json:"effects"`
	DependsOn          []previewDep    `json:"depends_on"`
	Conformance        string          `json:"conformance"`
	Tier               string          `json:"tier"`
	Blocklisted        bool            `json:"blocklisted"`
	BlockReason        string          `json:"block_reason,omitempty"`
	Source             string          `json:"source"`
	Tag                string          `json:"tag"`
	// Update is present when this plugin is already installed: the consent
	// DIFF against the manifest the install would replace. The settings
	// panel renders this instead of the full summary — standing consent
	// covers everything unchanged.
	Update *previewUpdate `json:"update,omitempty"`
}

// previewUpdate mirrors consentDiff for the settings panel, plus the
// installed version for the "v1 → v2" line.
type previewUpdate struct {
	InstalledVersion  string          `json:"installed_version"`
	AddedPrivileges   []string        `json:"added_privileges"`
	AddedOptional     []string        `json:"added_optional_privileges"`
	AddedEffects      []previewEffect `json:"added_effects"`
	RemovedPrivileges []string        `json:"removed_privileges"`
	RemovedOptional   []string        `json:"removed_optional_privileges"`
	RemovedEffects    []string        `json:"removed_effects"`
	Expands           bool            `json:"expands"`
}

// previewEffect is one consent unit of effects, carrying the author-written
// user-visible copy the install dialog renders.
type previewEffect struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Asserts     []string `json:"asserts"`
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

	effects := []previewEffect{}
	if manifest.Consumes != nil {
		for _, e := range manifest.Consumes.Effects {
			effects = append(effects, previewEffect{
				Name:        e.UserVisibleName,
				Description: e.UserVisibleDescription,
				Asserts:     e.AssertNames(),
			})
		}
	}
	result := previewResult{
		ID:                 manifest.ID,
		Name:               manifest.Name,
		Version:            manifest.Version,
		Description:        manifest.Description,
		Author:             manifest.Author,
		Privileges:         manifest.Privileges,
		OptionalPrivileges: manifest.OptionalPrivileges,
		Effects:            effects,
		DependsOn:          deps,
		Conformance:        cs.Status,
		Tier:               tier,
		Blocklisted:        blocklisted,
		BlockReason:        blockReason,
		Source:             source,
		Tag:                tag,
	}
	if result.Privileges == nil {
		result.Privileges = []string{}
	}
	if result.OptionalPrivileges == nil {
		result.OptionalPrivileges = []string{}
	}

	// Already installed → attach the consent diff, same basis as the
	// install path's confirmUpdate: the manifest at the swap target.
	if old, err := readManifest(filepath.Join(userPluginsDir(), manifest.ID, "plugin.json")); err == nil {
		d := diffConsent(old, manifest)
		up := previewUpdate{
			InstalledVersion:  old.Version,
			AddedPrivileges:   emptyNotNil(d.AddedPrivileges),
			AddedOptional:     emptyNotNil(d.AddedOptional),
			AddedEffects:      []previewEffect{},
			RemovedPrivileges: emptyNotNil(d.RemovedPrivileges),
			RemovedOptional:   emptyNotNil(d.RemovedOptional),
			RemovedEffects:    emptyNotNil(d.RemovedEffects),
			Expands:           d.expands(),
		}
		for _, e := range d.AddedEffects {
			up.AddedEffects = append(up.AddedEffects, previewEffect{
				Name:        e.UserVisibleName,
				Description: e.UserVisibleDescription,
				Asserts:     e.AssertNames(),
			})
		}
		result.Update = &up
	}

	data, _ := json.Marshal(result)
	fmt.Println(string(data))
}

func emptyNotNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
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
