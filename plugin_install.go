package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func cmdInstall(source string, build bool, force bool) {
	// Carries the catalog entry (with its registry counter-signature) when
	// installing by short name, so the install path can confirm the canonical
	// listing. nil for direct github:owner/repo or local installs.
	var entry *catalogEntry
	if isShortName(source) {
		e, err := resolveShortNameEntry(source)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		entry = e
		source = e.Source
	}

	if !force {
		checkBlocklist(source)
	}

	var err error
	if build {
		err = installFromSource(source)
	} else if isLocalPath(source) {
		err = installFromLocal(source)
	} else {
		err = installFromGitHub(source, entry)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func isLocalPath(source string) bool {
	return strings.HasPrefix(source, "/") ||
		strings.HasPrefix(source, "./") ||
		strings.HasPrefix(source, "~/") ||
		strings.HasPrefix(source, "..")
}

// --- Local install ---

func installFromLocal(source string) error {
	manifestPath, err := findManifest(source)
	if err != nil {
		return err
	}
	manifest, err := readManifest(manifestPath)
	if err != nil {
		return err
	}
	source = filepath.Dir(manifestPath)

	// The consent moment, same shape as the GitHub path — this used to be
	// the hole in "nothing installs unseen": files landed first and the
	// disclosure printed after, with no question even on a TTY. A local
	// re-install over an existing version asks about the diff, like any
	// other update.
	targetDir := filepath.Join(userPluginsDir(), manifest.ID)
	if old, rerr := readManifest(filepath.Join(targetDir, "plugin.json")); rerr == nil {
		if err := confirmUpdate(manifest, old, os.Stdin, installAssumeYes, stdinIsTTY()); err != nil {
			return err
		}
	} else if err := confirmInstall(manifest, os.Stdin, !installAssumeYes && stdinIsTTY()); err != nil {
		return err
	}

	os.MkdirAll(targetDir, 0o755)

	if err := safeCopyDir(source, targetDir, 0); err != nil {
		os.RemoveAll(targetDir)
		return fmt.Errorf("failed to copy plugin: %w", err)
	}

	if manifest.Run != "" {
		setExecutable(targetDir, manifest.Run)
	}

	fmt.Printf("Installed plugin '%s' v%s\n", manifest.Name, manifest.Version)
	checkDependencies(manifest)
	checkRuntime(manifest)
	notifyActuator()
	return nil
}

// --- GitHub install ---

func installFromGitHub(source string, catalog *catalogEntry) error {
	parsed, err := parseGitHubSource(source)
	if err != nil {
		return err
	}
	pluginName := pluginNameFromRepo(parsed.Repo)

	tempDir := filepath.Join(os.TempDir(), fmt.Sprintf("branchkit-install-%s", pluginName))
	os.RemoveAll(tempDir)
	os.MkdirAll(tempDir, 0o755)

	tarballPath, tag, attestation, err := downloadRelease(parsed, tempDir)
	if err != nil {
		os.RemoveAll(tempDir)
		return err
	}

	fmt.Println("Extracting...")
	extractDir := filepath.Join(tempDir, "extracted")
	os.MkdirAll(extractDir, 0o755)
	if err := extractTarball(tarballPath, extractDir); err != nil {
		os.RemoveAll(tempDir)
		return fmt.Errorf("failed to extract archive: %w", err)
	}

	manifestPath, err := findManifest(extractDir)
	if err != nil {
		os.RemoveAll(tempDir)
		return err
	}
	manifestDir := filepath.Dir(manifestPath)

	manifest, err := readManifest(manifestPath)
	if err != nil {
		os.RemoveAll(tempDir)
		return err
	}

	// Ownership claim vs. cryptographic identity — refuse a contradiction
	// BEFORE the consent prompt, so the user is never asked to approve an
	// install whose stated publisher the attestation disproves.
	if err := checkPublisherClaim(manifest.Publisher, attestation); err != nil {
		os.RemoveAll(tempDir)
		return err
	}

	// Attestation downgrade gate: an update whose predecessor was
	// author-verified must not quietly become unverified — writeSourceMeta
	// would record author_verified:false and the trust tier would drop
	// without a word. Checked before the consent prompt, like the
	// publisher claim: a security regression is not something to bundle
	// into a diff question.
	if prior, ok := readSourceMeta(filepath.Join(userPluginsDir(), manifest.ID)); ok {
		if prior.AuthorVerified && (attestation == nil || !attestation.Verified) {
			if err := confirmAttestationDowngrade(manifest.Name, os.Stdin, stdinIsTTY()); err != nil {
				os.RemoveAll(tempDir)
				return err
			}
		}
	}

	// Consent moment. A fresh install discloses everything and asks; an
	// update asks only about the DIFF against the manifest it will replace
	// (the old plugin.json is sitting right at the swap target) — nothing
	// new means no question, an expansion needs a fresh yes.
	if old, rerr := readManifest(filepath.Join(userPluginsDir(), manifest.ID, "plugin.json")); rerr == nil {
		if err := confirmUpdate(manifest, old, os.Stdin, installAssumeYes, stdinIsTTY()); err != nil {
			os.RemoveAll(tempDir)
			return err
		}
	} else if err := confirmInstall(manifest, os.Stdin, !installAssumeYes && stdinIsTTY()); err != nil {
		os.RemoveAll(tempDir)
		return err
	}

	// Registry counter-signature: for a canonical-registry install with a
	// verified author attestation, confirm BranchKit's counter-signature over
	// this exact manifest + attestation. Present-but-invalid is a hard
	// failure (a forged or retargeted canonical listing); absent is fine
	// (rollout / community). Only meaningful atop a verified author signature
	// — the counter-sig signs the attestation's digest. Verified against the
	// EXTRACTED manifest, before the existing install is touched: a failed
	// check on an update must not destroy the version already on disk (the
	// post-copy ordering this replaced did exactly that).
	registrySigned := false
	if catalog != nil && attestation != nil && attestation.Verified {
		manifestBytes, rerr := os.ReadFile(manifestPath)
		pub, kerr := registryPublicKey()
		if rerr == nil && kerr == nil {
			ok, verr := verifyCatalogCounterSig(pub, *catalog, manifestBytes)
			if verr != nil {
				os.RemoveAll(tempDir)
				return fmt.Errorf("registry counter-signature check failed: %w", verr)
			}
			registrySigned = ok
			if ok {
				fmt.Println("Registry counter-signature verified — canonical listing.")
			}
		}
	}

	// Stage beside the target, then swap. The previous version stays intact
	// until the new one is fully in place, and nothing from it survives the
	// swap — updates used to overlay the old directory, accreting stale
	// files across versions.
	targetDir := filepath.Join(userPluginsDir(), manifest.ID)
	stageDir := targetDir + ".installing"
	os.RemoveAll(stageDir)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		os.RemoveAll(tempDir)
		return fmt.Errorf("failed to create staging directory: %w", err)
	}
	if err := safeCopyDir(manifestDir, stageDir, 0); err != nil {
		os.RemoveAll(stageDir)
		os.RemoveAll(tempDir)
		return fmt.Errorf("failed to copy plugin: %w", err)
	}
	if manifest.Run != "" {
		setExecutable(stageDir, manifest.Run)
	}
	// Source metadata (update checking + the verified-author and
	// registry-signed records the actuator's trust-tier resolution reads)
	// goes into the stage so the swap lands it atomically with the files.
	writeSourceMeta(stageDir, fmt.Sprintf("%s/%s", parsed.Owner, parsed.Repo), tag, attestation, registrySigned)
	if err := os.RemoveAll(targetDir); err != nil {
		os.RemoveAll(stageDir)
		os.RemoveAll(tempDir)
		return fmt.Errorf("failed to clear previous install: %w", err)
	}
	if err := os.Rename(stageDir, targetDir); err != nil {
		os.RemoveAll(stageDir)
		os.RemoveAll(tempDir)
		return fmt.Errorf("failed to move plugin into place: %w", err)
	}

	fmt.Printf("Installed plugin '%s' v%s (%s) by github:%s\n", manifest.Name, manifest.Version, tag, parsed.Owner)
	printInstallInfo(manifest, parsed, tag)
	checkRuntime(manifest)
	notifyActuator()
	os.RemoveAll(tempDir)
	return nil
}

// --- Build from source ---

func installFromSource(source string) error {
	parsed, err := parseGitHubSource(source)
	if err != nil {
		return err
	}
	pluginName := pluginNameFromRepo(parsed.Repo)

	tempDir := filepath.Join(os.TempDir(), fmt.Sprintf("branchkit-build-%s", pluginName))
	os.RemoveAll(tempDir)

	fmt.Printf("Cloning %s/%s...\n", parsed.Owner, parsed.Repo)
	cloneArgs := []string{"clone", "--depth", "1"}
	if parsed.Version != "" {
		cloneArgs = append(cloneArgs, "--branch", parsed.Version)
	}
	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", parsed.Owner, parsed.Repo)
	cloneArgs = append(cloneArgs, repoURL, tempDir)

	cmd := exec.Command("git", cloneArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
	}

	manifestPath := filepath.Join(tempDir, "plugin.json")
	if _, err := os.Stat(manifestPath); err != nil {
		os.RemoveAll(tempDir)
		return fmt.Errorf("no plugin.json found in repository root")
	}

	// Detect build system and build
	switch {
	case fileExists(filepath.Join(tempDir, "go.mod")):
		fmt.Println("Building Go plugin...")
		cmd := exec.Command("go", "build", "-ldflags=-s -w", "-o", pluginName+"-plugin", ".")
		cmd.Dir = tempDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.RemoveAll(tempDir)
			return fmt.Errorf("go build failed: %w", err)
		}
	case fileExists(filepath.Join(tempDir, "Cargo.toml")):
		fmt.Println("Building Rust plugin...")
		cmd := exec.Command("cargo", "build", "--release")
		cmd.Dir = tempDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.RemoveAll(tempDir)
			return fmt.Errorf("cargo build failed: %w", err)
		}
	case fileExists(filepath.Join(tempDir, "Makefile")):
		fmt.Println("Building via Makefile...")
		cmd := exec.Command("make")
		cmd.Dir = tempDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.RemoveAll(tempDir)
			return fmt.Errorf("make failed: %w", err)
		}
	default:
		os.RemoveAll(tempDir)
		return fmt.Errorf("unknown build system — no go.mod, Cargo.toml, or Makefile found")
	}

	manifest, err := readManifest(manifestPath)
	if err != nil {
		os.RemoveAll(tempDir)
		return err
	}

	// The consent moment — after the build (so the question covers the
	// manifest that actually installs) and before anything lands in the
	// plugins directory. This path used to install without ever printing
	// the disclosure, let alone asking.
	targetDir := filepath.Join(userPluginsDir(), manifest.ID)
	if old, rerr := readManifest(filepath.Join(targetDir, "plugin.json")); rerr == nil {
		if err := confirmUpdate(manifest, old, os.Stdin, installAssumeYes, stdinIsTTY()); err != nil {
			os.RemoveAll(tempDir)
			return err
		}
	} else if err := confirmInstall(manifest, os.Stdin, !installAssumeYes && stdinIsTTY()); err != nil {
		os.RemoveAll(tempDir)
		return err
	}

	os.MkdirAll(targetDir, 0o755)

	// Copy plugin.json
	if err := copyFile(manifestPath, filepath.Join(targetDir, "plugin.json"), 0o644); err != nil {
		os.RemoveAll(targetDir)
		os.RemoveAll(tempDir)
		return fmt.Errorf("failed to copy manifest: %w", err)
	}

	// Copy binary
	if manifest.Run != "" {
		binaryName := strings.TrimPrefix(manifest.Run, "./")
		srcBinary := filepath.Join(tempDir, binaryName)
		if !fileExists(srcBinary) {
			// Check Rust target/release
			srcBinary = filepath.Join(tempDir, "target", "release", binaryName)
		}
		if !fileExists(srcBinary) {
			os.RemoveAll(tempDir)
			return fmt.Errorf("built binary '%s' not found", binaryName)
		}
		if err := copyFile(srcBinary, filepath.Join(targetDir, binaryName), 0o755); err != nil {
			os.RemoveAll(targetDir)
			os.RemoveAll(tempDir)
			return fmt.Errorf("failed to copy binary: %w", err)
		}
	}

	// Save source metadata for update checking
	writeSourceMeta(targetDir, fmt.Sprintf("%s/%s", parsed.Owner, parsed.Repo), "source-build", nil, false)

	fmt.Printf("Built and installed plugin '%s' v%s by github:%s\n", manifest.Name, manifest.Version, parsed.Owner)
	printInstallInfo(manifest, parsed, "source-build")
	checkRuntime(manifest)
	notifyActuator()
	os.RemoveAll(tempDir)
	return nil
}

