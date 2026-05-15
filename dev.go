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
var goTemplateFS embed.FS

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
		tmpl = promptInput("Template", "go")
	}
	if tmpl != "go" {
		fmt.Fprintf(os.Stderr, "Error: only 'go' template is supported currently\n")
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

	if err := scaffoldGoPlugin(name, data); err != nil {
		os.RemoveAll(name)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = filepath.Join(name, "src")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(name)
		fmt.Fprintf(os.Stderr, "Error: go mod tidy failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nCreated plugin %s/ (Go template)\n", name)
	fmt.Println()
	fmt.Println("  Next steps:")
	fmt.Printf("    cd %s\n", name)
	fmt.Println("    branchkit dev build")
	fmt.Println("    branchkit dev test .")
	fmt.Println("    branchkit plugin install . --build")
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

	goMod := fmt.Sprintf("module github.com/you/%s\n\ngo 1.24\n\nrequire github.com/branchkit/plugin-sdk-go v0.2.0\n", data.PluginID)
	if err := os.WriteFile(filepath.Join(dir, "src", "go.mod"), []byte(goMod), 0o644); err != nil {
		return fmt.Errorf("write go.mod: %w", err)
	}

	return nil
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
