package main

import (
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

func parseManifest(t *testing.T, s string) PluginManifest {
	t.Helper()
	var m PluginManifest
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return m
}

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = saved }()
	f()
	w.Close()
	os.Stdout = saved
	return <-done
}

// The ratchet against the actuator's own manifest schema rather than this
// tool's mirror structs. TestConsentAxesCoverEveryRequiresField can only see
// a field someone already added to RequiresCfg; a property the actuator adds
// to the schema, honours and enforces, but nobody mirrors here, was
// invisible to it — which is how `provides.native_messaging` and
// `provides.stages` went unshown at install. The property lists travel in
// the embedded vocabulary, derived from the schema and drift-gated in the
// app's build, so this sees what the actuator accepts.
func TestEverySchemaConsentFieldIsDisclosedOrExempt(t *testing.T) {
	schema := vocabulary.ManifestConsentBlocks
	for _, block := range []string{"requires", "provides", "consumes"} {
		if len(schema[block]) == 0 {
			t.Fatalf("embedded vocabulary has no %s properties — the ratchet would pass against nothing", block)
		}
	}

	inSchema := map[string]bool{}
	for block, fields := range schema {
		for _, f := range fields {
			inSchema[block+"."+f] = true
		}
	}

	// What the axis registry covers, as <block>.<field>.
	byAxis := map[string]string{}
	for _, ax := range consentAxes {
		key := ax.block + "." + ax.field
		if !inSchema[key] {
			t.Errorf("consent axis %q reads %s, which the manifest schema does not have — stale", ax.name, key)
		}
		byAxis[key] = ax.name
	}

	for key := range inSchema {
		axis, isAxis := byAxis[key]
		_, outside := disclosedOutsideAxes[key]
		reason, exempt := notConsentFields[key]
		switch {
		case isAxis && exempt:
			t.Errorf("%s is both disclosed (axis %q) and exempt as not consent — drop the exemption", key, axis)
		case outside && exempt:
			t.Errorf("%s is both disclosed outside the axes and exempt — drop the exemption", key)
		case exempt && strings.TrimSpace(reason) == "":
			t.Errorf("%s is exempt with no reason", key)
		case !isAxis && !outside && !exempt:
			t.Errorf("manifest property %s is shown nowhere at install or update: add a consent axis, or say in notConsentFields why it asks for nothing", key)
		}
	}
	for key := range notConsentFields {
		if !inSchema[key] {
			t.Errorf("notConsentFields names %s, which the manifest schema does not have — stale", key)
		}
	}
	for key, where := range disclosedOutsideAxes {
		if !inSchema[key] {
			t.Errorf("disclosedOutsideAxes names %s, which the manifest schema does not have — stale", key)
		}
		if strings.TrimSpace(where) == "" {
			t.Errorf("disclosedOutsideAxes names %s without saying where it is shown", key)
		}
	}
}