// --- Helpers ---

func readManifest(path string) (PluginManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PluginManifest{}, fmt.Errorf("failed to read %s: %w", path, err)
	}
	var m PluginManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return PluginManifest{}, fmt.Errorf("failed to parse plugin.json: %w", err)
	}
	if !validateID(m.ID) {
		return PluginManifest{}, fmt.Errorf("invalid plugin ID '%s' — must be lowercase letters, digits, and hyphens", m.ID)
	}
	return m, nil
}

func checkDependencies(manifest PluginManifest) {
	if len(manifest.DependsOn) == 0 {
		return
	}
	installedVersions := map[string]string{}
	for _, dp := range discoverPlugins() {
		installedVersions[dp.Manifest.ID] = dp.Manifest.Version
	}
	var missing []string
	var mismatched []string
	for _, dep := range manifest.DependsOn {
		version, found := installedVersions[dep.Plugin]
		if !found {
			label := dep.Plugin
			if dep.Version != "" {
				label += " " + dep.Version
			}
			if dep.Source != "" {
				label += " (" + dep.Source + ")"
			}
			missing = append(missing, label)
			continue
		}
		if dep.Version != "" && version != "" {
			ok, err := satisfiesConstraint(version, dep.Version)
			if err == nil && !ok {
				mismatched = append(mismatched, fmt.Sprintf(
					"%s: requires %s, installed %s", dep.Plugin, dep.Version, version))
			}
		}
	}
	if len(missing) > 0 {
		fmt.Println()
		fmt.Println("This plugin depends on plugins that are not installed:")
		for _, m := range missing {
			fmt.Printf("  - %s\n", m)
		}
		fmt.Println("Install them with: branchkit-cli plugin install <source>")
	}
	if len(mismatched) > 0 {
		fmt.Println()
		fmt.Println("Version mismatches:")
		for _, m := range mismatched {
			fmt.Printf("  - %s\n", m)
		}
	}
}

