package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
//
// The app reads exactly this JSON, and its reader documents the shape both
// writers share: change a field here and there together. Fields one side
// does not know are ignored by the other.
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

// installRecord computes the install record for the plugin in dir: the
// digests of its plugin.json and of every collection file that manifest
// names, with source, tag and the signing results. named is every file the
// manifest names, shipped or not (a name with no digest was not shipped).
//
// An installer computes it from the files it verified and staged, before
// they reach the plugin's folder, never by reading that folder back
// afterwards: a plugin still running from the folder can write it, and
// whatever it put there between the copy and the record would be recorded
// as installed. A difference that arrives that way leaves a record that
// does not speak for the folder, which the app reads as no record.
func installRecord(dir, source, tag string, attestation *AuthorAttestation, registrySigned bool) (meta SourceMeta, named []string, err error) {
	manifest, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return SourceMeta{}, nil, err
	}
	named, err = referencedFiles(manifest)
	if err != nil {
		return SourceMeta{}, nil, err
	}
	digests := make(map[string]string, len(named))
	for _, rel := range named {
		body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if os.IsNotExist(err) {
			// Named but not shipped (the app warns and loads the rest):
			// no digest, and the app reads the record only while it stays
			// away.
			continue
		}
		if err != nil {
			return SourceMeta{}, nil, err
		}
		sum := sha256.Sum256(body)
		digests[rel] = hex.EncodeToString(sum[:])
	}
	digest := sha256.Sum256(manifest)
	meta = SourceMeta{
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
	return meta, named, nil
}

// recordInstall writes meta as pluginID's install record, once the files it
// was computed from are in place; err is installRecord's. Written through a
// temporary file beside it and renamed into place, so the app reads the old
// record or the new one, never half of one.
// A record that cannot be computed or written is reported, and the one it
// would have replaced is removed: the plugin then reads as installed from
// nowhere (no catalog tier, not author-verified), and nothing reads the old
// install's source, tag or "bundled" for the new files.
func recordInstall(pluginID string, meta SourceMeta, err error) {
	if err == nil {
		err = saveSourceMeta(pluginID, meta)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "  WARN: could not record where '%s' was installed from: %v\n", pluginID, err)
		removeSourceMeta(pluginID)
	}
}

func saveSourceMeta(pluginID string, meta SourceMeta) error {
	path := sourceMetaPath(pluginID)
	if path == "" {
		return fmt.Errorf("not a plugin id")
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), pluginID+".json.tmp-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// writeSourceMeta records the plugin now in pluginDir as pluginID's install.
// Only for a copy the CLI itself just made from a folder the plugin cannot
// write (dev sync); an installer records from its staged files
// (installRecord, recordInstall).
func writeSourceMeta(pluginID, pluginDir, source, tag string, attestation *AuthorAttestation, registrySigned bool) {
	meta, _, err := installRecord(pluginDir, source, tag, attestation, registrySigned)
	recordInstall(pluginID, meta, err)
}

// dropUnshipped removes from the plugin folder dir each file the manifest
// names that the install did not ship (named, with no digest in meta). A
// copy made over an earlier install keeps that install's files; one the
// manifest still names would stand where the record says there is none,
// and the record would not speak for the folder.
func dropUnshipped(dir string, named []string, meta SourceMeta) {
	for _, rel := range named {
		if _, shipped := meta.CollectionFilesSHA256[rel]; !shipped {
			os.Remove(filepath.Join(dir, filepath.FromSlash(rel)))
		}
	}
}

// legacySourceFile is where installs once kept their record, inside the
// plugin's own folder. Nothing reads it; a folder copied in that still has
// one loses it, so no older reader takes it for this install's record.
const legacySourceFile = ".branchkit-source.json"

// approvedManifest is the update diff's "before": the manifest the person
// approved when the copy in dir was installed. It is read from dir, and
// counts only while its SHA-256 is the one pluginID's install record holds:
// every sandbox lets a plugin write its own folder, and a plugin that added
// a permission to its plugin.json there would otherwise see that permission
// pass as already granted when the next version asks for it.
//
// installed is false only when nothing is installed under the id (no folder
// and no record): a fresh install. A manifest the record does not name (no
// record, a manifest changed or removed since) gives one that requests
// nothing, so everything the new version requests is shown, and asked
// about, as new; unapproved then says why, for the person.
func approvedManifest(pluginID, dir string) (old PluginManifest, installed bool, unapproved string) {
	meta, recorded := readSourceMeta(pluginID)
	_, statErr := os.Lstat(dir)
	if !recorded && os.IsNotExist(statErr) {
		return PluginManifest{}, false, ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err == nil && recorded {
		sum := sha256.Sum256(data)
		if strings.EqualFold(meta.ManifestSHA256, hex.EncodeToString(sum[:])) {
			if json.Unmarshal(data, &old) == nil {
				return old, true, ""
			}
		}
	}
	// Name and version only, to say what is being replaced.
	var shown PluginManifest
	json.Unmarshal(data, &shown)
	old = PluginManifest{ID: pluginID, Name: shown.Name, Version: shown.Version}
	if old.Name == "" {
		old.Name = pluginID
	}
	if old.Version == "" {
		old.Version = "?"
	}
	why := "has no install record"
	if recorded {
		why = "has a plugin.json that is not the one installed"
	}
	return old, true, fmt.Sprintf("The installed '%s' %s, so everything this version requests is shown as new.", pluginID, why)
}

// installedForUpdate is approvedManifest for an installer, which tells the
// person when the diff starts from nothing.
func installedForUpdate(pluginID, dir string) (PluginManifest, bool) {
	old, installed, unapproved := approvedManifest(pluginID, dir)
	if unapproved != "" {
		fmt.Printf("\n%s\n", unapproved)
	}
	return old, installed
}

// checkAttestationDowngrade asks before an update whose predecessor was
// author-verified installs without a verified attestation: recording
// author_verified false would drop its trust tier without a word. The
// predecessor's result comes from its install record, which the plugin
// cannot write, never from its folder; and the record is read whether or
// not it still speaks for the folder, so a plugin that changed its own
// files cannot make the question go away.
func checkAttestationDowngrade(pluginID, name string, attestation *AuthorAttestation, in io.Reader, tty bool) error {
	prior, ok := readSourceMeta(pluginID)
	if !ok || !prior.AuthorVerified || (attestation != nil && attestation.Verified) {
		return nil
	}
	return confirmAttestationDowngrade(name, in, tty)
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

// withoutSourceMeta runs swap, which replaces pluginID's installed files,
// with its install record moved aside: while the files change, no record
// speaks for them, so the app never reads the old install's record for the
// new files (and a crash meanwhile leaves no record, granted nothing by
// default). If swap fails the old files are still there and the record is
// put back; if it succeeds the record is gone, for the caller to write anew.
func withoutSourceMeta(pluginID string, swap func() error) error {
	path := sourceMetaPath(pluginID)
	if path == "" {
		return swap()
	}
	aside := path + ".aside"
	setAside := os.Rename(path, aside) == nil
	err := swap()
	if setAside {
		if err != nil {
			os.Rename(aside, path)
		} else {
			os.Remove(aside)
		}
	}
	return err
}

// removeSourceMeta forgets pluginID's install record, on uninstall.
func removeSourceMeta(pluginID string) {
	if path := sourceMetaPath(pluginID); path != "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: could not remove %s: %v\n", path, err)
		}
	}
}
