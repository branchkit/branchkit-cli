package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Settings' "Review & install" hands the CLI `github:owner/repo@<tag>`, never
// a short name, and the counter-signature check used to run only for a short
// name — so a catalogued plugin installed from Settings was never checked.
// The install now finds the entry by the repository it installs, and every
// argument form reaches the same check with the same verdict: valid →
// registry-signed, absent → not registry-signed, tampered → refused.
func TestInstallCounterSigEveryArgumentForm(t *testing.T) {
	pub, priv := testKeypair(t)
	manifest := []byte(`{"id":"demo","run":"./demo"}`)
	hash := sha256HexBytes(manifest)
	good := signRegistryCounterSig(priv, hash)
	_, otherPriv := testKeypair(t)
	forged := signRegistryCounterSig(otherPriv, hash)

	cases := []struct {
		name      string
		signature string
		wantOK    bool
		wantErr   bool
	}{
		{"valid", good, true, false},
		{"missing", "", false, false},
		{"tampered", forged, false, true},
	}
	verified := &AuthorAttestation{Verified: true}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			catalog := fmt.Sprintf(`plugins:
  - id: demo
    source: github:Owner/branchkit-plugin-demo
    tier: approved
    manifest_sha256: %s
    registry_signature: %q
`, hash, c.signature)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Write([]byte(catalog))
			}))
			defer srv.Close()
			t.Setenv("BRANCHKIT_CATALOG_URL", srv.URL)

			short, err := resolveShortNameEntry("demo")
			if err != nil {
				t.Fatal(err)
			}
			forms := map[string]*catalogEntry{
				"short name":            short,
				"github: from Settings": nil,
				"plugin update":         nil,
			}
			sources := map[string]string{
				"short name":            short.Source,
				"github: from Settings": "github:owner/branchkit-plugin-demo@v1.2.0",
				"plugin update":         "owner/branchkit-plugin-demo",
			}
			for form, given := range forms {
				parsed, err := parseGitHubSource(sources[form])
				if err != nil {
					t.Fatal(err)
				}
				entry := catalogEntryForInstall(parsed, given)
				if entry == nil {
					t.Fatalf("%s: catalogued repo found no catalog entry", form)
				}
				ok, err := installCounterSig(pub, entry, verified, manifest)
				if ok != c.wantOK || (err != nil) != c.wantErr {
					t.Errorf("%s: ok=%v err=%v, want ok=%v err=%v", form, ok, err, c.wantOK, c.wantErr)
				}
				if err != nil && !strings.Contains(err.Error(), "registry counter-signature check failed") {
					t.Errorf("%s: refusal does not name the check: %v", form, err)
				}
			}
		})
	}
}

// A swapped manifest under a valid listing is refused too, and nothing is
// registry-signed without a verified author attestation or a catalog entry.
func TestInstallCounterSigPreconditions(t *testing.T) {
	pub, priv := testKeypair(t)
	manifest := []byte(`{"id":"demo"}`)
	hash := sha256HexBytes(manifest)
	entry := &catalogEntry{ID: "demo", ManifestSHA256: hash, RegistrySignature: signRegistryCounterSig(priv, hash)}

	if _, err := installCounterSig(pub, entry, &AuthorAttestation{Verified: true}, []byte(`{"id":"evil"}`)); err == nil {
		t.Error("a manifest other than the one counter-signed must refuse the install")
	}
	for name, att := range map[string]*AuthorAttestation{"nil": nil, "unverified": {Verified: false}} {
		if ok, err := installCounterSig(pub, entry, att, manifest); ok || err != nil {
			t.Errorf("%s attestation: ok=%v err=%v, want not registry-signed", name, ok, err)
		}
	}
	if ok, err := installCounterSig(pub, nil, &AuthorAttestation{Verified: true}, manifest); ok || err != nil {
		t.Errorf("uncatalogued: ok=%v err=%v, want not registry-signed", ok, err)
	}
}

// An uncatalogued repository, and an unreachable catalog, find no entry: the
// install proceeds not registry-signed, as a short name never could.
func TestCatalogEntryForInstallUncatalogued(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("plugins:\n  - id: demo\n    source: github:owner/branchkit-plugin-demo\n"))
	}))
	defer srv.Close()
	t.Setenv("BRANCHKIT_CATALOG_URL", srv.URL)
	parsed, _ := parseGitHubSource("github:someone/elsewhere@v1")
	if e := catalogEntryForInstall(parsed, nil); e != nil {
		t.Errorf("uncatalogued repo resolved to %+v", e)
	}
	t.Setenv("BRANCHKIT_CATALOG_URL", "http://127.0.0.1:1/catalog.yaml")
	parsed, _ = parseGitHubSource("github:owner/branchkit-plugin-demo")
	if e := catalogEntryForInstall(parsed, nil); e != nil {
		t.Errorf("unreachable catalog resolved to %+v", e)
	}
}