func printInstallInfo(manifest PluginManifest, source ResolvedSource, tag string) {
	// Catalog tier
	tier := lookupCatalogTier(manifest.ID)
	if tier != "" {
		fmt.Printf("  Catalog: %s\n", tier)
	}

	// Conformance
	cs := fetchConformanceStatus(source, tag)
	fmt.Println(formatConformanceStatus(cs))

	// The consent summary already printed at the confirm moment before the
	// files landed; only the post-install facts print here.

	// Dependencies
	checkDependencies(manifest)
}

// printConsentSummary prints what the plugin will be able to do: required and
// optional privileges, and the effects it will assert with the author-written
// user-visible copy. This is the disclosure half of install-time consent —
// granting stays in Settings, where the spawn gate holds unapproved plugins.
func printConsentSummary(manifest PluginManifest) {
	if len(manifest.Privileges) > 0 {
		fmt.Printf("  Privileges: %s\n", strings.Join(manifest.Privileges, ", "))
	}
	if len(manifest.OptionalPrivileges) > 0 {
		fmt.Printf("  Optional privileges: %s\n", strings.Join(manifest.OptionalPrivileges, ", "))
	}
	if manifest.Consumes == nil {
		return
	}
	for _, e := range manifest.Consumes.Effects {
		names := e.AssertNames()
		if len(names) == 0 {
			continue
		}
		label := e.UserVisibleName
		if label == "" {
			label = strings.Join(names, ", ")
		}
		if e.UserVisibleDescription != "" {
			fmt.Printf("  Effects — %s: %s\n", label, e.UserVisibleDescription)
		} else {
			fmt.Printf("  Effects — %s\n", label)
		}
	}
}

