package main

import (
	"fmt"
	"os"
	"strings"
)

func cmdInfo(pluginID string) {
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

	m := &dp.Manifest
	fmt.Printf("ID:          %s\n", m.ID)
	fmt.Printf("Name:        %s\n", m.Name)
	fmt.Printf("Version:     %s\n", m.Version)
	fmt.Printf("Description: %s\n", m.Description)
	fmt.Printf("Author:      %s\n", m.Author)
	fmt.Printf("Source:      %s\n", dp.Source)
	fmt.Printf("Directory:   %s\n", dp.ManifestDir)

	if m.Run != "" {
		fmt.Printf("Run:         %s\n", m.Run)
	}
	if m.ActionPrefix != "" {
		fmt.Printf("Action prefix: %s\n", m.ActionPrefix)
	}
	if len(m.Capabilities) > 0 {
		fmt.Printf("Capabilities: %s\n", strings.Join(m.Capabilities, ", "))
	}
	if len(m.DependsOn) > 0 {
		fmt.Printf("Depends on:  %s\n", strings.Join(m.DependsOn, ", "))
	}
	if len(m.HudTargets) > 0 {
		fmt.Printf("HUD targets: %s\n", strings.Join(m.HudTargets, ", "))
	}
}
