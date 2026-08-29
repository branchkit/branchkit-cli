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
	Run           string `json:"run,omitempty"`
	// The manifest key was renamed capabilities → privileges platform-wide;
	// this struct kept the old tag long enough that every "Privileges:" line
	// the CLI printed was empty. The field name follows the wire.
	Privileges         []string     `json:"privileges,omitempty"`
	OptionalPrivileges []string     `json:"optional_privileges,omitempty"`
	Consumes           *ConsumesCfg `json:"consumes,omitempty"`
	DependsOn          []Dependency `json:"depends_on,omitempty"`
	ActionPrefix       string       `json:"action_prefix,omitempty"`
	HudTargets         []string     `json:"hud_targets,omitempty"`
	Sockets            *SocketsCfg  `json:"sockets,omitempty"`
	Provides           *ProvidesCfg `json:"provides,omitempty"`
}

// ConsumesCfg mirrors the actuator's `consumes` field, deeply enough to show
// the consent-relevant declarations at install time. Everything else under
// `consumes` is the actuator's business.
type ConsumesCfg struct {
	Effects []EffectDeclaration `json:"effects,omitempty"`
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
	Models map[string]ModelDeclaration `json:"models,omitempty"`
}

// ModelDeclaration is one model a plugin's stages can load — the recipe this
// CLI executes. The actuator validates the shape at manifest load
// (`plugins/validate/manifest.rs`); the checks here are the ones that matter
// at fetch time, and they are enforced regardless of what validation ran.
type ModelDeclaration struct {
	Description string      `json:"description,omitempty"`
	SizeBytes   int64       `json:"size_bytes"`
	Parts       []ModelPart `json:"parts"`
	Requires    []string    `json:"requires,omitempty"`
}

// ModelPart is one step in assembling a model directory. Kind-tagged, five
// kinds; see notes/DESIGN_PLUGIN_MODEL_DECLARATION.md in branchkit/app.
type ModelPart struct {
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

// Dependency is an explicit plugin dependency with optional version constraint and source hint.
// Accepts either a bare string ("keyboard") or an object ({"plugin": "keyboard", "version": ">=1.0.0", "source": "github:owner/repo"}).
type Dependency struct {
	Plugin  string `json:"plugin"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source,omitempty"`
}

func (d *Dependency) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		d.Plugin = s
		d.Version = ""
		d.Source = ""
		return nil
	}
	type depObj struct {
		Plugin  string `json:"plugin"`
		Version string `json:"version,omitempty"`
		Source  string `json:"source,omitempty"`
	}
	var obj depObj
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	d.Plugin = obj.Plugin
	d.Version = obj.Version
	d.Source = obj.Source
	return nil
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
