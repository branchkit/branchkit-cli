package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// writeFakePlugin builds a plugin dir with runtime files (plugin.json, a
// data/ dir, an in-dir binary) plus build inputs that must NOT ship (src/,
// go.mod, a prior release output).
func writeFakePlugin(t *testing.T, runField string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("plugin.json", `{"id":"demo","name":"Demo","version":"1.0.0","run":"`+runField+`"}`)
	write("data/keys.json", `["a","b"]`)
	write("LICENSE", "MIT")
	write(strings.TrimPrefix(runField, "./"), "#!/bin/sh\necho hi\n") // in-dir binary
	// build inputs / junk that must be excluded:
	write("src/main.go", "package main")
	write("go.mod", "module demo")
	write("go.sum", "")
	write("branchkit-plugin-demo-linux-x86_64.tar.gz", "stale release output")
	write(".gitignore", "demo\n")
	return dir
}

func tarEntries(t *testing.T, tarGzPath string) map[string]tarEntry {
	t.Helper()
	raw, err := os.ReadFile(tarGzPath)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]tarEntry{}
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		body, _ := readAllTar(tr)
		out[hdr.Name] = tarEntry{mode: hdr.Mode, modTime: hdr.ModTime.Unix(), body: string(body)}
	}
	return out
}

type tarEntry struct {
	mode    int64
	modTime int64
	body    string
}

func readAllTar(tr *tar.Reader) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(tr)
	return buf.Bytes(), err
}

func TestCollectPayloadDenylist(t *testing.T) {
	dir := writeFakePlugin(t, "./demo")
	entries, err := collectPayload(dir, "./demo", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.tarPath] = true
	}
	// Runtime files ship.
	for _, want := range []string{"plugin.json", "data/keys.json", "LICENSE", "demo"} {
		if !got[want] {
			t.Errorf("payload missing %q; has %v", want, keys(got))
		}
	}
	// Build inputs / junk / prior outputs must NOT ship.
	for _, bad := range []string{"src/main.go", "go.mod", "go.sum", ".gitignore",
		"branchkit-plugin-demo-linux-x86_64.tar.gz"} {
		if got[bad] {
			t.Errorf("payload wrongly includes %q", bad)
		}
	}
}

func TestPayloadBinaryExecutableBit(t *testing.T) {
	dir := writeFakePlugin(t, "./demo")
	out := t.TempDir()
	entries, err := collectPayload(dir, "./demo", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	tarPath := filepath.Join(out, "p.tar.gz")
	if err := writeDeterministicTarGz(entries, tarPath); err != nil {
		t.Fatal(err)
	}
	got := tarEntries(t, tarPath)
	if got["demo"].mode != 0o755 {
		t.Errorf("run binary mode = %o, want 755", got["demo"].mode)
	}
	if got["plugin.json"].mode != 0o644 {
		t.Errorf("plugin.json mode = %o, want 644", got["plugin.json"].mode)
	}
	if got["demo"].modTime != 0 {
		t.Errorf("mtime not zeroed: %d", got["demo"].modTime)
	}
}

func TestPackageIsDeterministic(t *testing.T) {
	dir := writeFakePlugin(t, "./demo")
	out := t.TempDir()
	entries, err := collectPayload(dir, "./demo", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(out, "a.tar.gz")
	b := filepath.Join(out, "b.tar.gz")
	if err := writeDeterministicTarGz(entries, a); err != nil {
		t.Fatal(err)
	}
	if err := writeDeterministicTarGz(entries, b); err != nil {
		t.Fatal(err)
	}
	ba, _ := os.ReadFile(a)
	bb, _ := os.ReadFile(b)
	if !bytes.Equal(ba, bb) {
		t.Fatal("same payload produced different archive bytes — not reproducible")
	}
}

func TestBinaryOverrideReplacesInDirBinary(t *testing.T) {
	// Cross-compile case: the in-dir binary is the host build; --binary
	// supplies the target-platform build, which must win.
	dir := writeFakePlugin(t, "./demo")
	override := filepath.Join(t.TempDir(), "demo-linux")
	if err := os.WriteFile(override, []byte("LINUX BUILD"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := collectPayload(dir, "./demo", override, nil)
	if err != nil {
		t.Fatal(err)
	}
	var binaryEntries int
	var body string
	for _, e := range entries {
		if e.tarPath == "demo" {
			binaryEntries++
			data, _ := os.ReadFile(e.srcPath)
			body = string(data)
		}
	}
	if binaryEntries != 1 {
		t.Fatalf("expected exactly one 'demo' entry, got %d", binaryEntries)
	}
	if body != "LINUX BUILD" {
		t.Errorf("override binary not used; body = %q", body)
	}
}

func TestMissingBinaryIsError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "plugin.json"),
		[]byte(`{"id":"demo","run":"./demo"}`), 0o644)
	// run binary neither in-dir nor via --binary.
	if _, err := collectPayload(dir, "./demo", "", nil); err == nil {
		t.Fatal("expected an error when the run binary is absent")
	}
}

func TestReleaseArtifactName(t *testing.T) {
	if got := releaseArtifactName("demo", "linux", "amd64"); got != "branchkit-plugin-demo-linux-x86_64.tar.gz" {
		t.Errorf("got %q", got)
	}
	if got := releaseArtifactName("demo", "darwin", "arm64"); got != "branchkit-plugin-demo-darwin-arm64.tar.gz" {
		t.Errorf("got %q", got)
	}
}

// TestPackageRoundTripsThroughInstall proves package produces exactly what the
// install path consumes: extract the tarball and findManifest must locate the
// plugin.json, with the binary + data present.
func TestPackageRoundTripsThroughInstall(t *testing.T) {
	dir := writeFakePlugin(t, "./demo")
	out := t.TempDir()
	entries, err := collectPayload(dir, "./demo", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	tarPath := filepath.Join(out, "rel.tar.gz")
	if err := writeDeterministicTarGz(entries, tarPath); err != nil {
		t.Fatal(err)
	}

	extractDir := filepath.Join(out, "extracted")
	os.MkdirAll(extractDir, 0o755)
	if err := extractTarball(tarPath, extractDir); err != nil {
		t.Fatalf("install-path extract failed: %v", err)
	}
	mPath, err := findManifest(extractDir)
	if err != nil {
		t.Fatalf("install-path findManifest failed: %v", err)
	}
	mDir := filepath.Dir(mPath)
	for _, want := range []string{"demo", "data/keys.json"} {
		if !fileExists(filepath.Join(mDir, filepath.FromSlash(want))) {
			t.Errorf("extracted plugin missing %q", want)
		}
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
