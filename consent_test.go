package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Sandbox scope is the fourth diff axis — the one with no later consent
// moment anywhere, so a widening must count as an expansion and a scripted
// update must block on it (DESIGN_SANDBOX_CONSENT_SURFACE.md).
func TestDiffConsentSandboxAxis(t *testing.T) {
	old := PluginManifest{ID: "p", Run: "./p"}
	newM := PluginManifest{
		ID:       "p",
		Run:      "./p",
		Requires: RequiresCfg{Network: []byte(`{"hosts":["collect.example","api.example"]}`)},
	}
	d := diffConsent(old, newM)
	if !d.expands() {
		t.Fatal("added network hosts must read as an expansion")
	}
	if len(d.axis("network").Added) != 2 {
		t.Fatalf("expected both hosts in the diff, got %v", d.axis("network").Added)
	}

	// Same set, reordered: not a change.
	reordered := PluginManifest{
		ID:       "p",
		Run:      "./p",
		Requires: RequiresCfg{Network: []byte(`{"hosts":["api.example","collect.example"]}`)},
	}
	d = diffConsent(newM, reordered)
	if d.expands() || d.contracts() {
		t.Fatalf("reordering the host set is not a consent change: %+v", d)
	}

	// Preset widening localhost → outbound is an expansion (and a
	// contraction of the old member — both print).
	d = diffConsent(
		PluginManifest{ID: "p", Requires: RequiresCfg{Network: []byte(`"localhost"`)}},
		PluginManifest{ID: "p", Requires: RequiresCfg{Network: []byte(`"outbound"`)}},
	)
	if !d.expands() {
		t.Fatal("localhost → outbound must expand")
	}

	// A changed run command is expansion-class always.
	d = diffConsent(
		PluginManifest{ID: "p", Run: "./p"},
		PluginManifest{ID: "p", Run: "./other"},
	)
	if !d.expands() || !d.RunChanged {
		t.Fatal("a changed run command must require fresh consent")
	}

	// Dropping the network is a contraction, not an expansion.
	d = diffConsent(newM, old)
	if d.expands() {
		t.Fatalf("narrowing must not prompt as expansion: %+v", d)
	}
	if !d.contracts() {
		t.Fatal("dropped hosts must print as a contraction")
	}
}

// The downgrade gate is the inverse of the others: non-interactive REFUSES
// (a security regression is not a consent formality --yes may skip), and
// interactively the default is no.
func TestConfirmAttestationDowngrade(t *testing.T) {
	if err := confirmAttestationDowngrade("Example", strings.NewReader(""), false); err == nil {
		t.Fatal("scripted downgrade must refuse")
	}
	for _, no := range []string{"n\n", "\n", "nope\n", ""} {
		if err := confirmAttestationDowngrade("Example", strings.NewReader(no), true); err == nil {
			t.Fatalf("%q must decline", no)
		}
	}
	for _, yes := range []string{"y\n", "YES\n"} {
		if err := confirmAttestationDowngrade("Example", strings.NewReader(yes), true); err != nil {
			t.Fatalf("%q must accept a deliberate override: %v", yes, err)
		}
	}
}

// The consent moment: disclosure always, question only when interactive.
// Decline (or an empty/closed stdin) refuses the install.
func TestConfirmInstall(t *testing.T) {
	m := PluginManifest{
		ID: "example", Name: "Example", Version: "1.0.0",
		Requires: RequiresCfg{Privileges: []string{"dispatch"}},
	}

	if err := confirmInstall(m, strings.NewReader(""), false); err != nil {
		t.Fatalf("non-interactive must not prompt or fail: %v", err)
	}
	for _, yes := range []string{"y\n", "Y\n", "yes\n", " YES \n"} {
		if err := confirmInstall(m, strings.NewReader(yes), true); err != nil {
			t.Fatalf("%q must accept: %v", yes, err)
		}
	}
	for _, no := range []string{"n\n", "\n", "nope\n", ""} {
		if err := confirmInstall(m, strings.NewReader(no), true); err == nil {
			t.Fatalf("%q must decline", no)
		}
	}
}

func effectDecl(name, desc string, asserts ...string) EffectDeclaration {
	e := EffectDeclaration{UserVisibleName: name, UserVisibleDescription: desc}
	for _, a := range asserts {
		e.Asserts = append(e.Asserts, []byte(`"`+a+`"`))
	}
	return e
}

