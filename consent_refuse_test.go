package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// subjectFor judges an install (old nil) or an update the way judgeInstall
// does, without reading the plugins folder.
func subjectFor(t *testing.T, next string, old *string) consentSubject {
	t.Helper()
	s := consentSubject{}
	if old == nil {
		s.next, s.unjudged = judged(parseManifest(t, next), "someone/notes")
		return s
	}
	s.installed = true
	s.next, s.old, s.unjudged = judgedUpdate(parseManifest(t, next), parseManifest(t, *old), "someone/notes")
	return s
}

// confirmQuietly runs the consent moment with its output swallowed.
func confirmQuietly(t *testing.T, s consentSubject, answer string, assumeYes, tty bool) error {
	t.Helper()
	var err error
	captureStdout(t, func() { err = confirmConsentWith(s, strings.NewReader(answer), assumeYes, tty) })
	return err
}

const oldNotes = `{"id": "notes", "name": "Notes", "version": "1.0.0", "consumes": {"collections": ["alphabet"]}}`

const newNotes = `{"id": "notes", "name": "Notes", "version": "2.0.0", "consumes": {
	"collections": ["alphabet", "clipboard"],
	"effects": [{"asserts": ["camera.on"], "user_visible_name": "Camera"}]
}}`

// With the app not running, --yes cannot stand in for the app's answer: an
// install or update that adds a grant is refused, naming why and what to
// do, at a terminal or not. So is a run with nobody at the terminal.
func TestWithNoAppYesIsRefused(t *testing.T) {
	t.Setenv("BRANCHKIT_APP_SUPPORT", t.TempDir())
	old := oldNotes
	for name, s := range map[string]consentSubject{
		"install": subjectFor(t, notesManifest, nil),
		"update":  subjectFor(t, newNotes, &old),
	} {
		if s.unjudged == nil {
			t.Fatalf("%s: judged with no app running", name)
		}
		for _, tty := range []bool{false, true} {
			err := confirmQuietly(t, s, "y\n", true, tty)
			if err == nil {
				t.Fatalf("%s, tty=%v: --yes went ahead with no app to ask", name, tty)
			}
			msg := err.Error()
			for _, want := range []string{"--yes", "is BranchKit running?", "Start BranchKit", "run it at a terminal"} {
				if !strings.Contains(msg, want) {
					t.Errorf("%s: the refusal does not say %q: %s", name, want, msg)
				}
			}
		}
		err := confirmQuietly(t, s, "", false, false)
		if err == nil || !strings.Contains(err.Error(), "nobody at the terminal") {
			t.Errorf("%s: a scripted run with no app to ask: %v", name, err)
		}
	}
	if err := confirmQuietly(t, subjectFor(t, newNotes, &old), "", true, false); err == nil ||
		!strings.Contains(err.Error(), "update 'notes'") ||
		!strings.Contains(err.Error(), "clipboard") ||
		!strings.Contains(err.Error(), "effect Camera") ||
		strings.Contains(err.Error(), "alphabet") {
		t.Errorf("the refusal names what the update adds, and only that: %v", err)
	}
}

// With the app not running, a person at the terminal is asked, in the safe
// view, and their answer stands.
func TestWithNoAppATerminalIsAsked(t *testing.T) {
	t.Setenv("BRANCHKIT_APP_SUPPORT", t.TempDir())
	old := oldNotes
	for name, s := range map[string]consentSubject{
		"install": subjectFor(t, notesManifest, nil),
		"update":  subjectFor(t, newNotes, &old),
	} {
		var err error
		out := captureStdout(t, func() { err = confirmConsentWith(s, strings.NewReader("y\n"), false, true) })
		if err != nil {
			t.Errorf("%s: yes at the terminal was refused: %v", name, err)
		}
		if !strings.Contains(out, "Continue? [y/N]") || !strings.Contains(out, "could not be asked") {
			t.Errorf("%s: not asked, or not told the app was not asked: %q", name, out)
		}
		if err := confirmQuietly(t, s, "n\n", false, true); err == nil || !strings.Contains(err.Error(), "declined") {
			t.Errorf("%s: no at the terminal went ahead: %v", name, err)
		}
	}
}

