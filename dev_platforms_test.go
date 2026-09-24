package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The scan is the whole derivation: miss a call shape and the report quietly
// under-claims, which is worse than saying nothing because the author reads
// it as "nothing to worry about on Linux".

// testDoc is a slice of the shipped availability data's shape: rated native
// operations and one unrated method, each named in all three SDKs. The Go
// wrapper and constant for focused_window_id deliberately disagree on
// acronym casing, as the real ones do — the scan must take names from the
// data, never derive them.
func testDoc() *availabilityDoc {
	op := func(name, goW, tsW, pyW, goC, pyC string, avail map[string]string) availabilityOp {
		return availabilityOp{
			Name:         name,
			Wrappers:     map[string]string{"go": goW, "ts": tsW, "py": pyW},
			Constants:    map[string]string{"go": goC, "ts": goC, "py": pyC},
			Availability: avail,
		}
	}
	macOnly := map[string]string{"macos": "yes", "linux": "notyet", "windows": "notyet"}
	return &availabilityDoc{
		Platforms: []string{"macos", "linux", "windows"},
		Operations: []availabilityOp{
			op("native.warp_cursor", "NativeWarpCursor", "nativeWarpCursor", "native_warp_cursor",
				"MethodNativeWarpCursor", "METHOD_NATIVE_WARP_CURSOR", macOnly),
			op("native.dark_mode", "NativeDarkMode", "nativeDarkMode", "native_dark_mode",
				"MethodNativeDarkMode", "METHOD_NATIVE_DARK_MODE", macOnly),
			op("native.volume", "NativeVolume", "nativeVolume", "native_volume",
				"MethodNativeVolume", "METHOD_NATIVE_VOLUME", macOnly),
			op("native.focused_window_id", "NativeFocusedWindowID", "nativeFocusedWindowID", "native_focused_window_id",
				"MethodNativeFocusedWindowId", "METHOD_NATIVE_FOCUSED_WINDOW_ID", macOnly),
			op("native.list_spaces", "NativeListSpaces", "nativeListSpaces", "native_list_spaces",
				"MethodNativeListSpaces", "METHOD_NATIVE_LIST_SPACES", macOnly),
		},
		Unrated: []availabilityOp{
			op("input.type_text", "InputTypeText", "inputTypeText", "input_type_text",
				"MethodInputTypeText", "METHOD_INPUT_TYPE_TEXT", nil),
		},
	}
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func scan(t *testing.T, dir string, doc *availabilityDoc) *platformScan {
	t.Helper()
	r, err := scanPlatformCalls(dir, newMethodIndex(doc))
	if err != nil {
		t.Fatalf("scanPlatformCalls: %v", err)
	}
	return r
}

func opNames(r *platformScan) []string {
	out := make([]string, len(r.Operations))
	for i, op := range r.Operations {
		out[i] = op.Name
	}
	return out
}

func report(r *platformScan, doc *availabilityDoc) string {
	var b bytes.Buffer
	printPlatformReport(&b, r, doc)
	return b.String()
}

func TestScanGo(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"main.go": `package main

func run(p *Plugin) {
	p.NativeWarpCursor(1, 2)
	h.plugin.NativeFocusedWindowID()
	_ = p.Call(branchkit.MethodNativeDarkMode, nil, &out)
	_ = p.Call("native.volume", nil, &out)
	_ = PluginCall(ctx, "input.type_text", req)
	_ = p.NotAWrapper()
}
`,
		// Skipped: dependencies, generated handlers, tests, non-source.
		"vendor/v.go":    "package v\nfunc f(p *Plugin) { p.NativeListSpaces() }\n",
		"actions_gen.go": "package main\nfunc g(p *Plugin) { p.NativeListSpaces() }\n",
		"main_test.go":   "package main\nfunc h(p *Plugin) { p.NativeListSpaces() }\n",
		"notes.txt":      "p.NativeListSpaces()",
	})
	doc := testDoc()
	r := scan(t, dir, doc)

	want := []string{"native.dark_mode", "native.focused_window_id", "native.volume", "native.warp_cursor"}
	if got := opNames(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(r.Unrated, []string{"input.type_text"}) {
		t.Fatalf("unrated = %v", r.Unrated)
	}
	if r.StringCalls != 2 {
		t.Fatalf("string calls = %d, want 2 (%v)", r.StringCalls, r.StringMethods)
	}
	if r.Scanned["Go"] != 1 || r.TestsSkipped["Go"] != 1 || !r.Complete {
		t.Fatalf("coverage = scanned %v tests %v complete %v", r.Scanned, r.TestsSkipped, r.Complete)
	}
}

