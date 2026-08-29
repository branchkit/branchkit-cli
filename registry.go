package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const defaultCatalogURL = "https://raw.githubusercontent.com/branchkit/registry/main/catalog.yaml"

const maxCatalogSize = 1 * 1024 * 1024

type catalog struct {
	Plugins []catalogEntry `yaml:"plugins"`
}

type catalogEntry struct {
	ID          string   `yaml:"id"`
	Source      string   `yaml:"source"`
	Description string   `yaml:"description"`
	Categories  []string `yaml:"categories"`
	Tier        string   `yaml:"tier"`
	// Registry counter-signature, written by `registry sign` when a plugin is
	// admitted (DESIGN_PLUGIN_SIGNING_CHAIN step 5). It signs the manifest hash
	// (platform- and version-independent). Absent until counter-signed; the
	// install path treats absence as "not registry-signed", a present-but-
	// invalid signature as a hard fail.
	ManifestSHA256    string `yaml:"manifest_sha256,omitempty"`
	RegistrySignature string `yaml:"registry_signature,omitempty"`
}

func catalogURL() string {
	if url := os.Getenv("BRANCHKIT_CATALOG_URL"); url != "" {
		return url
	}
	return defaultCatalogURL
}

func fetchCatalog() (catalog, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", catalogURL(), nil)
	if err != nil {
		return catalog{}, fmt.Errorf("invalid catalog URL: %w", err)
	}
	req.Header.Set("User-Agent", "branchkit-cli")

	resp, err := client.Do(req)
	if err != nil {
		return catalog{}, fmt.Errorf("failed to fetch catalog: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return catalog{}, fmt.Errorf("catalog returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogSize))
	if err != nil {
		return catalog{}, fmt.Errorf("failed to read catalog: %w", err)
	}

	var cat catalog
	if err := yaml.Unmarshal(data, &cat); err != nil {
		return catalog{}, fmt.Errorf("failed to parse catalog: %w", err)
	}
	return cat, nil
}

// resolveShortNameEntry returns the full catalog entry for a short name —
// including the registry counter-signature fields the install path verifies.
func resolveShortNameEntry(name string) (*catalogEntry, error) {
	fmt.Printf("Looking up '%s' in catalog...\n", name)

	cat, err := fetchCatalog()
	if err != nil {
		return nil, err
	}

	for i := range cat.Plugins {
		if cat.Plugins[i].ID == name {
			fmt.Printf("Resolved '%s' → %s\n", name, cat.Plugins[i].Source)
			return &cat.Plugins[i], nil
		}
	}

	available := make([]string, 0, len(cat.Plugins))
	for _, entry := range cat.Plugins {
		available = append(available, entry.ID)
	}
	sort.Strings(available)
	return nil, fmt.Errorf(
		"plugin '%s' not found in catalog\n\nAvailable: %s\n\nFor unlisted plugins, use: branchkit-cli plugin install github:owner/repo",
		name, strings.Join(available, ", "),
	)
}

// findCatalogEntryBySource returns the catalog entry naming the same GitHub
// repo, or nil. Best-effort: an unreachable catalog returns nil — the
// counter-signature is soft-absent, matching install semantics — so updates
// never fail on registry downtime. This is what lets `plugin update`
// re-verify the counter-signature instead of passing catalog=nil and
// silently downgrading a catalog install's registry_signed record.
func findCatalogEntryBySource(source string) *catalogEntry {
	want := strings.ToLower(strings.TrimPrefix(source, "github:"))
	cat, err := fetchCatalog()
	if err != nil {
		return nil
	}
	for i := range cat.Plugins {
		have := strings.ToLower(strings.TrimPrefix(cat.Plugins[i].Source, "github:"))
		if have == want {
			return &cat.Plugins[i]
		}
	}
	return nil
}

func resolveShortName(name string) (string, error) {
	entry, err := resolveShortNameEntry(name)
	if err != nil {
		return "", err
	}
	return entry.Source, nil
}

// isShortName returns true if the source looks like a short plugin name
// (no slash, no path prefix, no github: URL) rather than an owner/repo or local path.
func isShortName(source string) bool {
	return !strings.Contains(source, "/") && !isLocalPath(source) && !strings.HasPrefix(source, "github:")
}
