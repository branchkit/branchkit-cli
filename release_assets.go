package main

// The app release's asset contract (release_assets.json): what a release of
// BranchKit itself publishes, and which file an install or an update uses on
// each OS and architecture. The file is embedded so this binary and the
// release assembler, which reads it from the source tree, name the same
// files.

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

//go:embed release_assets.json
var releaseAssetsJSON []byte

type releaseContract struct {
	About      []string          `json:"about"`
	Repository string            `json:"repository"` // "owner/name" on GitHub
	TagPrefix  string            `json:"tag_prefix"`
	Checksums  string            `json:"checksums"`
	Artifacts  []releaseArtifact `json:"artifacts"`
}

// releaseArtifact is one kind of file, for every architecture it ships on.
type releaseArtifact struct {
	OS    string            `json:"os"`   // Go's GOOS
	Kind  string            `json:"kind"` // dmg, zip, deb, rpm, appimage
	Name  string            `json:"name"` // with {version} and {arch}
	Arch  map[string]string `json:"arch"` // Go's GOARCH → its spelling in Name
	Use   []string          `json:"use"`  // "install", "update"
	About string            `json:"about"`
}

const (
	useInstall = "install"
	useUpdate  = "update"
)

// appVersionPattern is a release version: X.Y.Z, no pre-release suffix.
var appVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// loadReleaseContract decodes the embedded contract, refusing unknown fields
// and a contract that breaks its own rules (validate).
func loadReleaseContract() (*releaseContract, error) {
	dec := json.NewDecoder(bytes.NewReader(releaseAssetsJSON))
	dec.DisallowUnknownFields()
	var c releaseContract
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("release_assets.json: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("release_assets.json: %w", err)
	}
	return &c, nil
}

// validate holds the contract's rules: each artifact names its version and
// architecture, every file name is unique across the release, and each OS
// has exactly one file the updater uses.
func (c *releaseContract) validate() error {
	if c.Repository == "" || c.Checksums == "" {
		return fmt.Errorf("repository and checksums are required")
	}
	names := map[string]bool{c.Checksums: true}
	updates := map[string]int{}
	for _, a := range c.Artifacts {
		if !strings.Contains(a.Name, "{version}") || !strings.Contains(a.Name, "{arch}") {
			return fmt.Errorf("%s %s: name %q needs {version} and {arch}", a.OS, a.Kind, a.Name)
		}
		if len(a.Arch) == 0 || len(a.Use) == 0 {
			return fmt.Errorf("%s %s: arch and use are required", a.OS, a.Kind)
		}
		for _, u := range a.Use {
			if u != useInstall && u != useUpdate {
				return fmt.Errorf("%s %s: unknown use %q", a.OS, a.Kind, u)
			}
		}
		if a.updates() {
			updates[a.OS]++
		}
		for goarch := range a.Arch {
			name, _ := a.fileName("0.0.0", goarch)
			if names[name] {
				return fmt.Errorf("%s %s: file name %q is used twice", a.OS, a.Kind, name)
			}
			names[name] = true
		}
	}
	for _, a := range c.Artifacts {
		if updates[a.OS] != 1 {
			return fmt.Errorf("%s has %d update files, want exactly one", a.OS, updates[a.OS])
		}
	}
	return nil
}

func (a releaseArtifact) updates() bool { return slices.Contains(a.Use, useUpdate) }

// fileName is the artifact's file for one version and Go architecture, and
// false when it does not ship on that architecture.
func (a releaseArtifact) fileName(version, goarch string) (string, bool) {
	spelled, ok := a.Arch[goarch]
	if !ok {
		return "", false
	}
	return strings.NewReplacer("{version}", version, "{arch}", spelled).Replace(a.Name), true
}

// tag is the release tag of a version.
func (c *releaseContract) tag(version string) string { return c.TagPrefix + version }

// artifactFor picks the file of one kind for an OS and architecture; an
// empty kind means the one the updater uses there.
func (c *releaseContract) artifactFor(goos, goarch, kind string) (releaseArtifact, error) {
	var kinds []string
	for _, a := range c.Artifacts {
		if a.OS != goos {
			continue
		}
		kinds = append(kinds, a.Kind)
		if (kind == "" && a.updates()) || a.Kind == kind {
			if _, ok := a.Arch[goarch]; !ok {
				return releaseArtifact{}, fmt.Errorf("BranchKit releases no %s for %s on %s", a.Kind, goos, goarch)
			}
			return a, nil
		}
	}
	if len(kinds) == 0 {
		return releaseArtifact{}, fmt.Errorf("BranchKit releases nothing for %s", goos)
	}
	sort.Strings(kinds)
	return releaseArtifact{}, fmt.Errorf("no %q file for %s (it has: %s)", kind, goos, strings.Join(kinds, ", "))
}

// sumsLine is one line of sha256sum's text output: 64 lowercase hex digits,
// two spaces (or a space and the binary-mode '*'), and a bare file name: no
// folder separator, no control character (a CR from a CRLF file included).
var sumsLine = regexp.MustCompile(`^([0-9a-f]{64}) [ *]([^/\\\x00-\x1f\x7f]+)$`)

// parseSums reads a checksums file into name → SHA-256 hex. Anything else in
// it (a malformed line, a path, a name listed twice) is an error rather than
// skipped: the file is signed, so it is exactly what the release meant.
func parseSums(data []byte) (map[string]string, error) {
	sums := map[string]string{}
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		m := sumsLine.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("checksums line %d is not `<sha256>  <name>`: %q", i+1, line)
		}
		if _, dup := sums[m[2]]; dup {
			return nil, fmt.Errorf("checksums list %s twice", m[2])
		}
		sums[m[2]] = m[1]
	}
	return sums, nil
}
