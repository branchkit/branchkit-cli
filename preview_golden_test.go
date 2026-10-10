package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// goldenPreviewPath is the install preview this CLI writes for a manifest
// that declares something on every consent axis. The app's install panel
// renders this same file in its own tests, so the wire between the two is
// checked from both ends: change what the preview carries and this test
// fails until the file is regenerated, and the app's test then renders the
// regenerated file.
//
// Regenerate: UPDATE_GOLDEN=1 go test -run TestPreviewGolden .
const goldenPreviewPath = "testdata/install_preview.golden.json"

const goldenOldManifest = `{
	"id": "golden", "name": "Golden", "version": "1.0.0",
	"run": "./golden-plugin",
	"requires": {
		"privileges": ["screenshot"],
		"network": {"hosts": ["old.example"]}
	},
	"consumes": {
		"writes": ["notes"],
		"events": [{"pattern": "tempo.**", "source": "metronome"}]
	}
}`

const goldenNewManifest = `{
	"id": "golden", "name": "Golden", "version": "2.0.0",
	"description": "Declares something on every consent axis.",
	"author": "Example",
	"run": "./golden-plugin --v2",
	"requires": {
		"privileges": ["windows", "shell"],
		"optional_privileges": ["clipboard"],
		"network": {"hosts": ["api.example"], "requestable": true},
		"sockets": {"listen": [{"id": "extension"}]},
		"dbus": {"methods": ["session:org.example.App/org.example.App.Ping"]},
		"wmi": {"classes": ["root/cimv2/Win32_Battery"]},
		"protected_reads": ["password_managers"],
		"preferences": {"read": ["defaults:com.example.reader"], "write": ["defaults:com.example.writer"]},
		"devices": {"hid": ["046d:c52b"], "ble": ["fff0"]},
		"runtimes": ["python"],
		"capabilities": {"uses": ["speech.say", "native.dbus_call"], "required": ["speech.say"]},
		"reasons": {
			"privilege:shell": "To run the converter you pointed it at.",
			"dispatch:tiling": "So a button can move the window you named."
		}
	},
	"provides": {
		"artifacts": {
			"ocr-model": {"size_bytes": 52428800, "parts": [{"kind": "hf_files", "repo": "example/ocr", "revision": "0123456789abcdef0123456789abcdef01234567", "files": ["model.onnx"]}]},
			"dictionary": {"size_bytes": 2097152, "platform": "macos", "parts": [{"kind": "http_file", "url": "https://dl.example.com/dict.bin", "sha256": "00"}]}
		},
		"collections": {"golden_notes": {}},
		"blobs": {"frames": {"max_bytes": 1048576, "on_full": "evict_oldest"}},
		"stages": {"ocr": {}, "stt": {"binary": "bin/stt-engine"}},
		"native_messaging": {
			"host_name": "com.example.golden",
			"chrome_extension_ids": ["abcdefghijklmnopabcdefghijklmnop"],
			"firefox_extension_ids": ["golden@example.com"]
		}
	},
	"consumes": {
		"blobs": ["camera/frames"],
		"events": [
			{"pattern": "_platform.window.**"},
			{"pattern": "golden.*"},
			{"pattern": "tempo.**"},
			{"pattern": "voice.*", "source": "voice"}
		],
		"dispatch": ["tiling"],
		"collections": [{"name": "apps", "fields": ["name"]}, "golden_notes"],
		"writes": ["snippets", "golden_notes"],
		"effects": [{
			"asserts": ["suppress_notifications"],
			"user_visible_name": "Focus",
			"user_visible_description": "Mutes notifications while active."
		}]
	}
}`

func TestPreviewGolden(t *testing.T) {
	oldM := parseManifest(t, goldenOldManifest)
	newM := parseManifest(t, goldenNewManifest)
	up := previewDiff(oldM, newM, false)
	result := previewResult{
		PreviewFormat: previewFormat,
		ID:            newM.ID,
		Name:          newM.Name,
		Version:       newM.Version,
		Description:   newM.Description,
		Author:        newM.Author,
		Disclosures:   previewDisclosures(newM),
		Capabilities:  newM.Requires.Capabilities,
		Effects:       previewEffects(manifestEffects(newM)),
		Conformance:   "passed",
		Tier:          "community",
		Source:        "github.com/example/golden",
		Tag:           "v2.0.0",
		ConsentDigest: consentDigest(newM),
		Update:        &up,
	}

	// The fixture must exercise every axis, or an axis added later could
	// travel untested.
	seen := map[string]bool{}
	for _, d := range result.Disclosures {
		seen[d.Axis] = true
	}
	for _, ax := range consentAxes {
		if !seen[ax.name] {
			t.Errorf("the golden manifest declares nothing on axis %q — add it so the app's panel test renders it", ax.name)
		}
	}
	if !seen["run"] {
		t.Error("the golden manifest has no run command")
	}

	got, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(goldenPreviewPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPreviewPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPreviewPath)
	if err != nil {
		t.Fatalf("%v — regenerate with UPDATE_GOLDEN=1 go test -run TestPreviewGolden .", err)
	}
	if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), got) {
		t.Fatalf("the install preview no longer matches %s — if the change is meant, regenerate with UPDATE_GOLDEN=1 go test -run TestPreviewGolden . and check the app's panel test still passes\n\ngot:\n%s", goldenPreviewPath, got)
	}
}
