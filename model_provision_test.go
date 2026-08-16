package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `dest` and `path` come from a plugin author. The actuator confines them at
// manifest validation, but this tool also runs against manifests it never
// validated (a plugin installed while the app is not running), so the
// confinement has to hold here on its own.
func TestConfinedJoinRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"../outside", "a/../../outside", "/etc/passwd", ""} {
		if _, err := confinedJoin(root, rel); err == nil {
			t.Errorf("confinedJoin accepted %q — it must stay inside the model dir", rel)
		}
	}
	got, err := confinedJoin(root, "sub/dir/file.onnx")
	if err != nil {
		t.Fatalf("rejected a plain relative path: %v", err)
	}
	if !strings.HasPrefix(got, filepath.Clean(root)+string(os.PathSeparator)) {
		t.Errorf("joined path %q left the root", got)
	}
}

// A branch or tag can be repointed at different bytes under the same name,
// which is exactly what the pin exists to prevent. Only a full commit sha is
// accepted; a rewritten commit 404s instead, which fails loudly.
func TestRequireCommitShaRejectsMovableRefs(t *testing.T) {
	for _, rev := range []string{"main", "v1.0", "abc1234", "", strings.Repeat("z", 40)} {
		if err := requireCommitSha(rev); err == nil {
			t.Errorf("accepted %q as a pinned revision", rev)
		}
	}
	if err := requireCommitSha(strings.Repeat("a1b2", 10)); err != nil {
		t.Errorf("rejected a valid 40-char sha: %v", err)
	}
}

// The completeness gate. Without it a source that quietly stops serving one
// file leaves a directory that looks provisioned and fails at model load.
func TestCheckRequiresCatchesAMissingFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.onnx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkRequires(dir, []string{"model.onnx"}); err != nil {
		t.Fatalf("present file reported missing: %v", err)
	}
	if err := checkRequires(dir, []string{"model.onnx", "tokens.txt"}); err == nil {
		t.Error("missing tokens.txt was not caught")
	}
	if err := checkRequires(dir, []string{"../../host.token"}); err == nil {
		t.Error("a traversing requires entry must be refused, not resolved")
	}
}

// The receipt is what makes re-provisioning idempotent AND what detects a
// changed recipe. Both halves matter: same parts must compare equal, and any
// change to a pin must not.
func TestPartsDigestTracksThePins(t *testing.T) {
	a := []ModelPart{{Kind: "http_file", URL: "https://x/y", SHA256: strings.Repeat("a", 64), Dest: "y"}}
	b := []ModelPart{{Kind: "http_file", URL: "https://x/y", SHA256: strings.Repeat("b", 64), Dest: "y"}}
	if partsDigest(a) != partsDigest(a) {
		t.Fatal("digest is not stable for identical parts")
	}
	if partsDigest(a) == partsDigest(b) {
		t.Error("a changed sha256 must change the digest — otherwise a re-pinned model keeps old bytes")
	}
}

// End to end over the one part kind that needs no network: the model dir lands
// under the OWNING PLUGIN's namespace, and the receipt records the recipe.
func TestProvisionPluginFileModelLandsUnderThePluginNamespace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BRANCHKIT_DEV", "1")

	pluginDir := filepath.Join(home, "plugin")
	if err := os.MkdirAll(filepath.Join(pluginDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "assets", "bpe.model"), []byte("bpe"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := declaredModel{
		Ref:       "voice/test-model",
		Plugin:    "voice",
		PluginDir: pluginDir,
		Name:      "test-model",
		Decl: ModelDeclaration{
			SizeBytes: 3,
			Parts: []ModelPart{
				{Kind: "plugin_file", Path: "assets/bpe.model", Dest: "bpe.model"},
			},
			Requires: []string{"bpe.model"},
		},
	}
	provisionDeclaredModel(m)

	dest := filepath.Join(modelsDir(), "voice", "test-model", "bpe.model")
	if !fileExists(dest) {
		t.Fatalf("model file not at %s", dest)
	}
	data, err := os.ReadFile(receiptPath(filepath.Join(modelsDir(), "voice"), "test-model"))
	if err != nil {
		t.Fatalf("no receipt written: %v", err)
	}
	var r modelReceipt
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	if r.PartsDigest != partsDigest(m.Decl.Parts) {
		t.Error("receipt does not record the recipe it was provisioned from")
	}
	// The receipt is a SIBLING of the model dir, not a file inside it — the
	// model dir is scanned by an engine, and our bookkeeping is not part of
	// the model.
	if fileExists(filepath.Join(modelsDir(), "voice", "test-model", ".branchkit-model.json")) {
		t.Error("receipt leaked into the model directory")
	}
}

// The pre-namespace layout had models flat at `<models>/<name>`. Adopting one
// by rename saves re-downloading gigabytes — but only when it is COMPLETE by
// the declaration's own `requires`, so a half-finished legacy download is
// re-fetched rather than blessed.
func TestLegacyFlatModelIsAdoptedOnlyWhenComplete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BRANCHKIT_DEV", "1")

	pluginDir := filepath.Join(home, "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(modelsDir(), "legacy-model")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "model.onnx"), []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := declaredModel{
		Ref:       "voice/legacy-model",
		Plugin:    "voice",
		PluginDir: pluginDir,
		Name:      "legacy-model",
		Decl: ModelDeclaration{
			SizeBytes: 7,
			// Declares a file the legacy dir does NOT have, so adoption must
			// be refused. No parts can satisfy it either — but provisioning
			// stops at the incomplete-legacy check before touching the network.
			Parts:    []ModelPart{{Kind: "plugin_file", Path: "missing", Dest: "tokens.txt"}},
			Requires: []string{"model.onnx", "tokens.txt"},
		},
	}
	if err := checkRequires(legacy, m.Decl.Requires); err == nil {
		t.Fatal("fixture is wrong: the legacy dir should be incomplete")
	}

	// Complete it, and adoption is now the right call.
	if err := os.WriteFile(filepath.Join(legacy, "tokens.txt"), []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	provisionDeclaredModel(m)

	if fileExists(legacy) {
		t.Error("legacy dir still present — adoption should have moved it, not copied it")
	}
	adopted := filepath.Join(modelsDir(), "voice", "legacy-model", "model.onnx")
	if !fileExists(adopted) {
		t.Fatalf("adopted model not at %s", adopted)
	}
}

// A failed pin must leave NOTHING on disk. The first version of this code
// cleaned up with a `defer` and then exited the process on failure, so the
// defer never ran and the unverified bytes stayed in `<model>.partial` — for a
// large model, gigabytes of them.
func TestFailedAssemblyLeavesNoStagingBehind(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BRANCHKIT_DEV", "1")

	pluginDir := filepath.Join(home, "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := declaredModel{
		Ref:       "voice/broken",
		Plugin:    "voice",
		PluginDir: pluginDir,
		Name:      "broken",
		Decl: ModelDeclaration{
			SizeBytes: 1,
			// A plugin_file the plugin does not ship: fails without touching
			// the network, on the same path a bad checksum takes.
			Parts:    []ModelPart{{Kind: "plugin_file", Path: "missing.bin", Dest: "missing.bin"}},
			Requires: []string{"missing.bin"},
		},
	}
	pluginRoot := filepath.Join(modelsDir(), "voice")
	destDir := filepath.Join(pluginRoot, "broken")
	if err := assembleModel(m, pluginRoot, destDir, "digest"); err == nil {
		t.Fatal("expected assembly to fail")
	}
	for _, p := range []string{destDir, destDir + ".partial"} {
		if fileExists(p) {
			t.Errorf("%s survived a failed assembly", p)
		}
	}
}
