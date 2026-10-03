package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWithExeSuffixForFindsTheWindowsBuildOutput(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "snippets-plugin")
	if err := os.WriteFile(bare+".exe", []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := withExeSuffixFor(bare, ".exe"); got != bare+".exe" {
		t.Errorf("Windows: got %q, want the .exe the build wrote", got)
	}
	if got := withExeSuffixFor(bare, ""); got != bare {
		t.Errorf("no suffix: got %q, want the path unchanged", got)
	}
	if got := withExeSuffixFor(bare+".EXE", ".exe"); got != bare+".EXE" {
		t.Errorf("already suffixed: got %q, want it unchanged", got)
	}
	missing := filepath.Join(dir, "absent")
	if got := withExeSuffixFor(missing, ".exe"); got != missing {
		t.Errorf("neither exists: got %q, want the path as given", got)
	}
}
