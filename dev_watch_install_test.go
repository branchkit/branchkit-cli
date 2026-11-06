package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeWatchFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const watchManifest = `{"id": "snippets", "name": "Snippets", "version": "0.1.0", "run": "./snippets-plugin", "requires": {"privileges": ["input"]}}`

// `plugin install .` copies; a development checkout is linked. dev watch must
// tell the two apart, because a build in the working directory reaches the
// app only through the link.
func TestInstalledCopyDistinguishesCopyFromLink(t *testing.T) {
	support := t.TempDir()
	t.Setenv("BRANCHKIT_APP_SUPPORT", support)
	src := t.TempDir()
	writeWatchFile(t, filepath.Join(src, "plugin.json"), watchManifest)

	if got := installedCopy("snippets", src); got != "" {
		t.Fatalf("not installed: want \"\", got %q", got)
	}

	dst := filepath.Join(support, "plugins", "snippets")
	if runtime.GOOS != "windows" {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(src, dst); err != nil {
			t.Fatal(err)
		}
		if got := installedCopy("snippets", src); got != "" {
			t.Fatalf("linked install: want \"\", got %q", got)
		}
		os.Remove(dst)
	}

	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := safeCopyDir(src, dst, 0); err != nil {
		t.Fatal(err)
	}
	if got := installedCopy("snippets", src); got != dst {
		t.Fatalf("copied install: want %q, got %q", dst, got)
	}
	// Watching the installed copy itself is not watching a copy.
	if got := installedCopy("snippets", dst); got != "" {
		t.Fatalf("watching the install itself: want \"\", got %q", got)
	}
}

func TestSyncInstalledCopy(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeWatchFile(t, filepath.Join(src, "plugin.json"), watchManifest)
	writeWatchFile(t, filepath.Join(src, "snippets-plugin"), "v1")
	if err := safeCopyDir(src, dst, 0); err != nil {
		t.Fatal(err)
	}

	// A rebuild with the same consent surface is copied across.
	writeWatchFile(t, filepath.Join(src, "snippets-plugin"), "v2")
	changed, err := syncInstalledCopy(src, dst)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if changed {
		t.Fatal("plugin.json did not change, but sync reported it did")
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "snippets-plugin")); string(b) != "v2" {
		t.Fatalf("installed binary not updated: %q", b)
	}

	// A manifest asking for more is not copied: that goes through
	// `plugin install`, where the new request is shown.
	writeWatchFile(t, filepath.Join(src, "plugin.json"),
		strings.Replace(watchManifest, `["input"]`, `["input", "clipboard"]`, 1))
	writeWatchFile(t, filepath.Join(src, "snippets-plugin"), "v3")
	_, err = syncInstalledCopy(src, dst)
	if err == nil || !strings.Contains(err.Error(), "plugin install") {
		t.Fatalf("expected a refusal naming plugin install, got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "snippets-plugin")); string(b) != "v2" {
		t.Fatalf("refused sync still copied: %q", b)
	}

	// A manifest edit that asks for nothing new is copied, and reported.
	writeWatchFile(t, filepath.Join(src, "plugin.json"),
		strings.Replace(watchManifest, `"Snippets"`, `"Snippets!"`, 1))
	changed, err = syncInstalledCopy(src, dst)
	if err != nil || !changed {
		t.Fatalf("manifest edit: changed=%v err=%v", changed, err)
	}
}

// A developer's build synced over an installed copy is recorded as
// installed from the developer's folder. The app keeps a copy it seeded
// from its bundle equal to the bundle, so a "bundled" record would have the
// next launch put the bundle's copy back; a release's record covers no
// binary, so it would go on vouching for the developer's build.
func TestSyncRecordsTheDevelopersFolder(t *testing.T) {
	support := t.TempDir()
	t.Setenv("BRANCHKIT_APP_SUPPORT", support)
	src := t.TempDir()
	writeWatchFile(t, filepath.Join(src, "plugin.json"), watchManifest)
	writeWatchFile(t, filepath.Join(src, "snippets-plugin"), "v1")
	dst := filepath.Join(support, "plugins", "snippets")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := safeCopyDir(src, dst, 0); err != nil {
		t.Fatal(err)
	}
	srcAbs, _ := filepath.Abs(src)
	// The release case leaves plugin.json as it was: only the binary
	// changes, which no record covers.
	for _, tc := range []struct {
		source       string
		editManifest bool
	}{
		{bundledSource, true},
		{"acme/branchkit-plugin-snippets", false},
		{localSourcePrefix + "/elsewhere", true},
	} {
		writeSourceMeta("snippets", dst, tc.source, "v1", &AuthorAttestation{Verified: true, SAN: "acme"}, true)
		writeWatchFile(t, filepath.Join(src, "snippets-plugin"), "v2-"+tc.source)
		if tc.editManifest {
			writeWatchFile(t, filepath.Join(src, "plugin.json"),
				strings.Replace(watchManifest, `"Snippets"`, `"Snippets `+tc.source+`"`, 1))
		}
		if _, err := syncInstalledCopy(src, dst); err != nil {
			t.Fatalf("sync: %v", err)
		}
		meta, _ := readSourceMeta("snippets")
		if want := localSourcePrefix + srcAbs; meta.Source != want {
			t.Errorf("synced over a %q copy: recorded %q, want %q", tc.source, meta.Source, want)
		}
		if meta.AuthorVerified || meta.RegistrySigned {
			t.Errorf("synced over a %q copy: the developer's build kept the signing results", tc.source)
		}
		if want, _, _ := installRecord(src, "", "", nil, false); meta.ManifestSHA256 != want.ManifestSHA256 {
			t.Errorf("synced over a %q copy: the record is not for the synced manifest", tc.source)
		}
	}
}

// A sync that fails partway leaves no record: the release's would speak for
// the developer's files already copied. The next one that succeeds records
// the developer's folder.
func TestAFailedSyncLeavesNoRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a symlink in the developer's folder")
	}
	support := t.TempDir()
	t.Setenv("BRANCHKIT_APP_SUPPORT", support)
	src := t.TempDir()
	writeWatchFile(t, filepath.Join(src, "plugin.json"), watchManifest)
	dst := filepath.Join(support, "plugins", "snippets")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := safeCopyDir(src, dst, 0); err != nil {
		t.Fatal(err)
	}
	writeSourceMeta("snippets", dst, "acme/branchkit-plugin-snippets", "v1", &AuthorAttestation{Verified: true}, true)
	writeWatchFile(t, filepath.Join(src, "a-plugin"), "the developer's build")
	if err := os.Symlink("/nowhere", filepath.Join(src, "b-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := syncInstalledCopy(src, dst); err == nil {
		t.Fatal("a folder holding a symlink was copied")
	}
	if _, ok := readSourceMeta("snippets"); ok {
		t.Fatal("the release's record still speaks for a partly synced copy")
	}
	// The next sync that succeeds records the developer's folder.
	os.Remove(filepath.Join(src, "b-link"))
	if _, err := syncInstalledCopy(src, dst); err != nil {
		t.Fatalf("sync: %v", err)
	}
	srcAbs, _ := filepath.Abs(src)
	if meta, ok := readSourceMeta("snippets"); !ok || meta.Source != localSourcePrefix+srcAbs {
		t.Fatalf("after a failed sync, a good one recorded %+v (ok=%v)", meta, ok)
	}
}

func TestWatchedSourceIncludesPython(t *testing.T) {
	if !watchedSource("main.py") {
		t.Fatal("a Python plugin's source must count as a change")
	}
}
