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

// A Python plugin's run names the managed interpreter and a script. The
// script is the program: it must be present, and it ships with the rest of
// the directory (the vendored SDK included).
func TestPackageInterpretedRun(t *testing.T) {
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"plugin.json":           `{"id": "snippets", "run": "python3 main.py", "requires": {"runtimes": ["python"]}}`,
		"main.py":               "import branchkit\n",
		"actions_gen.py":        "",
		"branchkit/__init__.py": "",
	} {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := runProgram(dir, "python3 main.py"); got != "main.py" {
		t.Fatalf("runProgram = %q, want main.py", got)
	}
	entries, err := collectPayload(dir, "python3 main.py", "", nil)
	if err != nil {
		t.Fatalf("an interpreted plugin must package: %v", err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.tarPath] = true
	}
	for _, want := range []string{"plugin.json", "main.py", "actions_gen.py", "branchkit/__init__.py"} {
		if !got[want] {
			t.Errorf("payload missing %q; has %v", want, keys(got))
		}
	}
	if _, err := collectPayload(dir, "python3 missing.py", "", nil); err == nil {
		t.Fatal("a run script that does not exist must be an error")
	}
	if got := runProgram(dir, "./snippets-plugin"); got != "snippets-plugin" {
		t.Fatalf("compiled run: runProgram = %q", got)
	}
}

// A plugin that ships a pipeline stage: the stage binary is marked
// executable in the archive (it used to ship 0644 and install unrunnable),
// a missing one refuses the release, and a stage another OS's constraint
// excludes is not required.
func TestStageBinariesShipExecutable(t *testing.T) {
	dir := writeFakePlugin(t, "./demo")
	m := PluginManifest{Run: "./demo", Provides: &ProvidesCfg{Stages: map[string]StageDecl{
		"a":       {Binary: "engine"},
		"b":       {Binary: "./engine"},
		"bare":    {},
		"maconly": {Binary: "mac_engine", Platform: []byte(`"macos"`)},
		"desktop": {Binary: "desk_engine", Platform: []byte(`["linux","windows"]`)},
	}}}
	if got, want := strings.Join(stageBinaries(m, "linux"), ","), "bare,desk_engine,engine"; got != want {
		t.Fatalf("linux stage binaries = %s, want %s", got, want)
	}
	if got, want := strings.Join(stageBinaries(m, "windows"), ","), "bare.exe,desk_engine.exe,engine.exe"; got != want {
		t.Fatalf("windows stage binaries = %s, want %s", got, want)
	}
	if got, want := strings.Join(stageBinaries(m, "darwin"), ","), "bare,engine,mac_engine"; got != want {
		t.Fatalf("darwin stage binaries = %s, want %s", got, want)
	}

	entries, err := collectPayload(dir, "./demo", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := markStageBinaries(entries, []string{"engine"}); err == nil || !strings.Contains(err.Error(), "engine") {
		t.Fatalf("missing stage binary: err = %v, want one naming it", err)
	}

	for _, f := range []string{"engine", "bare", "mac_engine"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("bin"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entries, err = collectPayload(dir, "./demo", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := markStageBinaries(entries, stageBinaries(m, "darwin")); err != nil {
		t.Fatal(err)
	}
	tarPath := filepath.Join(t.TempDir(), "p.tar.gz")
	if err := writeDeterministicTarGz(entries, tarPath); err != nil {
		t.Fatal(err)
	}
	got := tarEntries(t, tarPath)
	for _, f := range []string{"engine", "bare", "mac_engine", "demo"} {
		if got[f].mode != 0o755 {
			t.Errorf("%s mode = %o, want 755", f, got[f].mode)
		}
	}
	if got["LICENSE"].mode != 0o644 {
		t.Errorf("LICENSE mode = %o, want 644", got["LICENSE"].mode)
	}
}
