package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Managed Python runtime — the delivery half of the Python scripting skin
// (docs/design/DESIGN_SCRIPTING_HOST_PLUGIN.md, "Python skin — subprocess shape";
// runtime plan: docs/design/DESIGN_MANAGED_PYTHON_RUNTIME.md). The CLI is the only
// component with network access: it downloads python-build-standalone for
// this OS/arch, verifies the pinned sha256, and unpacks the tree into
// <app_support>/runtimes/python/ — the exact directory a plugin's manifest
// `"runtimes": ["python"]` declaration resolves to read+exec grants on. The
// app itself never downloads anything; a plugin seeing no runtime on disk
// points the user at `branchkit-cli runtime install python`.
//
// Unlike Bun/Node above, a system Python is never used: the sandbox grant is
// on this one directory, so only a managed install is reachable from a
// confined plugin anyway.

const pythonVersion = "3.13.15"
const pythonBuildTag = "20260814"

// pythonChecksums pins the sha256 of the install_only_stripped tarball per
// GOOS/GOARCH, from the release's SHA256SUMS. A new version bumps all three
// constants together (version, tag, table) — an entry the table lacks is an
// unsupported platform, and a checksum mismatch aborts the install.
var pythonChecksums = map[string]string{
	"darwin/arm64":  "6d472fc49a4d95e58214a992c4c92aa73fe2a935837a01a9a36bab0bec6d72f3",
	"darwin/amd64":  "bf87354efcd9ae517da606fcda4e3a3f0d73a6f05ca7cba3c6d3c5270074bfc8",
	"linux/arm64":   "985efd78c1c6521b379f7c64c2a25e6a1130f07441d1af8be441aa05260886aa",
	"linux/amd64":   "aaca2af2ab4d7b68a712660d1334c0cfd5ec13c0312ccd30c29122d8d0342320",
	"windows/amd64": "07c977bbe4abad07e3bbc314608633e6c74eab482a7bae81f4361cda970b45e6",
	"windows/arm64": "f6bf0fa39ad3668dc1996052ec6eff58c54aa126bf203cf3728a47a830af2b5d",
}

// pythonTriple maps GOOS/GOARCH onto python-build-standalone's target
// triple naming.
func pythonTriple() (string, error) {
	arch := map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH]
	if arch == "" {
		return "", fmt.Errorf("unsupported architecture: %s", runtime.GOARCH)
	}
	switch runtime.GOOS {
	case "darwin":
		return arch + "-apple-darwin", nil
	case "linux":
		return arch + "-unknown-linux-gnu", nil
	case "windows":
		return arch + "-pc-windows-msvc", nil
	}
	return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
}

// managedPythonPath returns where the interpreter lands — the same path the
// scripts host execs through its sandbox grant.
func managedPythonPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(runtimesDir(), "python", "python.exe")
	}
	return filepath.Join(runtimesDir(), "python", "bin", "python3")
}

func managedPythonVersionPath() string {
	return filepath.Join(runtimesDir(), "python", "version.txt")
}

