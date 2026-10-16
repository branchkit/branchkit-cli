package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
)

// installAssumeYes suppresses the interactive consent question (--yes). The
// settings-driven install passes it — that path shows its own consent panel
// before ever invoking the CLI.
var installAssumeYes bool

// installExpectConsent is the consent digest of the preview the person
// approved (--expect-consent). The settings-driven install passes it, and the
// install refuses a release whose manifest would show something else.
var installExpectConsent string

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// confirmAttestationDowngrade gates the oldest downgrade there is: the
// installed version was author-verified, and the release replacing it
// carries no valid attestation. That is exactly what a compromised release
// pipeline without the signing key looks like, so it is never waved through
// silently — not even by --yes, which suppresses consent formalities, not
// security regressions. Interactive: explicit y/N, default no. Scripted:
// hard refusal naming the transition.
func confirmAttestationDowngrade(pluginName string, in io.Reader, interactive bool) error {
	fmt.Printf(
		"\nWARNING: the installed %s was author-verified; this release has NO valid\n"+
			"author attestation. A signing key does not disappear by accident — this is\n"+
			"what a compromised release pipeline looks like.\n",
		pluginName,
	)
	if !interactive {
		return fmt.Errorf(
			"refusing unattested update over an author-verified install of '%s' — "+
				"rerun in an interactive terminal to override deliberately",
			pluginName,
		)
	}
	fmt.Print("Install it anyway? [y/N] ")
	sc := bufio.NewScanner(in)
	ans := ""
	if sc.Scan() {
		ans = strings.ToLower(strings.TrimSpace(sc.Text()))
	}
	if ans != "y" && ans != "yes" {
		return fmt.Errorf("update declined: attestation downgrade on '%s'", pluginName)
	}
	return nil
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

// networkSet canonicalizes the manifest's `network` declaration for
// disclosure and diffing: absent → empty (no network, the tightest
// sandbox); a string preset → one "preset:" member; the host-scoped form →
// one member per host. Set-shaped so the update diff is order-insensitive.
func networkSet(m PluginManifest) []string {
	if len(m.Requires.Network) == 0 || string(m.Requires.Network) == "null" {
		return nil
	}
	var preset string
	if json.Unmarshal(m.Requires.Network, &preset) == nil {
		return []string{"preset:" + preset}
	}
	var obj struct {
		Hosts       []string `json:"hosts"`
		Requestable bool     `json:"requestable"`
	}
	if json.Unmarshal(m.Requires.Network, &obj) == nil {
		hosts := append([]string(nil), obj.Hosts...)
		sort.Strings(hosts)
		// A member of its own, so turning it on in an update is an
		// expansion (fresh consent), not a silent change to the same axis.
		if obj.Requestable {
			hosts = append(hosts, networkRequestable)
		}
		return hosts
	}
	// Unparseable network declarations are refused at load by the
	// platform; showing the raw bytes keeps the diff honest until then.
	return []string{string(m.Requires.Network)}
}

// displayNetworkList maps a networkSet slice through networkDisplay,
// non-nil for JSON.
func displayNetworkList(members []string) []string {
	out := []string{}
	for _, m := range members {
		out = append(out, networkDisplay(m))
	}
	return out
}

// networkDisplay renders one networkSet member for a human.
// networkRequestable marks a plugin that may ask for more hosts while it runs
// (`network.request_host`). Each such host arrives OFF on the plugin's page.
const networkRequestable = "requestable"

func networkDisplay(member string) string {
	switch member {
	case networkRequestable:
		return "more hosts it asks for while running (each off until you allow it)"
	case "preset:localhost":
		return "localhost only"
	case "preset:outbound":
		return "ANY host (outbound)"
	default:
		return member
	}
}

// socketsSet canonicalizes `sockets.listen` entries (whitespace-compacted
// raw JSON) so the diff compares declarations, not formatting.
func socketsSet(m PluginManifest) []string {
	if m.Requires.Sockets == nil {
		return nil
	}
	out := make([]string, 0, len(m.Requires.Sockets.Listen))
	for _, l := range m.Requires.Sockets.Listen {
		var buf bytes.Buffer
		if err := json.Compact(&buf, l); err == nil {
			out = append(out, buf.String())
		} else {
			out = append(out, string(l))
		}
	}
	sort.Strings(out)
	return out
}

// consentAxis is one set-shaped consent axis: a canonical extractor and
// the copy each surface uses, joined with what the app's registry says
// about it. The diff, the install summary, the update prompt, and the
// preview all iterate `consentAxes` (the one consent-axis registry: every
// surface iterates it rather than hand-enumerating axes) — the sandbox axes
// were invisible everywhere precisely because each surface enumerated by
// hand.
//
// Which axes exist is the app's: its registry travels in the embedded
// vocabulary (manifest_vocabulary.json, generated from the platform), and
// an axis it adds fails this CLI's tests until axisReaders says how to read
// and word it.
//
// Two consent units stay outside the table on their own shape: effects
// (declaration objects whose author-written copy the surfaces render, with
// the sorted-assert-set identity) and the run command (a scalar whose
// change is always expansion-class — there is no "narrower" program).
type consentAxis struct {
	name string
	// From the app's registry: the manifest property the axis reads,
	// `<block>.<field>` (two axes may read one property: `consumes.events`
	// splits into switched and always-delivered), what a member is, and
	// when an update adding one asks again.
	block  string
	field  string
	kind   axisKind
	update axisUpdate
	// This CLI's: how to read and word it.
	extract func(PluginManifest) []string
	display func(string) string
	// Full summary line for the install disclosure; "" suppresses.
	summary func([]string) string
	addFmt  string // one "+" line per added member (display applied)
	delFmt  string // one "-" line per removed member
}

// axisKind is what one member of an axis is to the person installing. The
// app's panel groups by it, so the CLI and the panel agree on which lines
// are switches without either naming an axis.
type axisKind string

const (
	// Each member becomes a grant record with a switch on the plugin's
	// page (off there until allowed, for a plugin that is not bundled).
	kindGrant axisKind = "grant"
	// Enforced from the manifest as written, with no later moment to say
	// no: the install or update screen is the only place a person sees it.
	kindScope axisKind = "scope"
	// Told, not asked: the member grants nothing (why the plugin asks, what
	// it downloads for its own stages, the data it keeps for itself).
	kindNote axisKind = "note"
)

// axisUpdate is when a member an update adds needs a fresh yes — which a
// scripted update with nobody at the terminal cannot give, so it blocks.
type axisUpdate string

const (
	// Always: a new member changes what the plugin can do the moment the
	// update lands.
	updateExpands axisUpdate = "expands"
	// Only when the new grant would arrive switched on, which is the case
	// for a plugin that ships with BranchKit. Any other plugin's new grant
	// arrives off on its page and waits for the person there — the
	// manifest is the request, the grant store the authority — so the
	// update itself widens nothing.
	updateExpandsWhenGrantedOn axisUpdate = "expands_when_granted_on"
	// Never: shown, but a change asks for nothing and revokes nothing.
	updateInformational axisUpdate = "informational"
)

// addExpands reports whether a member this axis gains in an update needs a
// fresh yes, given whether the plugin's new grants arrive on.
func (ax consentAxis) addExpands(grantsOn bool) bool {
	switch ax.update {
	case updateExpands:
		return true
	case updateExpandsWhenGrantedOn:
		return grantsOn
	case updateInformational:
		return false
	default:
		// A value from a newer app: ask.
		return true
	}
}

func plainDisplay(s string) string { return s }

// protectedReadDisplay names a protected kind of window in the person's
// words; an unknown name (a newer app's) is shown as spelled.
func protectedReadDisplay(s string) string {
	switch s {
	case "password_managers":
		return "what password managers show"
	case "branchkit_windows":
		return "what BranchKit's own windows show"
	}
	return s
}

// axisReaders is how this CLI reads and words each axis the app publishes:
// the extractor and the copy. Which axes exist, the manifest property each
// reads, what a member is (kind) and when an update adding one asks again
// (update) are the app's — consentAxes joins the two.
var axisReaders = []consentAxis{
	{
		name:    "privileges",
		extract: func(m PluginManifest) []string { return m.Requires.Privileges },
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Privileges: %s\n", strings.Join(v, ", "))
		},
		addFmt: "  + %s\n",
		delFmt: "  - %s\n",
	},
	{
		name:    "optional_privileges",
		extract: func(m PluginManifest) []string { return m.Requires.OptionalPrivileges },
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Optional privileges: %s\n", strings.Join(v, ", "))
		},
		addFmt: "  + %s (optional — granted only if you approve a later request)\n",
		delFmt: "  - %s (optional)\n",
	},
	// The sandbox axes. Network HOSTS are grant records since 2026-09-18:
	// like every other grant a non-bundled plugin asks for, each is allowed
	// on the plugin's page after install and read live by the plugin's
	// proxy, so switching one off takes effect on the next connection
	// without a restart. Listen sockets and runtimes stay disclosure-only:
	// spawn-time, all-or-nothing, no later grant moment, so install is the
	// only place a user sees them. Nothing printed means the
	// tightest sandbox.
	{
		name:    "network",
		extract: networkSet,
		display: networkDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Network: %s (allow each host on the plugin's page after install)\n", strings.Join(v, ", "))
		},
		addFmt: "  + network: %s (allow it on the plugin's page)\n",
		delFmt: "  - network: %s\n",
	},
	{
		name:    "sockets",
		extract: socketsSet,
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Listen sockets: %d loopback listener(s)\n", len(v))
		},
		addFmt: "  + listen socket: %s\n",
		delFmt: "  - listen socket: %s\n",
	},
	// D-Bus methods (Linux): like network hosts, each is a grant with a
	// switch on the plugin's page, off until allowed there for a plugin
	// that is not bundled, and read on every call.
	{
		name: "dbus",
		extract: func(m PluginManifest) []string {
			if m.Requires.Dbus == nil {
				return nil
			}
			return m.Requires.Dbus.Methods
		},
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  D-Bus: calls %s (allow each method on the plugin's page after install)\n", strings.Join(v, ", "))
		},
		addFmt: "  + D-Bus: %s (allow it on the plugin's page)\n",
		delFmt: "  - D-Bus: %s\n",
	},
	// WMI classes (Windows): the same, one grant per class read.
	{
		name: "wmi",
		extract: func(m PluginManifest) []string {
			if m.Requires.Wmi == nil {
				return nil
			}
			return m.Requires.Wmi.Classes
		},
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  WMI: reads %s (allow each class on the plugin's page after install)\n", strings.Join(v, ", "))
		},
		addFmt: "  + WMI: %s (allow it on the plugin's page)\n",
		delFmt: "  - WMI: %s\n",
	},
	// Protected windows: reading what a password manager's windows show,
	// or BranchKit's own (its permission prompt among them). Off for every
	// plugin, bundled or not, until the person turns it on on the plugin's
	// page, where turning it on is confirmed in BranchKit's own window.
	{
		name:    "protected_reads",
		extract: func(m PluginManifest) []string { return m.Requires.ProtectedReads },
		display: protectedReadDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Protected windows: reads %s (off until you turn each on on the plugin's page)\n", strings.Join(v, ", "))
		},
		addFmt: "  + reads protected windows: %s (off until you turn it on on the plugin's page)\n",
		delFmt: "  - reads protected windows: %s\n",
	},
	// Other apps' settings (macOS defaults, GSettings, the user's
	// registry): reading each declared domain is one grant, and changing
	// one declared for writing is a second grant with its own switch.
	// Changing is stated as dangerous: an app's settings can name a program
	// it runs, and no list of such settings can be complete.
	{
		name:    "preferences",
		extract: preferenceReads,
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  App settings: %s (allow each on the plugin's page after install)\n", strings.Join(v, ", "))
		},
		addFmt: "  + App settings: %s (allow it on the plugin's page)\n",
		delFmt: "  - App settings: %s\n",
	},
	{
		name:    "preference_writes",
		extract: preferenceWrites,
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Changes app settings: %s\n    Changing an app's settings can change what programs it runs (allow each on the plugin's page after install)\n", strings.Join(v, ", "))
		},
		addFmt: "  + Changes app settings: %s (can change what programs the app runs; allow it on the plugin's page)\n",
		delFmt: "  - Changes app settings: %s\n",
	},
	// Devices: HID products and Bluetooth LE services. A HID product is one
	// grant; a Bluetooth service is allowed per device on the plugin's page
	// when the plugin first reaches for one. Keyboards, pointers and
	// security keys are never reachable, whatever is declared.
	{
		name:    "devices",
		extract: deviceGrants,
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Devices: %s (allow each on the plugin's page after install)\n", strings.Join(v, ", "))
		},
		addFmt: "  + Device: %s (allow it on the plugin's page)\n",
		delFmt: "  - Device: %s\n",
	},
	// Not part of the request block: a blob read is a consent axis that
	// lives in `consumes`, because what is being asked for is another
	// plugin's data rather than a platform capability. It is here rather
	// than handled separately (as effects are) because it is a plain list
	// of names, which is exactly what an axis carries.
	{
		name: "blobs",
		extract: func(m PluginManifest) []string {
			if m.Consumes == nil {
				return nil
			}
			return m.Consumes.Blobs
		},
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Shared data: reads %s (allow each on the plugin's page after install)\n", strings.Join(v, ", "))
		},
		addFmt: "  + reads shared data: %s (allow it on the plugin's page)\n",
		delFmt: "  - reads shared data: %s\n",
	},
	// What the plugin consumes from other plugins and the platform: each a
	// grant record on the plugin's page, the same as a blob read. Until
	// these were axes the only place a person saw them was the card, after
	// install, and an update that started dispatching to a new plugin read
	// as "nothing new". A new one asks again only for a plugin whose grants
	// arrive on (updateExpandsWhenGrantedOn).
	//
	// Event subscriptions split in two, by the app's own rule for which
	// carry a switch (subscriptionIsGrant): platform events and those only
	// the plugin itself can send are always delivered, so they are shown
	// with the scope rather than as switches that switch nothing.
	{
		name: "events",
		extract: func(m PluginManifest) []string {
			return subscriptionKeys(m, true)
		},
		display: subscriptionDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Events: receives %s (allow each on the plugin's page after install)\n", strings.Join(v, ", "))
		},
		addFmt: "  + receives events: %s (allow it on the plugin's page)\n",
		delFmt: "  - receives events: %s\n",
	},
	{
		name: "always_events",
		extract: func(m PluginManifest) []string {
			return subscriptionKeys(m, false)
		},
		display: subscriptionDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Events always delivered: %s (platform events and its own; no switch)\n", strings.Join(v, ", "))
		},
		addFmt: "  + always receives events: %s (platform events and its own; no switch)\n",
		delFmt: "  - always receives events: %s\n",
	},
	{
		name: "dispatch",
		extract: func(m PluginManifest) []string {
			if m.Consumes == nil {
				return nil
			}
			return namedRefs(m.Consumes.Dispatch)
		},
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Runs other plugins' actions: %s (allow each on the plugin's page after install)\n", strings.Join(v, ", "))
		},
		addFmt: "  + runs other plugins' actions: %s (allow it on the plugin's page)\n",
		delFmt: "  - runs other plugins' actions: %s\n",
	},
	// Collections: another plugin's are grants; the plugin's own (those it
	// introduces under provides.collections) are not — the app passes a
	// plugin reaching its own data without consulting any grant, and gives
	// it no switch — so they are their own axis, told and never asked.
	{
		name: "collection_reads",
		extract: func(m PluginManifest) []string {
			if m.Consumes == nil {
				return nil
			}
			return othersCollections(m, namedRefs(m.Consumes.Collections))
		},
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Reads collections: %s (allow each on the plugin's page after install)\n", strings.Join(v, ", "))
		},
		addFmt: "  + reads collection: %s (allow it on the plugin's page)\n",
		delFmt: "  - reads collection: %s\n",
	},
	{
		name: "collection_writes",
		extract: func(m PluginManifest) []string {
			if m.Consumes == nil {
				return nil
			}
			return othersCollections(m, sortedUnique(m.Consumes.Writes))
		},
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Writes collections: %s (allow each on the plugin's page after install)\n", strings.Join(v, ", "))
		},
		addFmt: "  + writes collection: %s (allow it on the plugin's page)\n",
		delFmt: "  - writes collection: %s\n",
	},
	{
		name:    "own_collections",
		extract: ownCollectionsUsed,
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Uses its own collections: %s (its own data; no switch)\n", strings.Join(v, ", "))
		},
		addFmt: "  + uses its own collection: %s\n",
		delFmt: "  - no longer uses its own collection: %s\n",
	},
	// The PROVIDER's side of a blob, which the design says is disclosed at
	// install "like every other declaration" and which nothing showed
	// (2026-09-23 review). One member per blob carrying every consented
	// property, so an update that raises max_bytes or switches to
	// `hash: provider` reads as a changed member — expansion — rather than
	// slipping through as the same name.
	{
		name:    "provided_blobs",
		extract: providedBlobsSet,
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Offers shared data: %s\n", strings.Join(v, "; "))
		},
		addFmt: "  + offers shared data: %s\n",
		delFmt: "  - offers shared data: %s\n",
	},
	{
		name:    "runtimes",
		extract: func(m PluginManifest) []string { return m.Requires.Runtimes },
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Managed runtimes: %s\n", strings.Join(v, ", "))
		},
		addFmt: "  + runtime: %s (read+exec of the managed runtime)\n",
		delFmt: "  - runtime: %s\n",
	},
	// Platform operations the plugin cannot start without. Only `required`:
	// one added by an update can stop the plugin starting on this computer,
	// which is worth asking about again. A newly called optional operation
	// grants nothing (privileges do), so it is not a new consent question;
	// the app's install panel names the ones this computer lacks.
	{
		name: "capabilities",
		extract: func(m PluginManifest) []string {
			if m.Requires.Capabilities == nil {
				return nil
			}
			return m.Requires.Capabilities.Required
		},
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Cannot start without: %s\n", strings.Join(v, ", "))
		},
		addFmt: "  + cannot start without: %s\n",
		delFmt: "  - no longer requires: %s\n",
	},
	// Pipeline stages: each a separate program BranchKit starts in a
	// sandbox of its own. What it does there does not pass through the
	// plugin's calls to the platform, which is why the app treats a plugin
	// that ships one as able to act on its own. A member is the stage and
	// the program it runs, so pointing a stage at a different program is
	// a new member, as a changed run command is.
	{
		name:    "stages",
		extract: stagesSet,
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Pipeline stages: %s (programs of its own that BranchKit starts)\n", strings.Join(v, ", "))
		},
		addFmt: "  + pipeline stage: %s (a program of its own that BranchKit starts)\n",
		delFmt: "  - pipeline stage: %s\n",
	},
	// A browser extension paired with the plugin: the platform registers
	// these extension ids with each browser's native-messaging host, so
	// the extension can reach the plugin's listen socket.
	{
		name:    "native_messaging",
		extract: nativeMessagingSet,
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Browser extensions it pairs with: %s (registered with each browser)\n", strings.Join(v, ", "))
		},
		addFmt: "  + pairs with browser extension: %s\n",
		delFmt: "  - pairs with browser extension: %s\n",
	},
	// What its stages download when first needed (model weights, a
	// dictionary): the size and where from. Provisioning one opens no
	// access — the files land in a folder only the plugin's own stages
	// read — but a gigabyte from a host the person never named is worth
	// saying before it happens.
	{
		name:    "artifacts",
		extract: artifactsSet,
		display: plainDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Downloads when first needed: %s\n", strings.Join(v, "; "))
		},
		addFmt: "  + downloads when first needed: %s\n",
		delFmt: "  - no longer downloads: %s\n",
	},
	// The author's reason for each request, shown under that request on the
	// plugin's page too. A reason asks for nothing, so a reworded one is
	// told at update, never asked about.
	{
		name:    "reasons",
		extract: reasonsSet,
		display: reasonDisplay,
		summary: func(v []string) string {
			return fmt.Sprintf("  Why it asks: %s\n", strings.Join(v, "; "))
		},
		addFmt: "  + reason: %s\n",
		delFmt: "  - reason withdrawn: %s\n",
	},
}

