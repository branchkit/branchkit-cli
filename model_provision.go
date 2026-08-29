package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Provisioning models a PLUGIN declares, as opposed to the compiled-in catalog
// in model_download.go.
//
// The plugin says what its models are made of; this file executes that recipe
// and nothing else. Two properties are load-bearing and enforced here rather
// than trusted from the manifest:
//
//   - Every remote part is content-pinned (sha256, or a Hugging Face commit
//     sha) and the pin is VERIFIED after download. A manifest that reaches this
//     code unvalidated still cannot cause unpinned bytes to land on disk.
//   - Every part lands inside the model directory. `dest` is manifest-supplied,
//     so it is confined here the same way the actuator confines it.
//
// Layout: <app_support>/models/<plugin_id>/<model_name>. The plugin id
// namespace is what makes collisions impossible and what lets the actuator hand
// the stage `BRANCHKIT_MODELS_DIR=<...>/models/<plugin_id>` while granting the
// sandbox exactly the one model the pipeline named.
// See docs/design/DESIGN_PLUGIN_MODEL_DECLARATION.md in branchkit/app.

// receiptName is the sidecar recording what a model dir was provisioned from.
// A sibling dotfile rather than a file inside the model dir: the model dir is
// read by an engine that scans it (WhisperKit walks the folder), and the
// provisioning record is our bookkeeping, not part of the model.
func receiptPath(pluginRoot, modelName string) string {
	return filepath.Join(pluginRoot, "."+modelName+".branchkit-model.json")
}

type modelReceipt struct {
	Ref           string `json:"ref"`
	Plugin        string `json:"plugin"`
	Model         string `json:"model"`
	PartsDigest   string `json:"parts_digest"`
	SizeBytes     int64  `json:"size_bytes"`
	ProvisionedAt string `json:"provisioned_at"`
}

// declaredModel is one entry of the catalog assembled from installed manifests.
type declaredModel struct {
	Ref       string // "<plugin_id>/<model_name>"
	Plugin    string
	PluginDir string
	Name      string
	Decl      ModelDeclaration
}

// declaredModels walks every discovered plugin's manifest. This IS the catalog
// — there is no compiled-in list to drift from it, and a plugin can add a model
// without a CLI release.
func declaredModels() map[string]declaredModel {
	out := map[string]declaredModel{}
	for _, dp := range discoverPlugins() {
		if dp.Manifest.Provides == nil {
			continue
		}
		for name, decl := range dp.Manifest.Provides.Models {
			ref := dp.Manifest.ID + "/" + name
			if _, dup := out[ref]; dup {
				continue // first discovery path wins, same as plugin resolution
			}
			out[ref] = declaredModel{
				Ref:       ref,
				Plugin:    dp.Manifest.ID,
				PluginDir: dp.ManifestDir,
				Name:      name,
				Decl:      decl,
			}
		}
	}
	return out
}

// partsDigest fingerprints the recipe. A changed pin, url, or member list means
// a different model, so the receipt carrying a stale digest is what triggers
// re-provisioning instead of silently keeping old bytes.
func partsDigest(parts []ModelPart) string {
	data, err := json.Marshal(parts)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// provisionDeclaredModel assembles one declared model. Reports progress on the
// same NDJSON channel as the legacy path, because the shell reads it.
func provisionDeclaredModel(m declaredModel) {
	pluginRoot := filepath.Join(modelsDir(), m.Plugin)
	destDir := filepath.Join(pluginRoot, m.Name)
	digest := partsDigest(m.Decl.Parts)

	if fileExists(destDir) {
		if receiptMatches(pluginRoot, m, digest) {
			emitProgress(downloadProgress{Model: m.Ref, Status: "exists"})
			fmt.Fprintf(os.Stderr, "Model already provisioned: %s\n", destDir)
			return
		}
		// On disk but from a different recipe. Refuse rather than guess: the
		// bytes may be a model the user is mid-session with, and deleting
		// gigabytes on an inference about a digest is not this tool's call.
		fmt.Fprintf(os.Stderr,
			"Model %s exists but was provisioned from a different declaration.\n"+
				"Remove %s and run this again to re-provision.\n", m.Ref, destDir)
		emitProgress(downloadProgress{Model: m.Ref, Status: "error", Error: "declaration changed"})
		os.Exit(1)
	}

	// Pre-namespace layouts. Adopt by rename rather than re-downloading
	// gigabytes — but only if the directory is COMPLETE by the declaration's
	// own `requires`, so a half-finished legacy download is re-fetched instead
	// of being blessed.
	for _, legacy := range legacyLocations(m.Name) {
		if !fileExists(legacy) {
			continue
		}
		if err := checkRequires(legacy, m.Decl.Requires); err != nil {
			fmt.Fprintf(os.Stderr, "Ignoring incomplete legacy model at %s (%v)\n", legacy, err)
			continue
		}
		if err := os.MkdirAll(pluginRoot, 0o755); err != nil {
			break
		}
		if err := os.Rename(legacy, destDir); err != nil {
			break
		}
		writeReceipt(pluginRoot, m, digest)
		emitProgress(downloadProgress{Model: m.Ref, Status: "done"})
		fmt.Fprintf(os.Stderr, "Adopted existing model %s -> %s\n", legacy, destDir)
		return
	}

	emitProgress(downloadProgress{Model: m.Ref, Status: "downloading", Pct: 0})
	if err := assembleModel(m, pluginRoot, destDir, digest); err != nil {
		modelFail(m.Ref, err)
	}

	emitProgress(downloadProgress{Model: m.Ref, Status: "done"})
	fmt.Fprintf(os.Stderr, "Model provisioned at %s\n", destDir)
}

// assembleModel returns errors rather than exiting, so its staging cleanup
// actually runs: a failed pin means bytes we could not verify are on disk, and
// `os.Exit` skips deferred work, which left them there (a gigabyte of them for
// a large model).
func assembleModel(m declaredModel, pluginRoot, destDir, digest string) error {
	// Assemble in a sibling staging dir on the same filesystem, renamed into
	// place only once every part succeeded AND the completeness gate passed —
	// a partial model that looks ready is a much worse failure than a re-run.
	staging := destDir + ".partial"
	os.RemoveAll(staging)
	defer os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}

	for i, part := range m.Decl.Parts {
		if err := applyModelPart(m, part, staging); err != nil {
			return fmt.Errorf("part %d (%s): %w", i, part.Kind, err)
		}
	}
	if err := checkRequires(staging, m.Decl.Requires); err != nil {
		return err
	}
	if err := os.MkdirAll(pluginRoot, 0o755); err != nil {
		return err
	}
	if err := os.Rename(staging, destDir); err != nil {
		return err
	}
	writeReceipt(pluginRoot, m, digest)
	return nil
}

