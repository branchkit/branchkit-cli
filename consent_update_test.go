package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A new consumes grant asks again only when it would arrive switched on.
// For any plugin that does not ship with BranchKit it arrives off on the
// plugin's page, so a scripted update proceeds and says so; for one that
// does, the update would switch it on, so a scripted update blocks. New
// stages and paired extensions block either way: nothing later asks.
func TestConsumesGrantsAskOnlyWhenTheyArriveOn(t *testing.T) {
	base := parseManifest(t, `{"id": "x", "name": "X", "version": "1.0.0", "run": "./x"}`)
	consumes := parseManifest(t, `{
		"id": "x", "name": "X", "version": "2.0.0", "run": "./x",
		"consumes": {
			"events": [{"pattern": "tempo.**", "source": "metronome"}],
			"dispatch": ["tiling"],
			"collections": ["alphabet"],
			"writes": ["snippets"]
		}
	}`)
	d := diffConsent(base, consumes)
	if d.expands(false) {
		t.Errorf("new consumes grants that arrive off must not need a fresh yes: %+v", d)
	}
	if !d.expands(true) {
		t.Errorf("new consumes grants that arrive on must need a fresh yes: %+v", d)
	}

	out := captureStdout(t, func() {
		if err := confirmUpdate(consumes, base, strings.NewReader(""), false, false, false); err != nil {
			t.Errorf("a scripted update whose new grants arrive off must proceed: %v", err)
		}
	})
	for _, s := range []string{"each off until you allow it", "receives events: tempo.** (from metronome)", "tiling", "alphabet", "snippets"} {
		if !strings.Contains(out, s) {
			t.Errorf("the update does not say %q:\n%s", s, out)
		}
	}
	captureStdout(t, func() {
		if err := confirmUpdate(consumes, base, strings.NewReader(""), false, false, true); err == nil {
			t.Error("a scripted update that would switch new grants on must block")
		}
	})

	for _, extra := range []string{
		`"provides": {"stages": {"ocr": {}}}`,
		`"provides": {"native_messaging": {"host_name": "com.example.x", "chrome_extension_ids": ["abcdefghijklmnopabcdefghijklmnop"]}}`,
	} {
		m := parseManifest(t, `{"id": "x", "name": "X", "version": "2.0.0", "run": "./x", `+extra+`}`)
		if !diffConsent(base, m).expands(false) {
			t.Errorf("%s must need a fresh yes whatever the grants do", extra)
		}
		captureStdout(t, func() {
			if err := confirmUpdate(m, base, strings.NewReader(""), false, false, false); err == nil {
				t.Errorf("a scripted update adding %s must block", extra)
			}
		})
	}

	// The panel's rows say the same, member by member.
	up := previewDiff(base, consumes, false)
	if up.Expands || up.GrantsArriveOn {
		t.Errorf("preview: %+v", up)
	}
	for _, c := range up.Added {
		if c.Expands == nil || *c.Expands {
			t.Errorf("row %s/%s must say it asks nothing", c.Axis, c.Item)
		}
	}
	for _, c := range up.Removed {
		if c.Expands != nil {
			t.Errorf("a removed row carries no expands: %+v", c)
		}
	}
}

