package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func cmdRemove(pluginID string) {
	discovered := discoverPlugins()

	var dp *DiscoveredPlugin
	for i := range discovered {
		if discovered[i].Manifest.ID == pluginID {
			dp = &discovered[i]
			break
		}
	}
	if dp == nil {
		fmt.Fprintf(os.Stderr, "Plugin '%s' not found.\n", pluginID)
		os.Exit(1)
	}

	switch dp.Source {
	case SourceBundled:
		fmt.Fprintf(os.Stderr, "Plugin '%s' is bundled with BranchKit and cannot be removed.\n", pluginID)
		os.Exit(1)
	case SourceDev:
		fmt.Fprintf(os.Stderr, "Plugin '%s' is in the local dev directory. Remove it manually from %s.\n", pluginID, dp.ManifestDir)
		os.Exit(1)
	}

	if err := os.RemoveAll(dp.ManifestDir); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to remove plugin: %v\n", err)
		os.Exit(1)
	}
	removeBlobs(pluginID)
	fmt.Printf("Removed plugin '%s'.\n", pluginID)
	notifyActuator()
}

// removeBlobs clears the blobs a removed plugin provided and the platform's
// record of them. `lifetime: persistent` promises the bytes last until
// uninstall, and nothing did the uninstall half: they outlived the plugin.
// os.RemoveAll removes a symlink rather than following it, so a link the
// provider planted in its own blob directory cannot redirect this.
func removeBlobs(pluginID string) {
	root := appSupportDir()
	for _, p := range []string{
		filepath.Join(root, "blobs", pluginID),
		filepath.Join(root, "blob-state", pluginID+".json"),
	} {
		if err := os.RemoveAll(p); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove %s: %v\n", p, err)
		}
	}
}
