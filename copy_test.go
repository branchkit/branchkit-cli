package main

import (
	"os"
	"path/filepath"
	"strings"
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
	if !strings.Contains(err.Error(), "symlink") {
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
	if !strings.Contains(err.Error(), "depth") {
		t.Errorf("error should mention depth, got: %v", err)
	}
}

// A file copied over one a process has open (a running plugin binary) is
// replaced, not rewritten in place: the open file keeps its bytes. Rewriting
// in place is ETXTBSY on Linux and a sharing violation on Windows for a
// running program, and elsewhere changes the bytes under it.
func TestCopyFileReplacesRatherThanRewrites(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "new")
	dest := filepath.Join(dir, "plugin")
	os.WriteFile(src, []byte("new build"), 0o755)
	os.WriteFile(dest, []byte("old build"), 0o755)

	running, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()

	if err := copyFile(src, dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "new build" {
		t.Errorf("dest = %q, want the new build", got)
	}
	held := make([]byte, 64)
	n, _ := running.Read(held)
	if string(held[:n]) != "old build" {
		t.Errorf("the open file now reads %q: it was rewritten in place", held[:n])
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestReplaceDirSwapsAndRemovesThePrevious(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "plugin")
	stage := target + ".installing"
	os.MkdirAll(target, 0o755)
	os.WriteFile(filepath.Join(target, "stale.txt"), []byte("v1 only"), 0o644)
	os.MkdirAll(stage, 0o755)
	os.WriteFile(filepath.Join(stage, "plugin.json"), []byte("v2"), 0o644)

	if err := replaceDir(stage, target); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "plugin.json")); string(got) != "v2" {
		t.Errorf("plugin.json = %q, want v2", got)
	}
	if _, err := os.Stat(filepath.Join(target, "stale.txt")); !os.IsNotExist(err) {
		t.Error("nothing of the previous version survives the swap")
	}
	for _, gone := range []string{stage, target + ".previous"} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s left behind", gone)
		}
	}

	// A first install has no previous version to move aside.
	fresh := filepath.Join(root, "fresh")
	os.MkdirAll(fresh+".installing", 0o755)
	if err := replaceDir(fresh+".installing", fresh); err != nil {
		t.Fatal(err)
	}
}