// A collection the plugin introduces itself is its own data: the app
// passes it without a grant and gives it no switch, so it is told, never
// counted as a grant, and gaining or losing one asks and revokes nothing.
func TestOwnCollectionsAreNotGrants(t *testing.T) {
	m := parseManifest(t, `{
		"id": "notes",
		"provides": {"collections": {"notes": {}}},
		"consumes": {"collections": ["notes", "alphabet"], "writes": ["notes"]}
	}`)
	got := map[string][]string{}
	for _, d := range previewDisclosures(m) {
		got[d.Axis] = d.Items
		if d.Axis == "own_collections" && d.Kind != kindNote {
			t.Errorf("own collections are a note, not %q", d.Kind)
		}
	}
	if !reflect.DeepEqual(got["collection_reads"], []string{"alphabet"}) {
		t.Errorf("reads: %v", got["collection_reads"])
	}
	if got["collection_writes"] != nil {
		t.Errorf("writing its own collection is not a grant: %v", got["collection_writes"])
	}
	if !reflect.DeepEqual(got["own_collections"], []string{"notes"}) {
		t.Errorf("own: %v", got["own_collections"])
	}

	bare := parseManifest(t, `{"id": "notes", "provides": {"collections": {"notes": {}}}}`)
	if d := diffConsent(bare, m); !reflect.DeepEqual(d.axis("own_collections").Added, []string{"notes"}) {
		t.Errorf("own collection not shown as new: %+v", d)
	}
	// Only the other plugin's collection asks, and only if it arrives on.
	if d := diffConsent(bare, m); d.expands(false) || !d.expands(true) {
		t.Errorf("expands(false)=%v expands(true)=%v", d.expands(false), d.expands(true))
	}
	if d := diffConsent(m, bare); d.axis("own_collections").Removed == nil {
		t.Error("dropping an own collection is still shown")
	}
	ownOnly := parseManifest(t, `{"id": "notes", "provides": {"collections": {"notes": {}}}, "consumes": {"writes": ["notes"]}}`)
	if d := diffConsent(bare, ownOnly); d.expands(true) || d.contracts() {
		t.Errorf("its own collection asks and revokes nothing: %+v", d)
	}
	if d := diffConsent(ownOnly, bare); d.contracts() {
		t.Errorf("dropping its own collection revokes nothing: %+v", d)
	}
}

// Why the plugin asks, and what its stages download (how much, from where),
// are shown at install; a change to either is told at update and asks
// nothing.
func TestReasonsAndDownloadsAreToldNotAsked(t *testing.T) {
	m := parseManifest(t, `{
		"id": "x", "name": "X", "version": "2.0.0",
		"requires": {"privileges": ["shell"], "reasons": {"privilege:shell": "To run  the converter.", "odd": "Kept as written."}},
		"provides": {"artifacts": {"weights": {"size_bytes": 1610612736, "parts": [
			{"kind": "hf_folder", "repo": "example/weights", "revision": "0123456789abcdef0123456789abcdef01234567"},
			{"kind": "http_archive", "url": "https://cdn.example.org/extra.tar.gz", "sha256": "00"}
		]}}}
	}`)
	got := map[string]previewDisclosure{}
	for _, d := range previewDisclosures(m) {
		got[d.Axis] = d
	}
	r := got["reasons"]
	if r.Kind != kindNote || !reflect.DeepEqual(r.Items, []string{"odd: “Kept as written.”", "privilege shell: “To run the converter.”"}) {
		t.Errorf("reasons: %+v", r)
	}
	a := got["artifacts"]
	if a.Kind != kindNote || !reflect.DeepEqual(a.Items, []string{"weights (1.5 GB, from cdn.example.org, huggingface.co/example/weights)"}) {
		t.Errorf("artifacts: %+v", a)
	}
	out := captureStdout(t, func() { printConsentSummary(m) })
	for _, s := range []string{"Why it asks: odd", "Downloads when first needed: weights (1.5 GB"} {
		if !strings.Contains(out, s) {
			t.Errorf("install summary omits %q:\n%s", s, out)
		}
	}

	reworded := parseManifest(t, `{
		"id": "x", "name": "X", "version": "3.0.0",
		"requires": {"privileges": ["shell"], "reasons": {"privilege:shell": "To run the file converter."}}
	}`)
	d := diffConsent(m, reworded)
	if d.expands(true) || d.contracts() {
		t.Errorf("a reworded reason or a dropped download asks and revokes nothing: %+v", d)
	}
	if len(d.axis("reasons").Added) != 1 || len(d.axis("artifacts").Removed) != 1 {
		t.Errorf("the change is still shown: %+v", d)
	}
	out = captureStdout(t, func() {
		if err := confirmUpdate(reworded, m, strings.NewReader(""), false, false, true); err != nil {
			t.Errorf("a scripted update changing only notes must proceed: %v", err)
		}
	})
	if !strings.Contains(out, "Also changed (asks for nothing)") || strings.Contains(out, "grants will be revoked") {
		t.Errorf("notes are told apart from revocations:\n%s", out)
	}
}

