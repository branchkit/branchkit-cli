package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed templates/go/*
//go:embed templates/go/src/*
//go:embed templates/go/.github/workflows/*
var goTemplateFS embed.FS

//go:embed templates/ts/*
//go:embed templates/ts/src/*
//go:embed templates/ts/.github/workflows/*
var tsTemplateFS embed.FS

type templateData struct {
	PluginID     string
	PluginName   string
	Description  string
	ActionPrefix string
}

func cmdDevInit(args []string) {
	var name, tmpl, desc string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i+1 < len(args) {
				i++
				name = args[i]
			}
		case "--template":
			if i+1 < len(args) {
				i++
				tmpl = args[i]
			}
		case "--description":
			if i+1 < len(args) {
				i++
				desc = args[i]
			}
		}
	}

	if name == "" {
		name = promptInput("Plugin name (lowercase, hyphens ok)", "my-plugin")
	}
	if !validateID(name) {
		fmt.Fprintf(os.Stderr, "Error: plugin name must be lowercase alphanumeric with hyphens (got %q)\n", name)
		os.Exit(1)
	}

	if tmpl == "" {
		tmpl = promptInput("Template (go or ts)", "go")
	}
	if tmpl != "go" && tmpl != "ts" {
		fmt.Fprintf(os.Stderr, "Error: template must be 'go' or 'ts' (got %q)\n", tmpl)
		os.Exit(1)
	}

	if desc == "" {
		desc = promptInput("Description", "A BranchKit plugin")
	}

	if _, err := os.Stat(name); err == nil {
		fmt.Fprintf(os.Stderr, "Error: directory %q already exists\n", name)
		os.Exit(1)
	}

	data := templateData{
		PluginID:     name,
		PluginName:   toTitleCase(name),
		Description:  desc,
		ActionPrefix: strings.ReplaceAll(name, "-", ""),
	}

	switch tmpl {
	case "go":
		if err := scaffoldGoPlugin(name, data); err != nil {
			os.RemoveAll(name)
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		srcDir := filepath.Join(name, "src")
		// Resolve the SDK to whatever is newest, rather than to a version
		// baked into this binary months ago.
		get := exec.Command("go", "get", "github.com/branchkit/plugin-sdk-go@latest")
		get.Dir = srcDir
		get.Stdout = os.Stdout
		get.Stderr = os.Stderr
		if err := get.Run(); err != nil {
			os.RemoveAll(name)
			fmt.Fprintf(os.Stderr, "Error: resolving plugin-sdk-go failed: %v\n", err)
			os.Exit(1)
		}

		cmd := exec.Command("go", "mod", "tidy")
		cmd.Dir = srcDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.RemoveAll(name)
			fmt.Fprintf(os.Stderr, "Error: go mod tidy failed: %v\n", err)
			os.Exit(1)
		}

	case "ts":
		if err := scaffoldTSPlugin(name, data); err != nil {
			os.RemoveAll(name)
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if err := os.Chmod(filepath.Join(name, "run.sh"), 0o755); err != nil {
			os.RemoveAll(name)
			fmt.Fprintf(os.Stderr, "Error: chmod run.sh: %v\n", err)
			os.Exit(1)
		}

		bunPath := "bun"
		if managed := managedBunPath(); fileExists(managed) {
			bunPath = managed
		}
		cmd := exec.Command(bunPath, "install")
		cmd.Dir = name
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.RemoveAll(name)
			fmt.Fprintf(os.Stderr, "Error: bun install failed: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("\nCreated plugin %s/ (%s template)\n", name, tmpl)
	fmt.Println()
	fmt.Println("  Next steps:")
	fmt.Printf("    cd %s\n", name)
	fmt.Println("    branchkit-cli dev build")
	fmt.Println("    branchkit-cli dev test .")
	fmt.Println("    branchkit-cli plugin install . --build")
	fmt.Println()
}

func scaffoldGoPlugin(dir string, data templateData) error {
	templateFiles := []struct {
		src  string
		dest string
	}{
		{"templates/go/plugin.json.tmpl", "plugin.json"},
		{"templates/go/commands.json.tmpl", "commands.json"},
		{"templates/go/src/main.go.tmpl", "src/main.go"},
		{"templates/go/src/actions_gen.go.tmpl", "src/actions_gen.go"},
		{"templates/go/src/main_test.go.tmpl", "src/main_test.go"},
		{"templates/go/README.md.tmpl", "README.md"},
		{"templates/go/.github/workflows/conformance.yml.tmpl", ".github/workflows/conformance.yml"},
	}

	for _, tf := range templateFiles {
		destPath := filepath.Join(dir, tf.dest)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(destPath), err)
		}

		content, err := goTemplateFS.ReadFile(tf.src)
		if err != nil {
			return fmt.Errorf("read template %s: %w", tf.src, err)
		}

		tmpl, err := template.New(tf.src).Parse(string(content))
		if err != nil {
			return fmt.Errorf("parse template %s: %w", tf.src, err)
		}

		f, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("create %s: %w", destPath, err)
		}

		if err := tmpl.Execute(f, data); err != nil {
			f.Close()
			return fmt.Errorf("execute template %s: %w", tf.src, err)
		}
		f.Close()
	}

	// No SDK version pinned here on purpose. `dev init` resolves it with
	// `go get @latest` below, so a scaffolded plugin always starts on the
	// newest published SDK — a hardcoded version silently ages, and Go's
	// resolver prefers a stale tag over the branch, so the mistake is
	// invisible until an author wonders why a documented API is missing.
	goMod := fmt.Sprintf("module github.com/you/%s\n\ngo 1.24\n", data.PluginID)
	if err := os.WriteFile(filepath.Join(dir, "src", "go.mod"), []byte(goMod), 0o644); err != nil {
		return fmt.Errorf("write go.mod: %w", err)
	}

	return nil
}

func scaffoldTSPlugin(dir string, data templateData) error {
	templateFiles := []struct {
		src  string
		dest string
	}{
		{"templates/ts/plugin.json.tmpl", "plugin.json"},
		{"templates/ts/commands.json.tmpl", "commands.json"},
		{"templates/ts/run.sh.tmpl", "run.sh"},
		{"templates/ts/package.json.tmpl", "package.json"},
		{"templates/ts/README.md.tmpl", "README.md"},
		{"templates/ts/src/index.ts.tmpl", "src/index.ts"},
		{"templates/ts/src/index.test.ts.tmpl", "src/index.test.ts"},
		{"templates/ts/.github/workflows/conformance.yml.tmpl", ".github/workflows/conformance.yml"},
	}

	for _, tf := range templateFiles {
		destPath := filepath.Join(dir, tf.dest)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(destPath), err)
		}

		content, err := tsTemplateFS.ReadFile(tf.src)
		if err != nil {
			return fmt.Errorf("read template %s: %w", tf.src, err)
		}

		tmpl, err := template.New(tf.src).Parse(string(content))
		if err != nil {
			return fmt.Errorf("parse template %s: %w", tf.src, err)
		}

		f, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("create %s: %w", destPath, err)
		}

		if err := tmpl.Execute(f, data); err != nil {
			f.Close()
			return fmt.Errorf("execute template %s: %w", tf.src, err)
		}
		f.Close()
	}

	return nil
}

func cmdDevTest(args []string) {
	dir := "."
	jsonOutput := false
	staticOnly := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "--static-only":
			staticOnly = true
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

	if _, err := os.Stat(filepath.Join(absDir, "plugin.json")); err != nil {
		fmt.Fprintf(os.Stderr, "Error: no plugin.json found in %s\n", absDir)
		os.Exit(1)
	}

	phase := runStaticAnalysis(absDir)
	failures := printTestResults(phase, jsonOutput)

	if !staticOnly {
		harnessFailures := runHarnessConformance(absDir, jsonOutput)
		failures += harnessFailures
	}

	if failures > 0 {
		fmt.Fprintf(os.Stderr, "%d test(s) failed\n", failures)
		os.Exit(1)
	}
	if !jsonOutput {
		fmt.Println("All tests passed")
	}
}

func runHarnessConformance(dir string, jsonOutput bool) int {
	binary := findHarnessBinary()
	if binary == "" {
		if !jsonOutput {
			fmt.Println("Skipping conformance tests (branchkit-test-harness not found)")
			fmt.Println("  Install: cargo build -p branchkit-test-harness")
		}
		return 0
	}

	harnessArgs := []string{dir}
	if jsonOutput {
		harnessArgs = append(harnessArgs, "--json")
	}

	cmd := exec.Command(binary, harnessArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "Error running harness: %v\n", err)
		return 1
	}
	return 0
}

func findHarnessBinary() string {
	if env := os.Getenv("BRANCHKIT_TEST_HARNESS"); env != "" {
		return env
	}

	candidates := []string{
		"target/debug/branchkit-test-harness",
		"target/release/branchkit-test-harness",
		"../target/debug/branchkit-test-harness",
		"../target/release/branchkit-test-harness",
	}

	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if fileExists(abs) {
			return abs
		}
	}

	if p, err := exec.LookPath("branchkit-test-harness"); err == nil {
		return p
	}

	return ""
}

func cmdDevBuild(args []string) {
	dir := "."
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		dir = args[0]
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

	srcDir := filepath.Join(absDir, "src")

	switch {
	case fileExists(filepath.Join(srcDir, "go.mod")):
		binaryName := manifest.ID + "-plugin"
		if manifest.Run != "" {
			binaryName = strings.TrimPrefix(manifest.Run, "./")
		}
		outputPath := filepath.Join(absDir, binaryName)

		fmt.Printf("Building Go plugin %s...\n", manifest.ID)
		cmd := exec.Command("go", "build", "-o", outputPath, ".")
		cmd.Dir = srcDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Build failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Built %s\n", binaryName)

	case fileExists(filepath.Join(srcDir, "package.json")) || fileExists(filepath.Join(absDir, "package.json")):
		pkgDir := srcDir
		if !fileExists(filepath.Join(srcDir, "package.json")) {
			pkgDir = absDir
		}

		bunPath := "bun"
		if managed := managedBunPath(); fileExists(managed) {
			bunPath = managed
		}

		if _, err := exec.LookPath(bunPath); err != nil && bunPath == "bun" {
			fmt.Fprintf(os.Stderr, "Error: bun not found. Install it: https://bun.sh\n")
			os.Exit(1)
		}

		if !fileExists(filepath.Join(pkgDir, "node_modules")) {
			fmt.Printf("Installing dependencies for %s...\n", manifest.ID)
			cmd := exec.Command(bunPath, "install")
			cmd.Dir = pkgDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "bun install failed: %v\n", err)
				os.Exit(1)
			}
		}
		fmt.Printf("TypeScript plugin %s is ready (no build step needed)\n", manifest.ID)

	default:
		fmt.Fprintf(os.Stderr, "Error: unknown build system — expected go.mod in src/ or package.json\n")
		os.Exit(1)
	}
}

func promptInput(label, defaultVal string) string {
	fmt.Printf("%s [%s]: ", label, defaultVal)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text != "" {
			return text
		}
	}
	return defaultVal
}

func toTitleCase(s string) string {
	words := strings.Split(s, "-")
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
