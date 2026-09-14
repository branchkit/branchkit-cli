package main

import "testing"

// A bare name another listed plugin introduces is warned about, once per
// name; owned names never are; the plugin's own listing is not a rival.
func TestNamespaceWarnings(t *testing.T) {
	m := PluginManifest{ID: "acme", Provides: &ProvidesCfg{Collections: map[string]any{
		"snippets":            map[string]any{},
		"plugin.acme.secrets": map[string]any{},
		"widgets":             map[string]any{},
	}}}
	cat := catalog{Plugins: []catalogEntry{
		{ID: "snippets", Tier: "first-party", Collections: []string{"snippets"}},
		{ID: "acme", Tier: "community", Collections: []string{"widgets"}},
	}}
	warns := namespaceWarnings(&m, &cat)
	if len(warns) != 1 {
		t.Fatalf("expected one warning, got %d: %v", len(warns), warns)
	}
	if want := "collection 'snippets' is already introduced by 'snippets' (first-party)"; !contains(warns[0], want) {
		t.Fatalf("warning should name the collection and its introducer: %q", warns[0])
	}
	if !contains(warns[0], "plugin.acme.snippets") {
		t.Fatalf("warning should suggest the owned spelling: %q", warns[0])
	}
	if got := bareCollections(&m); len(got) != 2 || got[0] != "snippets" || got[1] != "widgets" {
		t.Fatalf("bare names sorted, owned excluded: %v", got)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
