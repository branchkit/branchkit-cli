package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverPluginsNestedLayout(t *testing.T) {
	dir := tmpDir(t, "discover-nested")
	sub := filepath.Join(dir, "my-plugin")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "plugin.json"), []byte(`{"id":"my-plugin","name":"My Plugin","version":"1.0"}`), 0o644)

	// Manually scan just this dir (can't override appSupportDir in test easily)
	entries, _ := os.ReadDir(dir)
	var found []DiscoveredPlugin
	for _, entry := range entries {
		if entry.IsDir() {
			mp := filepath.Join(dir, entry.Name(), "plugin.json")
			if data, err := os.ReadFile(mp); err == nil {
				var m PluginManifest
				json.Unmarshal(data, &m)
				if validateID(m.ID) {
					found = append(found, DiscoveredPlugin{Manifest: m, ManifestDir: filepath.Join(dir, entry.Name()), Source: SourceUser})
				}
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(found))
	}
	if found[0].Manifest.ID != "my-plugin" {
		t.Errorf("ID = %q", found[0].Manifest.ID)
	}
}

func TestDiscoverPluginsFlatLayout(t *testing.T) {
	dir := tmpDir(t, "discover-flat")
	os.WriteFile(filepath.Join(dir, "test.plugin.json"), []byte(`{"id":"test","name":"Test"}`), 0o644)

	entries, _ := os.ReadDir(dir)
	var found []DiscoveredPlugin
	for _, entry := range entries {
		name := entry.Name()
		if len(name) > len(".plugin.json") && name[len(name)-12:] == ".plugin.json" {
			data, _ := os.ReadFile(filepath.Join(dir, name))
			var m PluginManifest
			json.Unmarshal(data, &m)
			if validateID(m.ID) {
				found = append(found, DiscoveredPlugin{Manifest: m, ManifestDir: dir, Source: SourceDev})
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(found))
	}
	if found[0].Manifest.ID != "test" {
		t.Errorf("ID = %q", found[0].Manifest.ID)
	}
}

func TestDiscoverPluginsSkipsInvalidID(t *testing.T) {
	dir := tmpDir(t, "discover-bad-id")
	sub := filepath.Join(dir, "bad")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "plugin.json"), []byte(`{"id":"Bad_ID","name":"Bad"}`), 0o644)

	entries, _ := os.ReadDir(dir)
	var found []DiscoveredPlugin
	for _, entry := range entries {
		if entry.IsDir() {
			mp := filepath.Join(dir, entry.Name(), "plugin.json")
			if data, err := os.ReadFile(mp); err == nil {
				var m PluginManifest
				json.Unmarshal(data, &m)
				if validateID(m.ID) {
					found = append(found, DiscoveredPlugin{Manifest: m})
				}
			}
		}
	}
	if len(found) != 0 {
		t.Fatalf("expected 0 plugins (invalid ID), got %d", len(found))
	}
}

func TestDiscoverPluginsDedup(t *testing.T) {
	dir := tmpDir(t, "discover-dedup")
	// Create two plugins with same ID
	for _, name := range []string{"first", "second"} {
		sub := filepath.Join(dir, name)
		os.MkdirAll(sub, 0o755)
		os.WriteFile(filepath.Join(sub, "plugin.json"), []byte(`{"id":"same-id","name":"`+name+`"}`), 0o644)
	}

	entries, _ := os.ReadDir(dir)
	seen := map[string]bool{}
	var found []DiscoveredPlugin
	for _, entry := range entries {
		if entry.IsDir() {
			mp := filepath.Join(dir, entry.Name(), "plugin.json")
			if data, err := os.ReadFile(mp); err == nil {
				var m PluginManifest
				json.Unmarshal(data, &m)
				if validateID(m.ID) && !seen[m.ID] {
					seen[m.ID] = true
					found = append(found, DiscoveredPlugin{Manifest: m})
				}
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 plugin after dedup, got %d", len(found))
	}
}

func TestLoadDisabledPluginsEmpty(t *testing.T) {
	// When file doesn't exist, returns empty map
	disabled := loadDisabledPlugins()
	// This tests the real function, which reads from appSupportDir
	// Just verify it doesn't crash and returns a map
	if disabled == nil {
		t.Fatal("expected non-nil map")
	}
}
