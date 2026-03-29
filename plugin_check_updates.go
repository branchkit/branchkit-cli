package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// UpdateInfo describes an available update for one plugin.
type UpdateInfo struct {
	ID      string `json:"id"`
	Current string `json:"current"`
	Latest  string `json:"latest"`
	Source  string `json:"source"`
}

// checkUpdatesForPlugins returns update info for all user-installed plugins with newer releases.
func checkUpdatesForPlugins() []UpdateInfo {
	plugins := discoverPlugins()

	var updates []UpdateInfo
	for _, dp := range plugins {
		if dp.Source != SourceUser {
			continue
		}
		meta, ok := readSourceMeta(dp.ManifestDir)
		if !ok {
			continue
		}
		// source-build installs can't be version-checked against releases
		if meta.InstalledTag == "source-build" {
			continue
		}

		parsed, err := parseGitHubSource(meta.Source)
		if err != nil {
			continue
		}

		latest, err := fetchLatestTag(parsed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARN: could not check %s: %v\n", dp.Manifest.ID, err)
			continue
		}

		if latest != meta.InstalledTag {
			updates = append(updates, UpdateInfo{
				ID:      dp.Manifest.ID,
				Current: meta.InstalledTag,
				Latest:  latest,
				Source:  meta.Source,
			})
		}
	}
	return updates
}

func cmdCheckUpdates() {
	updates := checkUpdatesForPlugins()
	if updates == nil {
		updates = []UpdateInfo{}
	}
	data, _ := json.Marshal(updates)
	fmt.Println(string(data))
}
