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
	updates, _, _ := checkUpdatesCounted()
	return updates
}

// checkUpdatesCounted is checkUpdatesForPlugins, also counting the plugins
// whose source answered (reached) and those it could not ask (unreached).
// A plugin with no GitHub source or a source-build install is neither.
func checkUpdatesCounted() (updates []UpdateInfo, reached, unreached int) {
	plugins := discoverPlugins()

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
			unreached++
			continue
		}
		reached++

		if latest != meta.InstalledTag {
			updates = append(updates, UpdateInfo{
				ID:      dp.Manifest.ID,
				Current: meta.InstalledTag,
				Latest:  latest,
				Source:  meta.Source,
			})
		}
	}
	return updates, reached, unreached
}

// checkSummary is the line check-updates ends its stderr with, for the app:
// a check that reached nothing (offline) is retried sooner than one that did.
func checkSummary(reached, unreached int, catalogOK bool) string {
	catalog := "ok"
	if !catalogOK {
		catalog = "failed"
	}
	return fmt.Sprintf("CHECKED reached=%d unreached=%d catalog=%s", reached, unreached, catalog)
}

func cmdCheckUpdates() {
	// The app runs this on its update check, so this is also when its
	// listing of the registry is refreshed (fetchCatalog keeps it): Get
	// plugins then lists what the registry lists now. A failure only leaves
	// the old copy.
	_, catalogErr := fetchCatalog()
	updates, reached, unreached := checkUpdatesCounted()
	if updates == nil {
		updates = []UpdateInfo{}
	}
	data, _ := json.Marshal(updates)
	fmt.Println(string(data))
	fmt.Fprintln(os.Stderr, checkSummary(reached, unreached, catalogErr == nil))
}
