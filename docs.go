package main

// branchkit-cli docs — resolve the platform documentation that ships with the
// installed app.
//
// Why local rather than the website (docs/design/DESIGN_AGENT_AUTHORING_SURFACE.md):
// an agent authoring a plugin can grep a directory. Grep is exact, costs
// nothing, and supports refinement; fetching a site costs a request per page
// and requires already knowing which page you want, which is precisely what
// you do not know when you are stuck. Local docs also match the installed
// app's API version, while a website is always latest.
//
// The docs are copied into the app bundle by `just bundle`. `docs sync` copies
// them to the app support dir so the path stays stable when the app moves or
// updates, and stamps the copy so staleness is visible rather than silent.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// docsCacheDir is the synced location — stable across app updates and moves.
func docsCacheDir() string {
	return filepath.Join(appSupportDir(), "docs")
}

// bundledDocsDir locates the read-only copy inside the installed app.
// Returns "" when no bundle is found (CLI built from source, app not
// installed).
func bundledDocsDir() string {
	var candidates []string

	// The CLI normally runs from inside the bundle, where docs are a sibling
	// (Contents/Resources/branchkit-cli and Contents/Resources/docs).
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "docs"))
	}

	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/Applications/BranchKit.app/Contents/Resources/docs",
			filepath.Join(os.Getenv("HOME"), "Applications/BranchKit.app/Contents/Resources/docs"),
		)
	}

	for _, dir := range candidates {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

// resolveDocsDir returns the docs directory an agent should read, preferring
// an explicit override, then the synced cache, then the bundle.
func resolveDocsDir() (dir string, source string) {
	if override := os.Getenv("BRANCHKIT_DOCS_DIR"); override != "" {
		return override, "BRANCHKIT_DOCS_DIR"
	}
	if cache := docsCacheDir(); dirHasContent(cache) {
		return cache, "synced"
	}
	if bundled := bundledDocsDir(); bundled != "" {
		return bundled, "bundled"
	}
	return "", ""
}

func dirHasContent(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

func cmdDocs(args []string) {
	if len(args) == 0 {
		printDocsUsage()
		os.Exit(1)
	}
	switch args[0] {
	case "path":
		cmdDocsPath()
	case "sync":
		cmdDocsSync()
	default:
		fmt.Fprintf(os.Stderr, "Unknown docs command: %s\n", args[0])
		printDocsUsage()
		os.Exit(1)
	}
}

func cmdDocsPath() {
	dir, source := resolveDocsDir()
	if dir == "" {
		fmt.Fprintln(os.Stderr, "No platform docs found.")
		fmt.Fprintln(os.Stderr, "They ship with the app; install BranchKit, or set BRANCHKIT_DOCS_DIR.")
		os.Exit(1)
	}
	fmt.Println(dir)
	if stamp := readDocsStamp(dir); stamp != "" {
		fmt.Fprintf(os.Stderr, "(%s %s)\n", source, stamp)
	} else {
		fmt.Fprintf(os.Stderr, "(%s)\n", source)
	}
}

func cmdDocsSync() {
	src := bundledDocsDir()
	if src == "" {
		fmt.Fprintln(os.Stderr, "No bundled docs found — is BranchKit installed?")
		os.Exit(1)
	}
	dest := docsCacheDir()
	if err := os.RemoveAll(dest); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to clear %s: %v\n", dest, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create %s: %v\n", dest, err)
		os.Exit(1)
	}
	if err := safeCopyDir(src, dest, 0); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to copy docs: %v\n", err)
		os.Exit(1)
	}
	stamp := time.Now().UTC().Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(dest, "SYNCED"), []byte(stamp+"\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to stamp docs: %v\n", err)
		os.Exit(1)
	}
	count := countMarkdown(dest)
	fmt.Printf("Synced %d pages to %s\n", count, dest)
}

func readDocsStamp(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "SYNCED"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func countMarkdown(dir string) int {
	count := 0
	filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".md") {
			count++
		}
		return nil
	})
	return count
}

func printDocsUsage() {
	fmt.Println("Usage: branchkit-cli docs <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  path   Print the platform docs directory (grep it — it is markdown)")
	fmt.Println("  sync   Copy the app's bundled docs to a stable path that survives app updates")
}
