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

// loadDisabledPlugins reads the disabled plugin IDs from disk.
func loadDisabledPlugins() map[string]bool {
	path := filepath.Join(appSupportDir(), "disabled_plugins.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]bool{}
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return map[string]bool{}
	}
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}