func TestScanTypeScript(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"src/index.ts": `import { Plugin, MethodNativeDarkMode } from "@branchkitdev/plugin-sdk-ts";
const plugin = new Plugin();
await plugin.nativeWarpCursor(1, 2);
await plugin.call(MethodNativeDarkMode);
await plugin.call<{ level: number }>('native.volume');
await plugin.notify(` + "`input.type_text`" + `, { text: "hi" });
`,
		"src/view.tsx":        "export const v = () => plugin.nativeFocusedWindowID();\n",
		"src/index.test.ts":   "plugin.nativeListSpaces();\n",
		"src/actions_gen.ts":  "plugin.nativeListSpaces();\n",
		"src/types.d.ts":      "declare function f(): void; plugin.nativeListSpaces();\n",
		"node_modules/x/a.ts": "plugin.nativeListSpaces();\n",
		"dist/index.ts":       "plugin.nativeListSpaces();\n",
	})
	r := scan(t, dir, testDoc())

	want := []string{"native.dark_mode", "native.focused_window_id", "native.volume", "native.warp_cursor"}
	if got := opNames(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(r.Unrated, []string{"input.type_text"}) {
		t.Fatalf("unrated = %v", r.Unrated)
	}
	if r.StringCalls != 2 || r.Scanned["TypeScript"] != 2 || r.TestsSkipped["TypeScript"] != 1 {
		t.Fatalf("string calls %d, scanned %v, tests %v", r.StringCalls, r.Scanned, r.TestsSkipped)
	}
	// A Go-shaped selector in TypeScript is not a TypeScript wrapper.
	if strings.Contains(strings.Join(opNames(r), ","), "list_spaces") {
		t.Fatal("a skipped file leaked into the report")
	}
}

func TestScanPython(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"main.py": `import branchkit
from branchkit.contracts_gen import METHOD_NATIVE_DARK_MODE
plugin = branchkit.Plugin()
await plugin.native_warp_cursor(1, 2)
await plugin.call(METHOD_NATIVE_DARK_MODE)
plugin.call_sync('native.volume')
await plugin.call("input.type_text", {"text": "hi"})
`,
		"actions_gen.py": "plugin.native_list_spaces()\n",
		"test_main.py":   "plugin.native_list_spaces()\n",
		// The vendored SDK (pip install --target .) defines every wrapper;
		// scanning it would claim every operation there is.
		"branchkit/methods_gen.py":   "self.native_list_spaces()\n",
		"branchkit/contracts_gen.py": "METHOD_NATIVE_LIST_SPACES = \"native.list_spaces\"\n",
		".venv/lib/x.py":             "plugin.native_list_spaces()\n",
		"venv/pyvenv.cfg":            "home = /usr/bin\n",
		"venv/lib/y.py":              "plugin.native_list_spaces()\n",
	})
	r := scan(t, dir, testDoc())

	want := []string{"native.dark_mode", "native.volume", "native.warp_cursor"}
	if got := opNames(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(r.Unrated, []string{"input.type_text"}) {
		t.Fatalf("unrated = %v", r.Unrated)
	}
	if r.StringCalls != 2 || r.Scanned["Python"] != 1 || r.TestsSkipped["Python"] != 1 {
		t.Fatalf("string calls %d, scanned %v, tests %v", r.StringCalls, r.Scanned, r.TestsSkipped)
	}
}

