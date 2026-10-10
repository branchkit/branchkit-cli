package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const defaultCatalogURL = "https://raw.githubusercontent.com/branchkit/registry/main/catalog.yaml"

const maxCatalogSize = 1 * 1024 * 1024

type catalog struct {
	Plugins []catalogEntry `yaml:"plugins"`
}

type catalogEntry struct {
	ID          string   `yaml:"id"`
	Source      string   `yaml:"source"`
	Description string   `yaml:"description"`
	Categories  []string `yaml:"categories"`
	Tier        string   `yaml:"tier"`
	// Bare collection names this plugin introduces — the commons it claims.
	// Recorded so an author can learn a vocabulary exists before inventing a
	// rival name (`plugin package` warns on overlap). Owned `plugin.<id>.*`
	// names cannot collide and are not listed.
	Collections []string `yaml:"collections,omitempty"`
	// Registry counter-signature, written by `registry sign` when a plugin is
	// admitted to the canonical registry. It signs the manifest hash
	// (platform- and version-independent). Absent until counter-signed; the
	// install path treats absence as "not registry-signed", a present-but-
	// invalid signature as a hard fail.
	ManifestSHA256    string `yaml:"manifest_sha256,omitempty"`
	RegistrySignature string `yaml:"registry_signature,omitempty"`
}

func catalogURL() string {
	if url := os.Getenv("BRANCHKIT_CATALOG_URL"); url != "" {
		return url
	}
	return defaultCatalogURL
}

func fetchCatalog() (catalog, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", catalogURL(), nil)
	if err != nil {
		return catalog{}, fmt.Errorf("invalid catalog URL: %w", err)
	}
	req.Header.Set("User-Agent", "branchkit-cli")

	resp, err := client.Do(req)
	if err != nil {
		return catalog{}, fmt.Errorf("failed to fetch catalog: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return catalog{}, fmt.Errorf("catalog returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogSize))
	if err != nil {
		return catalog{}, fmt.Errorf("failed to read catalog: %w", err)
	}

	var cat catalog
	if err := yaml.Unmarshal(data, &cat); err != nil {
		return catalog{}, fmt.Errorf("failed to parse catalog: %w", err)
	}
	return cat, nil
}

// resolveShortNameEntry returns the full catalog entry for a short name —
// including the registry counter-signature fields the install path verifies.
func resolveShortNameEntry(name string) (*catalogEntry, error) {
	fmt.Printf("Looking up '%s' in catalog...\n", name)

	cat, err := fetchCatalog()
	if err != nil {
		return nil, err
	}

	for i := range cat.Plugins {
		if cat.Plugins[i].ID == name {
			fmt.Printf("Resolved '%s' → %s\n", name, cat.Plugins[i].Source)
			return &cat.Plugins[i], nil
		}
	}

	available := make([]string, 0, len(cat.Plugins))
	for _, entry := range cat.Plugins {
		available = append(available, entry.ID)
	}
	sort.Strings(available)
	return nil, fmt.Errorf(
		"plugin '%s' not found in catalog\n\nAvailable: %s\n\nFor unlisted plugins, use: branchkit-cli plugin install github:owner/repo",
		name, strings.Join(available, ", "),
	)
}

// findCatalogEntryBySource returns the catalog entry naming the same GitHub
// repo, or nil. Best-effort: an unreachable catalog returns nil — the
// counter-signature is soft-absent, matching install semantics — so updates
// never fail on registry downtime. This is what lets `plugin update`
// re-verify the counter-signature instead of passing catalog=nil and
// silently downgrading a catalog install's registry_signed record.
func findCatalogEntryBySource(source string) *catalogEntry {
	want := strings.ToLower(strings.TrimPrefix(source, "github:"))
	cat, err := fetchCatalog()
	if err != nil {
		return nil
	}
	for i := range cat.Plugins {
		have := strings.ToLower(strings.TrimPrefix(cat.Plugins[i].Source, "github:"))
		if have == want {
			return &cat.Plugins[i]
		}
	}
	return nil
}