// consentAxes is the app's consent-axis registry (vocabulary.ConsentAxes,
// generated from the platform's), in its order, each joined with its reader
// from axisReaders. The diff, the install summary, the update prompt and the
// preview all iterate it.
var consentAxes = joinAxes(vocabulary.ConsentAxes, axisReaders)

// disclosedOutsideAxes are manifest properties every disclosure surface
// shows by a path of their own rather than as an axis, keyed
// `<block>.<field>`, with where (the app's list).
var disclosedOutsideAxes = vocabulary.DisclosedOutsideAxes

// notConsentFields are the properties of `requires`, `provides` and
// `consumes` that ask for nothing and so are not disclosed, keyed
// `<block>.<field>`, each with the reason (the app's list). The ratchets
// on both sides fail on a property that is neither an axis, nor disclosed
// outside the axes, nor here.
var notConsentFields = vocabulary.NotConsentFields

// joinAxes gives each published axis its reader. An axis with no reader is
// a CLI built against a registry it cannot render, a build-time defect
// (TestEveryPublishedAxisHasAReader): it panics rather than install with a
// declaration left unshown.
func joinAxes(published []publishedAxis, readers []consentAxis) []consentAxis {
	byName := make(map[string]consentAxis, len(readers))
	for _, r := range readers {
		byName[r.name] = r
	}
	out := make([]consentAxis, 0, len(published))
	for _, p := range published {
		ax, ok := byName[p.Name]
		if !ok {
			panic(fmt.Sprintf("consent axis %q is published by the app but this CLI cannot read it", p.Name))
		}
		ax.block, ax.field, ax.kind, ax.update = p.Block, p.Field, p.Kind, p.Update
		out = append(out, ax)
	}
	return out
}

