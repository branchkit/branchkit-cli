package main

import (
	"fmt"
	"io"
	"os"
	"sort"
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
	if len(m.Requires.Privileges) > 0 {
		fmt.Printf("Privileges:  %s\n", strings.Join(m.Requires.Privileges, ", "))
	}
	if len(m.Requires.OptionalPrivileges) > 0 {
		fmt.Printf("Optional:    %s\n", strings.Join(m.Requires.OptionalPrivileges, ", "))
	}
	if len(m.HudTargets) > 0 {
		fmt.Printf("HUD targets: %s\n", strings.Join(m.HudTargets, ", "))
	}
	printActionTypes(os.Stdout, m)
}

// printActionTypes lists the actions a plugin accepts, by the full name a
// command or a dispatch uses, with their params.
func printActionTypes(w io.Writer, m *PluginManifest) {
	if len(m.ActionTypes) == 0 {
		return
	}
	// Actions route by prefix; without one the plugin handles none, and
	// printing names under its id would invent names that do not route.
	if m.ActionPrefix == "" {
		return
	}
	prefix := m.ActionPrefix
	keys := make([]string, 0, len(m.ActionTypes))
	for k := range m.ActionTypes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintln(w, "Actions:")
	for _, k := range keys {
		a := m.ActionTypes[k]
		fmt.Fprintf(w, "  %s.%s", prefix, k)
		if a.Label != "" {
			fmt.Fprintf(w, "  %s", a.Label)
		}
		fmt.Fprintln(w)
		for _, f := range a.Fields {
			req := "optional"
			if f.Required {
				req = "required"
			}
			fmt.Fprintf(w, "      %s (%s, %s)", f.Key, f.FieldType, req)
			if f.Placeholder != "" {
				fmt.Fprintf(w, "  %s", f.Placeholder)
			}
			fmt.Fprintln(w)
		}
	}
}
