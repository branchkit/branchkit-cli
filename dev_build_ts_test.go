package main

import (
	"encoding/json"
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
		{"empty listen", `{"id":"p","run":"./p-plugin","sockets":{"listen":[]}}`, "bun"},
		{"a listener", `{"id":"p","run":"./p-plugin","sockets":{"listen":[{"id":"ext","port":0}]}}`, "node"},
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
	if err != nil || name != exeName("greeter-plugin") {
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
