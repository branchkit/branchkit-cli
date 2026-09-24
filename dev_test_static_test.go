package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func manifestFromJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad test manifest: %v", err)
	}
	return m
}

func findResult(results []TestResult, name string) *TestResult {
	for i := range results {
		if results[i].Name == name {
			return &results[i]
		}
	}
	return nil
}

func statusesFor(results []TestResult, name string) []string {
	var out []string
	for _, r := range results {
		if r.Name == name {
			out = append(out, r.Status)
		}
	}
	return out
}

// Both wire forms of a consumes.collections entry parse, and the object
// form carries its fields.
func TestParseConsumedCollectionsAcceptsBothForms(t *testing.T) {
	m := manifestFromJSON(t, `{"consumes":{"collections":[
		"keycodes",
		{"name":"apps","fields":["spoken","bundle_id"]},
		{"name":"bare_object"}
	]}}`)
	got := parseConsumedCollections(m)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(got), got)
	}
	if got[0].Name != "keycodes" || len(got[0].Fields) != 0 {
		t.Errorf("bare string entry wrong: %+v", got[0])
	}
	if got[1].Name != "apps" || strings.Join(got[1].Fields, ",") != "spoken,bundle_id" {
		t.Errorf("shaped entry wrong: %+v", got[1])
	}
	if got[2].Name != "bare_object" || len(got[2].Fields) != 0 {
		t.Errorf("object-without-fields entry wrong: %+v", got[2])
	}
}

// Regression: a capture satisfied by a SHAPED consumes entry must not warn.
// The capture resolver read bare strings only, so introducing the object
// form would have made every shaped consumer look undeclared.
func TestCaptureReferenceSatisfiedByShapedConsumesEntry(t *testing.T) {
	m := manifestFromJSON(t, `{
		"id":"launcher",
		"consumes":{"collections":[{"name":"apps","fields":["spoken"]}]},
		"commands":[{"pattern":["focus","<apps>"],"action":{"type":"plugin","action_type":"launcher.focus"}}]
	}`)
	for _, r := range checkCaptureReferences(t.TempDir(), m) {
		if r.Status == "warn" && strings.Contains(r.Detail, "apps") {
			t.Fatalf("shaped consumes entry must satisfy the capture, got: %+v", r)
		}
	}
}

func TestBareCaptureOfADottedCollectionIsAnError(t *testing.T) {
	// A bare capture takes the collection name as its binding name, and
	// binding names cannot contain dots. Without this check the failure is
	// silent: the action param keeps `{plugin.acme.widgets}` as a literal,
	// and nothing reports it at load or at match time.
	dir := t.TempDir()
	cmds := `[{"pattern":["show","<plugin.acme.widgets>"],` +
		`"action":{"type":"acme.show","target":"{plugin.acme.widgets}"}}]`
	if err := os.WriteFile(filepath.Join(dir, "commands.json"), []byte(cmds), 0o644); err != nil {
		t.Fatal(err)
	}
	m := manifestFromJSON(t, `{
		"id":"acme",
		"provides":{"collections":{"plugin.acme.widgets":{}}},
		"collection_data":{"voice_commands":"commands.json"}
	}`)
	var got *TestResult
	for _, r := range checkCaptureReferences(dir, m) {
		if r.Status == "error" && strings.Contains(r.Detail, "binding name") {
			got = &r
		}
	}
	if got == nil {
		t.Fatal("a bare capture of a dotted collection must be reported")
	}
	if !strings.Contains(got.Detail, "<name:plugin.acme.widgets>") {
		t.Fatalf("the report must name the fix, got: %s", got.Detail)
	}
}

func TestExplicitBindingAllowsADottedCollection(t *testing.T) {
	// The escape hatch, and how voice's tie-choice collection moved into
	// `plugin.voice.*`: bind an explicit name and the dotted collection name
	// never becomes a binding name.
	dir := t.TempDir()
	cmds := `[{"pattern":["pick","<choice:plugin.acme.widgets>"],` +
		`"action":{"type":"acme.pick","target":"{choice}"}}]`
	if err := os.WriteFile(filepath.Join(dir, "commands.json"), []byte(cmds), 0o644); err != nil {
		t.Fatal(err)
	}
	m := manifestFromJSON(t, `{
		"id":"acme",
		"provides":{"collections":{"plugin.acme.widgets":{}}},
		"collection_data":{"voice_commands":"commands.json"}
	}`)
	for _, r := range checkCaptureReferences(dir, m) {
		if r.Status == "error" {
			t.Fatalf("an explicitly bound capture is legal, got: %+v", r)
		}
	}
}

func TestConsumedCollectionsBareFormPasses(t *testing.T) {
	m := manifestFromJSON(t, `{"consumes":{"collections":["apps","keycodes"]}}`)
	results := checkConsumedCollections(m)
	r := findResult(results, "consumed_collections")
	if r == nil || r.Status != "pass" {
		t.Fatalf("expected a pass, got: %+v", results)
	}
}

