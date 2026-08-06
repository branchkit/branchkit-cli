package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const bunVersion = "1.3.14"

// runtimesDir returns the path to BranchKit's managed runtimes directory.
func runtimesDir() string {
	return filepath.Join(appSupportDir(), "runtimes")
}

// managedBunPath returns the path where the managed Bun binary should be.
func managedBunPath() string {
	return filepath.Join(runtimesDir(), "bun", "bun")
}

// managedBunVersionPath returns the path to the version file.
func managedBunVersionPath() string {
	return filepath.Join(runtimesDir(), "bun", "version.txt")
}

// needsBun returns true if the manifest's run command requires Bun.
func needsBun(manifest PluginManifest) bool {
	return strings.HasPrefix(manifest.Run, "bun ")
}

// checkRuntime ensures the required runtime is available for the plugin.
func checkRuntime(manifest PluginManifest) {
	if !needsBun(manifest) {
		return
	}
	fmt.Println()
	if err := ensureBunRuntime(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not set up Bun runtime: %v\n", err)
		fmt.Fprintf(os.Stderr, "The plugin may fail to start. Install Bun manually: https://bun.sh\n")
	}
	// Listener-granted TS plugins additionally need Node as the runner
	// (bun stays the builder) — see the Node runtime section below.
	if needsNode(manifest) {
		if err := ensureNodeRuntime(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not set up Node runtime: %v\n", err)
			fmt.Fprintf(os.Stderr, "This plugin declares sockets.listen and cannot run under Bun (oven-sh/bun#22559). Install Node manually: https://nodejs.org\n")
		}
	}
}

// ensureBunRuntime checks for a Bun runtime and downloads it if missing.
func ensureBunRuntime() error {
	// 1. Check managed install
	if _, err := os.Stat(managedBunPath()); err == nil {
		// Check version
		if data, err := os.ReadFile(managedBunVersionPath()); err == nil {
			installed := strings.TrimSpace(string(data))
			if installed == bunVersion {
				return nil // correct version already installed
			}
			fmt.Printf("Updating Bun runtime: %s → %s\n", installed, bunVersion)
		}
		// Wrong version or no version file — re-download
		return downloadBun()
	}

	// 2. Check system PATH
	if path, err := exec.LookPath("bun"); err == nil {
		fmt.Printf("Using system Bun at %s\n", path)
		return nil
	}

	// 3. Download
	return downloadBun()
}

// downloadBun downloads the pinned Bun version for the current platform.
func downloadBun() error {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return fmt.Errorf("automatic Bun download is not supported on %s — install Bun manually: https://bun.sh", runtime.GOOS)
	}

	arch := runtime.GOARCH
	var archName string
	switch arch {
	case "arm64":
		archName = "aarch64"
	case "amd64":
		archName = "x64"
	default:
		return fmt.Errorf("unsupported architecture: %s", arch)
	}

	filename := fmt.Sprintf("bun-%s-%s.zip", runtime.GOOS, archName)
	url := fmt.Sprintf("https://github.com/oven-sh/bun/releases/download/bun-v%s/%s", bunVersion, filename)

	fmt.Printf("Downloading Bun v%s for %s...\n", bunVersion, archName)

	// Download to temp file
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download Bun: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("failed to download Bun: HTTP %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp("", "bun-download-*.zip")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	written, err := io.Copy(tmpFile, resp.Body)
	tmpFile.Close()
	if err != nil {
		return fmt.Errorf("failed to write download: %w", err)
	}
	fmt.Printf("Downloaded %.1f MB\n", float64(written)/1024/1024)

	// Extract
	bunDir := filepath.Join(runtimesDir(), "bun")
	os.MkdirAll(bunDir, 0o755)

	if err := extractBunFromZip(tmpPath, bunDir); err != nil {
		os.RemoveAll(bunDir)
		return fmt.Errorf("failed to extract Bun: %w", err)
	}

	// Verify the binary exists and is executable
	binPath := managedBunPath()
	if err := os.Chmod(binPath, 0o755); err != nil {
		return fmt.Errorf("failed to set executable permission: %w", err)
	}

	// Write version file
	if err := os.WriteFile(managedBunVersionPath(), []byte(bunVersion), 0o644); err != nil {
		return fmt.Errorf("failed to write version file: %w", err)
	}

	fmt.Printf("Bun v%s installed to %s\n", bunVersion, binPath)
	return nil
}

// extractBunFromZip extracts the bun binary from the downloaded zip.
// Bun zips contain a directory like "bun-darwin-aarch64/bun".
func extractBunFromZip(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		name := filepath.Base(f.Name)
		// We only need the "bun" binary itself
		if name != "bun" || f.FileInfo().IsDir() {
			continue
		}

		src, err := f.Open()
		if err != nil {
			return err
		}

		destPath := filepath.Join(destDir, "bun")
		dst, err := os.Create(destPath)
		if err != nil {
			src.Close()
			return err
		}

		_, err = io.Copy(dst, src)
		src.Close()
		dst.Close()
		if err != nil {
			return err
		}

		return nil // found and extracted
	}

	return fmt.Errorf("bun binary not found in archive")
}

