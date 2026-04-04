package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestNeedsBun(t *testing.T) {
	tests := []struct {
		run  string
		want bool
	}{
		{"bun run ./index.ts", true},
		{"bun run .", true},
		{"./my-plugin", false},
		{"go run .", false},
		{"node ./dist/index.js", false},
		{"", false},
		{"bunny", false},
	}
	for _, tt := range tests {
		m := PluginManifest{Run: tt.run}
		if got := needsBun(m); got != tt.want {
			t.Errorf("needsBun(%q) = %v, want %v", tt.run, got, tt.want)
		}
	}
}

func TestManagedBunPaths(t *testing.T) {
	bunPath := managedBunPath()
	versionPath := managedBunVersionPath()

	if filepath.Dir(bunPath) != filepath.Dir(versionPath) {
		t.Errorf("bun binary and version.txt should be in same directory: %s vs %s",
			filepath.Dir(bunPath), filepath.Dir(versionPath))
	}

	if filepath.Base(bunPath) != "bun" {
		t.Errorf("expected binary name 'bun', got %q", filepath.Base(bunPath))
	}
}

func TestExtractBunFromZip(t *testing.T) {
	dir := tmpDir(t, "bun-extract")
	zipPath := filepath.Join(dir, "test.zip")
	destDir := filepath.Join(dir, "extracted")
	os.MkdirAll(destDir, 0o755)

	createTestBunZip(t, zipPath, "bun-darwin-aarch64/bun", "fake-bun-binary")

	err := extractBunFromZip(zipPath, destDir)
	if err != nil {
		t.Fatalf("extractBunFromZip failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(destDir, "bun"))
	if err != nil {
		t.Fatalf("extracted binary not found: %v", err)
	}
	if string(data) != "fake-bun-binary" {
		t.Errorf("unexpected content: %q", string(data))
	}
}

func TestExtractBunFromZip_NoBinary(t *testing.T) {
	dir := tmpDir(t, "bun-extract-empty")
	zipPath := filepath.Join(dir, "test.zip")
	destDir := filepath.Join(dir, "extracted")
	os.MkdirAll(destDir, 0o755)

	createTestBunZip(t, zipPath, "readme.txt", "not bun")

	err := extractBunFromZip(zipPath, destDir)
	if err == nil {
		t.Fatal("expected error for missing bun binary in zip")
	}
}

func createTestBunZip(t *testing.T, zipPath, entryName, content string) {
	t.Helper()
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	fw, err := w.Create(entryName)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte(content))
	w.Close()
}