// grantsArriveOn reports whether the app will switch on the grants this
// plugin's manifest declares, without asking — true only for a plugin that
// ships with BranchKit, which the app recognises as a first-party catalog
// entry, in the branchkit organisation, installed from the repository the
// catalog names. Every other plugin's grants arrive off on its page and wait
// for the person there. repo is the `owner/repo` being installed; "" for a
// local folder, which the app never treats as first-party.
//
// The app decides from the catalog file it reads (localCatalog), not the
// registry online, so that is the answer — and only when the registry online
// agrees with it. Either one unreadable, or the two disagreeing (the registry
// moved after the app was built, a plugin promoted or demoted, an overridden
// catalog URL), answers true: the answer decides whether an update may
// proceed without a fresh yes, and an unknown answer must not be the one
// that skips the question.
func grantsArriveOn(pluginID, repo string) bool {
	if repo == "" {
		return false
	}
	local, ok := localCatalog()
	if !ok {
		return true
	}
	remote, err := remoteCatalog()
	if err != nil {
		return true
	}
	onLocally := catalogGrantsOn(local, pluginID, repo)
	if onLocally != catalogGrantsOn(remote, pluginID, repo) {
		return true
	}
	return onLocally
}

// remoteCatalog is the registry online; a variable so tests can stand in.
var remoteCatalog = fetchCatalog

// localCatalog is the catalog the app reads; a variable so tests can stand
// in. See readLocalCatalog.
var localCatalog = readLocalCatalog

// readLocalCatalog reads the catalog file the app decides first-party from,
// trying the places it tries in its order — the copy in its app-support
// folder, then the one in its bundle's resources, then a source checkout's
// registry — and returning the first that reads and parses. ok is false
// when none does.
func readLocalCatalog() (catalog, bool) {
	for _, path := range localCatalogPaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cat catalog
		if yaml.Unmarshal(data, &cat) == nil {
			return cat, true
		}
	}
	return catalog{}, false
}

// localCatalogPaths is where the app looks for its catalog, in its order.
func localCatalogPaths() []string {
	paths := []string{filepath.Join(appSupportDir(), "catalog.yaml")}
	if r := os.Getenv("BRANCHKIT_RESOURCES"); r != "" {
		paths = append(paths, filepath.Join(r, "catalog.yaml"))
	}
	exe, err := os.Executable()
	if err != nil {
		return paths
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if app := enclosingAppBundle(exe); app != "" {
		paths = append(paths, filepath.Join(app, "Contents", "Resources", "catalog.yaml"))
	} else {
		// Other packagings keep the app's resources beside its binaries.
		dir := filepath.Dir(exe)
		paths = append(paths, filepath.Join(dir, "resources", "catalog.yaml"), filepath.Join(dir, "catalog.yaml"))
	}
	// A development build reads the registry checkout in its source tree.
	for i, dir := 0, filepath.Dir(exe); i < 4; i, dir = i+1, filepath.Dir(dir) {
		paths = append(paths, filepath.Join(dir, "branchkit-registry", "catalog.yaml"))
	}
	return paths
}

// catalogGrantsOn is grantsArriveOn's rule against a fetched catalog.
func catalogGrantsOn(cat catalog, pluginID, repo string) bool {
	for _, e := range cat.Plugins {
		if e.ID != pluginID {
			continue
		}
		listed := strings.TrimPrefix(e.Source, "github:")
		owner, _, _ := strings.Cut(listed, "/")
		return e.Tier == "first-party" &&
			strings.EqualFold(owner, "branchkit") &&
			strings.EqualFold(listed, strings.TrimPrefix(repo, "github:"))
	}
	return false
}

func resolveShortName(name string) (string, error) {
	entry, err := resolveShortNameEntry(name)
	if err != nil {
		return "", err
	}
	return entry.Source, nil
}

// isShortName returns true if the source looks like a short plugin name
// (no slash, no path prefix, no github: URL) rather than an owner/repo or local path.
func isShortName(source string) bool {
	return !strings.Contains(source, "/") && !isLocalPath(source) && !strings.HasPrefix(source, "github:")
}
