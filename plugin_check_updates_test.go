package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadSourceMeta(t *testing.T) {
	dir := tmpDir(t, "source-meta")

	writeSourceMeta(dir, "drew/branchkit-plugin-test", "v1.2.3")

	meta, ok := readSourceMeta(dir)
	if !ok {
		t.Fatal("readSourceMeta returned false")
	}
	if meta.Source != "drew/branchkit-plugin-test" {
		t.Errorf("Source = %q, want drew/branchkit-plugin-test", meta.Source)
	}
	if meta.InstalledTag != "v1.2.3" {
		t.Errorf("InstalledTag = %q, want v1.2.3", meta.InstalledTag)
	}
}

func TestReadSourceMetaMissing(t *testing.T) {
	dir := tmpDir(t, "source-meta-missing")

	_, ok := readSourceMeta(dir)
	if ok {
		t.Fatal("readSourceMeta returned true for missing file")
	}
}

func TestReadSourceMetaBadJSON(t *testing.T) {
	dir := tmpDir(t, "source-meta-bad")
	os.WriteFile(filepath.Join(dir, sourceMetaFile), []byte("not json"), 0o644)

	_, ok := readSourceMeta(dir)
	if ok {
		t.Fatal("readSourceMeta returned true for bad JSON")
	}
}

func TestWriteSourceMetaOverwrites(t *testing.T) {
	dir := tmpDir(t, "source-meta-overwrite")

	writeSourceMeta(dir, "drew/branchkit-plugin-test", "v1.0.0")
	writeSourceMeta(dir, "drew/branchkit-plugin-test", "v2.0.0")

	meta, ok := readSourceMeta(dir)
	if !ok {
		t.Fatal("readSourceMeta returned false")
	}
	if meta.InstalledTag != "v2.0.0" {
		t.Errorf("InstalledTag = %q, want v2.0.0 (should overwrite)", meta.InstalledTag)
	}
}

func TestSourceMetaFileIsHidden(t *testing.T) {
	// .branchkit-source.json starts with dot — won't be picked up by plugin discovery
	if sourceMetaFile[0] != '.' {
		t.Errorf("sourceMetaFile = %q, should start with '.' to be hidden", sourceMetaFile)
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
