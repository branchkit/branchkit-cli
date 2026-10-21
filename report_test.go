package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAppBinaryCandidatesLookBesideTheCLIFirst(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		c := appBinaryCandidates("/x/bin", "/home/u", goos)
		if len(c) == 0 {
			t.Fatalf("%s: no candidates", goos)
		}
		if !strings.HasPrefix(c[0], filepath.Clean("/x/bin")) {
			t.Errorf("%s: first candidate %q is not beside the CLI", goos, c[0])
		}
	}
	mac := appBinaryCandidates("/x", "/home/u", "darwin")
	found := false
	for _, p := range mac {
		if p == "/Applications/BranchKit.app/Contents/MacOS/BranchKit" {
			found = true
		}
	}
	if !found {
		t.Errorf("macOS candidates miss /Applications: %v", mac)
	}
}
