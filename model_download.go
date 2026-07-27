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
	"whisperkit/openai_whisper-large-v3-v20240930": {
		HFRepo: "argmaxinc/whisperkit-coreml",
		HFPath: "openai_whisper-large-v3-v20240930",
		Size:   "1.5 GB",
	},
	"whisperkit/openai_whisper-base.en": {
		HFRepo: "argmaxinc/whisperkit-coreml",
		HFPath: "openai_whisper-base.en",
		Size:   "~150 MB",
	},
	"whisperkit/openai_whisper-small.en": {
		HFRepo: "argmaxinc/whisperkit-coreml",
		HFPath: "openai_whisper-small.en",
		Size:   "~500 MB",
	},
}

type modelEntry struct {
	HFRepo string // Hugging Face repo, e.g. argmaxinc/whisperkit-coreml
	HFPath string // model folder within the repo
	Size   string
}

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
	// Sherpa (NeMo) needs a multi-file assembly (two SHA-pinned downloads + four
	// vendored small files) into a FLAT model dir, unlike the single-archive
	// whisperkit/sherpa path below — handled separately.
	if ref == sherpaModelRef {
		assembleSherpaModel(ref)
		return
	}

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

	emitProgress(downloadProgress{Model: ref, Status: "done"})
	fmt.Fprintf(os.Stderr, "Model downloaded to %s\n", destDir)
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

// --- Sherpa (NeMo) offline command model -------------------------------------
//
// Unlike whisperkit (one archive → models/<engine>/<model>), the sherpa
// command model is assembled into a FLAT dir (models/sherpa-offline-nemo, where
// the stage looks) from two SHA-pinned downloads plus the small generic tokenizer
// files vendored in the app bundle. The big model.onnx is downloaded fresh; the
// tokenizer (bpe.model + bpe.vocab) comes from the bundle so the user's machine
// needs no Python build toolchain. No command grammar is vendored — the stage
// self-seeds it in-process from the live vocabulary. Mirrors `just sherpa-model-nemo`.

const (
	sherpaModelRef  = "sherpa/sherpa-offline-nemo"
	sherpaModelName = "sherpa-offline-nemo"
)

// sherpaDownload is one SHA-pinned fetch. If tarMembers is set, the download is a
// .tar.bz2 and those basenames are extracted; otherwise it's a single file saved
// as destName.
type sherpaDownload struct {
	url        string
	sha256     string
	tarMembers []string
	destName   string
}

var sherpaDownloads = []sherpaDownload{
	{
		url:        "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-ctc-en-conformer-medium.tar.bz2",
		sha256:     "08cb7b6ebc516a2577c5b152230730ebf5f937507260305ea592c7accd7f899b",
		tarMembers: []string{"model.onnx", "tokens.txt"},
	},
	{
		url:      "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx",
		sha256:   "9e2449e1087496d8d4caba907f23e0bd3f78d91fa552479bb9c23ac09cbb1fd6",
		destName: "silero_vad.onnx",
	},
}

// Small generic tokenizer files copied from the app bundle (sherpa-assets/<name>/),
// not downloaded. The command grammar is not vendored — the stage self-seeds it.
var sherpaBundledAssets = []string{"bpe.model", "bpe.vocab"}

func assembleSherpaModel(ref string) {
	destDir := filepath.Join(modelsDir(), sherpaModelName)
	if fileExists(destDir) {
		emitProgress(downloadProgress{Model: ref, Status: "exists"})
		fmt.Fprintf(os.Stderr, "Model already downloaded: %s\n", destDir)
		return
	}

	// The vendored tokenizer files ride in the app bundle next to this binary.
	// Fail before any download if they're absent (a bare/unbundled CLI can't
	// provision sherpa — dev uses `just sherpa-model-nemo`).
	assetsDir, err := sherpaAssetsDir()
	if err == nil {
		for _, a := range sherpaBundledAssets {
			if !fileExists(filepath.Join(assetsDir, a)) {
				err = fmt.Errorf("bundled asset missing: %s", filepath.Join(assetsDir, a))
				break
			}
		}
	}
	if err != nil {
		sherpaFail(ref, fmt.Errorf("sherpa assets unavailable (provision from the bundled app): %w", err))
	}

	// Assemble in a sibling staging dir on the same filesystem, then rename into
	// place atomically so a partial download never looks like a ready model.
	staging := filepath.Join(modelsDir(), "."+sherpaModelName+".partial")
	os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		sherpaFail(ref, err)
	}
	defer os.RemoveAll(staging)

	emitProgress(downloadProgress{Model: ref, Status: "downloading", Pct: 0})
	for _, d := range sherpaDownloads {
		if len(d.tarMembers) > 0 {
			tmp, err := os.CreateTemp("", "branchkit-sherpa-*.tar.bz2")
			if err != nil {
				sherpaFail(ref, err)
			}
			tmpPath := tmp.Name()
			tmp.Close()
			if err := downloadModelFile(ref, d.url, tmpPath); err != nil {
				os.Remove(tmpPath)
				sherpaFail(ref, err)
			}
			if err := verifySHA256(tmpPath, d.sha256); err != nil {
				os.Remove(tmpPath)
				sherpaFail(ref, err)
			}
			emitProgress(downloadProgress{Model: ref, Status: "extracting"})
			if err := extractTarBz2Members(tmpPath, staging, d.tarMembers); err != nil {
				os.Remove(tmpPath)
				sherpaFail(ref, err)
			}
			os.Remove(tmpPath)
		} else {
			out := filepath.Join(staging, d.destName)
			if err := downloadModelFile(ref, d.url, out); err != nil {
				sherpaFail(ref, err)
			}
			if err := verifySHA256(out, d.sha256); err != nil {
				sherpaFail(ref, err)
			}
		}
	}

	for _, a := range sherpaBundledAssets {
		if err := copyFile(filepath.Join(assetsDir, a), filepath.Join(staging, a), 0o644); err != nil {
			sherpaFail(ref, fmt.Errorf("copy bundled %s: %w", a, err))
		}
	}

	// Completeness gate — every file the stage expects must be present. No HL.fst:
	// the stage self-seeds the grammar from the live vocabulary at startup.
	for _, f := range []string{"model.onnx", "tokens.txt", "silero_vad.onnx", "bpe.model", "bpe.vocab"} {
		if !fileExists(filepath.Join(staging, f)) {
			sherpaFail(ref, fmt.Errorf("assembled model missing %s", f))
		}
	}

	if err := os.Rename(staging, destDir); err != nil {
		sherpaFail(ref, err)
	}

	emitProgress(downloadProgress{Model: ref, Status: "done"})
	fmt.Fprintf(os.Stderr, "Model assembled at %s\n", destDir)
}

func sherpaFail(ref string, err error) {
	emitProgress(downloadProgress{Model: ref, Status: "error", Error: err.Error()})
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(1)
}

// sherpaAssetsDir resolves the vendored sherpa assets bundled next to this binary
// at <exe-dir>/sherpa-assets/<name>/ (the app bundle's Contents/Resources layout).
func sherpaAssetsDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(exe), "sherpa-assets", sherpaModelName), nil
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
