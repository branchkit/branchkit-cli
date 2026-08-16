package main

import (
	"archive/tar"
	"compress/bzip2"
	"crypto/sha256"
	"encoding/hex"
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
	// WhisperKit models — Hugging Face. argmaxinc publishes each model as a FOLDER
	// of CoreML bundles (*.mlmodelc) + config files, NOT a single archive, so we
	// snapshot the folder (recursive tree walk + per-file fetch). The old <model>.zip
	// URLs 404 — argmaxinc never hosted zips.
	//
	// TokenizerRepo is the ORIGINAL OpenAI repo, which argmaxinc's CoreML folder
	// does not carry — see provisionTokenizer. Stated per model rather than derived
	// from the name: the mapping is WhisperKit's (`tokenizerNameForVariant`), the
	// folder names carry release-date suffixes that would need stripping, and a
	// wrong guess fails at first dictation rather than at download.
	"whisperkit/openai_whisper-large-v3-v20240930": {
		HFRepo:        "argmaxinc/whisperkit-coreml",
		HFPath:        "openai_whisper-large-v3-v20240930",
		TokenizerRepo: "openai/whisper-large-v3",
		Size:          "1.5 GB",
	},
	"whisperkit/openai_whisper-base.en": {
		HFRepo:        "argmaxinc/whisperkit-coreml",
		HFPath:        "openai_whisper-base.en",
		TokenizerRepo: "openai/whisper-base.en",
		Size:          "~150 MB",
	},
	"whisperkit/openai_whisper-small.en": {
		HFRepo:        "argmaxinc/whisperkit-coreml",
		HFPath:        "openai_whisper-small.en",
		TokenizerRepo: "openai/whisper-small.en",
		Size:          "~500 MB",
	},
}

type modelEntry struct {
	HFRepo        string // Hugging Face repo, e.g. argmaxinc/whisperkit-coreml
	HFPath        string // model folder within the repo
	TokenizerRepo string // original OpenAI repo holding the tokenizer (WhisperKit models only)
	Size          string
}

// tokenizerFiles are what swift-transformers reads to build the tokenizer.
// `.cache/huggingface/download/*.metadata` sidecars are deliberately NOT
// written: they are Hub bookkeeping we don't need, and they are what a
// provenanced binary could not delete on Sequoia (the /bin/rm workaround the
// stage used to carry).
var tokenizerFiles = []string{"tokenizer.json", "tokenizer_config.json", "config.json"}

