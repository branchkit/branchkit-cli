package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
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
	if len(m.Network) == 0 || string(m.Network) == "null" {
		return nil
	}
	var preset string
	if json.Unmarshal(m.Network, &preset) == nil {
		return []string{"preset:" + preset}
	}
	var obj struct {
		Hosts []string `json:"hosts"`
	}
	if json.Unmarshal(m.Network, &obj) == nil {
		hosts := append([]string(nil), obj.Hosts...)
		sort.Strings(hosts)
		return hosts
	}
	// Unparseable network declarations are refused at load by the
	// actuator; showing the raw bytes keeps the diff honest until then.
	return []string{string(m.Network)}
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
func networkDisplay(member string) string {
	switch member {
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
	if m.Sockets == nil {
		return nil
	}
	out := make([]string, 0, len(m.Sockets.Listen))
	for _, l := range m.Sockets.Listen {
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

// consentDiff is what changed, consent-wise, between the installed manifest
// and the one an update wants to put in its place. Four axes: privileges,
// optional privileges, effects, and sandbox scope (network, listen sockets,
// managed runtimes, the run command) — the last because it has NO later
// consent moment anywhere: the manifest declaration is the entire
// authority, enforced at next spawn (DESIGN_SANDBOX_CONSENT_SURFACE.md).
// Everything else about an update is the author's business.
type consentDiff struct {
	AddedPrivileges   []string
	AddedOptional     []string
	AddedEffects      []EffectDeclaration
	RemovedPrivileges []string
	RemovedOptional   []string
	RemovedEffects    []string // display labels
	AddedNetwork      []string
	RemovedNetwork    []string
	AddedSockets      []string
	RemovedSockets    []string
	AddedRuntimes     []string
	RemovedRuntimes   []string
	// The run command changing is a changed code identity — expansion-class
	// always (there is no "narrower" program).
	RunChanged bool
	RunOld     string
	RunNew     string
}

// expands reports whether the update asks for anything the installed version
// did not — the condition that requires fresh consent.
func (d consentDiff) expands() bool {
	return len(d.AddedPrivileges) > 0 || len(d.AddedOptional) > 0 || len(d.AddedEffects) > 0 ||
		len(d.AddedNetwork) > 0 || len(d.AddedSockets) > 0 || len(d.AddedRuntimes) > 0 ||
		d.RunChanged
}

func (d consentDiff) contracts() bool {
	return len(d.RemovedPrivileges) > 0 || len(d.RemovedOptional) > 0 || len(d.RemovedEffects) > 0 ||
		len(d.RemovedNetwork) > 0 || len(d.RemovedSockets) > 0 || len(d.RemovedRuntimes) > 0
}

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
	var d consentDiff
	d.AddedPrivileges, d.RemovedPrivileges = diffStrings(oldM.Privileges, newM.Privileges)
	d.AddedOptional, d.RemovedOptional = diffStrings(oldM.OptionalPrivileges, newM.OptionalPrivileges)

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

	d.AddedNetwork, d.RemovedNetwork = diffStrings(networkSet(oldM), networkSet(newM))
	d.AddedSockets, d.RemovedSockets = diffStrings(socketsSet(oldM), socketsSet(newM))
	d.AddedRuntimes, d.RemovedRuntimes = diffStrings(oldM.Runtimes, newM.Runtimes)
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
// to say no. Contractions always print, because the actuator will revoke
// those grants on reload and the user should know why.
func confirmUpdate(newM, oldM PluginManifest, in io.Reader, assumeYes, tty bool) error {
	d := diffConsent(oldM, newM)
	fmt.Printf("\nUpdating %s v%s → v%s\n", newM.Name, oldM.Version, newM.Version)

	if d.contracts() {
		fmt.Println("No longer requests (grants will be revoked):")
		for _, p := range d.RemovedPrivileges {
			fmt.Printf("  - %s\n", p)
		}
		for _, p := range d.RemovedOptional {
			fmt.Printf("  - %s (optional)\n", p)
		}
		for _, e := range d.RemovedEffects {
			fmt.Printf("  - effect: %s\n", e)
		}
		for _, n := range d.RemovedNetwork {
			fmt.Printf("  - network: %s\n", networkDisplay(n))
		}
		for _, l := range d.RemovedSockets {
			fmt.Printf("  - listen socket: %s\n", l)
		}
		for _, r := range d.RemovedRuntimes {
			fmt.Printf("  - runtime: %s\n", r)
		}
	}

	if !d.expands() {
		fmt.Println("No new privileges, effects, or sandbox scope.")
		return nil
	}

	fmt.Println("This update NEWLY requests:")
	for _, p := range d.AddedPrivileges {
		fmt.Printf("  + %s\n", p)
	}
	for _, p := range d.AddedOptional {
		fmt.Printf("  + %s (optional — granted only if you approve a later request)\n", p)
	}
	for _, e := range d.AddedEffects {
		if e.UserVisibleDescription != "" {
			fmt.Printf("  + effect: %s — %s\n", effectLabel(e), e.UserVisibleDescription)
		} else {
			fmt.Printf("  + effect: %s\n", effectLabel(e))
		}
	}
	// Sandbox scope has no later consent moment — the manifest is the
	// whole authority — so a widening here is exactly as consequential as
	// a new privilege and reads the same way.
	for _, n := range d.AddedNetwork {
		fmt.Printf("  + network: %s (enforced by the sandbox at next start)\n", networkDisplay(n))
	}
	for _, l := range d.AddedSockets {
		fmt.Printf("  + listen socket: %s\n", l)
	}
	for _, r := range d.AddedRuntimes {
		fmt.Printf("  + runtime: %s (read+exec of the managed runtime)\n", r)
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