// ownCollections is the set of collections the plugin introduces itself.
//
// Not every name it declares: the app gives a name two plugins declare to
// the one that had it first, and the other joins that collection and needs
// a grant to write it. This CLI cannot see who was first, so a name another
// plugin also claims — in the catalog, or installed here — is not counted
// as its own and stays on the grant axes, where it is asked about.
func ownCollections(m PluginManifest) map[string]bool {
	out := map[string]bool{}
	if m.Provides == nil {
		return out
	}
	claims := collectionClaimants()
	for name := range m.Provides.Collections {
		contested := false
		for _, id := range claims[name] {
			if id != m.ID {
				contested = true
			}
		}
		if !contested {
			out[name] = true
		}
	}
	return out
}

// collectionClaimants maps each collection name to the plugins that claim
// it: the catalog the app reads (each entry's `collections`) and the
// plugins installed here (their provides.collections). Read once per run; a
// variable so tests can stand in.
var collectionClaimants = sync.OnceValue(func() map[string][]string {
	out := map[string][]string{}
	if cat, ok := localCatalog(); ok {
		for _, e := range cat.Plugins {
			for _, name := range e.Collections {
				out[name] = append(out[name], e.ID)
			}
		}
	}
	for _, p := range discoverPlugins() {
		if p.Manifest.Provides == nil {
			continue
		}
		for name := range p.Manifest.Provides.Collections {
			out[name] = append(out[name], p.Manifest.ID)
		}
	}
	return out
})