// Self-consumption is the one shape the CLI can fully verify: both the
// declaration and the schema are in this file.
func TestConsumedCollectionsSelfConsumedFieldMismatchFails(t *testing.T) {
	m := manifestFromJSON(t, `{
		"provides":{"collections":{"apps":{"preset":"named_entities","fields":[
			{"key":"spoken"},{"key":"bundle_id"}
		]}}},
		"consumes":{"collections":[{"name":"apps","fields":["spoken","bundleId"]}]}
	}`)
	results := checkConsumedCollections(m)
	var failed *TestResult
	for i := range results {
		if results[i].Status == "fail" {
			failed = &results[i]
		}
	}
	if failed == nil {
		t.Fatalf("expected a fail for the misspelled field, got: %+v", results)
	}
	if !strings.Contains(failed.Detail, "bundleId") {
		t.Errorf("failure must name the offending field: %s", failed.Detail)
	}
}

func TestConsumedCollectionsSelfConsumedFieldMatchPasses(t *testing.T) {
	m := manifestFromJSON(t, `{
		"provides":{"collections":{"apps":{"preset":"named_entities","fields":[
			{"key":"spoken"},{"key":"bundle_id"}
		]}}},
		"consumes":{"collections":[{"name":"apps","fields":["spoken","bundle_id"]}]}
	}`)
	for _, s := range statusesFor(checkConsumedCollections(m), "consumed_collections") {
		if s == "fail" {
			t.Fatalf("a satisfied self-consumed shape must not fail: %+v", checkConsumedCollections(m))
		}
	}
}

// A cross-plugin shape is not checkable here — the provider's schema lives
// in another manifest. The CLI must stay quiet and say the platform checks
// it, rather than guessing.
func TestConsumedCollectionsCrossPluginShapeIsDeferredNotFailed(t *testing.T) {
	m := manifestFromJSON(t, `{"consumes":{"collections":[{"name":"apps","fields":["anything"]}]}}`)
	results := checkConsumedCollections(m)
	r := findResult(results, "consumed_collections")
	if r == nil || r.Status != "pass" {
		t.Fatalf("cross-plugin shape must pass locally, got: %+v", results)
	}
	if !strings.Contains(r.Detail, "checked at load") {
		t.Errorf("the pass should say where the real check happens: %s", r.Detail)
	}
}

func TestConsumedCollectionsMalformedEntryFails(t *testing.T) {
	for _, bad := range []string{
		`{"consumes":{"collections":[{"fields":["a"]}]}}`, // object with no name
		`{"consumes":{"collections":[""]}}`,               // empty name
		`{"consumes":{"collections":[42]}}`,               // wrong type
	} {
		results := checkConsumedCollections(manifestFromJSON(t, bad))
		found := false
		for _, r := range results {
			if r.Status == "fail" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a fail for %s, got: %+v", bad, results)
		}
	}
}

// The one-params dialect envelope check mirrors the platform's
// parse_action_or_template, sequences included, recursing into steps.
func TestActionEnvelopeErrors(t *testing.T) {
	obj := func(s string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatalf("bad test action: %v", err)
		}
		return m
	}
	cases := []struct {
		name    string
		action  string
		wantErr string // substring; "" = valid
	}{
		{"nested is valid", `{"type": "x.y", "params": {"a": 1}}`, ""},
		{"phase-only is valid", `{"type": "x.y", "phase": "start"}`, ""},
		{"flat payload key refused", `{"type": "x.y", "text": "hi"}`, "unknown key"},
		{"non-object params refused", `{"type": "x.y", "params": "hi"}`, "must be an object"},
		{"sequence is valid", `{"type": "sequence", "actions": [{"type": "x.y"}]}`, ""},
		{"sequence stray key refused", `{"type": "sequence", "actions": [], "repeat": 3}`, "unknown key"},
		{"sequence params refused", `{"type": "sequence", "actions": [], "params": {}}`, "unknown key"},
		{"sequence missing actions refused", `{"type": "sequence"}`, "requires an \"actions\" array"},
		{"flat sequence step refused", `{"type": "sequence", "actions": [{"type": "x.y", "text": "hi"}]}`, "sequence step 0"},
	}
	for _, tc := range cases {
		errs := actionEnvelopeErrors(obj(tc.action))
		if tc.wantErr == "" {
			if len(errs) != 0 {
				t.Errorf("%s: expected valid, got %v", tc.name, errs)
			}
			continue
		}
		if len(errs) == 0 {
			t.Errorf("%s: expected error containing %q, got none", tc.name, tc.wantErr)
			continue
		}
		if !strings.Contains(strings.Join(errs, "; "), tc.wantErr) {
			t.Errorf("%s: expected error containing %q, got %v", tc.name, tc.wantErr, errs)
		}
	}
}

