package main

// `branchkit-cli plugin package` — Layer 1 of the release pipeline
// (notes/PLAN_SIGNING_CHAIN_IMPL / the polyglot release design): turn a
// built plugin into the correctly-named, reproducible release tarball plus
// its SHA-256, ready to sign and upload.
//
// LANGUAGE- AND CI-AGNOSTIC BY DESIGN. It reads only plugin.json and files
// on disk; it never compiles anything. The one language-specific step
// (producing the binary) happens BEFORE this — Go, Rust, Python, or a bare
// shell script all feed the same command. That's why the release pipeline
// isn't per-language: only the build differs, and the build isn't here.
//
// Payload = everything in the plugin dir EXCEPT build inputs / VCS / dev
// tooling / prior release outputs (a denylist, so a new manifest field that
// references data files needs no change here), with the target binary
// swapped in when cross-compiled elsewhere (`--binary`).
//
// The archive is deterministic (sorted entries, zeroed mtimes, fixed
// uid/gid, normalized modes, pinned gzip header) so the same inputs produce
// byte-identical bytes — a precondition for meaningful checksums and
// signatures.

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// platformArchLabel maps a Go GOARCH to the label used in release artifact
// names (`amd64` → `x86_64`). Shared with the install path's expectation.
func platformArchLabel(goarch string) string {
	if goarch == "amd64" {
		return "x86_64"
	}
	return goarch
}

// releaseArtifactName is the canonical tarball name the install path looks
// for: branchkit-plugin-{name}-{os}-{arch}.tar.gz.
func releaseArtifactName(name, goos, goarch string) string {
	return fmt.Sprintf("branchkit-plugin-%s-%s-%s.tar.gz", name, goos, platformArchLabel(goarch))
}

// packageDenylist: top-level names and suffixes excluded from the payload.
// Build inputs (source, module/lock files), VCS, editor/OS junk, and prior
// release outputs. Everything else in the plugin dir ships — collection_data,
// LICENSE, README, etc. — with no per-plugin enumeration.
var packageDenylistNames = map[string]bool{
	".git": true, ".github": true, ".gitignore": true, ".gitmodules": true,
	"src": true, "node_modules": true,
	"go.mod": true, "go.sum": true,
	"package.json": true, "package-lock.json": true, "bun.lock": true, "bun.lockb": true,
	"tsconfig.json": true, "Taskfile.yml": true, "Justfile": true,
	"justfile": true, "Makefile": true, ".DS_Store": true,
}

func hasReleaseSuffix(name string) bool {
	return strings.HasSuffix(name, ".tar.gz") ||
		strings.HasSuffix(name, ".sha256") ||
		strings.HasSuffix(name, ".sigstore.json")
}

type payloadEntry struct {
	// tarPath is the path inside the archive (forward slashes, relative).
	tarPath string
	// srcPath is where the bytes come from on disk.
	srcPath string
	// executable marks the entry to receive 0755 (the run binary); others 0644.
	executable bool
}

