package main

import (
	"fmt"
	"os"
)

// cmdUpdate updates a single plugin or all plugins with available updates.
// If pluginID is empty, updates all user-installed plugins that have newer releases.
func cmdUpdate(pluginID string) {
	if pluginID == "" {
		cmdUpdateAll()
		return
	}

	plugins := discoverPlugins()
	var found *DiscoveredPlugin
	for i := range plugins {
		if plugins[i].Manifest.ID == pluginID {
			found = &plugins[i]
			break
		}
	}
	if found == nil {
		fmt.Fprintf(os.Stderr, "Error: plugin '%s' not found\n", pluginID)
		os.Exit(1)
	}
	if found.Source != SourceUser {
		fmt.Fprintf(os.Stderr, "Error: plugin '%s' is %s — only user-installed plugins can be updated\n", pluginID, found.Source)
		os.Exit(1)
	}

	meta, ok := readSourceMeta(found.ManifestDir)
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: plugin '%s' has no install source metadata — cannot determine update source\n", pluginID)
		os.Exit(1)
	}

	fmt.Printf("Updating %s from %s...\n", pluginID, meta.Source)
	if err := installFromGitHub(meta.Source); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdUpdateAll() {
	updates := checkUpdatesForPlugins()

	if len(updates) == 0 {
		fmt.Println("All plugins are up to date.")
		return
	}

	fmt.Printf("Updating %d plugin(s)...\n\n", len(updates))
	var failed int
	for _, u := range updates {
		fmt.Printf("--- %s (%s → %s) ---\n", u.ID, u.Current, u.Latest)
		if err := installFromGitHub(u.Source); err != nil {
			fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", u.ID, err)
			failed++
			continue
		}
		fmt.Println()
	}

	if failed > 0 {
		fmt.Fprintf(os.Stderr, "\n%d plugin(s) failed to update.\n", failed)
		os.Exit(1)
	}
	fmt.Printf("Updated %d plugin(s) successfully.\n", len(updates))
}
