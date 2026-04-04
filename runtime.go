package main

import (
	"archive/zip"
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

const bunVersion = "1.2.5"

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
