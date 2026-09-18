//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// linkPluginDir installs dir at link. A symlink needs SeCreateSymbolicLink,
// which a standard account holds only with Developer Mode on — the first
// Windows trial session (2026-09-18) failed every install with "A required
// privilege is not held by the client". A directory JUNCTION needs no
// privilege, the actuator walks it like any directory, and os.Remove
// deletes the junction without touching the target, so it keeps the
// install-by-link semantics the trial wants. Symlink first, junction on
// the privilege error.
func linkPluginDir(dir, link string) error {
	err := os.Symlink(dir, link)
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "privilege") {
		return err
	}
	out, jerr := exec.Command("cmd", "/c", "mklink", "/J", link, dir).CombinedOutput()
	if jerr != nil {
		return fmt.Errorf("symlink: %v; junction fallback: %v (%s)", err, jerr, strings.TrimSpace(string(out)))
	}
	return nil
}