// othersCollections drops the plugin's own collections from names.
func othersCollections(m PluginManifest, names []string) []string {
	own := ownCollections(m)
	var out []string
	for _, n := range names {
		if !own[n] {
			out = append(out, n)
		}
	}
	return out
}

// ownCollectionsUsed is each of its own collections the plugin declares it
// reads or writes.
func ownCollectionsUsed(m PluginManifest) []string {
	if m.Consumes == nil {
		return nil
	}
	own := ownCollections(m)
	var out []string
	for _, n := range append(namedRefs(m.Consumes.Collections), m.Consumes.Writes...) {
		if own[n] {
			out = append(out, n)
		}
	}
	return sortedUnique(out)
}

// artifactsSet is each artifact the plugin's stages can download, with its
// size and where from: "whisper (466 MB, from huggingface.co/openai/whisper-small)".
// The platforms it is for are named when it is not for every one, since
// only those computers fetch it.
func artifactsSet(m PluginManifest) []string {
	if m.Provides == nil {
		return nil
	}
	out := make([]string, 0, len(m.Provides.Artifacts))
	for name, a := range m.Provides.Artifacts {
		var from []string
		for _, p := range a.Parts {
			if src := artifactPartSource(p); src != "" {
				from = append(from, src)
			}
		}
		desc := humanBytes(a.SizeBytes)
		if from = sortedUnique(from); len(from) > 0 {
			desc += ", from " + strings.Join(from, ", ")
		}
		if a.Platform != nil {
			var oses []string
			for _, p := range *a.Platform {
				if name, ok := osNames[p]; ok {
					p = name
				}
				oses = append(oses, p)
			}
			desc += ", " + strings.Join(oses, "/") + " only"
		}
		out = append(out, fmt.Sprintf("%s (%s)", name, desc))
	}
	sort.Strings(out)
	return out
}

