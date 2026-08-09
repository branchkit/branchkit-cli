package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// discoverPlugins scans all plugin search paths and returns discovered plugins.
// Deduplicates by manifest ID (first found wins: user > bundled > dev).
func discoverPlugins() []DiscoveredPlugin {
	type searchPath struct {
		dir    string
		source PluginSource
	}

	paths := []searchPath{
		{filepath.Join(appSupportDir(), "plugins"), SourceUser},
	}

	// Bundled: {executable}/../Contents/Resources/plugins
	if exe, err := os.Executable(); err == nil {
		bundled := filepath.Join(filepath.Dir(exe), "..", "Contents", "Resources", "plugins")
		paths = append(paths, searchPath{bundled, SourceBundled})
	}

	// Dev fallback
	paths = append(paths, searchPath{"plugins", SourceDev})

	seen := map[string]bool{}
	var discovered []DiscoveredPlugin

	for _, sp := range paths {
		if info, err := os.Stat(sp.dir); err != nil || !info.IsDir() {
			continue
		}

		entries, err := os.ReadDir(sp.dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			var manifestPath, manifestDir string

			if entry.IsDir() {
				// Nested layout: plugins/voice/plugin.json
				candidate := filepath.Join(sp.dir, entry.Name(), "plugin.json")
				if _, err := os.Stat(candidate); err == nil {
					manifestPath = candidate
					manifestDir = filepath.Join(sp.dir, entry.Name())
				}
			} else if strings.HasSuffix(entry.Name(), ".plugin.json") {
				// Flat layout: plugins/voice.plugin.json
				manifestPath = filepath.Join(sp.dir, entry.Name())
				manifestDir = sp.dir
			}

			if manifestPath == "" {
				continue
			}

			data, err := os.ReadFile(manifestPath)
			if err != nil {
				continue
			}

			var manifest PluginManifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				continue
			}

			if !validateID(manifest.ID) {
				continue
			}

			if seen[manifest.ID] {
				continue
			}
			seen[manifest.ID] = true

			discovered = append(discovered, DiscoveredPlugin{
				Manifest:    manifest,
				ManifestDir: manifestDir,
				Source:      sp.source,
			})
		}
	}

	return discovered
}

// pluginState is the authoritative enabled/status for one plugin, as reported
// by the running actuator.
type pluginState struct {
	Enabled bool
	Status  string
}

// fetchPluginStates queries the running actuator for authoritative
// enabled/status per plugin (GET /v1/plugins). Returns (states, true) when the
// actuator answered; (nil, false) when it isn't reachable — no host token, not
// running, or a non-200 — so callers degrade to "unknown" rather than reading a
// stale on-disk file. The actuator is the single source of truth for disabled
// state (it's the only writer); the old disabled_plugins.json the CLI used to
// read was never written by the actuator.
func fetchPluginStates() (map[string]pluginState, bool) {
	token := readHostToken()
	if token == "" {
		return nil, false
	}
	raw, status, err := devHTTP("GET", "/v1/plugins", token, nil)
	if err != nil || status != 200 {
		return nil, false
	}
	var items []struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	m := make(map[string]pluginState, len(items))
	for _, it := range items {
		m[it.ID] = pluginState{Enabled: it.Enabled, Status: it.Status}
	}
	return m, true
}