// legacyLocations are the places a model of this name could be sitting from
// before model dirs were namespaced by owning plugin:
//
//   - `<models>/<name>` — the flat layout (the sherpa command model).
//   - `<models>/<engine>/<name>` — the per-ENGINE layout, where the directory
//     level was a vendor name (`whisperkit/openai_whisper-…`).
//
// The engine form is scoped to directories that are NOT a known plugin id, so
// this can never reach into a plugin's own namespace and take a model that
// belongs to it. Together with the completeness check at the call site, the
// worst case is that a stale directory is left alone.
func legacyLocations(name string) []string {
	root := modelsDir()
	out := []string{filepath.Join(root, name)}
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	known := knownPluginIDs()
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || known[e.Name()] {
			continue
		}
		out = append(out, filepath.Join(root, e.Name(), name))
	}
	return out
}

func modelFail(ref string, err error) {
	emitProgress(downloadProgress{Model: ref, Status: "error", Error: err.Error()})
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(1)
}

func receiptMatches(pluginRoot string, m declaredModel, digest string) bool {
	data, err := os.ReadFile(receiptPath(pluginRoot, m.Name))
	if err != nil {
		// No receipt: a model dir provisioned before receipts existed, or
		// adopted by hand. Treat as current — re-downloading on a missing
		// bookkeeping file would punish users for our own change.
		return true
	}
	var r modelReceipt
	if err := json.Unmarshal(data, &r); err != nil {
		return true
	}
	return r.PartsDigest == digest
}

func writeReceipt(pluginRoot string, m declaredModel, digest string) {
	r := modelReceipt{
		Ref:           m.Ref,
		Plugin:        m.Plugin,
		Model:         m.Name,
		PartsDigest:   digest,
		SizeBytes:     m.Decl.SizeBytes,
		ProvisionedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return
	}
	// Best-effort: a missing receipt costs a re-check, never correctness.
	_ = os.WriteFile(receiptPath(pluginRoot, m.Name), data, 0o644)
}

// checkRequires is the completeness gate. Without it, a source that quietly
// stops serving one file leaves a directory that looks provisioned and fails at
// model load, far from the cause.
func checkRequires(dir string, requires []string) error {
	for _, rel := range requires {
		clean, err := confinedJoin(dir, rel)
		if err != nil {
			return fmt.Errorf("requires %q: %w", rel, err)
		}
		if !fileExists(clean) {
			return fmt.Errorf("missing required file %q", rel)
		}
	}
	return nil
}

// confinedJoin joins a manifest-supplied relative path onto a root and refuses
// anything that could escape it. Mirrors the actuator's path_confine — the
// manifest is data from a plugin author, on both sides of the boundary.
func confinedJoin(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path must be relative, got %q", rel)
	}
	joined := filepath.Join(root, filepath.FromSlash(rel))
	cleanRoot := filepath.Clean(root)
	if joined != cleanRoot && !strings.HasPrefix(joined, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes the model directory", rel)
	}
	return joined, nil
}

