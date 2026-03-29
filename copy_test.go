package main

import (
	"os"
	"path/filepath"
	"testing"
)

func tmpDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "branchkit-cli-test-"+name)
	os.RemoveAll(dir)
	os.MkdirAll(dir, 0o755)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestSafeCopyBasic(t *testing.T) {
	src := tmpDir(t, "copy-src")
	dest := tmpDir(t, "copy-dest")

	os.WriteFile(filepath.Join(src, "file.txt"), []byte("hello"), 0o644)
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	os.WriteFile(filepath.Join(src, "sub", "nested.txt"), []byte("world"), 0o644)

	if err := safeCopyDir(src, dest, 0); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dest, "file.txt"))
	if string(data) != "hello" {
		t.Errorf("file.txt = %q, want %q", data, "hello")
	}
	data, _ = os.ReadFile(filepath.Join(dest, "sub", "nested.txt"))
	if string(data) != "world" {
		t.Errorf("sub/nested.txt = %q, want %q", data, "world")
	}
}

func TestSafeCopyRejectsSymlinks(t *testing.T) {
	src := tmpDir(t, "copy-symlink")
	dest := tmpDir(t, "copy-symlink-dest")

	os.WriteFile(filepath.Join(src, "real.txt"), []byte("data"), 0o644)
	os.Symlink(filepath.Join(src, "real.txt"), filepath.Join(src, "link.txt"))

	err := safeCopyDir(src, dest, 0)
	if err == nil {
		t.Fatal("expected error for symlink")
	}
	if !contains(err.Error(), "symlink") {
		t.Errorf("error should mention symlink, got: %v", err)
	}
}

func TestSafeCopyRejectsExcessiveDepth(t *testing.T) {
	src := tmpDir(t, "copy-deep")
	dest := tmpDir(t, "copy-deep-dest")

	path := src
	for i := 0; i <= maxCopyDepth; i++ {
		path = filepath.Join(path, "d")
		os.MkdirAll(path, 0o755)
		os.WriteFile(filepath.Join(path, "f.txt"), []byte("x"), 0o644)
	}

	err := safeCopyDir(src, dest, 0)
	if err == nil {
		t.Fatal("expected error for excessive depth")
	}
	if !contains(err.Error(), "depth") {
		t.Errorf("error should mention depth, got: %v", err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
