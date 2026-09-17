package main

import (
	"archive/tar"
	"archive/zip"
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

// Managed JavaScript toolchains.
//
// Bun and Node are BUILD tools here, never run-time dependencies: a
// TypeScript plugin ships as a compiled binary (see dev_build_ts.go), so an
// end user installing one needs neither. They are pinned, checksum-verified,
// and installed under the managed runtimes directory; an author's own Bun or
// Node is never consulted, so a build does not depend on what happens to be
// on someone's PATH (decided 2026-09-17, docs/design/PLAN_SCAFFOLD_TRIALS.md).
//
// Bun is the bundler and the default engine. Node is fetched only when a
// plugin declaring `sockets.listen` is first built: Bun cannot serve an
// inherited listener fd (oven-sh/bun#22559), compiled or not.

const bunVersion = "1.3.14"
const nodeVersion = "24.19.0"

// Pinned sha256 of each release artifact, keyed by GOOS/GOARCH. From the
// publishers' own SHASUMS256.txt for exactly the versions above; bump them
// together.
var bunChecksums = map[string]string{
	"darwin/arm64":  "d8b96221828ad6f97ac7ac0ab7e95872341af763001e8803e8267652c2652620",
	"darwin/amd64":  "4183df3374623e5bab315c547cfa0974533cd457d86b73b639f7a87974cd6633",
	"linux/arm64":   "a27ffb63a8310375836e0d6f668ae17fa8d8d18b88c37c821c65331973a19a3b",
	"linux/amd64":   "951ee2aee855f08595aeec6225226a298d3fea83a3dcd6465c09cbccdf7e848f",
	"windows/amd64": "0a0620930b6675d7ba440e81f4e0e00d3cfbe096c4b140d3fff02205e9e18922",
}

var nodeChecksums = map[string]string{
	"darwin/arm64":  "8294b7aa9b03997481c06babf1e8b270c859358f27da57a11509afe537ac381d",
	"darwin/amd64":  "d1b5e999db158c62fe8f7267a4476b035d8bd93b1a605bac24a3f0dd166e3316",
	"linux/arm64":   "d28c8a5bf0a808f0ed434a1dce8c54ae98f0371c0bd86ac58abc613f73e6643f",
	"linux/amd64":   "f625d97cd707df4ff96254916fbc5ff014f09c09effe5a1e0ca8f6d41a8789d4",
	"windows/arm64": "3958e4bb3f2d4ef37c938215dfc65a9d3c9d839b5060fec103bd2345fa78e951",
	"windows/amd64": "3602f2bb1a10f2cbab4c36886218a33c1ab3db87290e73b033c46c77147d0237",
}

// runtimesDir returns the path to BranchKit's managed runtimes directory.
func runtimesDir() string {
	return filepath.Join(appSupportDir(), "runtimes")
}

func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// managedBunPath returns the path where the managed Bun binary should be.
func managedBunPath() string {
	return filepath.Join(runtimesDir(), "bun", exeName("bun"))
}

// managedBunVersionPath returns the path to the version file.
func managedBunVersionPath() string {
	return filepath.Join(runtimesDir(), "bun", "version.txt")
}

// managedNodePath returns the path where the managed Node binary should be.
// (bin/ mirrors the upstream tarball layout; Windows ships a bare node.exe.)
func managedNodePath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(runtimesDir(), "node", "node.exe")
	}
	return filepath.Join(runtimesDir(), "node", "bin", "node")
}

func managedNodeVersionPath() string {
	return filepath.Join(runtimesDir(), "node", "version.txt")
}

// installedAt reports whether versionPath records exactly want and binPath
// exists — the whole "is the pinned toolchain already here" question.
func installedAt(binPath, versionPath, want string) bool {
	if _, err := os.Stat(binPath); err != nil {
		return false
	}
	data, err := os.ReadFile(versionPath)
	return err == nil && strings.TrimSpace(string(data)) == want
}

// ensureBunRuntime makes the pinned Bun available, downloading it if the
// managed install is missing or at another version.
func ensureBunRuntime() error {
	if installedAt(managedBunPath(), managedBunVersionPath(), bunVersion) {
		return nil
	}
	return downloadBun()
}

// ensureNodeRuntime is the same for Node.
func ensureNodeRuntime() error {
	if installedAt(managedNodePath(), managedNodeVersionPath(), nodeVersion) {
		return nil
	}
	return downloadNode()
}