func lookupCatalogTier(pluginID string) string {
	cat, err := fetchCatalog()
	if err != nil {
		return ""
	}
	for _, entry := range cat.Plugins {
		if entry.ID == pluginID {
			switch entry.Tier {
			case "first-party":
				return "First-party"
			case "approved":
				return "Approved"
			case "community":
				return "Community"
			default:
				return entry.Tier
			}
		}
	}
	return ""
}

func extractTarball(tarballPath, destDir string) error {
	f, err := os.Open(tarballPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, header.Name)
		// Prevent path traversal. filepath.Rel instead of a raw prefix
		// check: HasPrefix(Clean(target), Clean(destDir)) lets "../bevil"
		// escape /a/b into the sibling /a/bevil (prefix matches without a
		// separator boundary).
		rel, err := filepath.Rel(destDir, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("archive contains path traversal: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0o755)
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			io.Copy(out, tr)
			out.Close()
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("symlinks are not allowed in plugin archives: %s", header.Name)
		}
	}
	return nil
}

func findManifest(dir string) (string, error) {
	// Check root
	root := filepath.Join(dir, "plugin.json")
	if fileExists(root) {
		return root, nil
	}
	// Check one level deep
	var found []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			nested := filepath.Join(dir, entry.Name(), "plugin.json")
			if fileExists(nested) {
				found = append(found, nested)
			}
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no plugin.json found in extracted archive")
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("found %d plugin.json files in archive — expected exactly one", len(found))
	}
}

