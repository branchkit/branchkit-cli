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
// form carries its fields (docs/design/DESIGN_SHAPED_CONSUMPTION.md).
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
