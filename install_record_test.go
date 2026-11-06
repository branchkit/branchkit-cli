package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pinnedManifest = `{"id":"conv","name":"Converter","version":"1.0.0"}`

// recordedCopy puts a plugin folder with manifest under the app's plugins
// folder and records it as installed from acme/conv, the way an installer
// does: the record is computed from the files before they are copied.
func recordedCopy(t *testing.T, manifest string, attestation *AuthorAttestation) string {
	t.Helper()
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	meta, _, err := installRecord(stage, "acme/conv", "v1.0.0", attestation, false)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(userPluginsDir(), "conv")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := safeCopyDir(stage, dir, 0); err != nil {
		t.Fatal(err)
	}
	recordInstall("conv", meta, nil)
	return dir
}

// A plugin that adds "shell" to its own plugin.json before an update does
// not get it past the update's question: the diff starts from the manifest
// the install recorded, not from the one in the plugin's folder.
func TestUpdateDiffStartsFromTheApprovedManifest(t *testing.T) {
	t.Setenv("BRANCHKIT_APP_SUPPORT", t.TempDir())
	dir := recordedCopy(t, pinnedManifest, nil)
	next := parseManifest(t, `{"id":"conv","name":"Converter","version":"2.0.0",
		"requires":{"privileges":["shell"]}}`)

	old, installed, _ := approvedManifest("conv", dir)
	if !installed || old.Version != "1.0.0" {
		t.Fatalf("the recorded copy: installed=%v version=%q", installed, old.Version)
	}
	if !diffConsent(old, next).expands(true) {
		t.Fatal("shell is new against the approved manifest")
	}

	// The plugin writes the permission into its own manifest first.
	tampered := `{"id":"conv","name":"Converter","version":"1.0.0","requires":{"privileges":["shell"]}}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	var err error
	out := captureStdout(t, func() {
		old, installed = installedForUpdate("conv", dir)
		err = confirmUpdate(next, old, strings.NewReader(""), false, false, true)
	})
	if !installed {
		t.Fatal("a copy with a record is installed")
	}
	if len(old.Requires.Privileges) != 0 {
		t.Fatalf("the diff started from the plugin's own manifest: %v", old.Requires.Privileges)
	}
	if err == nil || !strings.Contains(out, "shell") {
		t.Fatalf("shell was not asked about (err=%v):\n%s", err, out)
	}
}

// Nothing installed is a fresh install. A folder with no record, or a record
// whose folder lost its manifest, is an update whose every request is new:
// deleting plugin.json does not turn an update into a fresh install.
func TestApprovedManifestWithoutARecord(t *testing.T) {
	t.Setenv("BRANCHKIT_APP_SUPPORT", t.TempDir())
	dir := filepath.Join(userPluginsDir(), "conv")
	if _, installed, _ := approvedManifest("conv", dir); installed {
		t.Fatal("nothing is installed")
	}

	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "plugin.json"),
		[]byte(`{"id":"conv","version":"1.0.0","requires":{"privileges":["shell"]}}`), 0o644)
	var old PluginManifest
	var installed bool
	old, installed, _ = approvedManifest("conv", dir)
	if !installed || len(old.Requires.Privileges) != 0 || old.Version != "1.0.0" {
		t.Fatalf("no record: installed=%v old=%+v", installed, old)
	}

	os.RemoveAll(dir)
	recordedCopy(t, pinnedManifest, nil)
	os.Remove(filepath.Join(dir, "plugin.json"))
	old, installed, _ = approvedManifest("conv", dir)
	if !installed || old.ID != "conv" {
		t.Fatalf("a record without its manifest: installed=%v old=%+v", installed, old)
	}
}

// A plugin that clears author_verified wherever it can write, and rewrites
// its manifest so the record no longer speaks for its folder, still gets
// the downgrade question: the gate reads the install record as written.
func TestDowngradeGateReadsTheRecordNotTheFolder(t *testing.T) {
	t.Setenv("BRANCHKIT_APP_SUPPORT", t.TempDir())
	verified := &AuthorAttestation{Verified: true, SAN: "https://github.com/acme/conv/.github/workflows/release.yml@refs/tags/v1.0.0"}
	dir := recordedCopy(t, pinnedManifest, verified)
	os.WriteFile(filepath.Join(dir, legacySourceFile),
		[]byte(`{"source":"acme/conv","author_verified":false}`), 0o644)
	os.WriteFile(filepath.Join(dir, "plugin.json"),
		[]byte(`{"id":"conv","name":"Converter","version":"1.0.1"}`), 0o644)

	var err error
	captureStdout(t, func() {
		err = checkAttestationDowngrade("conv", "Converter", nil, strings.NewReader(""), false)
	})
	if err == nil {
		t.Fatal("an unattested update over an author-verified install went through")
	}
	if err := checkAttestationDowngrade("conv", "Converter", verified, strings.NewReader(""), false); err != nil {
		t.Fatalf("an attested update: %v", err)
	}
	removeSourceMeta("conv")
	if err := checkAttestationDowngrade("conv", "Converter", nil, strings.NewReader(""), false); err != nil {
		t.Fatalf("no prior record, nothing to downgrade: %v", err)
	}
}

// A local install records the folder it copied, drops the record file an
// old install left in that folder, and removes a collection file an earlier
// install left behind that the new manifest names but does not ship.
func TestLocalInstallRecordsWhatItCopied(t *testing.T) {
	t.Setenv("BRANCHKIT_APP_SUPPORT", t.TempDir())
	prev := installAssumeYes
	installAssumeYes = true
	t.Cleanup(func() { installAssumeYes = prev })

	src := t.TempDir()
	manifest := `{"id":"conv","name":"Converter","version":"1.0.0",
		"collection_data":{"words":"data/words.json","gone":"data/gone.json"}}`
	os.WriteFile(filepath.Join(src, "plugin.json"), []byte(manifest), 0o644)
	os.MkdirAll(filepath.Join(src, "data"), 0o755)
	os.WriteFile(filepath.Join(src, "data", "words.json"), []byte(`["a"]`), 0o644)
	os.WriteFile(filepath.Join(src, legacySourceFile), []byte(`{"source":"bundled"}`), 0o644)

	dir := filepath.Join(userPluginsDir(), "conv")
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	os.WriteFile(filepath.Join(dir, "data", "gone.json"), []byte(`["stale"]`), 0o644)

	captureStdout(t, func() {
		if err := installFromLocal(src); err != nil {
			t.Errorf("install: %v", err)
		}
	})
	meta, ok := readSourceMeta("conv")
	if !ok {
		t.Fatal("no record")
	}
	want, _, _ := installRecord(src, meta.Source, "local", nil, false)
	if meta.ManifestSHA256 != want.ManifestSHA256 || meta.CollectionFilesSHA256["data/words.json"] != want.CollectionFilesSHA256["data/words.json"] {
		t.Fatalf("recorded %+v, want the copied folder's %+v", meta, want)
	}
	if _, err := os.Stat(filepath.Join(dir, legacySourceFile)); !os.IsNotExist(err) {
		t.Errorf("the copied folder's %s is still installed", legacySourceFile)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "gone.json")); !os.IsNotExist(err) {
		t.Error("a named file the install did not ship is still in the folder")
	}
	if old, installed, unapproved := approvedManifest("conv", dir); !installed || unapproved != "" || old.Version != "1.0.0" {
		t.Errorf("the record does not speak for what was installed: %v %+v", installed, old)
	}
}

// The record is written through a temporary file and renamed into place: a
// write leaves the one record and nothing beside it, and replaces the
// previous record whole.
func TestInstallRecordIsWrittenWhole(t *testing.T) {
	t.Setenv("BRANCHKIT_APP_SUPPORT", t.TempDir())
	recordedCopy(t, pinnedManifest, nil)
	recordInstall("conv", SourceMeta{Source: "acme/conv", InstalledTag: "v2.0.0"}, nil)
	entries, err := os.ReadDir(filepath.Dir(sourceMetaPath("conv")))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "conv.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("install-records holds %v, want only conv.json", names)
	}
	if meta, _ := readSourceMeta("conv"); meta.InstalledTag != "v2.0.0" || meta.ManifestSHA256 != "" {
		t.Fatalf("the record was not replaced whole: %+v", meta)
	}

	// A record that cannot be computed leaves none behind.
	recordInstall("conv", SourceMeta{}, os.ErrNotExist)
	if _, ok := readSourceMeta("conv"); ok {
		t.Fatal("a failed record kept the previous one")
	}
}