// collectPayload gathers the release payload from a plugin directory. When
// binaryOverride is non-empty it's the cross-compiled binary to include under
// the run-field basename (and any same-named file in the dir is skipped, so a
// linux binary never leaks into a macOS tarball). Deterministic (sorted).
func collectPayload(dir, runField, binaryOverride string, extraExcludes []string) ([]payloadEntry, error) {
	manifestPath := filepath.Join(dir, "plugin.json")
	if !fileExists(manifestPath) {
		return nil, fmt.Errorf("no plugin.json in %s", dir)
	}
	runBinaryBase := strings.TrimPrefix(runField, "./")

	excludeExtra := map[string]bool{}
	for _, e := range extraExcludes {
		excludeExtra[e] = true
	}

	var entries []payloadEntry
	walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		top := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if packageDenylistNames[top] || excludeExtra[top] {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil // directories are implied by their file entries
		}
		base := filepath.Base(rel)
		if hasReleaseSuffix(base) {
			return nil // never recurse a previous release output back in
		}
		// When a cross-compiled binary is supplied, skip an in-dir file of the
		// same name — the override is authoritative for the target platform.
		if binaryOverride != "" && base == runBinaryBase {
			return nil
		}
		entries = append(entries, payloadEntry{
			tarPath:    filepath.ToSlash(rel),
			srcPath:    path,
			executable: base == runBinaryBase,
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	if binaryOverride != "" {
		if !fileExists(binaryOverride) {
			return nil, fmt.Errorf("--binary %s does not exist", binaryOverride)
		}
		entries = append(entries, payloadEntry{
			tarPath:    runBinaryBase,
			srcPath:    binaryOverride,
			executable: true,
		})
	}

	// The run binary must be present one way or the other.
	if runBinaryBase != "" {
		hasBinary := false
		for _, e := range entries {
			if e.tarPath == runBinaryBase {
				hasBinary = true
				break
			}
		}
		if !hasBinary {
			return nil, fmt.Errorf("run binary %q not found in %s and no --binary given", runBinaryBase, dir)
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].tarPath < entries[j].tarPath })
	return entries, nil
}

// writeDeterministicTarGz writes the payload as a reproducible .tar.gz.
// Identical payload bytes + paths => identical archive bytes.
func writeDeterministicTarGz(entries []payloadEntry, outPath string) error {
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()

	gz, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		return err
	}
	// Pin the gzip header so it carries no timestamp or OS byte.
	gz.Header.OS = 255 // unknown
	tw := tar.NewWriter(gz)

	for _, e := range entries {
		data, err := os.ReadFile(e.srcPath)
		if err != nil {
			return err
		}
		mode := int64(0o644)
		if e.executable {
			mode = 0o755
		}
		hdr := &tar.Header{
			Name:     e.tarPath,
			Mode:     mode,
			Size:     int64(len(data)),
			Typeflag: tar.TypeReg,
			// Zeroed metadata for reproducibility.
			Uid: 0, Gid: 0, Uname: "", Gname: "",
			// ModTime left as the zero value → epoch.
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// writeChecksum writes "<hex>  <filename>\n" to <tarPath>.sha256, matching the
// same-origin checksum the install path already reads.
func writeChecksum(tarPath string) (string, error) {
	data, err := os.ReadFile(tarPath)
	if err != nil {
		return "", err
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(data))
	line := fmt.Sprintf("%s  %s\n", sum, filepath.Base(tarPath))
	if err := os.WriteFile(tarPath+".sha256", []byte(line), 0o644); err != nil {
		return "", err
	}
	return sum, nil
}

func cmdPluginPackage(args []string) {
	dir := "."
	binary := ""
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	name := ""
	outDir := "."
	var excludes []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--binary":
			i++
			binary = argAt(args, i)
		case "--os":
			i++
			goos = argAt(args, i)
		case "--arch":
			i++
			goarch = argAt(args, i)
		case "--name":
			i++
			name = argAt(args, i)
		case "--out":
			i++
			outDir = argAt(args, i)
		case "--exclude":
			i++
			excludes = append(excludes, argAt(args, i))
		default:
			if !strings.HasPrefix(args[i], "-") {
				dir = args[i]
			}
		}
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	manifest, err := readManifest(filepath.Join(absDir, "plugin.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading plugin.json: %v\n", err)
		os.Exit(1)
	}
	if name == "" {
		// Default to the manifest ID — the canonical short name the catalog
		// and the `branchkit-plugin-{name}` repo convention use.
		name = manifest.ID
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, "Error: plugin.json has no id and no --name given")
		os.Exit(1)
	}

	entries, err := collectPayload(absDir, manifest.Run, binary, excludes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	tarName := releaseArtifactName(name, goos, goarch)
	tarPath := filepath.Join(outDir, tarName)
	if err := writeDeterministicTarGz(entries, tarPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing archive: %v\n", err)
		os.Exit(1)
	}
	sum, err := writeChecksum(tarPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error writing checksum: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Packaged %s (%d files)\n", tarName, len(entries))
	fmt.Printf("  sha256: %s\n", sum)
	fmt.Println("  Next: sign it (attest-build-provenance in CI) and upload both files to the release.")
}

// argAt returns args[i] or exits with a usage error when the flag value is
// missing.
func argAt(args []string, i int) string {
	if i >= len(args) {
		fmt.Fprintln(os.Stderr, "Error: missing value for flag")
		os.Exit(1)
	}
	return args[i]
}
