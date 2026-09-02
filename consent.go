package main

import (
	"bufio"
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

// consentDiff is what changed, consent-wise, between the installed manifest
// and the one an update wants to put in its place. Only the three consent
// axes appear here — everything else about an update is the author's
// business.
type consentDiff struct {
	AddedPrivileges   []string
	AddedOptional     []string
	AddedEffects      []EffectDeclaration
	RemovedPrivileges []string
	RemovedOptional   []string
	RemovedEffects    []string // display labels
}

// expands reports whether the update asks for anything the installed version
// did not — the condition that requires fresh consent.
func (d consentDiff) expands() bool {
	return len(d.AddedPrivileges) > 0 || len(d.AddedOptional) > 0 || len(d.AddedEffects) > 0
}

func (d consentDiff) contracts() bool {
	return len(d.RemovedPrivileges) > 0 || len(d.RemovedOptional) > 0 || len(d.RemovedEffects) > 0
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
	}

	if !d.expands() {
		fmt.Println("No new privileges or effects.")
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
