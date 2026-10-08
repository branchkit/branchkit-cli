package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// cmdDevAgents writes the scaffold's agent instructions (AGENTS.md, and a
// CLAUDE.md that imports it) into a plugin that already exists: one made
// before `dev init` wrote them, or by hand. The text is the template's, so
// an old plugin's agent reads what a new one's does.
func cmdDevAgents(args []string) {
	dir := "."
	force := false
	for _, a := range args {
		switch {
		case a == "--force":
			force = true
		case !strings.HasPrefix(a, "-"):
			dir = a
		}
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	written, err := writeAgentInstructions(absDir, force)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	for _, line := range written {
		fmt.Println(line)
	}
}

// writeAgentInstructions renders AGENTS.md for the plugin's language. It
// refuses to replace an AGENTS.md without force, since the author may have
// added to it. CLAUDE.md is written only when absent; an existing one is
// the author's, and gets a suggestion instead of an edit.
func writeAgentInstructions(absDir string, force bool) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(absDir, "plugin.json"))
	if err != nil {
		return nil, fmt.Errorf("no plugin.json found in %s", absDir)
	}
	var m struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.ID == "" {
		return nil, fmt.Errorf("plugin.json has no id")
	}
	lang, fsys := pluginLanguage(absDir)
	if lang == "" {
		return nil, fmt.Errorf("cannot tell the plugin's language: expected src/go.mod, package.json, or main.py")
	}

	agents := filepath.Join(absDir, "AGENTS.md")
	if fileExists(agents) && !force {
		return nil, fmt.Errorf("%s exists; pass --force to replace it", agents)
	}
	src, err := fsys.ReadFile("templates/" + lang + "/AGENTS.md.tmpl")
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("AGENTS.md").Parse(string(src))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, templateData{PluginID: m.ID}); err != nil {
		return nil, err
	}
	if err := os.WriteFile(agents, out.Bytes(), 0o644); err != nil {
		return nil, err
	}
	written := []string{"Wrote AGENTS.md (" + lang + ")"}

	claude := filepath.Join(absDir, "CLAUDE.md")
	existing, err := os.ReadFile(claude)
	switch {
	case os.IsNotExist(err):
		if err := os.WriteFile(claude, []byte("@AGENTS.md\n"), 0o644); err != nil {
			return nil, err
		}
		written = append(written, "Wrote CLAUDE.md (imports AGENTS.md)")
	case err != nil:
		return nil, err
	case !strings.Contains(string(existing), "@AGENTS.md"):
		written = append(written, "CLAUDE.md exists and was left alone; add the line @AGENTS.md to it so Claude Code reads AGENTS.md")
	}
	return written, nil
}

// pluginLanguage reads the language from the source layout, by the same
// rules `dev build` uses, and returns that template's files.
func pluginLanguage(absDir string) (string, embed.FS) {
	switch {
	case fileExists(filepath.Join(absDir, "src", "go.mod")):
		return "go", goTemplateFS
	case fileExists(filepath.Join(absDir, "src", "package.json")) || fileExists(filepath.Join(absDir, "package.json")):
		return "ts", tsTemplateFS
	case fileExists(filepath.Join(absDir, "main.py")):
		return "py", pyTemplateFS
	}
	return "", embed.FS{}
}