type downloadProgress struct {
	Model  string `json:"model"`
	Status string `json:"status"`
	Pct    int    `json:"pct,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	Total  int64  `json:"total,omitempty"`
	Error  string `json:"error,omitempty"`
}

func emitProgress(p downloadProgress) {
	data, _ := json.Marshal(p)
	fmt.Println(string(data))
}

func modelsDir() string {
	return filepath.Join(appSupportDir(), "models")
}

func cmdModelDownload(ref string) {
	// A model a PLUGIN declares wins: that is the path this tool is moving to,
	// and the compiled-in catalog below is what it is replacing.
	if m, ok := declaredModels()[ref]; ok {
		provisionDeclaredModel(m)
		return
	}

	// LEGACY — the compiled-in catalog. Deleted when the stt stage moves into
	// voice and declares these three models itself; until then the WhisperKit
	// models live at `models/whisperkit/<name>` and the actuator's hardcoded
	// family grant is what admits them. See the sequence in
	// notes/DESIGN_PLUGIN_MODEL_DECLARATION.md.
	entry, ok := modelCatalog[ref]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown model: %s\n\nAvailable models:\n", ref)
		for key, e := range modelCatalog {
			fmt.Fprintf(os.Stderr, "  %-50s %s\n", key, e.Size)
		}
		printDeclaredModels(os.Stderr)
		os.Exit(1)
	}

	parts := strings.SplitN(ref, "/", 2)
	engine := parts[0]
	modelName := parts[1]

	destDir := filepath.Join(modelsDir(), engine, modelName)
	if fileExists(destDir) {
		// Not a plain early-return: models downloaded before tokenizer
		// provisioning existed are on disk WITHOUT one, and the stage now runs
		// network-denied, so their first dictation would fail. Repair in place
		// (a no-op once provisioned) rather than making users delete the model.
		if err := provisionTokenizer(ref, entry, destDir); err != nil {
			emitProgress(downloadProgress{Model: ref, Status: "error", Error: err.Error()})
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		emitProgress(downloadProgress{Model: ref, Status: "exists"})
		fmt.Fprintf(os.Stderr, "Model already downloaded: %s\n", destDir)
		return
	}

	emitProgress(downloadProgress{Model: ref, Status: "downloading", Pct: 0})

	if err := snapshotHFFolder(ref, entry.HFRepo, entry.HFPath, destDir); err != nil {
		emitProgress(downloadProgress{Model: ref, Status: "error", Error: err.Error()})
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if !fileExists(destDir) {
		emitProgress(downloadProgress{Model: ref, Status: "error", Error: "download did not produce expected directory"})
		fmt.Fprintf(os.Stderr, "Error: download did not produce expected directory: %s\n", destDir)
		os.Exit(1)
	}

	if err := provisionTokenizer(ref, entry, destDir); err != nil {
		emitProgress(downloadProgress{Model: ref, Status: "error", Error: err.Error()})
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	emitProgress(downloadProgress{Model: ref, Status: "done"})
	fmt.Fprintf(os.Stderr, "Model downloaded to %s\n", destDir)
}

// provisionTokenizer fetches the tokenizer WhisperKit needs into the model dir.
//
// Why this exists: `download: false` on WhisperKitConfig disables MODEL WEIGHT
// downloads only. The tokenizer lives in the original OpenAI repo, not in
// argmaxinc's CoreML folder, and WhisperKit falls back to fetching it from
// huggingface.co at load time when it is not on disk. The stt stage now runs
// network-denied, so that fetch is refused and the stage fails to load — which
// is the sandbox working, and makes provisioning this side of the boundary a
// requirement rather than an optimization (DESIGN_BUILTIN_STAGE_SANDBOX.md in
// branchkit/app).
//
// Files land in the layout WhisperKit's Hub-shaped search path expects:
// <model>/models/<org>/<name>/. That is where a real (unsandboxed) load leaves
// them, so this reproduces a known-good on-disk state rather than a new one.
func provisionTokenizer(ref string, entry modelEntry, destDir string) error {
	if entry.TokenizerRepo == "" {
		return nil // non-WhisperKit model, or one with the tokenizer in-folder
	}
	tokenizerDir := filepath.Join(destDir, "models", filepath.FromSlash(entry.TokenizerRepo))
	if fileExists(filepath.Join(tokenizerDir, "tokenizer.json")) {
		return nil // already provisioned (or seeded by an earlier live load)
	}

	emitProgress(downloadProgress{Model: ref, Status: "downloading-tokenizer"})
	// Staged then renamed, like the model snapshot: a half-written tokenizer
	// dir would satisfy the presence check above on the next run and then fail
	// at load, which is a much worse failure than re-downloading a few files.
	staging := tokenizerDir + ".partial"
	os.RemoveAll(staging)
	defer os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	for _, name := range tokenizerFiles {
		url := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", entry.TokenizerRepo, name)
		if err := httpGetToFile(client, url, filepath.Join(staging, name)); err != nil {
			return fmt.Errorf("download tokenizer %s from %s: %w", name, entry.TokenizerRepo, err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(tokenizerDir), 0o755); err != nil {
		return err
	}
	return os.Rename(staging, tokenizerDir)
}

// snapshotHFFolder downloads a Hugging Face repo subfolder (recursively) into
// destDir, atomically: files land in a sibling ".partial" staging dir that is
// renamed into place only after every file succeeds, so an interrupted download
// never looks like a ready model.
func snapshotHFFolder(ref, repo, folder, destDir string) error {
	if repo == "" || folder == "" {
		return fmt.Errorf("model has no Hugging Face source")
	}
	files, err := hfListFiles(repo, folder)
	if err != nil {
		return fmt.Errorf("list %s/%s: %w", repo, folder, err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no files found at %s/%s", repo, folder)
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}

	staging := destDir + ".partial"
	os.RemoveAll(staging)
	defer os.RemoveAll(staging)

	client := &http.Client{Timeout: 30 * time.Minute}
	prefix := folder + "/"
	var done int64
	lastPct := -1
	for _, f := range files {
		rel := strings.TrimPrefix(f.Path, prefix)
		out := filepath.Join(staging, rel)
		if !strings.HasPrefix(filepath.Clean(out), filepath.Clean(staging)) {
			return fmt.Errorf("path traversal in %s", f.Path)
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		url := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", repo, f.Path)
		if err := httpGetToFile(client, url, out); err != nil {
			return fmt.Errorf("download %s: %w", rel, err)
		}
		done += f.Size
		if total > 0 {
			pct := int(done * 100 / total)
			if pct != lastPct && pct%5 == 0 {
				lastPct = pct
				emitProgress(downloadProgress{Model: ref, Status: "downloading", Pct: pct, Bytes: done, Total: total})
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(destDir), 0o755); err != nil {
		return err
	}
	return os.Rename(staging, destDir)
}

type hfTreeEntry struct {
	Type string `json:"type"` // "file" | "directory"
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// hfListFiles recursively lists every file under repo/folder via the HF tree API.
// (WhisperKit model folders are small — tens of files — so no pagination needed.)
func hfListFiles(repo, folder string) ([]hfTreeEntry, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	var out []hfTreeEntry
	var walk func(path string) error
	walk = func(path string) error {
		url := fmt.Sprintf("https://huggingface.co/api/models/%s/tree/main/%s", repo, path)
		resp, err := client.Get(url)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
		}
		var entries []hfTreeEntry
		if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
			return err
		}
		for _, e := range entries {
			if e.Type == "directory" {
				if err := walk(e.Path); err != nil {
					return err
				}
			} else {
				out = append(out, e)
			}
		}
		return nil
	}
	if err := walk(folder); err != nil {
		return nil, err
	}
	return out, nil
}

// httpGetToFile downloads url to dest (overwriting).
func httpGetToFile(client *http.Client, url, dest string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
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
						Model:  ref,
						Status: "downloading",
						Pct:    pct,
						Bytes:  written,
						Total:  total,
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

func verifySHA256(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, expected) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", filepath.Base(path), got, expected)
	}
	return nil
}

// extractTarBz2Members writes the named members (matched by basename) from a
// .tar.bz2 into destDir, flattening any leading archive directories.
func extractTarBz2Members(archivePath, destDir string, members []string) error {
	want := make(map[string]bool, len(members))
	for _, m := range members {
		want[m] = true
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	tr := tar.NewReader(bzip2.NewReader(f))
	found := make(map[string]bool, len(members))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		base := filepath.Base(hdr.Name)
		if !want[base] {
			continue
		}
		out, err := os.Create(filepath.Join(destDir, base))
		if err != nil {
			return err
		}
		// hdr.Size is bounded by the archive; copy the declared length.
		if _, err := io.CopyN(out, tr, hdr.Size); err != nil {
			out.Close()
			return err
		}
		out.Close()
		found[base] = true
	}
	for m := range want {
		if !found[m] {
			return fmt.Errorf("archive missing expected member %s", m)
		}
	}
	return nil
}