func applyModelPart(m declaredModel, part ModelPart, staging string) error {
	destRoot := staging
	if part.Dest != "" && (part.Kind == "hf_folder" || part.Kind == "hf_files" || part.Kind == "http_archive") {
		var err error
		if destRoot, err = confinedJoin(staging, part.Dest); err != nil {
			return err
		}
		if err := os.MkdirAll(destRoot, 0o755); err != nil {
			return err
		}
	}

	switch part.Kind {
	case "hf_folder":
		if err := requireCommitSha(part.Revision); err != nil {
			return err
		}
		return hfSnapshotAtRevision(m.Ref, part.Repo, part.Path, part.Revision, destRoot)

	case "hf_files":
		if err := requireCommitSha(part.Revision); err != nil {
			return err
		}
		client := &http.Client{Timeout: 5 * time.Minute}
		for _, name := range part.Files {
			out, err := confinedJoin(destRoot, name)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			url := fmt.Sprintf("https://huggingface.co/%s/resolve/%s/%s", part.Repo, part.Revision, name)
			if err := httpGetToFile(client, url, out); err != nil {
				return fmt.Errorf("download %s from %s: %w", name, part.Repo, err)
			}
		}
		return nil

	case "http_archive":
		tmp, err := os.CreateTemp("", "branchkit-model-*.tar.bz2")
		if err != nil {
			return err
		}
		tmpPath := tmp.Name()
		tmp.Close()
		defer os.Remove(tmpPath)
		if err := downloadModelFile(m.Ref, part.URL, tmpPath); err != nil {
			return err
		}
		if err := verifySHA256(tmpPath, part.SHA256); err != nil {
			return err
		}
		emitProgress(downloadProgress{Model: m.Ref, Status: "extracting"})
		return extractTarBz2Members(tmpPath, destRoot, part.Members)

	case "http_file":
		out, err := confinedJoin(staging, part.Dest)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if err := downloadModelFile(m.Ref, part.URL, out); err != nil {
			return err
		}
		return verifySHA256(out, part.SHA256)

	case "plugin_file":
		src, err := confinedJoin(m.PluginDir, part.Path)
		if err != nil {
			return err
		}
		out, err := confinedJoin(staging, part.Dest)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if !fileExists(src) {
			return fmt.Errorf("plugin does not ship %q", part.Path)
		}
		return copyFile(src, out, 0o644)

	default:
		return fmt.Errorf("unknown part kind %q", part.Kind)
	}
}

// requireCommitSha refuses a branch or tag. Those can be repointed at different
// bytes under the same name, which is the whole thing the pin prevents; a
// commit that is rewritten away 404s instead, which fails loudly.
func requireCommitSha(rev string) error {
	if len(rev) != 40 {
		return fmt.Errorf("revision %q is not a 40-character commit sha", rev)
	}
	for _, c := range rev {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return fmt.Errorf("revision %q is not a 40-character commit sha", rev)
		}
	}
	return nil
}

// hfSnapshotAtRevision is snapshotHFFolder pinned to a commit: both the tree
// listing and every file fetch name the revision, so the listing and the bytes
// cannot come from different states of the repo.
func hfSnapshotAtRevision(ref, repo, folder, revision, destDir string) error {
	if repo == "" || folder == "" {
		return fmt.Errorf("hf_folder needs both repo and path")
	}
	files, err := hfListFilesAt(repo, folder, revision)
	if err != nil {
		return fmt.Errorf("list %s/%s@%s: %w", repo, folder, revision[:7], err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no files found at %s/%s@%s", repo, folder, revision[:7])
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}

	client := &http.Client{Timeout: 30 * time.Minute}
	prefix := folder + "/"
	var done int64
	lastPct := -1
	for _, f := range files {
		rel := strings.TrimPrefix(f.Path, prefix)
		out, err := confinedJoin(destDir, rel)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		url := fmt.Sprintf("https://huggingface.co/%s/resolve/%s/%s", repo, revision, f.Path)
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
	return nil
}

func hfListFilesAt(repo, folder, revision string) ([]hfTreeEntry, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	var out []hfTreeEntry
	var walk func(path string) error
	walk = func(path string) error {
		url := fmt.Sprintf("https://huggingface.co/api/models/%s/tree/%s/%s", repo, revision, path)
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

// printDeclaredModels lists what installed plugins offer — the replacement for
// a hardcoded "available models" blurb.
func printDeclaredModels(w *os.File) {
	declared := declaredModels()
	if len(declared) == 0 {
		return
	}
	refs := make([]string, 0, len(declared))
	for ref := range declared {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	fmt.Fprintln(w, "\nDeclared by installed plugins:")
	for _, ref := range refs {
		m := declared[ref]
		fmt.Fprintf(w, "  %-50s %-8s %s\n", ref, humanBytes(m.Decl.SizeBytes), m.Decl.Description)
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/float64(1<<20))
	case n > 0:
		return fmt.Sprintf("%.0f KB", float64(n)/float64(1<<10))
	default:
		return "?"
	}
}
