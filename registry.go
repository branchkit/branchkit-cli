package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const registryURL = "https://raw.githubusercontent.com/branchkit/registry/main/registry.json"

type registry struct {
	Version int                       `json:"version"`
	Plugins map[string]registryEntry  `json:"plugins"`
}

type registryEntry struct {
	Source      string   `json:"source"`
	Description string   `json:"description"`
	Categories  []string `json:"categories"`
	Verified    bool     `json:"verified"`
}

// resolveShortName looks up a short plugin name in the registry.
// Returns the full owner/repo source, or an error if not found.
func resolveShortName(name string) (string, error) {
	fmt.Printf("Looking up '%s' in registry...\n", name)

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", registryURL, nil)
	if err != nil {
		return "", fmt.Errorf("invalid registry URL: %w", err)
	}
	req.Header.Set("User-Agent", "branchkit-cli")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch registry: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("registry returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read registry: %w", err)
	}

	var reg registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return "", fmt.Errorf("failed to parse registry: %w", err)
	}

	entry, ok := reg.Plugins[name]
	if !ok {
		return "", fmt.Errorf("plugin '%s' not found in registry — use owner/repo format for unlisted plugins", name)
	}

	fmt.Printf("Resolved '%s' → %s\n", name, entry.Source)
	return entry.Source, nil
}

// isShortName returns true if the source looks like a short plugin name
// (no slash, no path prefix) rather than an owner/repo or local path.
func isShortName(source string) bool {
	return !strings.Contains(source, "/") && !isLocalPath(source)
}
