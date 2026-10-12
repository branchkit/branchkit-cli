package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// crossesPowerLine reports whether a plugin's first listing, or an update
// that newly crosses it, waits for a person at BranchKit, and why. The line:
// any privilege that acts on the computer, or one that reads something
// sensitive together with somewhere to send it (network beyond localhost).
// The app's privileges::crosses_power_line holds the same rule; both read
// each privilege's reach from the app's privilege registry.
func crossesPowerLine(m PluginManifest) (bool, []string) {
	networked := false
	for _, member := range networkSet(m) {
		switch member {
		case "preset:localhost", "localhost", "127.0.0.1":
		default:
			networked = true
		}
	}
	reasons := []string{}
	privs := append([]string(nil), m.Requires.Privileges...)
	sort.Strings(privs)
	for _, p := range privs {
		switch vocabulary.PrivilegeReach[p] {
		case "acts":
			reasons = append(reasons, p+": acts on the computer")
		case "reads":
			if networked {
				reasons = append(reasons, p+": reads something sensitive, with network access")
			}
		case "neither":
		default:
			// Not in the registry: a person looks rather than the line
			// letting an unknown name through.
			reasons = append(reasons, p+": not a privilege this CLI knows")
		}
	}
	return len(reasons) > 0, reasons
}

// cmdRegistryPowerLine prints, as JSON, whether the manifest at path crosses
// the power line and why. The registry's checks run it on each release.
func cmdRegistryPowerLine(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: branchkit-cli registry power-line <plugin.json>")
		os.Exit(1)
	}
	m, err := readManifest(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	crosses, reasons := crossesPowerLine(m)
	data, _ := json.Marshal(struct {
		ID      string   `json:"id"`
		Crosses bool     `json:"crosses"`
		Reasons []string `json:"reasons"`
	}{m.ID, crosses, reasons})
	fmt.Println(string(data))
}
