package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installedDir stands up an installed copy of id (its plugin.json) under a
// scratch app support, as an install leaves it.
func installedDir(t *testing.T, id, manifest string) string {
	t.Helper()
	t.Setenv("BRANCHKIT_APP_SUPPORT", t.TempDir())
	dir := filepath.Join(userPluginsDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestWriteReadSourceMeta(t *testing.T) {
	dir := installedDir(t, "test", `{"id":"test"}`)

	writeSourceMeta("test", dir, "drew/branchkit-plugin-test", "v1.2.3", nil, false)

	meta, ok := readSourceMeta("test")
	if !ok {
		t.Fatal("readSourceMeta returned false")
	}
	if meta.Source != "drew/branchkit-plugin-test" {
		t.Errorf("Source = %q, want drew/branchkit-plugin-test", meta.Source)
	}
	if meta.InstalledTag != "v1.2.3" {
		t.Errorf("InstalledTag = %q, want v1.2.3", meta.InstalledTag)
	}
	sum := sha256.Sum256([]byte(`{"id":"test"}`))
	if meta.ManifestSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("ManifestSHA256 = %q, want the installed plugin.json's digest", meta.ManifestSHA256)
	}
}

// The record also binds every file the manifest names for a collection, by
// the path the manifest writes, the way the app reads it; inline values name
// no file. A manifest naming a file outside its folder gets no record.
func TestSourceMetaBindsTheCollectionFilesTheManifestNames(t *testing.T) {
	manifest := `{"id":"test","collection_data":{"voice_commands":"commands.json","keys":{"a":1}},` +
		`"provides":{"collections":{"plugin.test.log":"collections/log.json","plugin.test.inline":{"description":"x"}}}}`
	dir := installedDir(t, "test", manifest)
	files := map[string]string{"commands.json": `[{"phrase":"go"}]`, "collections/log.json": `{"preset":"log"}`}
	for rel, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSourceMeta("test", dir, "x/y", "v1", nil, false)
	meta, ok := readSourceMeta("test")
	if !ok {
		t.Fatal("no record")
	}
	if len(meta.CollectionFilesSHA256) != len(files) {
		t.Errorf("digests %v, want one for each of %v", meta.CollectionFilesSHA256, files)
	}
	for rel, body := range files {
		sum := sha256.Sum256([]byte(body))
		if got := meta.CollectionFilesSHA256[rel]; got != hex.EncodeToString(sum[:]) {
			t.Errorf("%s: digest %q, want the file's", rel, got)
		}
	}

	removeSourceMeta("test")
	escaping := `{"id":"test","collection_data":{"voice_commands":"../elsewhere.json"}}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(escaping), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSourceMeta("test", dir, "x/y", "v1", nil, false)
	if _, ok := readSourceMeta("test"); ok {
		t.Error("a manifest naming a file outside its folder got a record")
	}
}

func TestPlainRelativePath(t *testing.T) {
	for rel, want := range map[string]bool{
		"commands.json": true, "collections/log.json": true, `collections\log.json`: true,
		"": false, "/etc/passwd": false, `\x`: false, "C:x": false, `C:\x`: false,
		"../x": false, "a/../../x": false, `a\..\x`: false,
	} {
		if got := plainRelativePath(rel); got != want {
			t.Errorf("plainRelativePath(%q) = %v, want %v", rel, got, want)
		}
	}
}

// The record decides the app's trust tier, so it must sit where the plugin
// cannot write: never inside the plugin's own (writable) install folder.
func TestSourceMetaLivesOutsideThePluginFolder(t *testing.T) {
	dir := installedDir(t, "voice", `{"id":"voice"}`)
	writeSourceMeta("voice", dir, "branchkit/branchkit-plugin-voice", "v1", nil, false)

	path := sourceMetaPath("voice")
	if want := filepath.Join(appSupportDir(), "install-records", "voice.json"); path != want {
		t.Errorf("record at %q, want %q", path, want)
	}
	if rel, err := filepath.Rel(dir, path); err == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("record %q is inside the plugin folder %q", path, dir)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "plugin.json" {
			t.Errorf("the install wrote %q into the plugin folder", e.Name())
		}
	}
	// A record the plugin writes into its own folder is not read.
	os.Remove(path)
	os.WriteFile(filepath.Join(dir, ".branchkit-source.json"),
		[]byte(`{"source":"branchkit/branchkit-plugin-voice"}`), 0o644)
	if _, ok := readSourceMeta("voice"); ok {
		t.Error("read a record from inside the plugin folder")
	}
}

func TestSourceMetaPathRefusesNonIDs(t *testing.T) {
	t.Setenv("BRANCHKIT_APP_SUPPORT", t.TempDir())
	for _, id := range []string{"", ".", "..", "../x", `a\b`, "a/b", ".hidden"} {
		if p := sourceMetaPath(id); p != "" {
			t.Errorf("sourceMetaPath(%q) = %q, want none", id, p)
		}
	}
}

func TestReadSourceMetaMissing(t *testing.T) {
	t.Setenv("BRANCHKIT_APP_SUPPORT", t.TempDir())

	_, ok := readSourceMeta("nothing")
	if ok {
		t.Fatal("readSourceMeta returned true for missing file")
	}
}

func TestReadSourceMetaBadJSON(t *testing.T) {
	t.Setenv("BRANCHKIT_APP_SUPPORT", t.TempDir())
	path := sourceMetaPath("bad")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("not json"), 0o644)

	_, ok := readSourceMeta("bad")
	if ok {
		t.Fatal("readSourceMeta returned true for bad JSON")
	}
}

func TestWriteSourceMetaOverwrites(t *testing.T) {
	dir := installedDir(t, "test", `{"id":"test"}`)

	writeSourceMeta("test", dir, "drew/branchkit-plugin-test", "v1.0.0", nil, false)
	writeSourceMeta("test", dir, "drew/branchkit-plugin-test", "v2.0.0", nil, false)

	meta, ok := readSourceMeta("test")
	if !ok {
		t.Fatal("readSourceMeta returned false")
	}
	if meta.InstalledTag != "v2.0.0" {
		t.Errorf("InstalledTag = %q, want v2.0.0 (should overwrite)", meta.InstalledTag)
	}
}

func TestRemoveSourceMeta(t *testing.T) {
	dir := installedDir(t, "test", `{"id":"test"}`)
	writeSourceMeta("test", dir, "x/y", "v1", nil, false)
	removeSourceMeta("test")
	if _, ok := readSourceMeta("test"); ok {
		t.Error("record survived removal")
	}
}

func TestUpdateInfoJSONRoundTrip(t *testing.T) {
	updates := []UpdateInfo{
		{ID: "test", Current: "v1.0.0", Latest: "v2.0.0", Source: "drew/branchkit-plugin-test"},
	}
	data, err := json.Marshal(updates)
	if err != nil {
		t.Fatal(err)
	}

	var parsed []UpdateInfo
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 {
		t.Fatalf("len = %d, want 1", len(parsed))
	}
	if parsed[0].ID != "test" || parsed[0].Latest != "v2.0.0" {
		t.Errorf("unexpected: %+v", parsed[0])
	}
}

func TestUpdateInfoEmptyArray(t *testing.T) {
	// Ensure empty produces [] not null
	var updates []UpdateInfo
	if updates == nil {
		updates = []UpdateInfo{}
	}
	data, _ := json.Marshal(updates)
	if string(data) != "[]" {
		t.Errorf("got %q, want []", data)
	}
}

func TestSourceMetaRecordsRegistrySigned(t *testing.T) {
	dir := installedDir(t, "x", `{"id":"x"}`)
	att := &AuthorAttestation{Verified: true, SAN: "https://github.com/acme/x"}
	writeSourceMeta("x", dir, "acme/x", "v1.0.0", att, true)
	meta, ok := readSourceMeta("x")
	if !ok {
		t.Fatal("source meta not written")
	}
	if !meta.AuthorVerified || !meta.RegistrySigned {
		t.Errorf("author_verified=%v registry_signed=%v, want both true", meta.AuthorVerified, meta.RegistrySigned)
	}
	// Unsigned install records both false.
	writeSourceMeta("x", dir, "x/y", "v1", nil, false)
	m2, _ := readSourceMeta("x")
	if m2.AuthorVerified || m2.RegistrySigned {
		t.Errorf("unsigned should be false/false, got %v/%v", m2.AuthorVerified, m2.RegistrySigned)
	}
}

func TestCheckSummary(t *testing.T) {
	for _, c := range []struct {
		reached, unreached int
		catalogOK          bool
		want               string
	}{
		{2, 1, true, "CHECKED reached=2 unreached=1 catalog=ok"},
		{0, 0, false, "CHECKED reached=0 unreached=0 catalog=failed"},
	} {
		if got := checkSummary(c.reached, c.unreached, c.catalogOK); got != c.want {
			t.Errorf("checkSummary(%d, %d, %v) = %q, want %q", c.reached, c.unreached, c.catalogOK, got, c.want)
		}
	}
}
