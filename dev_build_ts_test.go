package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tsManifestFrom(t *testing.T, raw string) tsManifest {
	t.Helper()
	var m tsManifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// The engine is decided by the manifest alone, and Node is chosen for exactly
// one reason: Bun cannot serve an inherited listener fd.
func TestTsEngine(t *testing.T) {
	cases := []struct{ name, manifest, want string }{
		{"no sockets", `{"id":"p","run":"./p-plugin"}`, "bun"},
		{"empty listen", `{"id":"p","run":"./p-plugin","requires":{"sockets":{"listen":[]}}}`, "bun"},
		{"a listener", `{"id":"p","run":"./p-plugin","requires":{"sockets":{"listen":[{"id":"ext","port":0}]}}}`, "node"},
		// The pre-`requires` shape must NOT be read as a listener: it is not
		// one, and silently choosing bun for a plugin that needs node is the
		// failure this whole move exists to prevent.
		{"flat sockets is not a listener", `{"id":"p","run":"./p-plugin","sockets":{"listen":[{"id":"ext","port":0}]}}`, "bun"},
	}
	for _, c := range cases {
		if got := tsEngine(tsManifestFrom(t, c.manifest)); got != c.want {
			t.Errorf("%s: engine = %q, want %q", c.name, got, c.want)
		}
	}
}

// `run` must name a program in the plugin directory. Every older shape is
// refused with the manifest lines to write instead.
func TestTsOutputName(t *testing.T) {
	ok := tsManifestFrom(t, `{"id":"greeter","run":"./greeter-plugin"}`)
	name, err := tsOutputName(ok)
	if err != nil || name != "greeter-plugin" {
		t.Fatalf("tsOutputName = (%q, %v)", name, err)
	}

	for _, run := range []string{"./run.sh", "bun run src/index.ts", "node dist/index.js", "greeter-plugin", "./bin/greeter-plugin", ""} {
		m := tsManifest{ID: "greeter", Run: run}
		_, err := tsOutputName(m)
		if err == nil {
			t.Errorf("run %q was accepted", run)
			continue
		}
		if !strings.Contains(err.Error(), `"./greeter-plugin"`) || !strings.Contains(err.Error(), "branchkit-cli") {
			t.Errorf("run %q: the error does not say what to write instead: %v", run, err)
		}
	}
}

// The static check refuses a JavaScript runtime as the launch program, and a
// shell script without the privilege that lets the sandbox exec one.
func TestRunBinaryRefusesUnspawnableLaunchers(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		manifest map[string]any
		status   string
	}{
		{map[string]any{"run": "bun run src/index.ts"}, "fail"},
		{map[string]any{"run": "node dist/index.js"}, "fail"},
		{map[string]any{"run": "./run.sh"}, "fail"},
		{map[string]any{"run": "./run.sh", "privileges": []any{"shell"}}, "warn"}, // allowed; the file is just absent here
	} {
		if got := checkRunBinary(dir, c.manifest); got.Status != c.status {
			t.Errorf("run %v: status %q, want %q (%s)", c.manifest["run"], got.Status, c.status, got.Detail)
		}
	}
}

// A cross-build lands under dist/<os>-<arch>/ and never where the host binary
// — possibly the one the running app is executing — lives.
func TestBuildTargetOutputPath(t *testing.T) {
	host := hostTarget()
	if got := host.outputPath("/p", "greeter-plugin"); got != filepath.Join("/p", exeName("greeter-plugin")) {
		t.Errorf("host output = %q", got)
	}
	win, err := parseBuildTarget("windows", "x64")
	if err != nil || win.goarch != "amd64" {
		t.Fatalf("parseBuildTarget(windows, x64) = (%v, %v)", win, err)
	}
	if !win.isHost() {
		want := filepath.Join("/p", "dist", "windows-amd64", "greeter-plugin.exe")
		if got := win.outputPath("/p", "greeter-plugin"); got != want {
			t.Errorf("windows output = %q, want %q", got, want)
		}
	}
	if _, err := parseBuildTarget("plan9", "amd64"); err == nil {
		t.Error("an unknown --os was accepted")
	}
	if _, err := parseBuildTarget("linux", "mips"); err == nil {
		t.Error("an unknown --arch was accepted")
	}
	zero, err := parseBuildTarget("", "")
	if err != nil || !zero.isHost() {
		t.Errorf("no flags must mean this machine: (%v, %v)", zero, err)
	}
}

// The injector that writes our shipped executable is pinned by DIGEST, not
// just by version. A version alone is the registry's word for it, and the
// registry is the party a supply-chain attack compromises.
func TestPostjectIsPinnedByDigestNotJustVersion(t *testing.T) {
	if len(postjectSHA256) != 64 {
		t.Fatalf("postjectSHA256 must be a full sha256 hex digest, got %q", postjectSHA256)
	}
	for _, c := range postjectSHA256 {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("postjectSHA256 has a non-hex character %q", c)
		}
	}
	if !strings.Contains(postjectTarballURL(), postjectVersion) {
		t.Fatalf("the URL must carry the pinned version, got %s", postjectTarballURL())
	}
}

// A tar entry that climbs out of the destination lands INSIDE it instead.
//
// Asserting the real mechanism, not the one the code reads like. The first
// version of this test was called ...RefusesPathTraversal and checked for an
// error; it passed while the escape guard never fired, because
// `filepath.Clean("/" + name)` had already rewritten `../escaped.txt` to
// `/escaped.txt`. Same safe outcome, different cause — and a test that
// credits the wrong cause stops protecting the right one the moment someone
// simplifies it away.
func TestExtractTarGzTreeNeutralisesPathTraversal(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("owned")
	if err := tw.WriteHeader(&tar.Header{
		Name: "../escaped.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	tw.Write(body)
	tw.Close()
	gz.Close()

	root := t.TempDir()
	inner := filepath.Join(root, "inner")
	if err := extractTarGzTree(&buf, inner); err != nil {
		// A refusal is also acceptable — it is the second lock doing the job.
		if !strings.Contains(err.Error(), "escapes") {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	// The load-bearing assertion: nothing above the destination.
	if _, err := os.Stat(filepath.Join(root, "escaped.txt")); err == nil {
		t.Fatal("a ../ entry was written OUTSIDE the destination")
	}
	// And the entry was confined rather than silently dropped, so the
	// mechanism is neutralisation and this test would notice if it changed.
	if _, err := os.Stat(filepath.Join(inner, "escaped.txt")); err != nil {
		t.Fatalf("expected the entry confined to the destination: %v", err)
	}
}

// The ordinary case still works, so the traversal guard has not been made so
// strict that a normal package fails to unpack.
func TestExtractTarGzTreeUnpacksANormalTree(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("console.log('hi')")
	if err := tw.WriteHeader(&tar.Header{
		Name: "package/dist/cli.js", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	tw.Write(body)
	tw.Close()
	gz.Close()

	dest := t.TempDir()
	if err := extractTarGzTree(&buf, dest); err != nil {
		t.Fatalf("a normal package must unpack: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "package", "dist", "cli.js"))
	if err != nil {
		t.Fatalf("expected the CLI at the path verifiedPostject looks for: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("contents differ: %q", got)
	}
}
