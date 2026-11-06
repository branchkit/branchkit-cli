package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	// CollectionFilesSHA256 is the hex SHA-256 of every file the manifest
	// names for a collection (see referencedFiles), keyed by the path exactly
	// as the manifest writes it; no entry for one that was not installed. They are part of the install as much as
	// plugin.json is (a collection's declaration, the commands it
	// contributes), so the app reads the record only while each still has its
	// digest too.
	CollectionFilesSHA256 map[string]string `json:"collection_files_sha256"`
}

// referencedFiles lists the files a manifest names for its collections: each
// string value of collection_data and of provides.collections, as written.
// The app computes the same set from the same JSON. An error when one is not
// a plain relative path inside the plugin's folder: no record can speak for
// such a manifest, and the app refuses to load it anyway.
func referencedFiles(manifest []byte) ([]string, error) {
	var m struct {
		CollectionData map[string]json.RawMessage `json:"collection_data"`
		Provides       struct {
			Collections map[string]json.RawMessage `json:"collections"`
		} `json:"provides"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, err
	}
	var out []string
	for _, group := range []map[string]json.RawMessage{m.CollectionData, m.Provides.Collections} {
		for _, raw := range group {
			var value any
			if json.Unmarshal(raw, &value) != nil {
				return nil, fmt.Errorf("plugin.json: %s is not JSON", raw)
			}
			rel, named := value.(string)
			if !named {
				continue // inline data or an inline declaration (null too)
			}
			if !plainRelativePath(rel) {
				return nil, fmt.Errorf("plugin.json names %q, which is not a path inside the plugin's folder", rel)
			}
			out = append(out, rel)
		}
	}
	return out, nil
}

// plainRelativePath: not empty, not absolute, not rooted, no volume or
// drive, no ".." component, read as the app reads a path on this OS (on
// Windows "\\" separates components too, and "C:x" names a drive).
func plainRelativePath(rel string) bool {
	if rel == "" || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || strings.HasPrefix(rel, "/") {
		return false
	}
	windows := runtime.GOOS == "windows"
	if windows && strings.HasPrefix(rel, `\`) {
		return false
	}
	for _, part := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || (windows && r == '\\') }) {
		if part == ".." {
			return false
		}
	}
	return true
}

// installRecordsDirName is the folder under app support holding install
// records; the platform names it INSTALL_RECORDS_DIR_NAME.
const installRecordsDirName = "install-records"

// bundledSource is the source the app records for a copy it seeded from its
// bundle (its BUNDLED_SOURCE). The app keeps such a copy equal to the
// bundle, so whatever puts other files there forgets that record first.
const bundledSource = "bundled"

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
// pluginDir and the collection files it names. Through a temporary file, so
// the app never reads half a record.
// A record that cannot be written is reported, and the one it would have
// replaced is removed: the plugin then reads as installed from nowhere (no
// catalog tier, not author-verified), and nothing reads the old install's
// source, tag or "bundled" for the new files.
func writeSourceMeta(pluginID, pluginDir, source, tag string, attestation *AuthorAttestation, registrySigned bool) {
	warn := func(err error) {
		fmt.Fprintf(os.Stderr, "  WARN: could not record where '%s' was installed from: %v\n", pluginID, err)
		removeSourceMeta(pluginID)
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
	files, err := referencedFiles(manifest)
	if err != nil {
		warn(err)
		return
	}
	digests := make(map[string]string, len(files))
	for _, rel := range files {
		body, err := os.ReadFile(filepath.Join(pluginDir, filepath.FromSlash(rel)))
		if os.IsNotExist(err) {
			// Named but not shipped (the app warns and loads the rest):
			// no digest, and the app reads the record only while it stays
			// away.
			continue
		}
		if err != nil {
			warn(err)
			return
		}
		sum := sha256.Sum256(body)
		digests[rel] = hex.EncodeToString(sum[:])
	}
	digest := sha256.Sum256(manifest)
	meta := SourceMeta{
		Source:                source,
		InstalledTag:          tag,
		RegistrySigned:        registrySigned,
		ManifestSHA256:        hex.EncodeToString(digest[:]),
		CollectionFilesSHA256: digests,
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