// A keybind binding's params must be an object, mirroring the seeder.
func TestKeybindNonObjectParamsFails(t *testing.T) {
	m := manifestFromJSON(t, `{
		"collection_data": {"keybinds": {
			"alt+p": {"action": "x.y", "params": "flat"},
			"alt+n": {"action": "x.y", "params": {"a": 1}}
		}}
	}`)
	results := checkKeybindBindings(m)
	bad := findResult(results, "keybind_alt+p")
	if bad == nil || bad.Status != "fail" {
		t.Fatalf("expected keybind_alt+p to fail, got %+v", results)
	}
	if good := findResult(results, "keybind_alt+n"); good != nil && good.Status == "fail" {
		t.Fatalf("nested binding should not fail: %+v", good)
	}
}

// `run` is a command line: an interpreter-run plugin is checked by its script,
// not by a file literally named "python3 main.py".
func TestRunTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	py := []any{"python"}
	cases := []struct {
		run         string
		runtimes    any
		target, via string
	}{
		{"python3 main.py", py, "main.py", "python3"},
		{"python3 -u ./main.py", py, "main.py", "python3"},
		{"python3", py, "", "python3"},
		{"bun run src/index.ts", []any{"bun"}, "src/index.ts", "bun"}, // a subcommand is not the script
		{"./run.sh", []any{"bun"}, "run.sh", ""},                      // a real file in the plugin is the program
		{"./hello-plugin --flag", nil, "hello-plugin", ""},
	}
	for _, c := range cases {
		target, via := runTarget(dir, c.run, c.runtimes)
		if target != c.target || via != c.via {
			t.Errorf("runTarget(%q) = (%q, %q), want (%q, %q)", c.run, target, via, c.target, c.via)
		}
	}
}

// A `provides.collections` entry may be written inline or as the path of a
// JSON file holding the same object. Both spellings must get the SAME
// checks: the file form went four+ weeks reported as
// "collection schema must be an object" — six failures against a manifest
// that was correct, on a feature the platform had shipped, which is the
// whole cost of a checker that knows one of two legal forms.
func TestProvidedCollectionFileReferenceIsValidatedLikeInline(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "collections"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A referenced file carrying an unknown preset must fail for the
	// PRESET, not for its spelling — proof the contents are really checked
	// rather than the reference merely being tolerated.
	if err := os.WriteFile(
		filepath.Join(dir, "collections", "bad_preset.json"),
		[]byte(`{"preset":"not_a_preset"}`), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "collections", "good.json"),
		[]byte(`{"preset":"log"}`), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	m := manifestFromJSON(t, `{"provides":{"collections":{
		"inline":  {"preset":"log"},
		"from_file": "collections/good.json",
		"bad_preset": "collections/bad_preset.json"
	}}}`)

	results := checkProvidedCollections(dir, m)

	if r := findResult(results, "collection_from_file"); r != nil {
		t.Fatalf("a valid file-referenced collection should raise nothing, got %q: %s", r.Status, r.Detail)
	}
	r := findResult(results, "collection_bad_preset")
	if r == nil || r.Status != "fail" {
		t.Fatalf("an unknown preset inside a referenced file must fail, got %+v", r)
	}
	if !strings.Contains(r.Detail, "not_a_preset") {
		t.Fatalf("the failure must name the preset, not the spelling: %s", r.Detail)
	}
}

// A referenced file that is missing or malformed FAILS and names the file.
// The platform stops the plugin on exactly this, so the check agrees with
// the runtime about what is fatal rather than passing something that will
// not load.
func TestProvidedCollectionMissingOrMalformedFileFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "collections"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "collections", "truncated.json"),
		[]byte(`{"preset":`), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	m := manifestFromJSON(t, `{"provides":{"collections":{
		"gone":      "collections/nope.json",
		"truncated": "collections/truncated.json",
		"escaping":  "../outside.json",
		"absolute":  "/etc/passwd"
	}}}`)

	results := checkProvidedCollections(dir, m)

	for name, want := range map[string]string{
		"collection_gone":      "unreadable",
		"collection_truncated": "not valid JSON",
		"collection_escaping":  "escapes the plugin directory",
		"collection_absolute":  "must be a relative path",
	} {
		r := findResult(results, name)
		if r == nil || r.Status != "fail" {
			t.Fatalf("%s: expected a failure, got %+v", name, r)
		}
		if !strings.Contains(r.Detail, want) {
			t.Fatalf("%s: detail %q should mention %q", name, r.Detail, want)
		}
	}
}

// The original defect's shape, kept as a regression: a value that is
// neither an object nor a string is still wrong, and the message now names
// both legal forms so the reader knows what to write.
func TestProvidedCollectionNonObjectNonStringStillFails(t *testing.T) {
	m := manifestFromJSON(t, `{"provides":{"collections":{"bogus": 42}}}`)
	results := checkProvidedCollections(t.TempDir(), m)
	r := findResult(results, "collection_bogus")
	if r == nil || r.Status != "fail" {
		t.Fatalf("a numeric collection entry must fail, got %+v", r)
	}
	if !strings.Contains(r.Detail, "path of a JSON file") {
		t.Fatalf("the message should name both legal forms: %s", r.Detail)
	}
}