// osNames spells a manifest's platform names for a person.
var osNames = map[string]string{"macos": "macOS", "linux": "Linux", "windows": "Windows"}

// artifactPartSource names where one part is fetched from: a Hugging Face
// repository, or the host of a URL.
func artifactPartSource(p ArtifactPart) string {
	if p.Repo != "" {
		return "huggingface.co/" + p.Repo
	}
	if p.URL != "" {
		if u, err := url.Parse(p.URL); err == nil && u.Host != "" {
			return u.Host
		}
		return p.URL
	}
	return ""
}

// reasonsSet is the author's reasons as `<kind>:<what>` + "\x00" + reason,
// sorted by request, so a reworded reason is a changed member.
func reasonsSet(m PluginManifest) []string {
	out := make([]string, 0, len(m.Requires.Reasons))
	for key, why := range m.Requires.Reasons {
		out = append(out, key+"\x00"+strings.Join(strings.Fields(why), " "))
	}
	sort.Strings(out)
	return out
}

// reasonKinds words each request kind a reason may be keyed by.
var reasonKinds = map[string]string{
	"privilege":          "privilege",
	"dispatch":           "runs actions of",
	"collection_read":    "reads collection",
	"collection_write":   "writes collection",
	"event_subscription": "receives events",
	"effect_assert":      "effect",
	"network_host":       "network",
	"device":             "device",
	"app_preference":     "app settings",
	"wmi_class":          "WMI",
	"dbus_method":        "D-Bus",
	"blob_read":          "reads shared data",
}

