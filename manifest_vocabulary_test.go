package main

import (
	"strings"
	"testing"
)

// The embedded vocabulary is generated. If it ever arrives empty, every
// check built on it silently passes everything — the exact failure mode
// these checks exist to prevent, one level up. So assert it has content
// before asserting anything about the checks themselves.
func TestEmbeddedVocabularyIsPopulated(t *testing.T) {
	if vocabulary.APIVersion == "" {
		t.Error("embedded api_version is empty")
	}
	if len(vocabulary.Privileges) == 0 {
		t.Error("embedded privileges list is empty — every privilege would validate")
	}
	if len(vocabulary.PluginMethods) == 0 {
		t.Error("embedded plugin_methods list is empty — every implements key would validate")
	}
	if !inList(vocabulary.PluginMethods, "on_action") {
		t.Errorf("on_action missing from plugin_methods: %v", vocabulary.PluginMethods)
	}
	if !inList(vocabulary.Privileges, "dispatch") {
		t.Error("dispatch missing from privileges")
	}
	if !inList(vocabulary.NonMethodImplementsKeys, "settings_tabs") {
		t.Error("settings_tabs must be accepted under implements")
	}
}

// An unrecognized `implements` key is INERT at runtime: nothing dispatches
// it and nothing says so. `PluginImplements` in the actuator records a real
// shipped bug from exactly this — a plugin declared
// `parse_key_event` and called it on every keypress, getting an error every
// time. That name is the regression case.
func TestImplementsRejectsInertMethodNames(t *testing.T) {
	m := manifestFromJSON(t, `{
		"id": "x",
		"implements": {"parse_key_event": true, "on_action": true, "settings_tabs": []}
	}`)
	results := checkImplementsMethods(m)

	got := findResult(results, "implements_parse_key_event")
	if got == nil {
		t.Fatalf("parse_key_event not flagged; got %+v", results)
	}
	if got.Status != "fail" {
		t.Errorf("want fail, got %q", got.Status)
	}
	if !strings.Contains(got.Detail, "inert") {
		t.Errorf("detail should say the declaration is inert: %q", got.Detail)
	}
	// The valid neighbours must not be flagged.
	if findResult(results, "implements_on_action") != nil {
		t.Error("on_action is a real method and must pass")
	}
	if findResult(results, "implements_settings_tabs") != nil {
		t.Error("settings_tabs is a declaration, not a method, and must pass")
	}
}

func TestImplementsSuggestsTheNearMiss(t *testing.T) {
	m := manifestFromJSON(t, `{"id": "x", "implements": {"on_actoin": true}}`)
	got := findResult(checkImplementsMethods(m), "implements_on_actoin")
	if got == nil {
		t.Fatal("typo not flagged")
	}
	if !strings.Contains(got.Detail, `did you mean "on_action"`) {
		t.Errorf("want a suggestion for the typo, got %q", got.Detail)
	}
}

func TestImplementsPassesAValidBlock(t *testing.T) {
	m := manifestFromJSON(t, `{
		"id": "x", "implements": {"on_action": true, "render_settings": true}
	}`)
	results := checkImplementsMethods(m)
	if len(results) != 1 || results[0].Status != "pass" {
		t.Fatalf("want one pass, got %+v", results)
	}
}

// Both privilege fields are checked. `optional_privileges` is the one a
// hand-enumerated implementation forgets, which is why the check iterates a
// list of field names rather than naming each.
func TestPrivilegeNamesCheckBothFields(t *testing.T) {
	m := manifestFromJSON(t, `{
		"id": "x",
		"privileges": ["clipbord"],
		"optional_privileges": ["audiox"]
	}`)
	results := checkPrivilegeNames(m)

	for _, name := range []string{"privileges_clipbord", "optional_privileges_audiox"} {
		got := findResult(results, name)
		if got == nil {
			t.Fatalf("%s not flagged; got %+v", name, results)
		}
		if got.Status != "fail" {
			t.Errorf("%s: want fail, got %q", name, got.Status)
		}
		if !strings.Contains(got.Detail, "did you mean") {
			t.Errorf("%s: want a suggestion, got %q", name, got.Detail)
		}
	}
}

func TestPrivilegeNamesPassKnownOnes(t *testing.T) {
	m := manifestFromJSON(t, `{"id": "x", "privileges": ["dispatch", "clipboard"]}`)
	results := checkPrivilegeNames(m)
	if len(results) != 1 || results[0].Status != "pass" {
		t.Fatalf("want one pass, got %+v", results)
	}
}

func TestMinAPIVersionRejectsTheUnsatisfiable(t *testing.T) {
	m := manifestFromJSON(t, `{"id": "x", "min_api_version": "9.0.0"}`)
	got := findResult(checkMinAPIVersion(m), "min_api_version")
	if got == nil || got.Status != "fail" {
		t.Fatalf("want fail for an unsatisfiable version, got %+v", got)
	}
	if !strings.Contains(got.Detail, "refused at load") {
		t.Errorf("detail should say what happens at load: %q", got.Detail)
	}
}

func TestMinAPIVersionAcceptsTheCurrentOne(t *testing.T) {
	m := manifestFromJSON(t, `{"id": "x", "min_api_version": "`+vocabulary.APIVersion+`"}`)
	got := findResult(checkMinAPIVersion(m), "min_api_version")
	if got == nil || got.Status != "pass" {
		t.Fatalf("the current API version must satisfy itself, got %+v", got)
	}
}

func TestMinAPIVersionRejectsNonsense(t *testing.T) {
	m := manifestFromJSON(t, `{"id": "x", "min_api_version": "one.two"}`)
	got := findResult(checkMinAPIVersion(m), "min_api_version")
	if got == nil || got.Status != "fail" {
		t.Fatalf("want fail for a malformed version, got %+v", got)
	}
}

// Absent is not a failure: all three fields are optional.
func TestVocabularyChecksAreSilentWhenFieldsAbsent(t *testing.T) {
	m := manifestFromJSON(t, `{"id": "x", "name": "X"}`)
	if r := checkImplementsMethods(m); len(r) != 0 {
		t.Errorf("implements absent should report nothing, got %+v", r)
	}
	if r := checkPrivilegeNames(m); len(r) != 0 {
		t.Errorf("privileges absent should report nothing, got %+v", r)
	}
	if r := checkMinAPIVersion(m); len(r) != 0 {
		t.Errorf("min_api_version absent should report nothing, got %+v", r)
	}
}

func TestNearestDeclinesAWildGuess(t *testing.T) {
	if n := nearest(vocabulary.Privileges, "zzzzzzzzzz"); n != "" {
		t.Errorf("want no suggestion for an unrelated name, got %q", n)
	}
}
