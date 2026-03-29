package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestIsLocalPath(t *testing.T) {
	locals := []string{"/usr/local/plugin", "./my-plugin", "~/plugins/p", "../plugin"}
	for _, p := range locals {
		if !isLocalPath(p) {
			t.Errorf("isLocalPath(%q) = false, want true", p)
		}
	}

	remotes := []string{"drew/branchkit-plugin-basetypes", "branchkit/voice"}
	for _, p := range remotes {
		if isLocalPath(p) {
			t.Errorf("isLocalPath(%q) = true, want false", p)
		}
	}
}

func TestFindManifestAtRoot(t *testing.T) {
	dir := tmpDir(t, "find-root")
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"test"}`), 0o644)

	path, err := findManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "plugin.json" {
		t.Errorf("got %s", path)
	}
}

func TestFindManifestNested(t *testing.T) {
	dir := tmpDir(t, "find-nested")
	sub := filepath.Join(dir, "my-plugin")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "plugin.json"), []byte(`{"id":"test"}`), 0o644)

	path, err := findManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(path)) != "my-plugin" {
		t.Errorf("got %s", path)
	}
}

func TestFindManifestNone(t *testing.T) {
	dir := tmpDir(t, "find-none")
	_, err := findManifest(dir)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFindManifestRejectsMultiple(t *testing.T) {
	dir := tmpDir(t, "find-multi")
	for _, name := range []string{"plugin-a", "plugin-b"} {
		sub := filepath.Join(dir, name)
		os.MkdirAll(sub, 0o755)
		os.WriteFile(filepath.Join(sub, "plugin.json"), []byte(`{"id":"`+name+`"}`), 0o644)
	}

	_, err := findManifest(dir)
	if err == nil {
		t.Fatal("expected error for multiple manifests")
	}
}

func TestFindManifestRootPriority(t *testing.T) {
	dir := tmpDir(t, "find-priority")
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"root"}`), 0o644)
	sub := filepath.Join(dir, "nested")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "plugin.json"), []byte(`{"id":"nested"}`), 0o644)

	path, err := findManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("expected root manifest, got %s", path)
	}
}

func TestExtractTarballRoundTrip(t *testing.T) {
	src := tmpDir(t, "tar-src")
	archiveDir := tmpDir(t, "tar-archive")
	dest := tmpDir(t, "tar-dest")

	os.WriteFile(filepath.Join(src, "plugin.json"), []byte(`{"id":"test"}`), 0o644)
	os.WriteFile(filepath.Join(src, "my-binary"), []byte("binary-content"), 0o755)

	// Create tarball
	tarballPath := filepath.Join(archiveDir, "test.tar.gz")
	f, _ := os.Create(tarballPath)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	for _, name := range []string{"plugin.json", "my-binary"} {
		data, _ := os.ReadFile(filepath.Join(src, name))
		info, _ := os.Stat(filepath.Join(src, name))
		header, _ := tar.FileInfoHeader(info, "")
		header.Name = name
		tw.WriteHeader(header)
		tw.Write(data)
	}
	tw.Close()
	gz.Close()
	f.Close()

	// Extract
	if err := extractTarball(tarballPath, dest); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dest, "plugin.json"))
	if string(data) != `{"id":"test"}` {
		t.Errorf("plugin.json = %q", data)
	}
	data, _ = os.ReadFile(filepath.Join(dest, "my-binary"))
	if string(data) != "binary-content" {
		t.Errorf("my-binary = %q", data)
	}
}

func TestExtractTarballRejectsSymlinks(t *testing.T) {
	archiveDir := tmpDir(t, "tar-symlink")
	dest := tmpDir(t, "tar-symlink-dest")

	tarballPath := filepath.Join(archiveDir, "evil.tar.gz")
	f, _ := os.Create(tarballPath)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	// Add a symlink entry
	tw.WriteHeader(&tar.Header{
		Name:     "evil-link",
		Typeflag: tar.TypeSymlink,
		Linkname: "/etc/passwd",
	})
	tw.Close()
	gz.Close()
	f.Close()

	err := extractTarball(tarballPath, dest)
	if err == nil {
		t.Fatal("expected error for symlink in archive")
	}
}

func TestReadManifestValid(t *testing.T) {
	dir := tmpDir(t, "manifest-valid")
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"test","name":"Test","version":"1.0"}`), 0o644)

	m, err := readManifest(filepath.Join(dir, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "test" || m.Name != "Test" || m.Version != "1.0" {
		t.Errorf("unexpected manifest: %+v", m)
	}
}

func TestReadManifestInvalidID(t *testing.T) {
	dir := tmpDir(t, "manifest-bad-id")
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"Bad_ID","name":"Test"}`), 0o644)

	_, err := readManifest(filepath.Join(dir, "plugin.json"))
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
}

func TestReadManifestBadJSON(t *testing.T) {
	dir := tmpDir(t, "manifest-bad-json")
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte("not json"), 0o644)

	_, err := readManifest(filepath.Join(dir, "plugin.json"))
	if err == nil {
		t.Fatal("expected error for bad JSON")
	}
}
