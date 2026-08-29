package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// `plugin update` used to pass catalog=nil into the install, so the registry
// counter-signature was never re-verified on updates and the plugin's
// registry_signed record was silently downgraded. The resolver this pins is
// what closes that: an update finds its catalog entry by source repo.
func TestFindCatalogEntryBySource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`plugins:
  - id: example
    source: github:owner/branchkit-plugin-example
    tier: approved
    manifest_sha256: abc
    registry_signature: sig
`))
	}))
	defer srv.Close()
	t.Setenv("BRANCHKIT_CATALOG_URL", srv.URL)

	// Source meta records "owner/repo" without the github: prefix; the
	// catalog carries it with. Both forms must resolve.
	for _, src := range []string{"owner/branchkit-plugin-example", "github:owner/branchkit-plugin-example", "OWNER/branchkit-plugin-example"} {
		entry := findCatalogEntryBySource(src)
		if entry == nil {
			t.Fatalf("source %q did not resolve", src)
		}
		if entry.RegistrySignature != "sig" {
			t.Fatalf("entry missing counter-sig fields: %+v", entry)
		}
	}

	if entry := findCatalogEntryBySource("owner/unlisted"); entry != nil {
		t.Fatalf("unlisted source resolved to %+v", entry)
	}
}

// An unreachable catalog must degrade to nil (counter-sig soft-absent), not
// fail the update.
func TestFindCatalogEntryBySourceUnreachable(t *testing.T) {
	t.Setenv("BRANCHKIT_CATALOG_URL", "http://127.0.0.1:1/catalog.yaml")
	if entry := findCatalogEntryBySource("owner/repo"); entry != nil {
		t.Fatalf("unreachable catalog resolved to %+v", entry)
	}
}
