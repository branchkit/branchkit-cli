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

func cmdInstall(source string, build bool) {
	// Resolve short names via registry (e.g., "voice" → "branchkit/branchkit-plugin-voice")
	if isShortName(source) {
		resolved, err := resolveShortName(source)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		source = resolved
	}

	var err error
	if build {
		err = installFromSource(source)
	} else if isLocalPath(source) {
		err = installFromLocal(source)
	} else {
		err = installFromGitHub(source)
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

	targetDir := filepath.Join(userPluginsDir(), manifest.ID)
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

func installFromGitHub(source string) error {
	parsed, err := parseGitHubSource(source)
	if err != nil {
		return err
	}
	pluginName := pluginNameFromRepo(parsed.Repo)

	tempDir := filepath.Join(os.TempDir(), fmt.Sprintf("branchkit-install-%s", pluginName))
	os.RemoveAll(tempDir)
	os.MkdirAll(tempDir, 0o755)

	tarballPath, tag, err := downloadRelease(parsed, tempDir)
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

	targetDir := filepath.Join(userPluginsDir(), manifest.ID)
	os.MkdirAll(targetDir, 0o755)

	if err := safeCopyDir(manifestDir, targetDir, 0); err != nil {
		os.RemoveAll(targetDir)
		os.RemoveAll(tempDir)
		return fmt.Errorf("failed to copy plugin: %w", err)
	}

	if manifest.Run != "" {
		setExecutable(targetDir, manifest.Run)
	}

	// Save source metadata for update checking
	writeSourceMeta(targetDir, fmt.Sprintf("%s/%s", parsed.Owner, parsed.Repo), tag)

	fmt.Printf("Installed plugin '%s' v%s (%s)\n", manifest.Name, manifest.Version, tag)
	checkDependencies(manifest)
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

	targetDir := filepath.Join(userPluginsDir(), manifest.ID)
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
	writeSourceMeta(targetDir, fmt.Sprintf("%s/%s", parsed.Owner, parsed.Repo), "source-build")

	fmt.Printf("Built and installed plugin '%s' v%s\n", manifest.Name, manifest.Version)
	checkDependencies(manifest)
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
	installed := map[string]bool{}
	for _, dp := range discoverPlugins() {
		installed[dp.Manifest.ID] = true
	}
	var missing []string
	for _, dep := range manifest.DependsOn {
		if !installed[dep] {
			missing = append(missing, dep)
		}
	}
	if len(missing) > 0 {
		fmt.Println()
		fmt.Println("This plugin depends on plugins that are not installed:")
		for _, dep := range missing {
			fmt.Printf("  - %s\n", dep)
		}
		fmt.Println("Install them with: branchkit-cli plugin install <source>")
	}
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
		// Prevent path traversal
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)) {
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

// SourceMeta records where a plugin was installed from, for update checking.
type SourceMeta struct {
	Source       string `json:"source"`        // "owner/repo"
	InstalledTag string `json:"installed_tag"` // e.g. "v3.0.0" or "source-build"
}

const sourceMetaFile = ".branchkit-source.json"

func writeSourceMeta(pluginDir, source, tag string) {
	meta := SourceMeta{Source: source, InstalledTag: tag}
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
