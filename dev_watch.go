package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// cmdDevWatch watches a plugin's sources and asks the running actuator to
// rebuild and restart it on change.
//
// It does NOT build anything itself. It used to — `go build` when it saw a
// src/go.mod, `bun install` when it saw a package.json — which made this a
// third place in the tree carrying per-language build knowledge, alongside
// the dev endpoint and the Justfile. The endpoint now runs the plugin's own
// declared `dev.build`, so every language works here for free and this file
// no longer has an opinion about toolchains.
func cmdDevWatch(args []string) {
	dir := "."
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			dir = a
		}
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	manifestPath := filepath.Join(absDir, "plugin.json")
	if _, err := os.Stat(manifestPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: no plugin.json found in %s\n", absDir)
		os.Exit(1)
	}

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading plugin.json: %v\n", err)
		os.Exit(1)
	}
	var manifest struct {
		ID  string `json:"id"`
		Run string `json:"run"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing plugin.json: %v\n", err)
		os.Exit(1)
	}

	// Where the running app loads this plugin from. `plugin install .`
	// COPIES the directory into the plugins folder, so a build here is
	// invisible to the app until it is copied across; a symlinked install
	// (a development checkout) is this directory itself.
	installed := installedCopy(manifest.ID, absDir)

	// An interpreted plugin has nothing to build, and for a linked install
	// the running app's file watcher already restarts it on save. Watching
	// here would just be a slower second copy of that, so say so and stop.
	// An installed COPY is not watched by the app, so the loop below copies
	// and restarts it.
	if installed == "" && manifest.Run != "" && !isCompiledRun(absDir, manifest.Run) {
		fmt.Printf("%s runs under an interpreter (`%s`), so there is nothing to build.\n",
			manifest.ID, manifest.Run)
		fmt.Println("Just save your file — the running app watches this plugin's source and restarts it.")
		return
	}

	// A Developer Access grant is per plugin: use this plugin's.
	devAccessPrefer = manifest.ID

	fmt.Printf("Watching %s for changes (Ctrl+C to stop)...\n", manifest.ID)
	if installed != "" {
		fmt.Printf("Installed copy: %s — each change is built here, copied there, and the plugin restarted.\n", installed)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Stamped after each rebuild, never before: a build step may rewrite
	// sources (templ regenerates *_templ.go), and treating those as a fresh
	// edit is a rebuild loop that never settles.
	since := time.Now()

	for {
		select {
		case <-sigCh:
			fmt.Println("\nStopped watching.")
			return
		case <-ticker.C:
			if !hasChanges(absDir, since) {
				continue
			}

			fmt.Printf("\nChange detected — rebuilding %s...\n", manifest.ID)
			token := readHostToken()
			if token == "" {
				fmt.Println("No credentials — is the app running? Turn on Developer Access on the plugin's card, or pass --dev to target a development build.")
				since = time.Now()
				continue
			}

			// Scoped token, or an installed copy: the app won't build this
			// directory for us. Run the local build first — the same code
			// `dev build` runs, for every language — then copy it into the
			// installed plugin if that is a copy, and the reload below
			// becomes a restart of what is now on disk.
			var ok, manifestReloaded bool
			if devAccessScope != "" || installed != "" {
				if err := buildPluginDir(absDir, hostTarget()); err != nil {
					fmt.Printf("Local build failed: %v\n", err)
					since = time.Now()
					continue
				}
				if installed != "" {
					changed, err := syncInstalledCopy(absDir, installed)
					if err != nil {
						fmt.Println(err)
						since = time.Now()
						continue
					}
					// The app's own watcher reloads a changed plugin.json
					// (collections, commands) after a short debounce; the
					// restart spawns from the manifest it holds, so let
					// that land first.
					if changed {
						manifestReloaded = true
						time.Sleep(manifestSettle)
					}
				}
				ok = restartViaEndpoint(manifest.ID, token)
			} else {
				ok, manifestReloaded = reloadViaEndpoint(manifest.ID, token)
			}
			switch {
			case ok && manifestReloaded:
				fmt.Printf("Rebuilt and reloaded %s (manifest changes applied).\n", manifest.ID)
			case ok:
				fmt.Printf("Rebuilt and reloaded %s.\n", manifest.ID)
			default:
				fmt.Println("Watching for next change...")
			}
			since = time.Now()
		}
	}
}

// isCompiledRun reports whether the run command's program is a file inside
// the plugin directory — the same derivation the actuator's file watcher
// uses to tell a compiled plugin from an interpreted one, with no list of
// languages on either side.
func isCompiledRun(pluginDir, runCmd string) bool {
	fields := strings.Fields(runCmd)
	if len(fields) == 0 {
		return false
	}
	program := strings.TrimPrefix(fields[0], "./")
	info, err := os.Stat(filepath.Join(pluginDir, program))
	return err == nil && !info.IsDir()
}

// hasChanges reports whether any source or manifest file under the plugin
// directory was modified after `since`. Walks the tree rather than one
// flat src/, so a plugin that keeps sources anywhere is covered.
func hasChanges(pluginDir string, since time.Time) bool {
	found := false
	_ = filepath.Walk(pluginDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found {
			return nil
		}
		if !watchedSource(info.Name()) {
			return nil
		}
		if info.ModTime().After(since) {
			found = true
		}
		return nil
	})
	return found
}

func watchedSource(name string) bool {
	for _, ext := range []string{".go", ".templ", ".rs", ".ts", ".js", ".py", ".html", ".css", ".json"} {
		if strings.HasSuffix(name, ext) {
			// connect.json is written by the plugin's own listener at
			// runtime; treating it as an edit would rebuild on every start.
			return name != "connect.json"
		}
	}
	return false
}

func readHostToken() string {
	if err := resolveDevBaseURL(); err != nil {
		addressWarnOnce.Do(func() { fmt.Fprintf(os.Stderr, "%v\n", err) })
		return ""
	}
	path := filepath.Join(appSupportDir(), "host.token")
	data, err := os.ReadFile(path)
	if err != nil {
		// No host token — not a dev build. A production install reaches
		// the app through a per-plugin Developer Access grant instead
		// (a scoped, revocable per-plugin grant the user flips on the
		// plugin's Settings card): the app writes
		// dev-access/<plugin-id>.json with {plugin_id, port, token} when
		// the user flips the toggle. Scoped: the server answers only for
		// that plugin, so which file we pick matters only when several
		// grants exist — take the first, deterministic by name.
		return readDevAccessToken()
	}
	return strings.TrimSpace(string(data))
}

// devAccessScope is set alongside readDevAccessToken's result: the plugin
// id a scoped token answers for. Empty when running on a host token.
var devAccessScope string

// devAccessPrefer names the plugin whose Developer Access grant
// readDevAccessToken tries first. Empty: the first grant by name.
var devAccessPrefer string
var addressWarnOnce sync.Once

// readDevAccessToken resolves the first Developer Access discovery file,
// rewires devBaseURL to the app's real port, and returns the scoped token.
// Empty string when no grant exists.
func readDevAccessToken() string {
	dir := filepath.Join(appSupportDir(), "dev-access")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".json") && n != "enabled.json" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	// A caller that knows its plugin tries that plugin's grant first: a
	// token scoped to another plugin is refused for this one.
	if devAccessPrefer != "" {
		want := devAccessPrefer + ".json"
		for i, n := range names {
			if n == want {
				names = append([]string{n}, append(names[:i:i], names[i+1:]...)...)
				break
			}
		}
	}
	for _, n := range names {
		raw, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		var d struct {
			PluginID string `json:"plugin_id"`
			Token    string `json:"token"`
		}
		if json.Unmarshal(raw, &d) != nil || d.Token == "" {
			continue
		}
		// The port comes from the address file (resolveDevBaseURL); this
		// file carries the grant only.
		devAccessScope = d.PluginID
		fmt.Fprintf(os.Stderr, "(developer access: scoped to plugin '%s' via %s)\n", d.PluginID, n)
		return d.Token
	}
	return ""
}

func reloadViaEndpoint(pluginID, token string) (ok, manifestReloaded bool) {
	// Scoped token (production install): the app will not run builds for
	// us — `rebuild` is dev-build-only because it executes the manifest's
	// build command unsandboxed. Build locally (`branchkit-cli dev build`)
	// and ask the app only to RESTART, reloading what is on disk.
	if devAccessScope != "" {
		return restartViaEndpoint(pluginID, token), false
	}
	client := devClient(60 * time.Second)
	url := fmt.Sprintf("%s/dev/plugins/%s/rebuild", devBaseURL, pluginID)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return false, false
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		OK               bool     `json:"ok"`
		Error            string   `json:"error"`
		Output           string   `json:"output"`
		ManifestReloaded bool     `json:"manifest_reloaded"`
		Warnings         []string `json:"warnings"`
	}
	if json.Unmarshal(body, &result) == nil && result.OK {
		// Warns don't block the reload, but they must reach the terminal
		// the author is watching — the reload's own warnings plus any
		// cross-plugin warnings the edit introduced.
		for _, w := range result.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
		return true, result.ManifestReloaded
	}
	if result.Error != "" {
		fmt.Fprintf(os.Stderr, "Reload error: %s\n", result.Error)
	}
	// Compiler output is the whole point of a watch loop — without it the
	// author sees "build failed" and has to go find the actual error.
	if result.Output != "" {
		fmt.Fprintln(os.Stderr, strings.TrimSpace(result.Output))
	}
	return false, false
}

// restartViaEndpoint reloads a plugin's binary + manifest without asking the
// app to build anything — the scoped-token half of the watch loop.
func restartViaEndpoint(pluginID, token string) bool {
	client := devClient(30 * time.Second)
	url := fmt.Sprintf("%s/dev/plugins/%s/restart", devBaseURL, pluginID)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// installedCopy returns the directory the app loads plugin id from when that
// directory is a COPY of srcDir — what `branchkit-cli plugin install .`
// makes. It returns "" when the installed plugin is srcDir itself or a
// symlink to it (a linked development install), and when the plugin is not
// installed in the plugins folder at all.
func installedCopy(id, srcDir string) string {
	dst := filepath.Join(userPluginsDir(), id)
	info, err := os.Lstat(dst)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ""
	}
	a, errA := filepath.EvalSymlinks(dst)
	b, errB := filepath.EvalSymlinks(srcDir)
	if errA != nil || errB != nil || a == b {
		return ""
	}
	return dst
}

// syncInstalledCopy copies a rebuilt plugin over its installed copy. It
// refuses when the manifest now asks for more than the installed one did
// (a new privilege, effect, host, or run command): that change goes through
// `plugin install`, where it is shown before it lands.
//
// changed reports whether plugin.json differed from the installed one.
func syncInstalledCopy(srcDir, dst string) (changed bool, err error) {
	newM, err := readManifest(filepath.Join(srcDir, "plugin.json"))
	if err != nil {
		return false, err
	}
	if oldM, err := readManifest(filepath.Join(dst, "plugin.json")); err == nil {
		if diffConsent(oldM, newM).expands() {
			return false, fmt.Errorf("plugin.json now requests more than the installed copy — run `branchkit-cli plugin install .` to review the change, then save again")
		}
	}
	before, _ := os.ReadFile(filepath.Join(dst, "plugin.json"))
	after, _ := os.ReadFile(filepath.Join(srcDir, "plugin.json"))
	if err := safeCopyDir(srcDir, dst, 0); err != nil {
		return false, fmt.Errorf("copying into %s: %w", dst, err)
	}
	if newM.Run != "" {
		setExecutable(dst, newM.Run)
	}
	return !bytes.Equal(before, after), nil
}

// manifestSettle is how long dev watch waits after copying a changed
// plugin.json before restarting: longer than the app's file-watcher
// debounce (500 ms).
const manifestSettle = 1500 * time.Millisecond
