package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// powerLineAxes places every consent axis (consentAxes, the app's registry)
// on the registry's review line. The line is computed by iterating
// consentAxes, never a hand list, and TestEveryConsentAxisIsOnThePowerLine
// fails when an axis is added without a place here, so a new grant cannot
// slip under the line unnoticed. Erring wide is the point: a plugin wrongly
// put over the line costs a person a look, one wrongly left under it
// reaches users unread.
//
//	"privilege" — each member's side comes from the privilege registry
//	              (privilege_reach): acts, reads or neither.
//	"acts"      — harm past the sandbox with no network at all.
//	"reads"     — reads something another app, plugin or device holds:
//	              over the line together with network access.
//	"network"   — the way out that turns a read into a leak.
//	"neither"   — inside the sandbox, provided rather than taken, or told
//	              rather than granted.
var powerLineAxes = map[string]string{
	"privileges":          "privilege",
	"optional_privileges": "privilege", // granted at run time, held all the same
	"network":             "network",
	"sockets":             "neither", // listens on this computer only
	"dbus":                "acts",    // any D-Bus method, other services included
	"wmi":                 "reads",
	"preferences":         "reads", // other apps' settings
	"preference_writes":   "acts",  // changing them
	"devices":             "reads",
	"blobs":               "reads", // other plugins' byte channels
	"events":              "reads", // other plugins' and the platform's events
	"always_events":       "neither",
	"dispatch":            "acts", // runs other plugins' actions
	"collection_reads":    "reads",
	"collection_writes":   "acts", // writes another plugin's data
	"own_collections":     "neither",
	"provided_blobs":      "neither",
	"runtimes":            "neither", // managed runtimes inside the sandbox
	"capabilities":        "neither", // operations, each gated by a privilege
	"stages":              "neither", // a stage's own reach is its stage.* privilege
	"native_messaging":    "acts",    // a bridge to a browser extension
	"artifacts":           "neither",
	"reasons":             "neither",
	"protected_reads":     "reads", // password managers', BranchKit's own windows
}

// crossesPowerLine reports whether a plugin's first listing, or an update
// that newly crosses it, waits for a person at BranchKit, and why: anything
// that acts past its sandbox, or anything that reads what is not its own
// together with network access beyond localhost.
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
	add := func(why string) {
		for _, r := range reasons {
			if r == why {
				return
			}
		}
		reasons = append(reasons, why)
	}
	for _, ax := range consentAxes {
		members := ax.extract(m)
		if len(members) == 0 {
			continue
		}
		switch role := powerLineAxes[ax.name]; role {
		case "privilege":
			for _, p := range members {
				switch vocabulary.PrivilegeReach[p] {
				case "acts":
					add("privilege " + p + ": acts past its sandbox")
				case "reads":
					if networked {
						add("privilege " + p + ": reads something sensitive, with network access")
					}
				case "neither":
				default:
					// Not in the registry: a person looks rather than the
					// line letting an unknown name through.
					add("privilege " + p + ": not a privilege this CLI knows")
				}
			}
		case "acts":
			add(ax.name + ": acts past its sandbox")
		case "reads":
			if networked {
				add(ax.name + ": reads what is not its own, with network access")
			}
		case "network", "neither":
		default:
			add(ax.name + ": a grant this CLI has not placed on the line")
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
