package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every template gives a coding agent its instructions in the place each
// agent looks: AGENTS.md for most, CLAUDE.md (an import of it) for Claude
// Code. A template that drops them leaves that language's authors' agents
// guessing APIs, which is what the files exist to prevent.
var devSubcommandRe = regexp.MustCompile(`branchkit-cli dev ([a-z-]+)`)

func TestEveryScaffoldShipsAgentInstructions(t *testing.T) {
	mainSrc, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	data := templateData{PluginID: "my-plugin", PluginName: "My Plugin", Description: "d", ActionPrefix: "myplugin", Phrase: "hello", Keybind: "alt+shift+h"}
	for name, scaffold := range map[string]func(string, templateData) error{
		"go": scaffoldGoPlugin, "ts": scaffoldTSPlugin, "py": scaffoldPyPlugin,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := scaffold(dir, data); err != nil {
				t.Fatal(err)
			}
			agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
			if err != nil {
				t.Fatal(err)
			}
			s := string(agents)
			for _, want := range []string{"`my-plugin`", "plugin.my-plugin.<name>", "branchkit-cli docs path", "branchkit-cli dev test .",
				"branchkit-cli dev diagnose my-plugin", `dev say "<phrase>" --simulate`, "branchkit-cli dev chain <tr_id>", "branchkit-cli dev plog my-plugin --since 60s"} {
				if !strings.Contains(s, want) {
					t.Errorf("AGENTS.md lacks %q", want)
				}
			}
			if strings.Contains(s, "{{") {
				t.Error("AGENTS.md has an unrendered template action")
			}
			// Every dev command the instructions teach must be one the CLI has:
			// an agent that runs a command that does not exist stops trusting
			// the rest of the file.
			for _, sub := range devSubcommandRe.FindAllStringSubmatch(s, -1) {
				if !strings.Contains(string(mainSrc), "case \""+sub[1]+"\":") {
					t.Errorf("AGENTS.md teaches `branchkit-cli dev %s`, which main.go does not dispatch", sub[1])
				}
			}
			claude, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(claude)) != "@AGENTS.md" {
				t.Errorf("CLAUDE.md should import AGENTS.md, got %q", claude)
			}
		})
	}
}

func TestDevAgentsWritesInstructionsIntoAnExistingPlugin(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"old-plugin"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeAgentInstructions(dir, false); err != nil {
		t.Fatal(err)
	}
	agents, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if !strings.Contains(string(agents), "(`old-plugin`, Python)") {
		t.Errorf("AGENTS.md should be the Python template for old-plugin:\n%.200s", agents)
	}
	if claude, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md")); string(claude) != "@AGENTS.md\n" {
		t.Errorf("CLAUDE.md = %q", claude)
	}
	if _, err := writeAgentInstructions(dir, false); err == nil {
		t.Error("a second run must not replace AGENTS.md without --force")
	}

	// An author's own CLAUDE.md is theirs: suggested to, never edited.
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("my notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := writeAgentInstructions(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if claude, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md")); string(claude) != "my notes\n" {
		t.Errorf("an existing CLAUDE.md was changed: %q", claude)
	}
	if !strings.Contains(strings.Join(out, "\n"), "add the line @AGENTS.md") {
		t.Errorf("expected a suggestion to import AGENTS.md, got %v", out)
	}
}