// The app switches a plugin's grants on only for a first-party catalog
// entry in the branchkit organisation, installed from the repository the
// catalog names.
func TestCatalogGrantsOn(t *testing.T) {
	cat := catalog{Plugins: []catalogEntry{
		{ID: "voice", Source: "github:branchkit/branchkit-plugin-voice", Tier: "first-party"},
		{ID: "fake", Source: "github:someone/branchkit-plugin-fake", Tier: "first-party"},
		{ID: "snip", Source: "github:branchkit/branchkit-plugin-snip", Tier: "community"},
	}}
	cases := []struct {
		id, repo string
		want     bool
	}{
		{"voice", "branchkit/branchkit-plugin-voice", true},
		{"voice", "BranchKit/Branchkit-Plugin-Voice", true},
		{"voice", "github:branchkit/branchkit-plugin-voice", true},
		{"voice", "someone/branchkit-plugin-voice", false},
		{"fake", "someone/branchkit-plugin-fake", false},
		{"snip", "branchkit/branchkit-plugin-snip", false},
		{"unlisted", "branchkit/branchkit-plugin-unlisted", false},
	}
	for _, c := range cases {
		if got := catalogGrantsOn(cat, c.id, c.repo); got != c.want {
			t.Errorf("%s from %s: %v, want %v", c.id, c.repo, got, c.want)
		}
	}
	if grantsArriveOn("voice", "") {
		t.Error("a local folder's grants never arrive on")
	}
}

// The tier shown at install and preview is the entry for THIS repo: a
// lookalike repo whose plugin.json declares a catalogued id gets no tier,
// never the real plugin's First-party.
func TestCatalogTierMatchesSourceAndID(t *testing.T) {
	cat := catalog{Plugins: []catalogEntry{
		{ID: "voice", Source: "github:branchkit/branchkit-plugin-voice", Tier: "first-party"},
		{ID: "snip", Source: "github:someone/branchkit-plugin-snip", Tier: "community"},
		{ID: "pinned", Source: "github:branchkit/branchkit-plugin-pinned@v1.2.0", Tier: "approved"},
	}}
	cases := []struct {
		id, repo, want string
	}{
		{"voice", "branchkit/branchkit-plugin-voice", "First-party"},
		{"voice", "BranchKit/Branchkit-Plugin-Voice", "First-party"},
		{"voice", "github:branchkit/branchkit-plugin-voice", "First-party"},
		{"voice", "evil/branchkit-plugin-x", ""},
		{"voice", "evil/branchkit-plugin-voice", ""},
		{"voice", "", ""},
		{"x", "branchkit/branchkit-plugin-voice", ""},
		{"snip", "someone/branchkit-plugin-snip", "Community"},
		{"pinned", "branchkit/branchkit-plugin-pinned", "Approved"},
		{"unlisted", "someone/branchkit-plugin-unlisted", ""},
	}
	for _, c := range cases {
		if got := catalogTier(cat, c.id, c.repo); got != c.want {
			t.Errorf("%s from %q: %q, want %q", c.id, c.repo, got, c.want)
		}
	}
}

// TestMain keeps every test off this computer's catalog, its installed
// plugins and the network: each test that needs a catalog or a claimant
// stands one in.
func TestMain(m *testing.M) {
	localCatalog = func() (catalog, bool) { return catalog{}, false }
	remoteCatalog = func() (catalog, error) { return catalog{}, errors.New("no network in tests") }
	collectionClaimants = func() map[string][]string { return nil }
	os.Exit(m.Run())
}