// cmdRuntimeInstall is `branchkit-cli runtime install <name>`.
func cmdRuntimeInstall(name string) {
	switch name {
	case "python":
		if err := installPythonRuntime(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown runtime %q — installable runtimes: python\n", name)
		os.Exit(1)
	}
}

// cmdRuntimeList prints each managed runtime with its installed version.
func cmdRuntimeList() {
	entries, _ := os.ReadDir(runtimesDir())
	if len(entries) == 0 {
		fmt.Println("No managed runtimes installed.")
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		version := "unknown version"
		if data, err := os.ReadFile(filepath.Join(runtimesDir(), e.Name(), "version.txt")); err == nil {
			version = strings.TrimSpace(string(data))
		}
		fmt.Printf("  %-8s %s\n", e.Name(), version)
	}
}

// installPythonRuntime downloads, verifies, and unpacks the pinned CPython.
// Idempotent: an install at the pinned version returns immediately.
func installPythonRuntime() error {
	pin := fmt.Sprintf("%s+%s", pythonVersion, pythonBuildTag)
	if data, err := os.ReadFile(managedPythonVersionPath()); err == nil {
		installed := strings.TrimSpace(string(data))
		if installed == pin {
			fmt.Printf("Python %s already installed at %s\n", pin, managedPythonPath())
			return nil
		}
		fmt.Printf("Updating Python runtime: %s → %s\n", installed, pin)
	}

	triple, err := pythonTriple()
	if err != nil {
		return err
	}
	wantSum, ok := pythonChecksums[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return fmt.Errorf("no checksum pinned for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	asset := fmt.Sprintf("cpython-%s+%s-%s-install_only_stripped.tar.gz",
		pythonVersion, pythonBuildTag, triple)
	url := fmt.Sprintf("https://github.com/astral-sh/python-build-standalone/releases/download/%s/%s",
		pythonBuildTag, asset)

	fmt.Printf("Downloading Python %s for %s...\n", pin, triple)
	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download Python: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("failed to download Python: HTTP %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp("", "python-download-*.tar.gz")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	hasher := sha256.New()
	written, err := io.Copy(tmpFile, io.TeeReader(resp.Body, hasher))
	tmpFile.Close()
	if err != nil {
		return fmt.Errorf("failed to write download: %w", err)
	}
	fmt.Printf("Downloaded %.1f MB\n", float64(written)/1024/1024)

	gotSum := hex.EncodeToString(hasher.Sum(nil))
	if gotSum != wantSum {
		return fmt.Errorf("checksum mismatch for %s:\n  want %s\n  got  %s\nrefusing to install", asset, wantSum, gotSum)
	}

	// Unpack into a staging dir beside the target, then swap — a torn
	// extraction must never leave a half-tree at the path the sandbox
	// grants exec on.
	target := filepath.Join(runtimesDir(), "python")
	staging := target + ".partial"
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return fmt.Errorf("failed to create staging dir: %w", err)
	}
	if err := extractPythonTarGz(tmpPath, staging); err != nil {
		os.RemoveAll(staging)
		return fmt.Errorf("failed to extract Python: %w", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "version.txt"), []byte(pin), 0o644); err != nil {
		os.RemoveAll(staging)
		return fmt.Errorf("failed to write version file: %w", err)
	}
	if err := os.RemoveAll(target); err != nil {
		os.RemoveAll(staging)
		return fmt.Errorf("failed to clear previous install: %w", err)
	}
	if err := os.Rename(staging, target); err != nil {
		os.RemoveAll(staging)
		return fmt.Errorf("failed to move runtime into place: %w", err)
	}

	fmt.Printf("Python %s installed to %s\n", pin, managedPythonPath())
	return nil
}

// extractPythonTarGz unpacks the whole tree, stripping the archive's
// leading "python/" component so bin/lib land directly under destDir.
// Modes are preserved (the interpreter and shared objects need exec bits)
// and in-tree symlinks are recreated; anything escaping destDir is refused.
func extractPythonTarGz(tarPath, destDir string) error {
	f, err := os.Open(tarPath)
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
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")
		rel, ok := strings.CutPrefix(name, "python/")
		if !ok || rel == "" {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(rel))
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("archive entry escapes destination: %s", hdr.Name)
		}
		dest := filepath.Join(destDir, clean)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			dst, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			_, err = io.Copy(dst, tr)
			dst.Close()
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Only links that stay inside the tree (bin/python3 →
			// python3.13 and friends).
			linkTarget := filepath.FromSlash(hdr.Linkname)
			joined := filepath.Clean(filepath.Join(filepath.Dir(dest), linkTarget))
			if filepath.IsAbs(linkTarget) || !strings.HasPrefix(joined, filepath.Clean(destDir)+string(filepath.Separator)) {
				return fmt.Errorf("archive symlink escapes destination: %s -> %s", hdr.Name, hdr.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			_ = os.Remove(dest)
			if err := os.Symlink(linkTarget, dest); err != nil {
				return err
			}
		}
	}

	if _, err := os.Stat(managedPythonInDir(destDir)); err != nil {
		return fmt.Errorf("interpreter missing after extraction (%s)", managedPythonInDir(destDir))
	}
	return nil
}

// managedPythonInDir is managedPythonPath relative to an arbitrary root —
// used to validate the staging tree before it is swapped into place.
func managedPythonInDir(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "python.exe")
	}
	return filepath.Join(dir, "bin", "python3")
}
