package main

// What the running app says it will do with a manifest's requests
// (consent.judge), and the safe view this CLI takes when it cannot ask.
//
// Which of a plugin's grants arrive switched on, which collections are its
// own, which events reach it with no switch, and which blobs and effects
// are its own are the app's to decide: it decides them at boot and in every
// check, from records this CLI cannot read. So the CLI asks the app and
// shows its answer. When the app is not running, or does not answer, the
// CLI keeps no second copy of those rules to fall back on: every request
// counts as a grant that arrives switched on, and no collection counts as
// the plugin's own. That view can only ask more often than the app would,
// never less.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// judgement is the app's answer for one manifest.
type judgement struct {
	PluginID string `json:"plugin_id"`
	// GrantsByDefault: boot switches on what the plugin declares without
	// asking (it ships with BranchKit and is installed from the source the
	// catalog names for it). Otherwise each grant arrives off on its page.
	GrantsByDefault bool           `json:"grants_by_default"`
	Members         []judgedMember `json:"members"`
}

// judgedMember is one member of one consent axis, as the app treats it.
type judgedMember struct {
	// The consent axis, or "effects" for consumes.effects.
	Axis string `json:"axis"`
	// The member: the grant's key (a subscription's `pattern:source`), or
	// as the plugin's page words it.
	Member string `json:"member"`
	// grant, scope or note. A member of a grant axis can be a note: a read
	// of the plugin's own blob, an effect it defines.
	Kind axisKind `json:"kind"`
	// Whether its record is on after the app's next boot.
	On bool `json:"on"`
	// Why the app refuses it whatever its switch says, when it does.
	Refused *string `json:"refused,omitempty"`
}

// judgeTimeout bounds one question. The app answers from memory, so a slow
// answer is an app that cannot be asked.
const judgeTimeout = 5 * time.Second

// errNoOperatorEndpoint: the address file names no operator socket or pipe,
// the only endpoint that answers a caller with no token.
var errNoOperatorEndpoint = errors.New("the app offers no operator socket")

// unreachedError is a question that never reached the app: no address file
// (BranchKit is not running), a stale one, or no answer on its operator
// endpoint. Two errors are a running app's, and plain: an app that answered
// and declined to judge (a manifest it will not read), and a live address
// file naming no operator endpoint (the app runs, but its socket or pipe
// did not bind, and only that endpoint answers a caller with no token).
type unreachedError struct{ err error }

func (e unreachedError) Error() string { return e.err.Error() }
func (e unreachedError) Unwrap() error { return e.err }

// appUnreached reports whether err is a question that never reached the app.
func appUnreached(err error) bool {
	var u unreachedError
	return errors.As(err, &u)
}

