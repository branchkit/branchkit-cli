package main

import (
	"fmt"
	"os"
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
	fmt.Printf("Removed plugin '%s'.\n", pluginID)
	notifyActuator()
}