// The diff iterates the consent-axis registry, and an effect's identity is
// its asserted names — copy edits are not a consent change.
func TestDiffConsent(t *testing.T) {
	oldM := PluginManifest{
		Requires: RequiresCfg{
			Privileges:         []string{"windows", "shell"},
			OptionalPrivileges: []string{"power"},
		},
		Consumes: &ConsumesCfg{Effects: []EffectDeclaration{
			effectDecl("Focus", "old copy", "suppress_notifications"),
			effectDecl("Capture", "", "suppress_keybinds"),
		}},
	}
	newM := PluginManifest{
		Requires: RequiresCfg{
			Privileges:         []string{"windows", "screenshot"},
			OptionalPrivileges: []string{"power", "clipboard"},
		},
		Consumes: &ConsumesCfg{Effects: []EffectDeclaration{
			effectDecl("Focus Mode", "new copy", "suppress_notifications"),
		}},
	}
	d := diffConsent(oldM, newM)

	if len(d.axis("privileges").Added) != 1 || d.axis("privileges").Added[0] != "screenshot" {
		t.Fatalf("added privileges: %v", d.axis("privileges").Added)
	}
	if len(d.axis("privileges").Removed) != 1 || d.axis("privileges").Removed[0] != "shell" {
		t.Fatalf("removed privileges: %v", d.axis("privileges").Removed)
	}
	if len(d.axis("optional_privileges").Added) != 1 || d.axis("optional_privileges").Added[0] != "clipboard" {
		t.Fatalf("added optional: %v", d.axis("optional_privileges").Added)
	}
	if len(d.AddedEffects) != 0 {
		t.Fatalf("a copy edit is not a new effect: %v", d.AddedEffects)
	}
	if len(d.RemovedEffects) != 1 || d.RemovedEffects[0] != "Capture" {
		t.Fatalf("removed effects: %v", d.RemovedEffects)
	}
	if !d.expands() || !d.contracts() {
		t.Fatalf("expands=%v contracts=%v", d.expands(), d.contracts())
	}

	same := diffConsent(oldM, oldM)
	if same.expands() || same.contracts() {
		t.Fatalf("identical manifests must diff empty: %+v", same)
	}
}

// Update consent is diff-driven: nothing new → no question anywhere; an
// expansion asks on a TTY, is skipped by --yes, and BLOCKS a scripted
// update — nobody was there to say no.
func TestConfirmUpdate(t *testing.T) {
	oldM := PluginManifest{ID: "x", Name: "X", Version: "1", Requires: RequiresCfg{Privileges: []string{"windows"}}}
	sameM := PluginManifest{ID: "x", Name: "X", Version: "2", Requires: RequiresCfg{Privileges: []string{"windows"}}}
	moreM := PluginManifest{ID: "x", Name: "X", Version: "2", Requires: RequiresCfg{Privileges: []string{"windows", "shell"}}}
	lessM := PluginManifest{ID: "x", Name: "X", Version: "2"}

	// No expansion: proceeds without reading stdin, TTY or not.
	for _, tty := range []bool{true, false} {
		if err := confirmUpdate(sameM, oldM, strings.NewReader(""), false, tty); err != nil {
			t.Fatalf("no-expansion update must not prompt (tty=%v): %v", tty, err)
		}
		if err := confirmUpdate(lessM, oldM, strings.NewReader(""), false, tty); err != nil {
			t.Fatalf("contraction-only update must not prompt (tty=%v): %v", tty, err)
		}
	}

	// Expansion, TTY: the answer decides.
	if err := confirmUpdate(moreM, oldM, strings.NewReader("y\n"), false, true); err != nil {
		t.Fatalf("yes must accept: %v", err)
	}
	if err := confirmUpdate(moreM, oldM, strings.NewReader("n\n"), false, true); err == nil {
		t.Fatal("no must decline")
	}

	// Expansion, no TTY, no --yes: blocked.
	if err := confirmUpdate(moreM, oldM, strings.NewReader("y\n"), false, false); err == nil {
		t.Fatal("a scripted expanding update must block")
	}

	// --yes skips the question in either mode.
	for _, tty := range []bool{true, false} {
		if err := confirmUpdate(moreM, oldM, strings.NewReader(""), true, tty); err != nil {
			t.Fatalf("--yes must proceed (tty=%v): %v", tty, err)
		}
	}
}