// downloadVerified fetches url into a temp file and checks its sha256 against
// want. The caller removes the returned path. Nothing is unpacked, and
// nothing under runtimes/ is touched, until the sum matches.
func downloadVerified(url, want string, timeout time.Duration) (string, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download failed: HTTP %d for %s", resp.StatusCode, url)
	}
	tmp, err := os.CreateTemp("", "branchkit-runtime-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body)
	tmp.Close()
	if err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("failed to write download: %w", err)
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != want {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("checksum mismatch for %s: got %s, pinned %s — refusing to install", url, got, want)
	}
	fmt.Printf("Downloaded %.1f MB, checksum verified\n", float64(written)/1024/1024)
	return tmp.Name(), nil
}

// downloadBun installs the pinned Bun for this platform.
func downloadBun() error {
	key := runtime.GOOS + "/" + runtime.GOARCH
	want, ok := bunChecksums[key]
	if !ok {
		return fmt.Errorf("no Bun build is pinned for %s", key)
	}
	archName := map[string]string{"arm64": "aarch64", "amd64": "x64"}[runtime.GOARCH]
	filename := fmt.Sprintf("bun-%s-%s.zip", runtime.GOOS, archName)
	url := fmt.Sprintf("https://github.com/oven-sh/bun/releases/download/bun-v%s/%s", bunVersion, filename)

	fmt.Printf("Downloading Bun v%s for %s...\n", bunVersion, key)
	zipPath, err := downloadVerified(url, want, 120*time.Second)
	if err != nil {
		return err
	}
	defer os.Remove(zipPath)

	bunDir := filepath.Join(runtimesDir(), "bun")
	if err := os.MkdirAll(bunDir, 0o755); err != nil {
		return fmt.Errorf("failed to create runtime dir: %w", err)
	}
	if err := extractBunFromZip(zipPath, bunDir); err != nil {
		os.RemoveAll(bunDir)
		return fmt.Errorf("failed to extract Bun: %w", err)
	}
	if err := os.Chmod(managedBunPath(), 0o755); err != nil {
		return fmt.Errorf("failed to set executable permission: %w", err)
	}
	if err := os.WriteFile(managedBunVersionPath(), []byte(bunVersion), 0o644); err != nil {
		return fmt.Errorf("failed to write version file: %w", err)
	}
	fmt.Printf("Bun v%s installed to %s\n", bunVersion, managedBunPath())
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

	want := exeName("bun")
	for _, f := range r.File {
		if filepath.Base(f.Name) != want || f.FileInfo().IsDir() {
			continue
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		dst, err := os.Create(filepath.Join(destDir, want))
		if err != nil {
			src.Close()
			return err
		}
		_, err = io.Copy(dst, src)
		src.Close()
		dst.Close()
		return err
	}
	return fmt.Errorf("%s binary not found in archive", want)
}

// downloadNode installs the pinned Node for this platform: ONLY the node
// binary. It is copied into a plugin as the single-executable host, so npm,
// npx and the headers have no use here.
func downloadNode() error {
	key := runtime.GOOS + "/" + runtime.GOARCH
	want, ok := nodeChecksums[key]
	if !ok {
		return fmt.Errorf("no Node build is pinned for %s", key)
	}
	archName := map[string]string{"arm64": "arm64", "amd64": "x64"}[runtime.GOARCH]

	var url string
	if runtime.GOOS == "windows" {
		url = fmt.Sprintf("https://nodejs.org/dist/v%s/win-%s/node.exe", nodeVersion, archName)
	} else {
		url = fmt.Sprintf("https://nodejs.org/dist/v%s/node-v%s-%s-%s.tar.gz",
			nodeVersion, nodeVersion, runtime.GOOS, archName)
	}
	fmt.Printf("Downloading Node v%s for %s...\n", nodeVersion, key)
	tmpPath, err := downloadVerified(url, want, 300*time.Second)
	if err != nil {
		return err
	}
	defer os.Remove(tmpPath)

	nodeRoot := filepath.Join(runtimesDir(), "node")
	if err := os.MkdirAll(filepath.Dir(managedNodePath()), 0o755); err != nil {
		return fmt.Errorf("failed to create runtime dir: %w", err)
	}
	if runtime.GOOS == "windows" {
		err = copyFile(tmpPath, managedNodePath(), 0o755)
	} else {
		var f *os.File
		if f, err = os.Open(tmpPath); err == nil {
			err = extractNodeFromTarGz(f, managedNodePath())
			f.Close()
		}
	}
	if err != nil {
		os.RemoveAll(nodeRoot)
		return fmt.Errorf("failed to unpack Node: %w", err)
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
