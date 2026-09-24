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
	"runtime"
	"strings"
	"time"
)

// Building a TypeScript plugin.
//
// A TypeScript plugin ships as ONE compiled binary in its own directory and
// its manifest says `run: "./<id>-plugin"` — the shape a Go plugin has. That
// is not a style choice. Measured 2026-09-17 against the app's sandbox
// (the scaffold trials): a shell wrapper cannot be exec'd, an
// interpreted `bun run` dies reading its ancestor directories, and Node on a
// loose bundle needs an lstat grant on them. A program in the plugin's own
// directory is what the exec rule already allows, and it resolves nothing at
// startup. The user installing the plugin needs no JavaScript runtime at all.
//
// The engine is chosen HERE, from the manifest, and the author never picks:
//
//   - Bun (`bun build --compile`) by default.
//   - A Node single-executable when the manifest declares `sockets.listen`.
//     Bun cannot serve an inherited listener fd, compiled or not — it reports
//     LISTENING and silently binds a different port (oven-sh/bun#22559). The
//     day that is fixed, the Node branch is deleted and nothing an author
//     sees changes.
//
// Both toolchains are the managed, pinned ones (runtime.go).

// postjectVersion pins the injector that writes the blob into the Node binary.
const postjectVersion = "1.0.0-alpha.6"

// postjectSHA256 is OUR record of what that version's npm tarball hashes to,
// checked before the tool is allowed to touch a binary we ship.
//
// This used to be `bun x postject@<version>` and nothing else. Node, Bun and
// CPython are all downloaded through `downloadVerified` against a sha256 we
// recorded ourselves, and refuse to install on a mismatch — postject was the
// one link in the chain without that, and it is the link that WRITES THE
// EXECUTABLE. A compromised injector can put anything into the binary a user
// runs, which makes it a better target than the runtimes it is injecting into.
//
// A version alone is not integrity. npm treats published versions as
// immutable and `bun x` verifies against the registry's own metadata, so this
// is not unverified so much as verified BY THE REGISTRY — which is the party
// a supply-chain attack compromises. Recording the digest here means the
// build trusts a number in this repository instead.
//
// Recorded 2026-09-21 by downloading the tarball and hashing it; the same
// bytes also match npm's published sha512, so the pin is of an artifact that
// was authentic at the time it was pinned.
//
// To move the pin: change both constants together, and get the new digest
// with
//
//	curl -sL https://registry.npmjs.org/postject/-/postject-<ver>.tgz | shasum -a 256
const postjectSHA256 = "d1447b53e87d49ddaf7fb3350c870afafa72760eca47f6d5cce4cefd537e7d92"

// postjectTarballURL is the registry path for the pinned version.
func postjectTarballURL() string {
	return "https://registry.npmjs.org/postject/-/postject-" + postjectVersion + ".tgz"
}

// verifiedPostject downloads the pinned tarball, refuses it unless the digest
// matches, unpacks it, and returns the path to its CLI entrypoint.
//
// Cached under the same directory the other verified runtimes use, so a
// rebuild does not re-download; the cache is keyed by version AND digest, so
// changing either fetches afresh rather than reusing something that was
// verified against a different pin.
func verifiedPostject() (string, error) {
	dest := filepath.Join(runtimesDir(), "postject-"+postjectVersion+"-"+postjectSHA256[:12])
	cli := filepath.Join(dest, "package", "dist", "cli.js")
	if _, err := os.Stat(cli); err == nil {
		return cli, nil
	}

	tgz, err := downloadVerified(postjectTarballURL(), postjectSHA256, 5*time.Minute)
	if err != nil {
		return "", fmt.Errorf("postject: %w", err)
	}
	defer os.Remove(tgz)

	// Unpack only after the digest matched: nothing untrusted reaches the
	// filesystem under runtimes/, the same order downloadVerified's own
	// comment describes for Node and Bun.
	f, err := os.Open(tgz)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := extractTarGzTree(f, dest); err != nil {
		os.RemoveAll(dest)
		return "", fmt.Errorf("postject: unpacking failed: %w", err)
	}
	if _, err := os.Stat(cli); err != nil {
		os.RemoveAll(dest)
		return "", fmt.Errorf("postject: %s missing after unpack — the package layout changed", cli)
	}
	return cli, nil
}

// seaFuse is the sentinel Node looks for to know a blob was injected. It is a
// constant of Node's single-executable feature, not a secret.
const seaFuse = "NODE_SEA_FUSE_fce680ab2cc467b6e072b8b5df1996b2"

// tsBuildDir is the scratch directory inside the plugin. `plugin package`
// excludes it.
const tsBuildDir = ".branchkit-build"