// What was shown nowhere is now shown at install and diffed at update:
// the stages it ships, the browser extensions it pairs with, what it
// consumes from other plugins, and the program it runs.
func TestUnshownDeclarationsAreDisclosedAndExpand(t *testing.T) {
	m := parseManifest(t, `{
		"id": "x", "name": "X", "version": "1.0.0",
		"run": "./x-plugin",
		"provides": {
			"stages": {"ocr": {}, "stt": {"binary": "bin/stt-engine"}},
			"native_messaging": {
				"host_name": "com.example.pair",
				"chrome_extension_ids": ["abcdefghijklmnopabcdefghijklmnop"],
				"firefox_extension_ids": ["pair@example.com"]
			}
		},
		"consumes": {
			"events": [{"pattern": "_platform.window.**"}, {"pattern": "tempo.**", "source": "metronome"}],
			"dispatch": ["tiling", {"prefix": "browser", "actions": ["browser.open"]}],
			"collections": ["alphabet", {"name": "apps", "fields": ["name"]}],
			"writes": ["snippets"]
		}
	}`)

	got := map[string][]string{}
	for _, d := range previewDisclosures(m) {
		got[d.Axis] = d.Items
		if d.Text == "" {
			t.Errorf("axis %s has no line of copy", d.Axis)
		}
	}
	want := map[string][]string{
		"stages":            {"ocr", "stt (bin/stt-engine)"},
		"native_messaging":  {"Chrome extension abcdefghijklmnopabcdefghijklmnop", "Firefox extension pair@example.com"},
		"events":            {"tempo.** (from metronome)"},
		"always_events":     {"_platform.window.**"},
		"dispatch":          {"browser", "tiling"},
		"collection_reads":  {"alphabet", "apps"},
		"collection_writes": {"snippets"},
		"run":               {"./x-plugin"},
	}
	for axis, items := range want {
		if !reflect.DeepEqual(got[axis], items) {
			t.Errorf("%s: disclosed %v, want %v", axis, got[axis], items)
		}
	}

	// The terminal summary carries the same.
	out := captureStdout(t, func() { printConsentSummary(m) })
	for _, s := range []string{"Pipeline stages: ocr", "pair@example.com", "_platform.window.**", "tempo.** (from metronome)", "tiling", "alphabet", "snippets", "Runs: ./x-plugin"} {
		if !strings.Contains(out, s) {
			t.Errorf("install summary omits %q:\n%s", s, out)
		}
	}

	// Each one gained by an update needs fresh consent.
	base := parseManifest(t, `{"id": "x", "name": "X", "version": "0.9.0", "run": "./x-plugin"}`)
	d := diffConsent(base, m)
	for _, axis := range []string{"stages", "native_messaging", "events", "always_events", "dispatch", "collection_reads", "collection_writes"} {
		if len(d.axis(axis).Added) == 0 {
			t.Errorf("an update adding %s does not show it as new", axis)
		}
	}
	if !d.expands(true) {
		t.Fatal("an update adding stages, extensions and consumption must require fresh consent")
	}

	// Pointing a stage at a different program is a change of member.
	moved := parseManifest(t, `{"id": "x", "provides": {"stages": {"ocr": {"binary": "other"}}}}`)
	same := parseManifest(t, `{"id": "x", "provides": {"stages": {"ocr": {}}}}`)
	if !diffConsent(same, moved).expands(true) {
		t.Error("a stage that runs a different program must require fresh consent")
	}
	// Narrowing a dispatch to fewer action types is not a new grant: the
	// grant is per prefix.
	wide := parseManifest(t, `{"id": "x", "consumes": {"dispatch": ["tiling"]}}`)
	narrow := parseManifest(t, `{"id": "x", "consumes": {"dispatch": [{"prefix": "tiling", "actions": ["tiling.snap"]}]}}`)
	if d := diffConsent(wide, narrow); d.expands(true) || d.contracts() {
		t.Errorf("narrowing a dispatch's actions is not a consent change: %+v", d)
	}
}

// The panel's data and the terminal's copy come from the same registry: the
// update rows carry the terminal's lines without their markers, and each
// row says whether its member is a grant.
func TestPreviewDiffCarriesTheTerminalCopy(t *testing.T) {
	oldM := parseManifest(t, `{"id": "x", "version": "1.0.0", "requires": {"privileges": ["shell"], "network": {"hosts": ["old.example"]}}}`)
	newM := parseManifest(t, `{"id": "x", "version": "2.0.0", "requires": {"network": {"hosts": ["new.example"]}, "runtimes": ["python"]}, "consumes": {"events": [{"pattern": "tempo.**"}]}}`)
	up := previewDiff(oldM, newM, true)
	if !up.Expands || up.InstalledVersion != "1.0.0" {
		t.Fatalf("diff: %+v", up)
	}
	added := map[string]previewChange{}
	for _, c := range up.Added {
		added[c.Axis] = c
	}
	if c := added["network"]; c.Text != "network: new.example (allow it on the plugin's page)" || c.Kind != kindGrant {
		t.Errorf("network row: %+v", c)
	}
	if c := added["runtimes"]; c.Kind == kindGrant || !strings.Contains(c.Text, "runtime: python") {
		t.Errorf("runtimes row: %+v", c)
	}
	if c := added["events"]; c.Kind != kindGrant || c.Item != "tempo.**" {
		t.Errorf("events row: %+v", c)
	}
	removed := map[string]string{}
	for _, c := range up.Removed {
		removed[c.Axis] = c.Text
	}
	if removed["privileges"] != "shell" || removed["network"] != "network: old.example" {
		t.Errorf("removed rows: %+v", up.Removed)
	}

	// The install disclosure: every axis with members, in registry order,
	// the run command last.
	disc := previewDisclosures(parseManifest(t, `{"id": "x", "run": "./x", "requires": {"privileges": ["shell"], "sockets": {"listen": [{"name": "ws"}]}}}`))
	var axes []string
	for _, d := range disc {
		axes = append(axes, d.Axis)
	}
	if !reflect.DeepEqual(axes, []string{"privileges", "sockets", "run"}) {
		t.Errorf("disclosure order: %v", axes)
	}
	if disc[1].Text != "Listen sockets: 1 loopback listener(s)" || disc[1].Kind == kindGrant {
		t.Errorf("sockets line: %+v", disc[1])
	}
	if disc[0].Kind != kindGrant || disc[0].Items[0] != "shell" {
		t.Errorf("privileges entry: %+v", disc[0])
	}
}