// --- Node runtime (listener-granted TS plugins) ---
//
// Bun cannot serve an inherited listener fd (oven-sh/bun#22559; node:http's
// listen({fd}) silently rebinds), so a TS plugin declaring `sockets.listen`
// runs under NODE — the actuator builds it with bun (--target=node bundle)
// and executes the bundle with node (actuator lifecycle.rs). The CLI's job
// here mirrors ensureBunRuntime: make a pinned Node available in the managed
// runtimes dir so the user never installs anything. A system Node is used
// only when it meets the version floor — never blindly.

const nodeVersion = "24.19.0"

// nodeMajorFloor is the minimum system-Node major we accept instead of
// downloading: 20+ covers everything the SDK bundle needs (node: prefixed
// builtins, fd listeners, stable fetch).
const nodeMajorFloor = 20

// managedNodePath returns the path where the managed Node binary should be.
// (bin/ subdir mirrors the upstream tarball layout the actuator resolves.)
func managedNodePath() string {
	return filepath.Join(runtimesDir(), "node", "bin", "node")
}

func managedNodeVersionPath() string {
	return filepath.Join(runtimesDir(), "node", "version.txt")
}

// needsNode returns true when the plugin is a Bun-run TS plugin that declares
// loopback listeners — the combination that must execute under Node.
func needsNode(manifest PluginManifest) bool {
	return needsBun(manifest) && manifest.Sockets != nil && len(manifest.Sockets.Listen) > 0
}

// ensureNodeRuntime checks for a usable Node and downloads the pinned one if
// missing. Order: managed install (version-pinned), system PATH (accepted
// only at or above the major floor), download.
func ensureNodeRuntime() error {
	if _, err := os.Stat(managedNodePath()); err == nil {
		if data, err := os.ReadFile(managedNodeVersionPath()); err == nil {
			if strings.TrimSpace(string(data)) == nodeVersion {
				return nil
			}
			fmt.Printf("Updating Node runtime: %s → %s\n", strings.TrimSpace(string(data)), nodeVersion)
		}
		return downloadNode()
	}

	if path, err := exec.LookPath("node"); err == nil {
		if out, err := exec.Command(path, "--version").Output(); err == nil {
			v := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
			var major int
			fmt.Sscanf(v, "%d", &major)
			if major >= nodeMajorFloor {
				fmt.Printf("Using system Node %s at %s\n", v, path)
				return nil
			}
			fmt.Printf("System Node %s is below the v%d floor — downloading pinned Node\n", v, nodeMajorFloor)
		}
	}

	return downloadNode()
}

// downloadNode downloads the pinned Node version for the current platform and
// extracts ONLY bin/node (the bundle runner needs no npm/npx/headers).
func downloadNode() error {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return fmt.Errorf("automatic Node download is not supported on %s — install Node manually: https://nodejs.org", runtime.GOOS)
	}

	osName := runtime.GOOS // darwin | linux
	var archName string
	switch runtime.GOARCH {
	case "arm64":
		archName = "arm64"
	case "amd64":
		archName = "x64"
	default:
		return fmt.Errorf("unsupported architecture: %s", runtime.GOARCH)
	}

	url := fmt.Sprintf("https://nodejs.org/dist/v%s/node-v%s-%s-%s.tar.gz",
		nodeVersion, nodeVersion, osName, archName)
	fmt.Printf("Downloading Node v%s for %s-%s...\n", nodeVersion, osName, archName)

	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download Node: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("failed to download Node: HTTP %d", resp.StatusCode)
	}

	nodeBin := filepath.Join(runtimesDir(), "node", "bin")
	if err := os.MkdirAll(nodeBin, 0o755); err != nil {
		return fmt.Errorf("failed to create runtime dir: %w", err)
	}

	if err := extractNodeFromTarGz(resp.Body, filepath.Join(nodeBin, "node")); err != nil {
		os.RemoveAll(filepath.Join(runtimesDir(), "node"))
		return fmt.Errorf("failed to extract Node: %w", err)
	}
	if err := os.Chmod(managedNodePath(), 0o755); err != nil {
		return fmt.Errorf("failed to set executable permission: %w", err)
	}
	if err := os.WriteFile(managedNodeVersionPath(), []byte(nodeVersion), 0o644); err != nil {
		return fmt.Errorf("failed to write version file: %w", err)
	}
	fmt.Printf("Node v%s installed to %s\n", nodeVersion, managedNodePath())
	return nil
}

// extractNodeFromTarGz streams the tarball and writes just the bin/node entry.
func extractNodeFromTarGz(r io.Reader, destPath string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// Tarball layout: node-vX-os-arch/bin/node
		if hdr.Typeflag == tar.TypeReg && strings.HasSuffix(hdr.Name, "/bin/node") {
			dst, err := os.Create(destPath)
			if err != nil {
				return err
			}
			_, err = io.Copy(dst, tr)
			dst.Close()
			return err
		}
	}
	return fmt.Errorf("bin/node not found in archive")
}