// reasonDisplay renders one reasonsSet member: the request, then the
// author's sentence in quotes.
func reasonDisplay(member string) string {
	key, why, _ := strings.Cut(member, "\x00")
	if kind, what, ok := strings.Cut(key, ":"); ok {
		if words, known := reasonKinds[kind]; known {
			key = words + " " + what
		}
	}
	return fmt.Sprintf("%s: “%s”", key, why)
}

// subscriptionKey is a subscription's consent identity, in the form the app
// keys its grant record: the pattern, then ":" and the source when it names
// one. Two subscriptions to one pattern from different sources are two
// grants, and narrowing or widening the source is a different grant.
func subscriptionKey(e SubscriptionDecl) string {
	if e.Source != nil {
		return e.Pattern + ":" + *e.Source
	}
	return e.Pattern
}

// subscriptionDisplay renders a subscriptionKey for a person: the source,
// when there is one, named rather than joined by a colon.
func subscriptionDisplay(key string) string {
	if pattern, source, ok := strings.Cut(key, ":"); ok {
		return fmt.Sprintf("%s (from %s)", pattern, source)
	}
	return key
}

// subscriptionIsGrant reports whether the app gives a subscription a switch
// on the plugin's page, by the app's rules: never for a platform event
// (`_platform.*`, always delivered), and never for one only the plugin
// itself can satisfy — its source is the plugin or one of its stages
// (`<id>.<stage>`), or, with no source, the pattern's first segment is the
// plugin's id, the only namespace it may emit under.
func subscriptionIsGrant(e SubscriptionDecl, owner string) bool {
	if strings.HasPrefix(e.Pattern, "_platform.") {
		return false
	}
	if e.Source != nil {
		src := *e.Source
		stageOwner, _, isStage := strings.Cut(src, ".")
		return src != owner && !(isStage && stageOwner == owner)
	}
	first, _, _ := strings.Cut(e.Pattern, ".")
	return first != owner
}

// subscriptionKeys is the manifest's subscriptions that do (grants true) or
// do not carry a switch, as sorted subscriptionKeys.
func subscriptionKeys(m PluginManifest, grants bool) []string {
	if m.Consumes == nil {
		return nil
	}
	out := make([]string, 0, len(m.Consumes.Events))
	for _, e := range m.Consumes.Events {
		if subscriptionIsGrant(e, m.ID) == grants {
			out = append(out, subscriptionKey(e))
		}
	}
	return sortedUnique(out)
}

// sortedUnique returns the set of v, sorted, nil when empty.
func sortedUnique(v []string) []string {
	if len(v) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(v))
	for _, s := range v {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func namedRefs(refs []NamedRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, string(r))
	}
	return sortedUnique(out)
}

// stagesSet is each shipped stage with the program it runs ("ocr" when the
// binary is the stage's name, the platform's default; "ocr (bin/ocr-engine)"
// otherwise).
func stagesSet(m PluginManifest) []string {
	if m.Provides == nil {
		return nil
	}
	out := make([]string, 0, len(m.Provides.Stages))
	for name, s := range m.Provides.Stages {
		if s.Binary == "" || s.Binary == name {
			out = append(out, name)
		} else {
			out = append(out, fmt.Sprintf("%s (%s)", name, s.Binary))
		}
	}
	return sortedUnique(out)
}