// askJudgement asks the running app what it will do with m if installed
// from source (what the install record will say: `owner/repo`,
// `local:<path>`, or `bundled`). It sends the manifest as written, not as
// this CLI parsed it, so the app judges every field, including ones this
// CLI does not read. The question needs no token: the operator socket's
// file mode admits only this user, and the operation writes nothing.
func askJudgement(m PluginManifest, source string) (*judgement, error) {
	if err := resolveDevBaseURL(); err != nil {
		return nil, unreachedError{err}
	}
	if devUnixSocket == "" && devPipe == "" {
		return nil, errNoOperatorEndpoint
	}
	manifest := m.raw
	if len(manifest) == 0 {
		var err error
		if manifest, err = json.Marshal(m); err != nil {
			return nil, err
		}
	}
	body, err := json.Marshal(struct {
		Manifest json.RawMessage `json:"manifest"`
		Source   string          `json:"source"`
	}{manifest, source})
	if err != nil {
		return nil, err
	}
	resp, err := devClient(judgeTimeout).Post(devBaseURL+"/v1/consent/judge", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, unreachedError{err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the app answered %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var j judgement
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("the app's answer does not parse: %w", err)
	}
	if j.PluginID != m.ID {
		return nil, fmt.Errorf("the app judged %q, not %q", j.PluginID, m.ID)
	}
	return &j, nil
}

// judged returns m carrying the app's judgement, or m as it was (the safe
// view) with the reason the app could not be asked.
func judged(m PluginManifest, source string) (PluginManifest, error) {
	j, err := askJudgement(m, source)
	if err != nil {
		m.judgement = nil
		return m, err
	}
	m.judgement = j
	return m, nil
}

// judgedUpdate judges both sides of an update from source. The diff reads
// each side by its own judgement, so both are judged or neither is: one
// side in the safe view and the other in the app's would show a member
// that did not change as dropped from one axis and added to another. An
// old manifest this CLI could not read (no raw bytes: only its name and
// version are known) declares nothing to judge.
func judgedUpdate(newM, oldM PluginManifest, source string) (PluginManifest, PluginManifest, error) {
	newM, err := judged(newM, source)
	if err != nil {
		oldM.judgement = nil
		return newM, oldM, err
	}
	if len(oldM.raw) == 0 {
		oldM.judgement = &judgement{PluginID: oldM.ID, GrantsByDefault: newM.judgement.GrantsByDefault}
		return newM, oldM, nil
	}
	if oldM, err = judged(oldM, source); err != nil {
		newM.judgement = nil
		return newM, oldM, err
	}
	return newM, oldM, nil
}

// noteUnjudged tells a person at the terminal that the app could not be
// asked, and what that means for what follows. Silent when it was asked.
func noteUnjudged(m PluginManifest, why error) {
	if m.judgement != nil || why == nil {
		return
	}
	fmt.Printf("\nBranchKit could not be asked how it will treat these requests (%v).\n"+
		"It decides which are the plugin's own and which arrive switched on, so here\n"+
		"none counts as its own and every new one counts as switched on.\n", why)
}

// grantsArriveOn reports whether the plugin's new grants arrive switched
// on: the app's answer, or yes when it was not asked.
func grantsArriveOn(m PluginManifest) bool {
	return m.judgement == nil || m.judgement.GrantsByDefault
}

// judgedAs reports whether the app judged member on axis, and as what.
func (m PluginManifest) judgedAs(axis, member string) (axisKind, bool) {
	if m.judgement == nil {
		return "", false
	}
	for _, jm := range m.judgement.Members {
		if jm.Axis == axis && jm.Member == member {
			return jm.Kind, true
		}
	}
	return "", false
}

// isOwn reports whether the app judged member of axis to be the plugin's
// own: a note on a grant axis (a read of its own blob, an effect it
// defines), which no switch decides. Never in the safe view.
func (m PluginManifest) isOwn(axis, member string) bool {
	kind, ok := m.judgedAs(axis, member)
	return ok && kind == kindNote
}

// refuseUnasked fails closed when the app could not be reached and nobody
// will be asked: a run with --yes, or with no person at the terminal, that
// would add a grant. In the safe view every grant counts as arriving
// switched on, so going ahead would take for granted what only the app can
// say: that a community plugin's new grants wait, off, on its page. At a
// terminal the person is asked instead (confirmInstall, confirmUpdate). A
// run the app answered keeps --yes: Settings passes it, having shown the
// app's answer. What adds no grant (a scope, a note, a run command) goes
// on as before: none of it depends on the app's answer.
//
// A running app that could not judge (it declined: a manifest over its
// size limit, an installed copy's manifest it no longer reads; or it runs
// with no operator endpoint to ask on) leaves the safe view shown, and
// --yes stands: the person approved that view in Settings
// (--expect-consent), and telling them to start a running app would be
// wrong.
func refuseUnasked(s consentSubject, assumeYes, tty bool) error {
	if !appUnreached(s.unjudged) || (tty && !assumeYes) {
		return nil
	}
	added := grantsAdded(s)
	if len(added) == 0 {
		return nil
	}
	verb := "install"
	if s.installed {
		verb = "update"
	}
	if len(added) > 3 {
		added = append(added[:3:3], fmt.Sprintf("%d more", len(added)-3))
	}
	how := "with --yes"
	if !assumeYes {
		how = "with nobody at the terminal to ask"
	}
	return fmt.Errorf(
		"refusing to %s '%s' %s: it adds %s, and BranchKit could not be reached to ask whether those arrive switched on (%v). "+
			"Start BranchKit and run this again, or run it at a terminal without --yes to be asked",
		verb, s.next.ID, how, strings.Join(added, ", "), s.unjudged,
	)
}

// grantsAdded names what the install or update adds on a grant axis, and
// each effect it newly asserts, as the plugin's page would word them.
// Nothing counts as the plugin's own here: refuseUnasked asks only when
// the app was not reached, and the safe view judges nothing its own.
func grantsAdded(s consentSubject) []string {
	old := s.old
	if !s.installed {
		old = PluginManifest{ID: s.next.ID}
	}
	d := diffConsent(old, s.next)
	var out []string
	for _, ax := range consentAxes {
		if ax.kind != kindGrant {
			continue
		}
		for _, m := range d.axis(ax.name).Added {
			if !d.ownAdded(ax, m) {
				out = append(out, ax.display(m))
			}
		}
	}
	for _, e := range d.AddedEffects {
		out = append(out, "effect "+effectLabel(e))
	}
	return out
}