// seaLoader is the CommonJS entry of a Node single-executable. Node requires
// that entry to be CommonJS, and a plugin is an ES module — the scaffold ends
// in a top-level `await plugin.run()`, which does not build as CommonJS. So
// the plugin is bundled as ESM, embedded as an asset, and imported from here.
// Authors write the same code for either engine.
const seaLoader = `// Generated by branchkit-cli. Do not edit.
const { getAsset } = require("node:sea");
const source = getAsset("plugin.mjs", "utf8");
import("data:text/javascript;base64," + Buffer.from(source).toString("base64")).catch((err) => {
  process.stderr.write("plugin failed to load: " + (err && err.stack ? err.stack : err) + "\n");
  process.exit(1);
});
`

type tsManifest struct {
	ID       string `json:"id"`
	Run      string `json:"run"`
	Requires struct {
		Sockets *struct {
			Listen []json.RawMessage `json:"listen"`
		} `json:"sockets"`
	} `json:"requires"`
}

// buildTarget is the platform a build is FOR. The zero value is this machine.
// A cross-build writes to dist/<os>-<arch>/ and never touches the host binary
// the running app may be executing.
type buildTarget struct {
	goos, goarch string
}

func hostTarget() buildTarget { return buildTarget{runtime.GOOS, runtime.GOARCH} }

func (t buildTarget) isHost() bool {
	return (t.goos == "" && t.goarch == "") || (t.goos == runtime.GOOS && t.goarch == runtime.GOARCH)
}

func (t buildTarget) resolved() buildTarget {
	if t.goos == "" {
		t.goos = runtime.GOOS
	}
	if t.goarch == "" {
		t.goarch = runtime.GOARCH
	}
	return t
}

func (t buildTarget) String() string { return t.goos + "-" + t.goarch }

// parseBuildTarget validates --os/--arch. `x64` is accepted for `amd64`,
// since that is the label release artifacts and Bun both use.
func parseBuildTarget(goos, goarch string) (buildTarget, error) {
	if goarch == "x64" || goarch == "x86_64" {
		goarch = "amd64"
	}
	if goarch == "aarch64" {
		goarch = "arm64"
	}
	t := buildTarget{goos, goarch}.resolved()
	switch t.goos {
	case "darwin", "linux", "windows":
	default:
		return t, fmt.Errorf("unknown --os %q (darwin, linux, windows)", t.goos)
	}
	switch t.goarch {
	case "amd64", "arm64":
	default:
		return t, fmt.Errorf("unknown --arch %q (amd64, arm64)", t.goarch)
	}
	return t, nil
}

// outputPath is where the built program lands for a target.
func (t buildTarget) outputPath(absDir, base string) string {
	t = t.resolved()
	if t.goos == "windows" {
		base += ".exe"
	}
	if t.isHost() {
		return filepath.Join(absDir, base)
	}
	return filepath.Join(absDir, "dist", t.String(), base)
}

// tsEngine is the whole engine decision.
func tsEngine(m tsManifest) string {
	if m.Requires.Sockets != nil && len(m.Requires.Sockets.Listen) > 0 {
		return "node"
	}
	return "bun"
}

