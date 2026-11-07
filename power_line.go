package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
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
//	"network"   — every member is a way out, localhost included: a port on
//	              this computer can be another plugin that relays what it
//	              is sent. A port on this computer is also a read (another
//	              program's service), so loopback access crosses the line.
//	"way out"   — a way out on its own: a listen socket answers any local
//	              process or web page that connects.
//	"neither"   — inside the sandbox, provided rather than taken, or told
//	              rather than granted.
var powerLineAxes = map[string]string{
	"privileges":          "privilege",
	"optional_privileges": "privilege", // granted at run time, held all the same
	"network":             "network",
	"sockets":             "way out", // anything on this computer can connect and take what it serves
	"dbus":                "acts",    // any D-Bus method, other services included
	"wmi":                 "reads",
	"preferences":         "reads", // other apps' settings
	"preference_writes":   "acts",  // changing them
	"devices":             "acts",  // a device grant writes to the hardware as well as reading it
	"blobs":               "reads", // other plugins' byte channels
	"events":              "reads", // other plugins' and the platform's events
	"always_events":       "neither",
	"dispatch":            "acts", // runs other plugins' actions
	"collection_reads":    "reads",
	"collection_writes":   "acts", // writes another plugin's data
	"own_collections":     "neither",
	"provided_blobs":      "neither",
	"runtimes":            "neither", // managed runtimes inside the sandbox
	"resources":           "neither", // a larger share of memory, inside the sandbox
	"interrupts":          "neither", // its own cards, shown and spoken sooner
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
	reasons := []string{}
	add := func(why string) {
		for _, r := range reasons {
			if r == why {
				return
			}
		}
		reasons = append(reasons, why)
	}
	// First pass: is there a way out? Any network member that leaves this
	// computer, or any listen socket.
	networked := false
	for _, ax := range consentAxes {
		members := ax.extract(m)
		switch powerLineAxes[ax.name] {
		case "way out":
			networked = networked || len(members) > 0
		case "network":
			networked = networked || len(networkSet(m)) > 0
		}
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
						add("privilege " + p + ": reads something sensitive, with a way out (network or a listen socket)")
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
				add(ax.name + ": reads what is not its own, with a way out (network or a listen socket)")
			}
		case "network":
			// A port on this computer is another program's service: a read,
			// and the network access itself is the way out.
			for _, h := range networkSet(m) {
				if isLoopbackMember(h) {
					add("network " + h + ": reaches services on this computer")
					break
				}
			}
		case "way out", "neither":
		default:
			add(ax.name + ": a grant this CLI has not placed on the line")
		}
	}
	return len(reasons) > 0, reasons
}

// isLoopbackMember reports whether a networkSet member reaches this
// computer, by the same rule as the app's proxy (is_loopback_name in its
// host proxy, which decides where a connection really goes): the localhost
// preset, localhost or any *.localhost name, or an address of this computer
// (isThisMachine) in any spelling the system resolver reads as one,
// including the shorthand IPv4 forms (127.1, 2130706433, 0x7f000001);
// case-insensitive, with or without a port or a trailing dot. A name that
// merely starts with "127." (127.attacker.com) resolves wherever its owner
// says, so it is not loopback.
func isLoopbackMember(member string) bool {
	if member == "preset:localhost" {
		return true
	}
	member = strings.TrimSpace(member)
	// The app splits an entry at its last ':' when a port follows, so an
	// unbracketed IPv6 address with a port (::ffff:127.0.0.1:6379, the only
	// way a manifest can write one) is that address and port. Either
	// reading of the member counts, so the line errs toward a review.
	if isLoopbackHost(member) {
		return true
	}
	if h, _, err := net.SplitHostPort(member); err == nil {
		return isLoopbackHost(h)
	}
	if i := strings.LastIndexByte(member, ':'); i >= 0 {
		if _, err := strconv.ParseUint(member[i+1:], 10, 16); err == nil {
			return isLoopbackHost(member[:i])
		}
	}
	return false
}

// isLoopbackHost is isLoopbackMember for a host with no port.
func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimRight(strings.Trim(host, "[]"), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return isThisMachine(ip)
	}
	ip, ok := numericIPv4(host)
	return ok && isThisMachine(ip)
}

// isThisMachine reports whether a connection to ip reaches this computer:
// a loopback or unspecified address, also when carried in an IPv4-mapped
// (::ffff:127.0.0.1) or IPv4-compatible (::127.0.0.1) IPv6 address.
func isThisMachine(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	ip16 := ip.To16()
	if ip16 == nil || ip.To4() != nil {
		return false
	}
	for _, b := range ip16[:12] {
		if b != 0 {
			return false
		}
	}
	v4 := net.IP(ip16[12:])
	return v4[0] == 127 || v4.Equal(net.IPv4zero.To4())
}

// numericIPv4 reads the shorthand IPv4 forms the system resolver accepts:
// one to four dot-separated parts, each decimal, 0x hex or leading-zero
// octal, the last filling the remaining bytes (127.1, 2130706433 and
// 0x7f000001 are all 127.0.0.1). host is lowercase.
func numericIPv4(host string) (net.IP, bool) {
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return nil, false
	}
	nums := make([]uint64, len(parts))
	for i, p := range parts {
		var n uint64
		var err error
		switch {
		case p == "0x":
			n = 0
		case strings.HasPrefix(p, "0x"):
			n, err = strconv.ParseUint(p[2:], 16, 64)
		case len(p) > 1 && p[0] == '0':
			n, err = strconv.ParseUint(p[1:], 8, 64)
		default:
			n, err = strconv.ParseUint(p, 10, 64)
		}
		if err != nil {
			return nil, false
		}
		nums[i] = n
	}
	last := len(nums) - 1
	if nums[last] >= uint64(1)<<(8*(4-last)) {
		return nil, false
	}
	v := nums[last]
	for i, n := range nums[:last] {
		if n > 255 {
			return nil, false
		}
		v += n << (8 * (3 - i))
	}
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)), true
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
