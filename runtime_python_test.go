package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeTarGz builds a small python-build-standalone-shaped archive.
func writeTarGz(t *testing.T, path string, entries []tar.Header, bodies map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for _, hdr := range entries {
		h := hdr
		if body, ok := bodies[h.Name]; ok {
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if body, ok := bodies[h.Name]; ok {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// The extractor strips the leading python/ component, preserves modes, and
// recreates in-tree symlinks — the tree the sandbox grant execs through.
func TestExtractPythonTarGzLayout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink layout is the unix tarball shape")
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "py.tar.gz")
	interp := "python/bin/python3.13"
	writeTarGz(t, archive, []tar.Header{
		{Name: "python/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "python/bin/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: interp, Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "python/bin/python3", Typeflag: tar.TypeSymlink, Linkname: "python3.13"},
		{Name: "python/lib/libpython.dylib", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{
		interp:                       "#!fake interpreter",
		"python/lib/libpython.dylib": "not really a dylib",
	})

	dest := filepath.Join(dir, "out")
	if err := extractPythonTarGz(archive, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	info, err := os.Stat(filepath.Join(dest, "bin", "python3.13"))
	if err != nil {
		t.Fatalf("interpreter missing: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("interpreter lost its exec bit: %v", info.Mode())
	}
	// The bin/python3 symlink is what managedPythonPath execs.
	resolved, err := filepath.EvalSymlinks(filepath.Join(dest, "bin", "python3"))
	if err != nil {
		t.Fatalf("bin/python3 symlink broken: %v", err)
	}
	if filepath.Base(resolved) != "python3.13" {
		t.Errorf("symlink resolved to %s", resolved)
	}
}

// A symlink or path pointing outside the destination is refused — the
// archive feeds a directory the sandbox grants exec on.
func TestExtractPythonTarGzRefusesEscapes(t *testing.T) {
	dir := t.TempDir()

	pathEscape := filepath.Join(dir, "path-escape.tar.gz")
	writeTarGz(t, pathEscape, []tar.Header{
		{Name: "python/../outside.txt", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{"python/../outside.txt": "escaped"})
	if err := extractPythonTarGz(pathEscape, filepath.Join(dir, "out1")); err == nil {
		t.Error("path traversal entry must be refused")
	}

	linkEscape := filepath.Join(dir, "link-escape.tar.gz")
	writeTarGz(t, linkEscape, []tar.Header{
		{Name: "python/bin/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "python/bin/evil", Typeflag: tar.TypeSymlink, Linkname: "../../../../etc/passwd"},
	}, nil)
	if err := extractPythonTarGz(linkEscape, filepath.Join(dir, "out2")); err == nil {
		t.Error("symlink escaping the destination must be refused")
	}
}

func TestPythonChecksumPinnedForThisPlatform(t *testing.T) {
	if _, ok := pythonChecksums[runtime.GOOS+"/"+runtime.GOARCH]; !ok {
		t.Skipf("no pin for %s/%s (unsupported platform)", runtime.GOOS, runtime.GOARCH)
	}
	if _, err := pythonTriple(); err != nil {
		t.Errorf("platform has a checksum pin but no triple: %v", err)
	}
}