// tsOutputName validates `run` and returns the binary's file name. `run` must
// name a program in the plugin directory; anything else is a manifest from
// before plugins were compiled, and the error says what to write instead.
func tsOutputName(m tsManifest) (string, error) {
	want := "./" + m.ID + "-plugin"
	run := strings.TrimSpace(m.Run)
	words := strings.Fields(run)
	if len(words) != 1 || !strings.HasPrefix(run, "./") || strings.HasSuffix(run, ".sh") ||
		strings.ContainsAny(strings.TrimPrefix(run, "./"), `/\`) {
		return "", fmt.Errorf(
			"plugin.json `run` is %q, but a TypeScript plugin runs as a compiled binary in its own directory.\n"+
				"  Set \"run\": %q and add \"dev\": {\"build\": [[\"branchkit-cli\", \"dev\", \"build\"]], \"build_dir\": \".\"}.\n"+
				"  (A shell wrapper or `bun run …` cannot start under the plugin sandbox.)",
			m.Run, want)
	}
	return strings.TrimPrefix(run, "./"), nil
}

// tsEntry finds the plugin's entry module.
func tsEntry(absDir string) (string, error) {
	for _, rel := range []string{"src/index.ts", "index.ts", "src/main.ts"} {
		if fileExists(filepath.Join(absDir, rel)) {
			return rel, nil
		}
	}
	return "", fmt.Errorf("no entry module found — expected src/index.ts")
}

// bunEnv keeps Bun's package cache under the managed runtimes directory, so a
// build leaves nothing in the author's home directory.
func bunEnv() []string {
	return append(os.Environ(), "BUN_INSTALL_CACHE_DIR="+filepath.Join(runtimesDir(), "bun", "cache"))
}

func runTool(dir string, env []string, program string, args ...string) error {
	cmd := exec.Command(program, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// buildTypeScriptPlugin compiles the plugin in absDir, for this machine, to
// the binary its manifest names.
func buildTypeScriptPlugin(absDir string) error {
	return buildTypeScriptPluginFor(absDir, hostTarget())
}

// buildTypeScriptPluginFor compiles the plugin for target and reports what it
// built.
func buildTypeScriptPluginFor(absDir string, target buildTarget) error {
	target = target.resolved()
	raw, err := os.ReadFile(filepath.Join(absDir, "plugin.json"))
	if err != nil {
		return fmt.Errorf("reading plugin.json: %w", err)
	}
	var m tsManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("parsing plugin.json: %w", err)
	}
	outName, err := tsOutputName(m)
	if err != nil {
		return err
	}
	entry, err := tsEntry(absDir)
	if err != nil {
		return err
	}
	if err := ensureBunRuntime(); err != nil {
		return fmt.Errorf("the managed Bun toolchain is unavailable: %w", err)
	}
	bun := managedBunPath()

	pkgDir := absDir
	if !fileExists(filepath.Join(absDir, "package.json")) && fileExists(filepath.Join(absDir, "src", "package.json")) {
		pkgDir = filepath.Join(absDir, "src")
	}
	if !fileExists(filepath.Join(pkgDir, "node_modules")) {
		fmt.Printf("Installing dependencies for %s...\n", m.ID)
		// A lockfile is the author saying "these versions": install exactly
		// them, so a build reproduces what was tested. A fresh scaffold has
		// none yet, and the plain install writes it.
		install := []string{"install"}
		if fileExists(filepath.Join(pkgDir, "bun.lock")) || fileExists(filepath.Join(pkgDir, "bun.lockb")) {
			install = append(install, "--frozen-lockfile")
		}
		if err := runTool(pkgDir, bunEnv(), bun, install...); err != nil {
			return fmt.Errorf("bun install failed: %w", err)
		}
	}

	engine := tsEngine(m)
	out := target.outputPath(absDir, outName)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	// Build beside the target and rename over it: a running plugin keeps its
	// old inode, and nothing ever observes a half-written binary.
	tmp := filepath.Join(filepath.Dir(out), ".building-"+filepath.Base(out))
	os.Remove(tmp)
	defer os.Remove(tmp)

	forWhom := ""
	if !target.isHost() {
		forWhom = " for " + target.String()
	}
	switch engine {
	case "bun":
		fmt.Printf("Building TypeScript plugin %s%s (Bun %s)...\n", m.ID, forWhom, bunVersion)
		err = buildWithBun(absDir, bun, entry, tmp, target)
	case "node":
		fmt.Printf("Building TypeScript plugin %s%s (Node %s — it declares sockets.listen, which Bun cannot serve)...\n", m.ID, forWhom, nodeVersion)
		err = buildWithNode(absDir, bun, entry, tmp, target)
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, out); err != nil {
		return err
	}
	size := int64(0)
	if info, err := os.Stat(out); err == nil {
		size = info.Size()
	}
	rel, rerr := filepath.Rel(absDir, out)
	if rerr != nil {
		rel = out
	}
	fmt.Printf("Built %s (%s engine, %d MB)\n", rel, engine, size/(1024*1024))
	return nil
}

// buildWithBun compiles a standalone Bun executable.
//
// The two --no-compile-autoload flags are REQUIRED, not tidiness. Without
// them the binary's startup autoload of `.env`/`bunfig.toml` fails inside the
// plugin sandbox and takes `process.env` with it, silently: the plugin runs
// with ZERO environment variables — no plugin id, no BRANCHKIT_PLUGIN_DIR, no
// LISTEN_FDS — and logs nothing about it.
func buildWithBun(absDir, bun, entry, out string, target buildTarget) error {
	args := []string{"build", entry,
		"--compile",
		"--no-compile-autoload-dotenv",
		"--no-compile-autoload-bunfig",
		"--outfile", out}
	if !target.isHost() {
		// Bun fetches the target platform's runtime itself and embeds it.
		arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[target.goarch]
		args = append(args, "--target=bun-"+target.goos+"-"+arch)
	}
	err := runTool(absDir, bunEnv(), bun, args...)
	if err != nil {
		return fmt.Errorf("bun build --compile failed: %w", err)
	}
	return nil
}

// buildWithNode produces a Node single-executable: bundle as ESM, embed it as
// an asset behind the CommonJS loader, generate the blob, and inject it into a
// copy of the managed Node binary.
func buildWithNode(absDir, bun, entry, out string, target buildTarget) error {
	if err := ensureNodeRuntime(); err != nil {
		return fmt.Errorf("the managed Node toolchain is unavailable: %w", err)
	}
	// The blob is always generated by THIS machine's Node. It is portable
	// across platforms as long as the snapshot and code cache are off (the
	// defaults used here) and the Node version matches the binary it is
	// injected into — which it does: both come from the one pin.
	node := managedNodePath()
	hostBinary := node
	if !target.isHost() {
		if target.goos == "darwin" && runtime.GOOS != "darwin" {
			return fmt.Errorf("a Node-engine plugin for macOS must be built on macOS: the injected binary has to be re-signed with codesign")
		}
		cross, err := ensureCrossNode(target.goos, target.goarch)
		if err != nil {
			return fmt.Errorf("the Node binary for %s is unavailable: %w", target, err)
		}
		hostBinary = cross
	}

	work := filepath.Join(absDir, tsBuildDir)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	bundle := filepath.Join(work, "plugin.mjs")
	loader := filepath.Join(work, "main.cjs")
	blob := filepath.Join(work, "sea.blob")
	config := filepath.Join(work, "sea.json")

	// `--conditions=bun`: an SDK installed from source ships `src/` and no
	// `dist/`, and its package `exports` reach `src` only through the `bun`
	// condition. Without it a Node-target build cannot resolve the SDK.
	err := runTool(absDir, bunEnv(), bun, "build", entry,
		"--target=node", "--format=esm", "--conditions=bun", "--outfile", bundle)
	if err != nil {
		return fmt.Errorf("bundling for Node failed: %w", err)
	}
	if err := os.WriteFile(loader, []byte(seaLoader), 0o644); err != nil {
		return err
	}
	cfg, _ := json.Marshal(map[string]any{
		"main":                          loader,
		"output":                        blob,
		"disableExperimentalSEAWarning": true,
		"assets":                        map[string]string{"plugin.mjs": bundle},
	})
	if err := os.WriteFile(config, cfg, 0o644); err != nil {
		return err
	}
	if err := runTool(absDir, os.Environ(), node, "--experimental-sea-config", config); err != nil {
		return fmt.Errorf("generating the single-executable blob failed: %w", err)
	}

	if err := copyFile(hostBinary, out, 0o755); err != nil {
		return fmt.Errorf("copying the Node binary: %w", err)
	}
	// macOS: the copy carries Node's signature, which the injection
	// invalidates. Strip it first and ad-hoc sign after, or the kernel kills
	// the binary at launch.
	if target.goos == "darwin" {
		if err := runTool(absDir, os.Environ(), "codesign", "--remove-signature", out); err != nil {
			return fmt.Errorf("codesign --remove-signature failed: %w", err)
		}
	}
	// The digest-verified local copy, not `bun x postject@<ver>`: the tool
	// that writes our shipped executable is held to the same standard as the
	// runtimes it injects into.
	postjectCLI, err := verifiedPostject()
	if err != nil {
		return err
	}
	inject := []string{postjectCLI, out, "NODE_SEA_BLOB", blob, "--sentinel-fuse", seaFuse}
	if target.goos == "darwin" {
		inject = append(inject, "--macho-segment-name", "NODE_SEA")
	}
	if err := runTool(absDir, bunEnv(), bun, inject...); err != nil {
		return fmt.Errorf("injecting the blob failed: %w", err)
	}
	if target.goos == "darwin" {
		if err := runTool(absDir, os.Environ(), "codesign", "--sign", "-", out); err != nil {
			return fmt.Errorf("codesign failed: %w", err)
		}
	}
	return nil
}

// extractTarGzTree unpacks a gzipped tar into dest, confining every entry to
// it.
//
// Traversal is handled by NEUTRALISING the name, not by detecting it:
// `filepath.Clean("/" + name)` resolves `..` against a virtual root, so
// `../../etc/passwd` becomes `/etc/passwd` and joins to `dest/etc/passwd`.
// Measured 2026-09-21 — an entry named `../escaped.txt` lands at
// `dest/escaped.txt` and the guard below does not fire, which is the correct
// outcome by a different mechanism than the one it looks like.
//
// The explicit check is kept as a second lock rather than removed, because
// the neutralisation above is a property of `filepath` and Windows path
// semantics are not the same as Unix ones (drive-relative names, `\`
// separators). It is expected to be unreachable on Unix; a test asserts the
// neutralisation, which is the behaviour that actually protects the tree.
//
// The digest is checked BEFORE this is called, so the two together mean the
// bytes are the ones we pinned and they land only where we intended.
//
// Only regular files and directories are extracted. A symlink inside the
// archive is skipped rather than followed: postject needs none, and a link
// is the other way an archive reaches outside itself.
func extractTarGzTree(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	clean := filepath.Clean(dest) + string(os.PathSeparator)
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.Clean("/"+hdr.Name))
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), clean) {
			return fmt.Errorf("archive entry %q escapes the destination", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			// Bounded so a crafted archive cannot fill the disk; postject is
			// ~1.4 MB and no single file in it is near this.
			if _, err := io.Copy(f, io.LimitReader(tr, 64<<20)); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
}