// What adds no grant does not depend on the app's answer, so --yes covers
// it with the app not running: a run command, a dropped grant.
func TestWithNoAppWhatAddsNoGrantProceeds(t *testing.T) {
	t.Setenv("BRANCHKIT_APP_SUPPORT", t.TempDir())
	if err := confirmQuietly(t, subjectFor(t, `{"id": "notes", "version": "1.0.0", "run": "./notes"}`, nil), "", true, false); err != nil {
		t.Errorf("an install that adds no grant: %v", err)
	}
	old := newNotes
	if err := confirmQuietly(t, subjectFor(t, oldNotes, &old), "", true, false); err != nil {
		t.Errorf("an update that only drops grants: %v", err)
	}
}

// When the app answers, --yes works as it always has: Settings shows the
// app's answer and then installs with --yes.
func TestWhenTheAppAnswersYesProceeds(t *testing.T) {
	fakeApp(t, func(req judgeRequest) (int, any) {
		return http.StatusOK, judgement{PluginID: "notes", GrantsByDefault: false, Members: []judgedMember{
			answered("collection_reads", "alphabet", kindGrant),
			answered("collection_reads", "clipboard", kindGrant),
		}}
	})
	old := oldNotes
	for name, s := range map[string]consentSubject{
		"install": subjectFor(t, notesManifest, nil),
		"update":  subjectFor(t, newNotes, &old),
	} {
		if s.unjudged != nil {
			t.Fatalf("%s: the app answered, yet: %v", name, s.unjudged)
		}
		if err := confirmQuietly(t, s, "", true, false); err != nil {
			t.Errorf("%s: --yes refused though the app answered: %v", name, err)
		}
	}
}

// An address file whose operator socket nobody answers on is an app that
// cannot be reached, refused like no app at all.
func TestAnUnansweredSocketIsRefusedLikeNoApp(t *testing.T) {
	support := t.TempDir()
	t.Setenv("BRANCHKIT_APP_SUPPORT", support)
	sdir, err := os.MkdirTemp("/tmp", "bk-judge")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sdir) })
	run := filepath.Join(support, "run")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	addr := `{"v":1,"pid":` + strconv.Itoa(os.Getpid()) + `,"ui":{"port":1},"operator":{"socket":"` + filepath.Join(sdir, "gone.sock") + `"}}`
	if err := os.WriteFile(filepath.Join(run, "address.json"), []byte(addr), 0o600); err != nil {
		t.Fatal(err)
	}
	s := subjectFor(t, notesManifest, nil)
	if !appUnreached(s.unjudged) {
		t.Fatalf("a socket nobody answers on reached the app: %v", s.unjudged)
	}
	if err := confirmQuietly(t, s, "", true, false); err == nil || !strings.Contains(err.Error(), "could not be reached") {
		t.Errorf("--yes went ahead with the app unreachable: %v", err)
	}
}

// A running app that answers and declines to judge (a manifest over its
// size limit, an installed copy's manifest it no longer reads) leaves the
// safe view shown, and --yes stands: Settings showed that view and passes
// --yes, and the app is running, so "start BranchKit" would be wrong.
func TestAnAppThatDeclinesToJudgeKeepsYes(t *testing.T) {
	fakeApp(t, func(judgeRequest) (int, any) {
		return http.StatusBadRequest, "consent.judge: the manifest does not parse"
	})
	old := oldNotes
	for name, s := range map[string]consentSubject{
		"install": subjectFor(t, notesManifest, nil),
		"update":  subjectFor(t, newNotes, &old),
	} {
		if s.unjudged == nil || appUnreached(s.unjudged) || s.next.judgement != nil {
			t.Fatalf("%s: want an answered refusal in the safe view, got %v", name, s.unjudged)
		}
		if err := confirmQuietly(t, s, "", true, false); err != nil {
			t.Errorf("%s: --yes refused though the app is running: %v", name, err)
		}
	}
}
