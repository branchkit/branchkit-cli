package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// PluginManifest represents the plugin.json manifest — only fields the CLI needs.
type PluginManifest struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Version       string `json:"version"`
	Description   string `json:"description"`
	Author        string `json:"author"`
	MinAPIVersion string `json:"min_api_version,omitempty"`
	// Publisher is the provider-anchored identity claim (`github:spotify`),
	// distinct from Author (free text). The install path cross-checks it
	// against the Sigstore attestation's repo owner — see
	// checkPublisherClaim in attestation.go.
	Publisher    string       `json:"publisher,omitempty"`
	Run          string       `json:"run,omitempty"`
	Consumes     *ConsumesCfg `json:"consumes,omitempty"`
	ActionPrefix string       `json:"action_prefix,omitempty"`
	HudTargets   []string     `json:"hud_targets,omitempty"`
	Provides     *ProvidesCfg `json:"provides,omitempty"`
	// Requires is everything the user is shown and accepts at install.
	// One block rather than five loose fields so this tool, the actuator's
	// install panel and the signing chain read the same subtree instead of
	// each rebuilding the list.
	Requires RequiresCfg `json:"requires,omitempty"`
}

// RequiresCfg mirrors the actuator's `requires` block.
type RequiresCfg struct {
	// The manifest key was renamed capabilities → privileges platform-wide;
	// this struct kept the old tag long enough that every "Privileges:" line
	// the CLI printed was empty. The field name follows the wire.
	Privileges         []string    `json:"privileges,omitempty"`
	OptionalPrivileges []string    `json:"optional_privileges,omitempty"`
	Sockets            *SocketsCfg `json:"sockets,omitempty"`
	// Network mirrors the actuator's `network` field — the string presets
	// ("localhost", "outbound") or the host-scoped `{ "hosts": [...] }`
	// form. Raw because both shapes are legal; `networkSet` canonicalizes.
	// Sandbox scope is consent surface: disclosed at install, diffed at
	// update (sockets and runtimes have no later grant moment; hosts become
	// grants on the plugin's page).
	Network json.RawMessage `json:"network,omitempty"`
	// Managed runtimes the sandbox grants read+exec on.
	Runtimes []string `json:"runtimes,omitempty"`
}

// ConsumesCfg mirrors the actuator's `consumes` field, deeply enough to show
// the consent-relevant declarations at install time. Everything else under
// `consumes` is the actuator's business.
type ConsumesCfg struct {
	Effects []EffectDeclaration `json:"effects,omitempty"`
	// Byte channels this plugin asked to read, each `<provider>/<name>`.
	// A consent axis: the platform never reads the bytes, but it decides
	// who may (one grant per <provider>/<name>).
	Blobs []string `json:"blobs,omitempty"`
}

// EffectDeclaration is one consent unit of effects the plugin will assert:
// the author-written user_visible_* copy is exactly what an install prompt
// shows (the actuator validates both non-empty for that purpose).
type EffectDeclaration struct {
	Asserts                []json.RawMessage `json:"asserts,omitempty"`
	UserVisibleName        string            `json:"user_visible_name,omitempty"`
	UserVisibleDescription string            `json:"user_visible_description,omitempty"`
}

// AssertNames flattens the asserts entries — each is either a bare string or
// an object {"name": ..., "args": ...} — into effect names.
func (e *EffectDeclaration) AssertNames() []string {
	var names []string
	for _, raw := range e.Asserts {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			names = append(names, s)
			continue
		}
		var obj struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &obj); err == nil && obj.Name != "" {
			names = append(names, obj.Name)
		}
	}
	return names
}

// ProvidesCfg mirrors the actuator's `provides` field, deeply enough to read
// the model declarations this CLI provisions. Everything else under `provides`
// is the actuator's business.
type ProvidesCfg struct {
	Artifacts map[string]ArtifactDeclaration `json:"artifacts,omitempty"`
	// Collections this plugin introduces, by name. Only the NAMES are read
	// here — for the shared-name check against the catalog
	// (namespace_check.go); shapes are the actuator's business.
	Collections map[string]any `json:"collections,omitempty"`
	// Blobs this plugin PROVIDES — a byte channel granted plugins read
	// straight off disk. Read here only to disclose the provider's side at
	// install (the disk ceiling, what sheds when full, how long the bytes
	// live, and whether the platform or the provider vouches for them).
	Blobs map[string]BlobDecl `json:"blobs,omitempty"`
}

