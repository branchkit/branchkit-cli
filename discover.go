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

	// Bundled: the plugins shipped inside the enclosing .app.
	//
	// Derived from the enclosing bundle rather than by counting `..` levels
	// from the executable. The old spelling was
	// `{exe}/../Contents/Resources/plugins`, which is only correct for a
	// binary in `Contents/MacOS`; this CLI ships in `Contents/Resources`, so
	// it resolved to `<app>/Contents/Contents/Resources/plugins` and the
	// bundled plugins were never found. Invisible until model provisioning
	// started depending on it — in development the app-support symlinks
	// answer first, and every bundled run silently fell through to the dev
	// path.
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		if app := enclosingAppBundle(exe); app != "" {
			paths = append(paths, searchPath{filepath.Join(app, "Contents", "Resources", "plugins"), SourceBundled})
		} else {
			// Non-macOS packaging: plugins sit next to the binary.
			paths = append(paths, searchPath{filepath.Join(filepath.Dir(exe), "plugins"), SourceBundled})
		}
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
		m[it.ID] = pluginState{Enabled: it.Enabled}
	}
	return m, true
}

// enclosingAppBundle returns the nearest ancestor directory ending in `.app`,
// or "" when the path is not inside one (a development build tree).
func enclosingAppBundle(path string) string {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if strings.HasSuffix(dir, ".app") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
	}
}