// nativeMessagingSet is each extension id the platform would register for
// the plugin, naming its browser.
func nativeMessagingSet(m PluginManifest) []string {
	if m.Provides == nil || m.Provides.NativeMessaging == nil {
		return nil
	}
	nm := m.Provides.NativeMessaging
	out := make([]string, 0, len(nm.ChromeExtensionIDs)+len(nm.FirefoxExtensionIDs)+len(nm.SafariExtensionIDs))
	for _, id := range nm.ChromeExtensionIDs {
		out = append(out, "Chrome extension "+id)
	}
	for _, id := range nm.FirefoxExtensionIDs {
		out = append(out, "Firefox extension "+id)
	}
	for _, id := range nm.SafariExtensionIDs {
		out = append(out, "Safari extension "+id)
	}
	return sortedUnique(out)
}

// runSummary is the install line for the run command: the program the
// plugin's process is. Outside the axis table (see consentAxis) because it
// is one scalar, and a changed one is always an expansion.
func runSummary(m PluginManifest) string {
	if m.Run == "" {
		return ""
	}
	return fmt.Sprintf("  Runs: %s (in the plugin's sandbox)\n", m.Run)
}

// disclosureText turns one line of the terminal copy into the line the
// app's panel shows: the indent, any "+"/"-" marker and the line breaks
// gone, the words the same. One copy, so the terminal and the panel
// cannot say different things about the same member.
func disclosureText(line string) string {
	var parts []string
	for _, l := range strings.Split(line, "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			parts = append(parts, l)
		}
	}
	s := strings.Join(parts, " — ")
	s = strings.TrimPrefix(s, "+ ")
	return strings.TrimPrefix(s, "- ")
}

// axisDiff is one axis's added/removed members, canonical form.
type axisDiff struct {
	Added   []string
	Removed []string
}

// consentDiff is what changed, consent-wise, between the installed manifest
// and the one an update wants to put in its place. Everything else about an
// update is the author's business.
type consentDiff struct {
	Axes           map[string]axisDiff
	AddedEffects   []EffectDeclaration
	RemovedEffects []string // display labels
	RunChanged     bool
	RunOld         string
	RunNew         string
}

// expands reports whether the update asks for anything the installed version
// did not — the condition that requires fresh consent. grantsOn is whether
// the plugin's new grants would arrive switched on (grantsArriveOn): a new
// grant that arrives off widens nothing until the person allows it.
func (d consentDiff) expands(grantsOn bool) bool {
	for _, ax := range consentAxes {
		if len(d.Axes[ax.name].Added) > 0 && ax.addExpands(grantsOn) {
			return true
		}
	}
	return len(d.AddedEffects) > 0 || d.RunChanged
}

// contracts reports whether the update drops anything whose grant the app
// will revoke. A note axis (a reason, a download) revokes nothing.
func (d consentDiff) contracts() bool {
	for _, ax := range consentAxes {
		if len(d.Axes[ax.name].Removed) > 0 && ax.update != updateInformational {
			return true
		}
	}
	return len(d.RemovedEffects) > 0
}

// axis returns one axis's diff by registry name (empty if none).
func (d consentDiff) axis(name string) axisDiff { return d.Axes[name] }

// effectKey is an effect declaration's consent identity: the sorted set of
// asserted names. Copy edits (user_visible_*) are not a consent change.
func effectKey(e EffectDeclaration) string {
	names := append([]string(nil), e.AssertNames()...)
	sort.Strings(names)
	return strings.Join(names, ",")
}

func effectLabel(e EffectDeclaration) string {
	if e.UserVisibleName != "" {
		return e.UserVisibleName
	}
	return strings.Join(e.AssertNames(), ", ")
}

