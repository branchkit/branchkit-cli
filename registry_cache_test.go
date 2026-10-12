package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The default registry's catalog is kept for the app's listing, atomically
// and only when it parsed to at least one plugin, and never as catalog.yaml,
// the file the app decides trust from.
func TestCatalogFromTheRegistryIsKeptForTheApp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BRANCHKIT_APP_SUPPORT", dir)
	data := []byte("plugins:\n- id: weather\n  source: github:jane/branchkit-plugin-weather\n  description: d\n  tier: community\n")
	maybeCacheCatalog(defaultCatalogURL, data, catalog{Plugins: []catalogEntry{{ID: "weather"}}})
	got, err := os.ReadFile(filepath.Join(dir, listingFile))
	if err != nil || string(got) != string(data) {
		t.Fatalf("catalog not kept: %v %q", err, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "catalog.yaml")); !os.IsNotExist(err) {
		t.Fatalf("wrote the trust catalog: %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".catalog-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

// An overridden URL (a test or a fork) or an empty catalog never writes the
// listing the app reads.
func TestCatalogIsNotKeptFromAnywhereElse(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BRANCHKIT_APP_SUPPORT", dir)
	one := catalog{Plugins: []catalogEntry{{ID: "x"}}}
	maybeCacheCatalog("https://example.invalid/catalog.yaml", []byte("plugins: []"), one)
	maybeCacheCatalog(defaultCatalogURL, []byte("plugins: []"), catalog{})
	if _, err := os.Stat(filepath.Join(dir, listingFile)); !os.IsNotExist(err) {
		t.Fatalf("catalog written when it should not be: %v", err)
	}
}