// The answer follows the catalog the app reads, and only when the registry
// online agrees with it: a disagreement either way, or either one
// unreadable, asks.
func TestGrantsArriveOnFollowsTheAppsCatalog(t *testing.T) {
	savedLocal, savedRemote := localCatalog, remoteCatalog
	defer func() { localCatalog, remoteCatalog = savedLocal, savedRemote }()
	cat := func(tier string) catalog {
		return catalog{Plugins: []catalogEntry{{ID: "voice", Source: "github:branchkit/branchkit-plugin-voice", Tier: tier}}}
	}
	const repo = "branchkit/branchkit-plugin-voice"
	cases := []struct {
		name          string
		local, remote string // "" = unreadable
		want          bool
	}{
		{"both first-party", "first-party", "first-party", true},
		{"both community", "community", "community", false},
		{"app first-party, online community", "first-party", "community", true},
		{"app community, online first-party", "community", "first-party", true},
		{"app catalog unreadable", "", "community", true},
		{"online catalog unreachable", "community", "", true},
	}
	for _, c := range cases {
		localCatalog = func() (catalog, bool) { return cat(c.local), c.local != "" }
		remoteCatalog = func() (catalog, error) {
			if c.remote == "" {
				return catalog{}, errors.New("unreachable")
			}
			return cat(c.remote), nil
		}
		if got := grantsArriveOn("voice", repo); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// The app gives a collection name two plugins declare to the one that had it
// first; the other needs a grant to write it. A name another plugin also
// claims, in the catalog or installed here, is not counted as the plugin's
// own: it stays a grant, and is asked about.
func TestAContestedCollectionIsNotOwn(t *testing.T) {
	saved := collectionClaimants
	defer func() { collectionClaimants = saved }()
	m := parseManifest(t, `{
		"id": "notes",
		"provides": {"collections": {"snippets": {}, "notes": {}}},
		"consumes": {"writes": ["snippets", "notes"]}
	}`)
	collectionClaimants = func() map[string][]string {
		return map[string][]string{"snippets": {"snippets", "notes"}, "notes": {"notes"}}
	}
	got := map[string][]string{}
	for _, d := range previewDisclosures(m) {
		got[d.Axis] = d.Items
	}
	if !reflect.DeepEqual(got["collection_writes"], []string{"snippets"}) {
		t.Errorf("a contested name must stay a grant: %v", got["collection_writes"])
	}
	if !reflect.DeepEqual(got["own_collections"], []string{"notes"}) {
		t.Errorf("only an uncontested name is its own: %v", got["own_collections"])
	}
}

// The places the app reads its catalog from, in its order: its app-support
// copy first.
func TestLocalCatalogPathsStartWithAppSupport(t *testing.T) {
	t.Setenv("BRANCHKIT_APP_SUPPORT", t.TempDir())
	t.Setenv("BRANCHKIT_RESOURCES", filepath.Join(t.TempDir(), "res"))
	paths := localCatalogPaths()
	if len(paths) < 2 || paths[0] != filepath.Join(os.Getenv("BRANCHKIT_APP_SUPPORT"), "catalog.yaml") ||
		paths[1] != filepath.Join(os.Getenv("BRANCHKIT_RESOURCES"), "catalog.yaml") {
		t.Fatalf("paths: %v", paths)
	}
	if err := os.WriteFile(paths[1], nil, 0o644); err == nil {
		t.Fatal("resources dir should not exist")
	}
	if err := os.WriteFile(paths[0], []byte("plugins:\n  - id: voice\n    source: github:branchkit/branchkit-plugin-voice\n    tier: first-party\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, ok := readLocalCatalog()
	if !ok || len(cat.Plugins) != 1 || cat.Plugins[0].Tier != "first-party" {
		t.Fatalf("read %+v ok=%v", cat, ok)
	}
}

// The axes are the app's: every one its registry publishes has a reader
// here, in its order, carrying its kind and update rule; no reader is left
// for an axis the app no longer publishes; and a registry naming an axis
// this CLI cannot read stops the CLI rather than leaving it unshown.
func TestConsentAxesAreTheAppsRegistry(t *testing.T) {
	published := vocabulary.ConsentAxes
	if len(published) == 0 {
		t.Fatal("the embedded vocabulary publishes no consent axes")
	}
	if len(consentAxes) != len(published) {
		t.Fatalf("%d axes joined from %d published", len(consentAxes), len(published))
	}
	for i, p := range published {
		ax := consentAxes[i]
		if ax.name != p.Name || ax.block != p.Block || ax.field != p.Field || ax.kind != p.Kind || ax.update != p.Update {
			t.Errorf("axis %d: joined %+v from %+v", i, ax, p)
		}
		switch p.Kind {
		case kindGrant, kindScope, kindNote:
		default:
			t.Errorf("axis %s: kind %q is not one this CLI renders", p.Name, p.Kind)
		}
		switch p.Update {
		case updateExpands, updateExpandsWhenGrantedOn, updateInformational:
		default:
			t.Errorf("axis %s: update rule %q is not one this CLI applies", p.Name, p.Update)
		}
	}
	names := map[string]bool{}
	for _, p := range published {
		names[p.Name] = true
	}
	for _, r := range axisReaders {
		if !names[r.name] {
			t.Errorf("axisReaders reads %q, which the app no longer publishes — stale", r.name)
		}
	}

	defer func() {
		if recover() == nil {
			t.Error("an axis with no reader must stop the CLI")
		}
	}()
	joinAxes(append(append([]publishedAxis{}, published...), publishedAxis{Name: "from_the_future"}), axisReaders)
}