// SourceMeta records where a plugin was installed from (for update checking)
// and the signing outcome at install time (for the actuator's trust-tier
// resolution — DESIGN_PLUGIN_SIGNING_CHAIN).
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
	// actuator resolve RegistrySigned rather than a catalog claim.
	RegistrySigned bool `json:"registry_signed"`
}

const sourceMetaFile = ".branchkit-source.json"

func writeSourceMeta(pluginDir, source, tag string, attestation *AuthorAttestation, registrySigned bool) {
	meta := SourceMeta{Source: source, InstalledTag: tag, RegistrySigned: registrySigned}
	if attestation != nil && attestation.Verified {
		meta.AuthorVerified = true
		meta.AuthorIdentity = attestation.SAN
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(filepath.Join(pluginDir, sourceMetaFile), data, 0o644)
}

func readSourceMeta(pluginDir string) (SourceMeta, bool) {
	data, err := os.ReadFile(filepath.Join(pluginDir, sourceMetaFile))
	if err != nil {
		return SourceMeta{}, false
	}
	var meta SourceMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return SourceMeta{}, false
	}
	return meta, true
}

func setExecutable(dir, runCmd string) {
	binaryName := strings.TrimPrefix(runCmd, "./")
	binaryPath := filepath.Join(dir, binaryName)
	if fileExists(binaryPath) {
		os.Chmod(binaryPath, 0o755)
	} else {
		fmt.Fprintf(os.Stderr, "  WARN: Binary '%s' not found in %s\n", binaryName, dir)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
