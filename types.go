package main

import (
	"os"
	"path/filepath"
)

// PluginManifest represents the plugin.json manifest — only fields the CLI needs.
type PluginManifest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Description  string   `json:"description"`
	Author       string   `json:"author"`
	Run          string   `json:"run,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
	ActionPrefix string   `json:"action_prefix,omitempty"`
	HudTargets   []string `json:"hud_targets,omitempty"`
}

// PluginSource indicates where a plugin was discovered.
type PluginSource string

const (
	SourceUser    PluginSource = "user"
	SourceBundled PluginSource = "bundled"
	SourceDev     PluginSource = "dev"
)

// DiscoveredPlugin is a plugin found on disk with its manifest, directory, and source.
type DiscoveredPlugin struct {
	Manifest    PluginManifest
	ManifestDir string
	Source      PluginSource
}

// appSupportDir returns the BranchKit app support directory.
func appSupportDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/branchkit-fallback"
	}
	name := "BranchKit"
	if os.Getenv("BRANCHKIT_DEV") != "" {
		name = "BranchKitDev"
	}
	dir := filepath.Join(home, "Library", "Application Support", name)
	os.MkdirAll(dir, 0o755)
	return dir
}

// userPluginsDir returns the user-installed plugins directory.
func userPluginsDir() string {
	return filepath.Join(appSupportDir(), "plugins")
}
