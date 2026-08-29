package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// installAssumeYes suppresses the interactive consent question (--yes). The
// settings-driven install passes it — that path shows its own consent panel
// before ever invoking the CLI.
var installAssumeYes bool

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// confirmInstall shows what the plugin will be able to do and — when a human
// is at the terminal — asks before anything lands on disk. The disclosure
// always prints; the question is TTY-only, so scripted installs and the
// settings path never hang on a prompt nobody can answer. Granting stays in
// Settings (the spawn gate holds unapproved plugins); this is the moment
// that guarantees nothing installs unseen.
func confirmInstall(manifest PluginManifest, in io.Reader, interactive bool) error {
	fmt.Printf("\n%s v%s will be able to:\n", manifest.Name, manifest.Version)
	printConsentSummary(manifest)
	if !interactive {
		fmt.Println()
		return nil
	}
	fmt.Print("Continue? [y/N] ")
	sc := bufio.NewScanner(in)
	if !sc.Scan() {
		return fmt.Errorf("install declined")
	}
	switch strings.ToLower(strings.TrimSpace(sc.Text())) {
	case "y", "yes":
		return nil
	default:
		return fmt.Errorf("install declined")
	}
}
