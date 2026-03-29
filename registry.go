package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const defaultRegistryURL = "https://raw.githubusercontent.com/branchkit/registry/main/registry.json"

// Maximum registry size to prevent DoS (1 MB).
const maxRegistrySize = 1 * 1024 * 1024

type registry struct {
	Version int                      `json:"version"`
	Plugins map[string]registryEntry `json:"plugins"`
}

type registryEntry struct {
	Source      string   `json:"source"`
	Description string   `json:"description"`
	Categories  []string `json:"categories"`
	Verified    bool     `json:"verified"`
}

func registryURL() string {
	if url := os.Getenv("BRANCHKIT_REGISTRY_URL"); url != "" {
		return url
	}
	return defaultRegistryURL
}

// fetchRegistry downloads and parses the plugin registry.
func fetchRegistry() (registry, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", registryURL(), nil)
	if err != nil {
		return registry{}, fmt.Errorf("invalid registry URL: %w", err)
	}
	req.Header.Set("User-Agent", "branchkit-cli")

	resp, err := client.Do(req)
	if err != nil {
		return registry{}, fmt.Errorf("failed to fetch registry: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return registry{}, fmt.Errorf("registry returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRegistrySize))
	if err != nil {
		return registry{}, fmt.Errorf("failed to read registry: %w", err)
	}

	var reg registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return registry{}, fmt.Errorf("failed to parse registry: %w", err)
	}
	return reg, nil
}

// resolveShortName looks up a short plugin name in the registry.
// Returns the full owner/repo source, or an error if not found.
func resolveShortName(name string) (string, error) {
	fmt.Printf("Looking up '%s' in registry...\n", name)

	reg, err := fetchRegistry()
	if err != nil {
		return "", err
	}

	entry, ok := reg.Plugins[name]
	if !ok {
		available := make([]string, 0, len(reg.Plugins))
		for k := range reg.Plugins {
			available = append(available, k)
		}
		sort.Strings(available)
		return "", fmt.Errorf(
			"plugin '%s' not found in registry\n\nAvailable: %s\n\nFor unlisted plugins, use: branchkit-cli plugin install owner/repo",
			name, strings.Join(available, ", "),
		)
	}

	fmt.Printf("Resolved '%s' → %s\n", name, entry.Source)
	return entry.Source, nil
}

// isShortName returns true if the source looks like a short plugin name
// (no slash, no path prefix) rather than an owner/repo or local path.
func isShortName(source string) bool {
	return !strings.Contains(source, "/") && !isLocalPath(source)
}
