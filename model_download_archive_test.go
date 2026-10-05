package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// archive builds a .tar.bz2 of `files` (relative path -> content) under one
// top folder, the shape sherpa-onnx's model releases have.
func archive(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("no tar to build the fixture archive")
	}
	src := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(src, "voice-model", rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "model.tar.bz2")
	if b, err := exec.Command("tar", "-cjf", out, "-C", src, "voice-model").CombinedOutput(); err != nil {
		t.Fatalf("tar: %v %s", err, b)
	}
	return out
}

func TestArchiveFilesFlattenAndDirectoriesKeepTheirTree(t *testing.T) {
	a := archive(t, map[string]string{
		"model.onnx":                  "m",
		"tokens.txt":                  "t",
		"espeak-ng-data/en_dict":      "d",
		"espeak-ng-data/voices/en/us": "v",
		"README.md":                   "not wanted",
	})
	dest := t.TempDir()
	if err := extractTarBz2Members(a, dest, []string{"model.onnx", "tokens.txt", "espeak-ng-data/"}); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"model.onnx":                  "m",
		"tokens.txt":                  "t",
		"espeak-ng-data/en_dict":      "d",
		"espeak-ng-data/voices/en/us": "v",
	} {
		got, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", rel, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err == nil {
		t.Error("an unnamed member was extracted")
	}
}

func TestArchiveMissingDirectoryMemberIsAnError(t *testing.T) {
	a := archive(t, map[string]string{"model.onnx": "m"})
	if err := extractTarBz2Members(a, t.TempDir(), []string{"model.onnx", "espeak-ng-data/"}); err == nil {
		t.Fatal("a directory member the archive lacks must fail the install")
	}
}

func TestUnderDirNeverEscapes(t *testing.T) {
	dirs := []string{"data/"}
	for _, name := range []string{"/abs/data/x", "../data/x", "top/data/../../x", "data"} {
		if _, _, ok := underDir(name, dirs); ok {
			t.Errorf("%q matched", name)
		}
	}
	if _, rel, ok := underDir("top/data/a/b", dirs); !ok || rel != "data/a/b" {
		t.Errorf("top/data/a/b -> %q %v", rel, ok)
	}
}
