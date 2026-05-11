package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// modelCatalog defines known models and their download sources.
// The voice plugin has a parallel catalog for UI display — this one
// has the URLs the CLI needs to actually fetch them.
var modelCatalog = map[string]modelEntry{
	// Vosk models — upstream GitHub releases
	"vosk/vosk-model-small-en-us-0.15": {
		URL:  "https://alphacephei.com/vosk/models/vosk-model-small-en-us-0.15.zip",
		Size: "68 MB",
	},
	"vosk/vosk-model-en-us-0.22-lgraph": {
		URL:  "https://alphacephei.com/vosk/models/vosk-model-en-us-0.22-lgraph.zip",
		Size: "204 MB",
	},
	"vosk/vosk-model-en-us-0.22": {
		URL:  "https://alphacephei.com/vosk/models/vosk-model-en-us-0.22.zip",
		Size: "1.8 GB",
	},

	// WhisperKit models — Hugging Face
	"whisperkit/openai_whisper-large-v3-v20240930": {
		URL:  "https://huggingface.co/argmaxinc/whisperkit-coreml/resolve/main/openai_whisper-large-v3-v20240930.zip",
		Size: "1.62 GB",
	},
	"whisperkit/openai_whisper-base.en": {
		URL:  "https://huggingface.co/argmaxinc/whisperkit-coreml/resolve/main/openai_whisper-base.en.zip",
		Size: "67 MB",
	},
	"whisperkit/openai_whisper-small.en": {
		URL:  "https://huggingface.co/argmaxinc/whisperkit-coreml/resolve/main/openai_whisper-small.en.zip",
		Size: "166 MB",
	},
}

type modelEntry struct {
	URL  string
	Size string
}

type downloadProgress struct {
	Model   string `json:"model"`
	Status  string `json:"status"`
	Pct     int    `json:"pct,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	Total   int64  `json:"total,omitempty"`
	Error   string `json:"error,omitempty"`
}

func emitProgress(p downloadProgress) {
	data, _ := json.Marshal(p)
	fmt.Println(string(data))
}

func modelsDir() string {
	return filepath.Join(appSupportDir(), "models")
}

func cmdModelDownload(ref string) {
	entry, ok := modelCatalog[ref]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown model: %s\n\nAvailable models:\n", ref)
		for key, e := range modelCatalog {
			fmt.Fprintf(os.Stderr, "  %-50s %s\n", key, e.Size)
		}
		os.Exit(1)
	}

	parts := strings.SplitN(ref, "/", 2)
	engine := parts[0]
	modelName := parts[1]

	destDir := filepath.Join(modelsDir(), engine, modelName)
	if fileExists(destDir) {
		emitProgress(downloadProgress{Model: ref, Status: "exists"})
		fmt.Fprintf(os.Stderr, "Model already downloaded: %s\n", destDir)
		return
	}

	emitProgress(downloadProgress{Model: ref, Status: "downloading", Pct: 0})

	tmpFile, err := os.CreateTemp("", "branchkit-model-*")
	if err != nil {
		emitProgress(downloadProgress{Model: ref, Status: "error", Error: err.Error()})
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	if err := downloadModelFile(ref, entry.URL, tmpPath); err != nil {
		emitProgress(downloadProgress{Model: ref, Status: "error", Error: err.Error()})
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	emitProgress(downloadProgress{Model: ref, Status: "extracting"})

	extractDir := filepath.Join(modelsDir(), engine)
	os.MkdirAll(extractDir, 0o755)

	if strings.HasSuffix(entry.URL, ".zip") {
		if err := extractModelZip(tmpPath, extractDir); err != nil {
			emitProgress(downloadProgress{Model: ref, Status: "error", Error: err.Error()})
			fmt.Fprintf(os.Stderr, "Error extracting: %v\n", err)
			os.Exit(1)
		}
	} else {
		if err := extractTarball(tmpPath, extractDir); err != nil {
			emitProgress(downloadProgress{Model: ref, Status: "error", Error: err.Error()})
			fmt.Fprintf(os.Stderr, "Error extracting: %v\n", err)
			os.Exit(1)
		}
	}

	if !fileExists(destDir) {
		emitProgress(downloadProgress{Model: ref, Status: "error", Error: "extraction did not produce expected directory"})
		fmt.Fprintf(os.Stderr, "Error: extraction did not produce expected directory: %s\n", destDir)
		os.Exit(1)
	}

	emitProgress(downloadProgress{Model: ref, Status: "done"})
	fmt.Fprintf(os.Stderr, "Model downloaded to %s\n", destDir)
}

func downloadModelFile(ref, url, destPath string) error {
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	outFile, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	total := resp.ContentLength
	var written int64
	buf := make([]byte, 256*1024)
	lastPct := -1

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := outFile.Write(buf[:n]); err != nil {
				return err
			}
			written += int64(n)

			if total > 0 {
				pct := int(written * 100 / total)
				if pct != lastPct && pct%5 == 0 {
					lastPct = pct
					emitProgress(downloadProgress{
						Model: ref,
						Status: "downloading",
						Pct:   pct,
						Bytes: written,
						Total: total,
					})
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	return nil
}

func extractModelZip(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		target := filepath.Join(destDir, f.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)) {
			return fmt.Errorf("archive contains path traversal: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0o755)
			continue
		}

		if f.FileInfo().Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not allowed in model archives: %s", f.Name)
		}

		os.MkdirAll(filepath.Dir(target), 0o755)
		src, err := f.Open()
		if err != nil {
			return err
		}
		dst, err := os.Create(target)
		if err != nil {
			src.Close()
			return err
		}
		_, err = io.Copy(dst, src)
		src.Close()
		dst.Close()
		if err != nil {
			return err
		}
	}

	return nil
}


