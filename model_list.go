package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type modelInfo struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   string `json:"size"`
}

func cmdModelList() {
	models := discoverModels()
	if len(models) == 0 {
		fmt.Println("No models installed.")
		fmt.Println()
		fmt.Println("Download a model:")
		fmt.Println("  branchkit-cli model download vosk/vosk-model-small-en-us-0.15")
		fmt.Println("  branchkit-cli model download whisperkit/openai_whisper-large-v3-v20240930")
		return
	}

	useJSON := len(os.Args) >= 4 && os.Args[3] == "--json"
	if useJSON {
		data, _ := json.Marshal(models)
		fmt.Println(string(data))
		return
	}

	for _, m := range models {
		fmt.Printf("  %-10s %-45s %s\n", m.Engine, m.Name, m.Size)
	}
}

func discoverModels() []modelInfo {
	base := modelsDir()
	var models []modelInfo

	engines, err := os.ReadDir(base)
	if err != nil {
		return nil
	}

	for _, eng := range engines {
		if !eng.IsDir() {
			continue
		}
		engineName := eng.Name()
		engineDir := filepath.Join(base, engineName)

		entries, err := os.ReadDir(engineDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			modelPath := filepath.Join(engineDir, entry.Name())
			models = append(models, modelInfo{
				Engine: engineName,
				Name:   entry.Name(),
				Path:   modelPath,
				Size:   dirSize(modelPath),
			})
		}
	}

	return models
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