// The request block and the consent-axis registry must stay the same set.
//
// This is the ratchet the block exists for. Before it, the fields a user is
// shown at install were a hand-written list in three places — here, the
// actuator's InstallPreviewTemplate, and the sandbox compiler — and a new
// capability field could join the manifest, be honoured by the loader and
// enforced by the sandbox while the install prompt said nothing. The user
// would approve something never shown to them.
//
// Reflecting over RequiresCfg's JSON tags rather than restating the names
// is the point: add a field to the block and this fails until the axis
// exists, which is the opposite of the silence it replaces.
func TestConsentAxesCoverEveryRequiresField(t *testing.T) {
	tags := func(v any) map[string]bool {
		out := map[string]bool{}
		rt := reflect.TypeOf(v)
		for i := range rt.NumField() {
			tag := rt.Field(i).Tag.Get("json")
			if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
				out[name] = true
			}
		}
		return out
	}

	// Every field of the request block must have an axis. This is the
	// direction that prevents the security failure: a capability the
	// platform enforces but never shows at install.
	declared := tags(RequiresCfg{})
	if len(declared) == 0 {
		t.Fatal("reflection found no json-tagged fields — the test is not testing anything")
	}
	covered := map[string]bool{}
	for _, ax := range consentAxes {
		if ax.block == "" {
			covered[ax.name] = true
		}
	}
	for field := range declared {
		if !covered[field] {
			t.Errorf("requires.%s has no consent axis — it would be enforced but never shown at install", field)
		}
	}

	// And no axis may be stale. An axis need not come from the request
	// block — `blobs` asks for another plugin's data rather than a platform
	// capability, and lives in `consumes` — but it must name a real
	// manifest field somewhere, derived from the types rather than from a
	// list someone remembers to update.
	elsewhere := tags(ConsumesCfg{})
	for axis := range covered {
		if !declared[axis] && !elsewhere[axis] {
			t.Errorf("consent axis %q is a field of neither RequiresCfg nor ConsumesCfg — stale", axis)
		}
	}
	// Axes that name their block explicitly are checked against it.
	blocks := map[string]map[string]bool{"provides": tags(ProvidesCfg{})}
	for _, ax := range consentAxes {
		if ax.block == "" {
			continue
		}
		fields, ok := blocks[ax.block]
		if !ok || !fields[ax.field] {
			t.Errorf("consent axis %q names %s.%s, which is not a manifest field — stale", ax.name, ax.block, ax.field)
		}
	}
}

// The provider's side of a blob is disclosed, and raising its ceiling is a
// consent change rather than the same name slipping through.
func TestProvidedBlobsAreDisclosedAndACeilingRaiseExpands(t *testing.T) {
	mk := func(max int64, hash string) PluginManifest {
		return PluginManifest{Provides: &ProvidesCfg{Blobs: map[string]BlobDecl{
			"frames": {MaxBytes: max, OnFull: "evict_oldest", Hash: hash},
		}}}
	}
	got := providedBlobsSet(mk(64<<20, "provider"))
	if len(got) != 1 || !strings.Contains(got[0], "64 MB") || !strings.Contains(got[0], "not checked") {
		t.Fatalf("disclosure: %v", got)
	}
	d := diffConsent(mk(64<<20, ""), mk(1<<30, ""))
	if !d.expands() {
		t.Fatalf("raising max_bytes must require fresh consent: %+v", d)
	}
}

// A plugin that may request hosts at runtime says so at install, and
// gaining that ability in an update needs fresh consent.
func TestRequestableHostsAreDisclosedAndGainingThemExpands(t *testing.T) {
	mk := func(net string) PluginManifest {
		var m PluginManifest
		m.Requires.Network = json.RawMessage(net)
		return m
	}
	set := networkSet(mk(`{"hosts":[],"requestable":true}`))
	if len(set) != 1 || networkDisplay(set[0]) == set[0] {
		t.Fatalf("requestable not disclosed: %v", set)
	}
	if !diffConsent(mk(`{"hosts":["a.test"]}`), mk(`{"hosts":["a.test"],"requestable":true}`)).expands() {
		t.Fatal("becoming requestable must require fresh consent")
	}
}
