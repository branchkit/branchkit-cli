package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// crossesPowerLine reports whether a plugin's first listing, or an update
// that newly crosses it, waits for a person at BranchKit, and why.
//
// The line: anything that ACTS past the plugin's sandbox, or anything that
// READS something sensitive together with somewhere to send it (network
// beyond localhost). It reads every grant the manifest can be given, not
// only `requires.privileges`:
//
//   - privileges, required AND optional: an optional privilege is granted at
//     run time, so the plugin can hold it all the same; each one's side of
//     the line comes from the app's privilege registry (privilege_reach);
//   - acts: changing another app's settings (preferences.write), calling
//     D-Bus methods, running other plugins' actions (consumes.dispatch),
//     writing another plugin's collections (consumes.writes), and bridging
//     to a browser extension (provides.native_messaging);
//   - reads: WMI classes, HID and Bluetooth devices, and other plugins'
//     byte channels (consumes.blobs).
//
// Erring wide is the point: a plugin wrongly put over the line costs a
// person a look, one wrongly left under it reaches users unread.
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
	acts := func(what string) { reasons = append(reasons, what+": acts past its sandbox") }
	reads := func(what string) {
		if networked {
			reasons = append(reasons, what+": reads something sensitive, with network access")
		}
	}

	privs := append(append([]string(nil), m.Requires.Privileges...), m.Requires.OptionalPrivileges...)
	sort.Strings(privs)
	seen := map[string]bool{}
	for _, p := range privs {
		if seen[p] {
			continue
		}
		seen[p] = true
		switch vocabulary.PrivilegeReach[p] {
		case "acts":
			acts("privilege " + p)
		case "reads":
			reads("privilege " + p)
		case "neither":
		default:
			// Not in the registry: a person looks rather than the line
			// letting an unknown name through.
			reasons = append(reasons, "privilege "+p+": not a privilege this CLI knows")
		}
	}

	r := m.Requires
	if r.Preferences != nil && len(r.Preferences.Write) > 0 {
		acts("changes other apps' settings")
	}
	if r.Dbus != nil && len(r.Dbus.Methods) > 0 {
		acts("calls D-Bus methods")
	}
	if m.Provides != nil && m.Provides.NativeMessaging != nil {
		acts("bridges to a browser extension")
	}
	if c := m.Consumes; c != nil {
		if len(c.Dispatch) > 0 {
			acts("runs other plugins' actions")
		}
		if len(c.Writes) > 0 {
			acts("writes other plugins' collections")
		}
		if len(c.Blobs) > 0 {
			reads("reads other plugins' byte channels")
		}
	}
	if r.Wmi != nil && len(r.Wmi.Classes) > 0 {
		reads("reads WMI classes")
	}
	if r.Devices != nil && len(r.Devices.Hid)+len(r.Devices.Ble) > 0 {
		reads("reads devices")
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