func diffStrings(oldList, newList []string) (added, removed []string) {
	oldSet := map[string]bool{}
	for _, s := range oldList {
		oldSet[s] = true
	}
	newSet := map[string]bool{}
	for _, s := range newList {
		newSet[s] = true
		if !oldSet[s] {
			added = append(added, s)
		}
	}
	for _, s := range oldList {
		if !newSet[s] {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func manifestEffects(m PluginManifest) []EffectDeclaration {
	if m.Consumes == nil {
		return nil
	}
	return m.Consumes.Effects
}

func diffConsent(oldM, newM PluginManifest) consentDiff {
	d := consentDiff{Axes: map[string]axisDiff{}}
	for _, ax := range consentAxes {
		added, removed := diffStrings(ax.extract(oldM), ax.extract(newM))
		if len(added) > 0 || len(removed) > 0 {
			d.Axes[ax.name] = axisDiff{Added: added, Removed: removed}
		}
	}

	oldEffects := map[string]bool{}
	for _, e := range manifestEffects(oldM) {
		oldEffects[effectKey(e)] = true
	}
	newEffects := map[string]bool{}
	for _, e := range manifestEffects(newM) {
		newEffects[effectKey(e)] = true
		if !oldEffects[effectKey(e)] {
			d.AddedEffects = append(d.AddedEffects, e)
		}
	}
	for _, e := range manifestEffects(oldM) {
		if !newEffects[effectKey(e)] {
			d.RemovedEffects = append(d.RemovedEffects, effectLabel(e))
		}
	}
	sort.Strings(d.RemovedEffects)

	if oldM.Run != newM.Run {
		d.RunChanged = true
		d.RunOld = oldM.Run
		d.RunNew = newM.Run
	}
	return d
}

// confirmUpdate is the update-time consent moment: diff-only, not a re-read
// of the whole manifest. An update that requests nothing new proceeds
// without a question — the standing consent covers it. One that EXPANDS
// what the plugin can do needs a fresh yes: asked on a TTY, skipped by
// --yes (the settings panel asked already), and BLOCKED otherwise — a
// scripted update must not widen a plugin's reach because nobody was there
// to say no. Contractions always print, because the platform will revoke
// those grants on reload and the user should know why.
//
// grantsOn is whether the plugin's new grants arrive switched on
// (grantsArriveOn). When they do not, a new grant on an
// updateExpandsWhenGrantedOn axis is printed as arriving off and asks
// nothing here: the plugin's page asks.
func confirmUpdate(newM, oldM PluginManifest, in io.Reader, assumeYes, tty, grantsOn bool) error {
	d := diffConsent(oldM, newM)
	fmt.Printf("\nUpdating %s v%s → v%s\n", newM.Name, oldM.Version, newM.Version)

	if d.contracts() {
		fmt.Println("No longer requests (grants will be revoked):")
		for _, ax := range consentAxes {
			if ax.update == updateInformational {
				continue
			}
			for _, m := range d.axis(ax.name).Removed {
				fmt.Printf(ax.delFmt, ax.display(m))
			}
		}
		for _, e := range d.RemovedEffects {
			fmt.Printf("  - effect: %s\n", e)
		}
	}

	// Told, not asked: what changed on the note axes, and new grants that
	// arrive off.
	var arriveOff, notes []string
	for _, ax := range consentAxes {
		ad := d.axis(ax.name)
		if ax.update == updateInformational {
			for _, m := range ad.Added {
				notes = append(notes, fmt.Sprintf(ax.addFmt, ax.display(m)))
			}
			for _, m := range ad.Removed {
				notes = append(notes, fmt.Sprintf(ax.delFmt, ax.display(m)))
			}
		} else if !ax.addExpands(grantsOn) {
			for _, m := range ad.Added {
				arriveOff = append(arriveOff, fmt.Sprintf(ax.addFmt, ax.display(m)))
			}
		}
	}
	if len(arriveOff) > 0 {
		fmt.Println("New requests, each off until you allow it on the plugin's page:")
		for _, line := range arriveOff {
			fmt.Print(line)
		}
	}
	if len(notes) > 0 {
		fmt.Println("Also changed (asks for nothing):")
		for _, line := range notes {
			fmt.Print(line)
		}
	}

	if !d.expands(grantsOn) {
		fmt.Println("No new privileges, effects, or sandbox scope.")
		return nil
	}

	fmt.Println("This update NEWLY requests:")
	for _, ax := range consentAxes {
		if !ax.addExpands(grantsOn) {
			continue
		}
		for _, m := range d.axis(ax.name).Added {
			fmt.Printf(ax.addFmt, ax.display(m))
		}
	}
	for _, e := range d.AddedEffects {
		if e.UserVisibleDescription != "" {
			fmt.Printf("  + effect: %s — %s\n", effectLabel(e), e.UserVisibleDescription)
		} else {
			fmt.Printf("  + effect: %s\n", effectLabel(e))
		}
	}
	if d.RunChanged {
		fmt.Printf("  + run command changed: %q → %q\n", d.RunOld, d.RunNew)
	}

	if assumeYes {
		fmt.Println()
		return nil
	}
	if !tty {
		return fmt.Errorf(
			"update expands what '%s' can do — re-run interactively to review, or pass --yes",
			newM.ID,
		)
	}
	fmt.Print("Continue? [y/N] ")
	sc := bufio.NewScanner(in)
	if !sc.Scan() {
		return fmt.Errorf("update declined")
	}
	switch strings.ToLower(strings.TrimSpace(sc.Text())) {
	case "y", "yes":
		return nil
	default:
		return fmt.Errorf("update declined")
	}
}

// providedBlobsSet renders each provided blob as one consentable line.
func providedBlobsSet(m PluginManifest) []string {
	if m.Provides == nil {
		return nil
	}
	var out []string
	for name, b := range m.Provides.Blobs {
		onFull := "refuses new data when full"
		if b.OnFull == "evict_oldest" {
			onFull = "drops the oldest data when full"
		}
		life := "cleared when the plugin stops"
		if b.Lifetime == "persistent" {
			life = "kept until uninstall"
		}
		hash := "checked by BranchKit"
		if b.Hash == "provider" {
			hash = "integrity is the plugin's claim, not checked"
		}
		out = append(out, fmt.Sprintf("%s (up to %s on disk, %s, %s, %s)",
			name, humanBytes(b.MaxBytes), onFull, life, hash))
	}
	sort.Strings(out)
	return out
}