// String-named calls: every call shape in every language is counted, a name
// in a platform namespace that matches nothing is surfaced as a probable
// typo, and a string outside every platform namespace is not a method at all.
func TestStringCallDetection(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.go": `package main
func f(p *Plugin) {
	p.Call("native.warp_cursor", req, nil)
	p.CallWithTimeout("native.volume", nil, &out, d)
	p.Notify("native.dark_mode", nil)
	p.Call("native.dark_mod", nil, nil)
	callback("user.login")
	p.HandleAction("helloworld.greet", fn)
}
`,
		"b.ts": `plugin.call("native.volume");
plugin.notify('native.dark_mode');
`,
		"c.py": `await plugin.call("native.volume")
plugin.notify('native.focused_window_id')
`,
	})
	r := scan(t, dir, testDoc())

	wantCounts := map[string]int{
		"native.warp_cursor":       1,
		"native.volume":            3,
		"native.dark_mode":         2,
		"native.dark_mod":          1,
		"native.focused_window_id": 1,
	}
	if !reflect.DeepEqual(r.StringMethods, wantCounts) {
		t.Fatalf("string methods = %v, want %v", r.StringMethods, wantCounts)
	}
	if r.StringCalls != 8 {
		t.Fatalf("string calls = %d, want 8", r.StringCalls)
	}
	if !reflect.DeepEqual(r.UnknownStrings, []string{"native.dark_mod"}) {
		t.Fatalf("unknown strings = %v", r.UnknownStrings)
	}
	out := report(r, testDoc())
	if !strings.Contains(out, "8 call(s) name the method as a string") || !strings.Contains(out, "native.dark_mod") {
		t.Fatalf("report does not surface the string calls:\n%s", out)
	}
}

// The bug this command had: a TypeScript or Python plugin was told it "calls
// no platform operations; runs anywhere", because only .go files were read.
// "Runs anywhere" may only be printed when the scan saw everything.
func TestRunsAnywhereOnlyWhenCoverageIsComplete(t *testing.T) {
	doc := testDoc()

	clean := t.TempDir()
	writeFiles(t, clean, map[string]string{"main.go": "package main\nfunc main() {}\n"})
	if out := report(scan(t, clean, doc), doc); !strings.Contains(out, "runs anywhere") {
		t.Fatalf("a fully scanned plugin with no calls should say so:\n%s", out)
	}

	cases := map[string]map[string]string{
		"javascript only": {"index.js": "plugin.nativeWarpCursor(1, 2)\n"},
		"go beside lua":   {"main.go": "package main\n", "script.lua": "call('native.volume')\n"},
		"no source":       {"README.md": "hello\n"},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, files)
			r := scan(t, dir, doc)
			out := report(r, doc)
			if r.Complete || strings.Contains(out, "runs anywhere the platform does") {
				t.Fatalf("partial scan issued a clean bill:\n%s", out)
			}
		})
	}
}

// Data generated before TypeScript and Python names existed carries only Go
// wrappers and no unrated list. The scan must say it could not resolve those
// forms rather than report the plugin as calling nothing.
func TestOldDataIsNamedNotTrusted(t *testing.T) {
	old := testDoc()
	for i := range old.Operations {
		old.Operations[i].Wrappers = map[string]string{"go": old.Operations[i].Wrappers["go"]}
		old.Operations[i].Constants = nil
	}
	old.Unrated = nil

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"main.py": "await plugin.native_warp_cursor(1, 2)\n" +
			"await plugin.call('native.volume')\nawait plugin.call('native.newer_than_data')\n",
	})
	r := scan(t, dir, old)
	if r.Complete || !reflect.DeepEqual(r.Unresolved, []string{"Python"}) {
		t.Fatalf("complete %v, unresolved %v", r.Complete, r.Unresolved)
	}
	// String calls still resolve — they need no naming data. Old data cannot
	// tell a typo from a method it never listed, so an unmatched name is
	// reported as a call, not as a typo.
	if !reflect.DeepEqual(opNames(r), []string{"native.volume"}) {
		t.Fatalf("operations = %v", opNames(r))
	}
	if !reflect.DeepEqual(r.Unrated, []string{"native.newer_than_data"}) || len(r.UnknownStrings) != 0 {
		t.Fatalf("unrated %v unknown %v", r.Unrated, r.UnknownStrings)
	}
	out := report(r, old)
	if !strings.Contains(out, "Python typed wrappers NOT resolved") || strings.Contains(out, "runs anywhere the platform does") {
		t.Fatalf("old data not named:\n%s", out)
	}
}

func TestScanRejectsNonDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f.go")
	if err := os.WriteFile(file, []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := scanPlatformCalls(file, newMethodIndex(testDoc())); err == nil {
		t.Fatal("a file path should not be reported on as a plugin directory")
	}
}

// `undeclared` must not render as `yes`. An operation that has not said where
// it works is not the same claim as one that has, and collapsing the two is
// how an author ships against an assumption nobody made.
func TestUndeclaredHasItsOwnMark(t *testing.T) {
	if availabilityMark["undeclared"] == availabilityMark["yes"] {
		t.Fatal("undeclared renders identically to yes")
	}
	for _, state := range []string{"yes", "notyet", "never", "undeclared"} {
		if availabilityMark[state] == "" {
			t.Errorf("state %q has no mark", state)
		}
	}
}
