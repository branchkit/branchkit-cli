package main

import (
	"encoding/json"
	"testing"
)

// The manifest key is `privileges` — the CLI carried the pre-rename
// `capabilities` tag long enough that every "Privileges:" line it printed
// (and the settings install preview) rendered empty. This parses the shape
// real manifests actually have, including the consent-relevant fields the
// install summary shows.
func TestManifestParsesConsentFields(t *testing.T) {
	raw := `{
		"id": "example",
		"name": "Example",
		"version": "1.0.0",
		"privileges": ["dispatch", "filesystem"],
		"optional_privileges": ["display", "power"],
		"consumes": {
			"effects": [{
				"asserts": ["suppress_notifications", {"name": "disable_screen_dim"}],
				"user_visible_name": "Focus Mode",
				"user_visible_description": "Mutes notifications while active."
			}]
		}
	}`
	var m PluginManifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(m.Privileges) != 2 || m.Privileges[0] != "dispatch" {
		t.Fatalf("privileges not parsed: %v", m.Privileges)
	}
	if len(m.OptionalPrivileges) != 2 {
		t.Fatalf("optional_privileges not parsed: %v", m.OptionalPrivileges)
	}
	if m.Consumes == nil || len(m.Consumes.Effects) != 1 {
		t.Fatalf("consumes.effects not parsed: %+v", m.Consumes)
	}
	e := m.Consumes.Effects[0]
	if e.UserVisibleName != "Focus Mode" || e.UserVisibleDescription == "" {
		t.Fatalf("effect consent copy not parsed: %+v", e)
	}
	names := e.AssertNames()
	if len(names) != 2 || names[0] != "suppress_notifications" || names[1] != "disable_screen_dim" {
		t.Fatalf("assert names (string and object forms) not flattened: %v", names)
	}
}