// BlobDecl is the part of a `provides.blobs` entry a person consents to.
// Defaults mirror the actuator's (`plugins/manifest.rs` BlobDeclaration).
type BlobDecl struct {
	ContentType string `json:"content_type,omitempty"`
	MaxBytes    int64  `json:"max_bytes"`
	OnFull      string `json:"on_full,omitempty"`  // refuse (default) | evict_oldest
	Lifetime    string `json:"lifetime,omitempty"` // session (default) | persistent
	Hash        string `json:"hash,omitempty"`     // platform (default) | provider
}

// ArtifactDeclaration is one model a plugin's stages can load — the recipe this
// CLI executes. The actuator validates the shape at manifest load
// (`plugins/validate/manifest.rs`); the checks here are the ones that matter
// at fetch time, and they are enforced regardless of what validation ran.
type ArtifactDeclaration struct {
	Description string         `json:"description,omitempty"`
	SizeBytes   int64          `json:"size_bytes"`
	Parts       []ArtifactPart `json:"parts"`
	Requires    []string       `json:"requires,omitempty"`
	// Platform is the OSes this model is for — a single name or a list, the
	// same shape a stage's `platform` takes. Nil means every platform.
	Platform *PlatformConstraint `json:"platform,omitempty"`
}

// PlatformConstraint is a manifest `platform` value: "macos" or ["linux",
// "windows"]. It unmarshals both spellings.
type PlatformConstraint []string

func (p *PlatformConstraint) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*p = PlatformConstraint{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*p = PlatformConstraint(many)
	return nil
}

// MatchesCurrent reports whether this constraint admits the running OS. A nil
// receiver (no constraint) admits every OS.
func (p *PlatformConstraint) MatchesCurrent() bool {
	if p == nil {
		return true
	}
	current := map[string]string{"darwin": "macos", "linux": "linux", "windows": "windows"}[runtime.GOOS]
	for _, name := range *p {
		if name == current {
			return true
		}
	}
	return false
}

// ArtifactPart is one step in assembling a model directory. Kind-tagged; the
// kinds share this one struct and each reads only the fields grouped under
// its name below.
type ArtifactPart struct {
	Kind string `json:"kind"`
	// hf_folder / hf_files
	Repo     string   `json:"repo,omitempty"`
	Path     string   `json:"path,omitempty"`
	Revision string   `json:"revision,omitempty"`
	Files    []string `json:"files,omitempty"`
	// http_archive / http_file
	URL     string   `json:"url,omitempty"`
	SHA256  string   `json:"sha256,omitempty"`
	Members []string `json:"members,omitempty"`
	// where it lands, relative to the model dir
	Dest string `json:"dest,omitempty"`
}

// SocketsCfg mirrors the actuator's `sockets` manifest field, just deeply
// enough to know whether the plugin declares loopback listeners — that
// decides its runtime (a listener-granted TS plugin runs under Node, not
// Bun; see runtime.go's needsNode).
type SocketsCfg struct {
	Listen []json.RawMessage `json:"listen,omitempty"`
}

// PluginSource indicates where a plugin was discovered.
type PluginSource string

const (
	SourceUser    PluginSource = "user"
	SourceBundled PluginSource = "bundled"
	SourceDev     PluginSource = "dev"
)

// DiscoveredPlugin is a plugin found on disk with its manifest, directory, and source.
type DiscoveredPlugin struct {
	Manifest    PluginManifest
	ManifestDir string
	Source      PluginSource
}

// appSupportDir returns the BranchKit app support directory, matching the
// actuator's app_support_dir() resolution on each OS: Application Support
// on macOS, %APPDATA% on Windows, XDG data home elsewhere.
func appSupportDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/branchkit-fallback"
	}
	name := "BranchKit"
	if os.Getenv("BRANCHKIT_DEV") != "" {
		name = "BranchKitDev"
	}
	var dir string
	switch runtime.GOOS {
	case "darwin":
		dir = filepath.Join(home, "Library", "Application Support", name)
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		dir = filepath.Join(base, name)
	default:
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			base = filepath.Join(home, ".local", "share")
		}
		dir = filepath.Join(base, name)
	}
	os.MkdirAll(dir, 0o755)
	return dir
}

// userPluginsDir returns the user-installed plugins directory.
func userPluginsDir() string {
	return filepath.Join(appSupportDir(), "plugins")
}
