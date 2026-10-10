package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type previewResult struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Author      string `json:"author"`
	// Disclosures is what the plugin asks for and declares, one entry per
	// consent axis it declares anything on, in registry order, then its
	// run command. Generated from consentAxes, so an axis added there
	// reaches the app's install panel without either side naming it: the
	// panel renders the axes it groups specially (privileges by risk) and
	// every other entry as the line it carries.
	Disclosures []previewDisclosure `json:"disclosures"`
	// Carried for the app, which alone knows what this computer refuses.
	Capabilities *capabilitiesDecl `json:"capabilities,omitempty"`
	Effects      []previewEffect   `json:"effects"`
	Conformance  string            `json:"conformance"`
	Tier         string            `json:"tier"`
	Blocklisted  bool              `json:"blocklisted"`
	BlockReason  string            `json:"block_reason,omitempty"`
	Source       string            `json:"source"`
	Tag          string            `json:"tag"`
	// Update is present when this plugin is already installed: the consent
	// DIFF against the manifest the install would replace. The settings
	// panel renders this above the full summary — standing consent covers
	// everything unchanged.
	Update *previewUpdate `json:"update,omitempty"`
}

// previewDisclosure is one consent axis as the install panel shows it.
type previewDisclosure struct {
	Axis string `json:"axis"`
	// Items are the members, display-ready (a privilege's bare name).
	Items []string `json:"items"`
	// Text is the terminal's install line for the axis, unindented.
	Text string `json:"text"`
	// Grant: each item is a switch on the plugin's page after install.
	// Otherwise it is enforced from the manifest with no later prompt.
	Grant bool `json:"grant"`
}

// previewChange is one member an update adds or removes.
type previewChange struct {
	Axis string `json:"axis"`
	Item string `json:"item"`
	// Text is the terminal's update line for the member, without its
	// "+"/"-" marker.
	Text  string `json:"text"`
	Grant bool   `json:"grant"`
}

// previewUpdate mirrors consentDiff for the settings panel, plus the
// installed version for the "v1 → v2" line.
type previewUpdate struct {
	InstalledVersion string          `json:"installed_version"`
	Added            []previewChange `json:"added"`
	Removed          []previewChange `json:"removed"`
	AddedEffects     []previewEffect `json:"added_effects"`
	RemovedEffects   []string        `json:"removed_effects"`
	RunChanged       bool            `json:"run_changed"`
	Expands          bool            `json:"expands"`
}

// previewEffect is one consent unit of effects, carrying the author-written
// user-visible copy the install dialog renders.
type previewEffect struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Asserts     []string `json:"asserts"`
}

// previewDisclosures is the install disclosure as data: the same loop, the
// same copy, as printConsentSummary.
func previewDisclosures(m PluginManifest) []previewDisclosure {
	out := []previewDisclosure{}
	for _, ax := range consentAxes {
		values := ax.extract(m)
		if len(values) == 0 {
			continue
		}
		display := make([]string, len(values))
		for i, v := range values {
			display[i] = ax.display(v)
		}
		out = append(out, previewDisclosure{
			Axis:  ax.name,
			Items: display,
			Text:  disclosureText(ax.summary(display)),
			Grant: ax.grant,
		})
	}
	if line := runSummary(m); line != "" {
		out = append(out, previewDisclosure{
			Axis:  "run",
			Items: []string{m.Run},
			Text:  disclosureText(line),
		})
	}
	return out
}

func previewEffects(effects []EffectDeclaration) []previewEffect {
	out := []previewEffect{}
	for _, e := range effects {
		out = append(out, previewEffect{
			Name:        e.UserVisibleName,
			Description: e.UserVisibleDescription,
			Asserts:     e.AssertNames(),
		})
	}
	return out
}

// previewDiff is the update consent diff as data, from the same diff and
// the same copy as confirmUpdate.
func previewDiff(oldM, newM PluginManifest) previewUpdate {
	d := diffConsent(oldM, newM)
	up := previewUpdate{
		InstalledVersion: oldM.Version,
		Added:            []previewChange{},
		Removed:          []previewChange{},
		AddedEffects:     previewEffects(d.AddedEffects),
		RemovedEffects:   emptyNotNil(d.RemovedEffects),
		RunChanged:       d.RunChanged,
		Expands:          d.expands(),
	}
	for _, ax := range consentAxes {
		for _, m := range d.axis(ax.name).Added {
			up.Added = append(up.Added, previewChange{
				Axis:  ax.name,
				Item:  ax.display(m),
				Text:  disclosureText(fmt.Sprintf(ax.addFmt, ax.display(m))),
				Grant: ax.grant,
			})
		}
		for _, m := range d.axis(ax.name).Removed {
			up.Removed = append(up.Removed, previewChange{
				Axis:  ax.name,
				Item:  ax.display(m),
				Text:  disclosureText(fmt.Sprintf(ax.delFmt, ax.display(m))),
				Grant: ax.grant,
			})
		}
	}
	return up
}

func cmdPreview(source string) {
	if isShortName(source) {
		resolved, err := resolveShortName(source)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		source = resolved
	}

	parsed, err := parseGitHubSource(source)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	tag := parsed.Version
	if tag == "" {
		t, err := fetchLatestTag(parsed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		tag = t
	}

	manifest, err := fetchManifestFromRepo(parsed, tag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Check blocklist
	bl := fetchBlocklist()
	var blocklisted bool
	var blockReason string
	if bl != nil {
		for _, entry := range bl.Blocked {
			if matchesBlockedSource(source, entry.Source) {
				blocklisted = true
				blockReason = entry.Reason
				break
			}
		}
	}

	// Check conformance
	cs := fetchConformanceStatus(parsed, tag)

	// Check catalog tier
	tier := lookupCatalogTier(manifest.ID)

	result := previewResult{
		ID:           manifest.ID,
		Name:         manifest.Name,
		Version:      manifest.Version,
		Description:  manifest.Description,
		Author:       manifest.Author,
		Disclosures:  previewDisclosures(manifest),
		Effects:      previewEffects(manifestEffects(manifest)),
		Conformance:  cs.Status,
		Tier:         tier,
		Blocklisted:  blocklisted,
		BlockReason:  blockReason,
		Source:       source,
		Tag:          tag,
		Capabilities: manifest.Requires.Capabilities,
	}

	// Already installed → attach the consent diff, same basis as the
	// install path's confirmUpdate: the manifest at the swap target.
	if old, err := readManifest(filepath.Join(userPluginsDir(), manifest.ID, "plugin.json")); err == nil {
		up := previewDiff(old, manifest)
		result.Update = &up
	}

	data, _ := json.Marshal(result)
	fmt.Println(string(data))
}

func emptyNotNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func fetchManifestFromRepo(source ResolvedSource, tag string) (PluginManifest, error) {
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/plugin.json",
		source.Owner, source.Repo, tag)

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return PluginManifest{}, err
	}
	req.Header.Set("User-Agent", "branchkit-cli")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return PluginManifest{}, fmt.Errorf("failed to fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return PluginManifest{}, fmt.Errorf("no plugin.json found at %s/%s@%s", source.Owner, source.Repo, tag)
	}
	if resp.StatusCode >= 300 {
		return PluginManifest{}, fmt.Errorf("GitHub returned %d fetching manifest", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return PluginManifest{}, fmt.Errorf("failed to read manifest: %w", err)
	}

	var m PluginManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return PluginManifest{}, fmt.Errorf("invalid plugin.json: %w", err)
	}
	return m, nil
}
