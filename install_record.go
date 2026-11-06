package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SourceMeta is a plugin's install record: where it was installed from (for
// update checking) and the signing outcome at install time (for the
// platform's trust-tier resolution: the platform never sees artifact bytes,
// so the CLI records the outcome here).
//
// It lives at <app support>/install-records/<id>.json, never inside the
// plugin's own folder: every sandbox backend lets a plugin write its install
// folder, so a record there could be rewritten to claim the plugin came from
// the catalog's own repository (or from the app bundle) and be handed a
// first-party plugin's default grants. No sandbox grant names the
// install-records folder. The app writes the same path for the plugins it
// seeds from its bundle.
type SourceMeta struct {
	Source       string `json:"source"`        // "owner/repo"
	InstalledTag string `json:"installed_tag"` // e.g. "v3.0.0" or "source-build"
	// AuthorVerified is true iff a Sigstore attestation was present and
	// verified AND bound to Source at install time.
	AuthorVerified bool `json:"author_verified"`
	// AuthorIdentity is the verified signer identity (cert SAN) — display +
	// audit. Empty when unverified.
	AuthorIdentity string `json:"author_identity,omitempty"`
	// RegistrySigned is true iff BranchKit's registry counter-signature over
	// this exact listing verified — the canonical-listing signal that lets the
	// platform resolve RegistrySigned rather than a catalog claim.
	RegistrySigned bool `json:"registry_signed"`
	// ManifestSHA256 is the hex SHA-256 of the plugin.json this install put
	// in place. The app reads the record only while the installed manifest
	// still has this digest, so a plugin that rewrites its own manifest (or
	// another copy dropped in under the same id) carries no record.
	ManifestSHA256 string `json:"manifest_sha256"`
}

// installRecordsDirName is the folder under app support holding install
// records; the platform names it INSTALL_RECORDS_DIR_NAME.
const installRecordsDirName = "install-records"

// localSourcePrefix marks a SourceMeta.Source recorded by a local-folder
// install, followed by the folder's absolute path.
const localSourcePrefix = "local:"

// sourceMetaPath is where pluginID's install record lives, or "" for an id
// that is not one plain file name.
func sourceMetaPath(pluginID string) string {
	if pluginID == "" || strings.HasPrefix(pluginID, ".") || strings.ContainsAny(pluginID, `/\`) {
		return ""
	}
	return filepath.Join(appSupportDir(), installRecordsDirName, pluginID+".json")
}

// writeSourceMeta records pluginID's install, bound to the plugin.json now in
// pluginDir. Through a temporary file, so the app never reads half a record.
// A record that cannot be written is reported: the plugin then reads as
// installed from nowhere (no catalog tier, not author-verified).
func writeSourceMeta(pluginID, pluginDir, source, tag string, attestation *AuthorAttestation, registrySigned bool) {
	warn := func(err error) {
		fmt.Fprintf(os.Stderr, "  WARN: could not record where '%s' was installed from: %v\n", pluginID, err)
	}
	path := sourceMetaPath(pluginID)
	if path == "" {
		warn(fmt.Errorf("not a plugin id"))
		return
	}
	manifest, err := os.ReadFile(filepath.Join(pluginDir, "plugin.json"))
	if err != nil {
		warn(err)
		return
	}
	digest := sha256.Sum256(manifest)
	meta := SourceMeta{
		Source:         source,
		InstalledTag:   tag,
		RegistrySigned: registrySigned,
		ManifestSHA256: hex.EncodeToString(digest[:]),
	}
	if attestation != nil && attestation.Verified {
		meta.AuthorVerified = true
		meta.AuthorIdentity = attestation.SAN
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		warn(err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		warn(err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		warn(err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		warn(err)
	}
}

// readSourceMeta reads pluginID's install record. It does not check the
// manifest digest: the CLI wrote the record, so its source is still where
// an update comes from even when the installed copy has since changed.
func readSourceMeta(pluginID string) (SourceMeta, bool) {
	path := sourceMetaPath(pluginID)
	if path == "" {
		return SourceMeta{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SourceMeta{}, false
	}
	var meta SourceMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return SourceMeta{}, false
	}
	return meta, true
}

// removeSourceMeta forgets pluginID's install record, on uninstall.
func removeSourceMeta(pluginID string) {
	if path := sourceMetaPath(pluginID); path != "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: could not remove %s: %v\n", path, err)
		}
	}
}
