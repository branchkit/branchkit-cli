package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type modelInfo struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   string `json:"size"`
}

func cmdModelList() {
	models := discoverModels()
	useJSON := len(os.Args) >= 4 && os.Args[3] == "--json"
	if useJSON {
		data, _ := json.Marshal(models)
		fmt.Println(string(data))
		return
	}

	if len(models) == 0 {
		fmt.Println("No models installed.")
	}
	for _, m := range models {
		fmt.Printf("  %-10s %-45s %s\n", m.Engine, m.Name, m.Size)
	}
	printDeclaredModels(os.Stdout)
	// Anything at the models root that is neither a plugin namespace nor a
	// declared model is REPORTED, not hidden. Retired engines leave their model
	// dirs behind, and a listing that quietly skips what it does not recognize
	// is how `models/sherpa` and `models/sherpa-offline` sat unnoticed.
	if unclaimed := unclaimedModelDirs(); len(unclaimed) > 0 {
		fmt.Println("\nUnclaimed directories under models/ (no plugin claims these):")
		for _, u := range unclaimed {
			fmt.Printf("  %-45s %s\n", u, dirSize(filepath.Join(modelsDir(), u)))
		}
	}
}

// discoverModels walks `<models>/<plugin_id>/<model>`. The engine column is the
// owning plugin, which is what the directory level actually means since model
// dirs became namespaced.
func discoverModels() []modelInfo {
	base := modelsDir()
	var models []modelInfo

	owners, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	known := knownPluginIDs()

	for _, owner := range owners {
		if !owner.IsDir() || strings.HasPrefix(owner.Name(), ".") {
			continue
		}
		if !known[owner.Name()] {
			continue // reported separately as unclaimed
		}
		ownerDir := filepath.Join(base, owner.Name())
		entries, err := os.ReadDir(ownerDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			modelPath := filepath.Join(ownerDir, entry.Name())
			models = append(models, modelInfo{
				Engine: owner.Name(),
				Name:   entry.Name(),
				Path:   modelPath,
				Size:   dirSize(modelPath),
			})
		}
	}

	return models
}

func knownPluginIDs() map[string]bool {
	out := map[string]bool{}
	for _, dp := range discoverPlugins() {
		out[dp.Manifest.ID] = true
	}
	return out
}

func unclaimedModelDirs() []string {
	entries, err := os.ReadDir(modelsDir())
	if err != nil {
		return nil
	}
	known := knownPluginIDs()
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || known[e.Name()] {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func dirSize(path string) string {
	var total int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})

	switch {
	case total >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(total)/float64(1<<30))
	case total >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(total)/float64(1<<20))
	default:
		return fmt.Sprintf("%.0f KB", float64(total)/float64(1<<10))
	}
}
