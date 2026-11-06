package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// withJudgement attaches an answer as the app would give it, for tests of
// what the CLI does with one.
func withJudgement(m PluginManifest, grantsByDefault bool, members ...judgedMember) PluginManifest {
	m.judgement = &judgement{PluginID: m.ID, GrantsByDefault: grantsByDefault, Members: members}
	return m
}

func answered(axis, name string, kind axisKind) judgedMember {
	return judgedMember{Axis: axis, Member: name, Kind: kind}
}

// fakeApp stands up an app answering on an operator socket, named by an
// address file in a scratch app-support folder, and returns the requests
// it was sent.
func fakeApp(t *testing.T, answer func(req judgeRequest) (int, any)) *[]judgeRequest {
	t.Helper()
	support := t.TempDir()
	t.Setenv("BRANCHKIT_APP_SUPPORT", support)
	// t.TempDir() nests deep under /var/folders on macOS and can exceed the
	// 104-byte socket path cap; a short /tmp dir keeps the path legal.
	sdir, err := os.MkdirTemp("/tmp", "bk-judge")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sdir) })
	sock := filepath.Join(sdir, "op.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var seen []judgeRequest
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req judgeRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		req.path, req.method, req.auth = r.URL.Path, r.Method, r.Header.Get("Authorization")
		seen = append(seen, req)
		status, out := answer(req)
		w.WriteHeader(status)
		if s, ok := out.(string); ok {
			_, _ = io.WriteString(w, s)
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { srv.Close() })
	run := filepath.Join(support, "run")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	addr := `{"v":1,"pid":` + strconv.Itoa(os.Getpid()) + `,"ui":{"port":1},"operator":{"socket":"` + sock + `"}}`
	if err := os.WriteFile(filepath.Join(run, "address.json"), []byte(addr), 0o600); err != nil {
		t.Fatal(err)
	}
	return &seen
}

type judgeRequest struct {
	Manifest json.RawMessage `json:"manifest"`
	Source   string          `json:"source"`
	path     string
	method   string
	auth     string
}

const notesManifest = `{
	"id": "notes", "name": "Notes", "version": "2.0.0",
	"future_field": {"kept": true},
	"provides": {"collections": {"notes": {}}},
	"consumes": {
		"collections": ["notes", "alphabet"],
		"writes": ["notes"],
		"events": [{"pattern": "_platform.window.**"}, {"pattern": "voice.*"}],
		"blobs": ["notes/pages", "camera/frames"],
		"effects": [{"asserts": ["notes.pinned"], "user_visible_name": "Pinned"}]
	}
}`

// The app is asked over its operator socket with no token, about the
// manifest as written (fields this CLI does not read included) and the
// source the install record will name; and what it answers is what the
// install shows.
func TestTheAppIsAskedAndItsAnswerShown(t *testing.T) {
	seen := fakeApp(t, func(req judgeRequest) (int, any) {
		return http.StatusOK, judgement{PluginID: "notes", GrantsByDefault: false, Members: []judgedMember{
			answered("events", "voice.*", kindGrant),
			answered("always_events", "_platform.window.**", kindScope),
			answered("collection_reads", "alphabet", kindGrant),
			answered("own_collections", "notes", kindNote),
			answered("blobs", "notes/pages", kindNote),
			answered("blobs", "camera/frames", kindGrant),
			answered("effects", "notes.pinned", kindNote),
		}}
	})
	m, err := judged(parseManifest(t, notesManifest), "someone/notes")
	if err != nil {
		t.Fatalf("the app answered, yet: %v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("asked %d times", len(*seen))
	}
	req := (*seen)[0]
	if req.method != http.MethodPost || req.path != "/v1/consent/judge" || req.auth != "" || req.Source != "someone/notes" {
		t.Errorf("request: %s %s auth=%q source=%q", req.method, req.path, req.auth, req.Source)
	}
	if !strings.Contains(string(req.Manifest), "future_field") {
		t.Errorf("the manifest was not sent as written: %s", req.Manifest)
	}
	if grantsArriveOn(m) {
		t.Error("the app said its grants arrive off")
	}
	got := map[string][]string{}
	for _, d := range previewDisclosures(m) {
		got[d.Axis] = d.Items
	}
	want := map[string][]string{
		"collection_reads": {"alphabet"},
		"own_collections":  {"notes"},
		"events":           {"voice.*"},
		"always_events":    {"_platform.window.**"},
	}
	for axis, items := range want {
		if !reflect.DeepEqual(got[axis], items) {
			t.Errorf("%s: %v, want %v", axis, got[axis], items)
		}
	}
	if got["collection_writes"] != nil {
		t.Errorf("writing its own collection is no grant: %v", got["collection_writes"])
	}

	// In an update, what the app judged its own asks nothing even where
	// grants arrive on; another plugin's blob still does.
	bare := withJudgement(parseManifest(t, `{"id": "notes", "version": "1.0.0"}`), true)
	own := withJudgement(parseManifest(t, `{"id": "notes", "version": "2.0.0", "consumes": {
		"writes": ["notes"], "blobs": ["notes/pages"], "effects": [{"asserts": ["notes.pinned"]}]}}`), true,
		answered("own_collections", "notes", kindNote),
		answered("blobs", "notes/pages", kindNote),
		answered("effects", "notes.pinned", kindNote))
	if d := diffConsent(bare, own); d.expands(true) {
		t.Errorf("its own blob, collection and effect ask nothing: %+v", d)
	}
	other := withJudgement(parseManifest(t, `{"id": "notes", "consumes": {"blobs": ["camera/frames"]}}`), true,
		answered("blobs", "camera/frames", kindGrant))
	if !diffConsent(bare, other).expands(true) {
		t.Error("another plugin's blob arriving on asks")
	}
	up := previewDiff(bare, own, true)
	for _, c := range up.Added {
		if c.Axis == "blobs" && (c.Kind != kindNote || *c.Expands || !strings.Contains(c.Text, "its own")) {
			t.Errorf("own blob row: %+v", c)
		}
	}
}

// What the app judged the plugin's own is told, never asked about or
// revoked: dropped in an update, it is not listed as a revoked grant; at a
// fresh install it says it has no switch; and an effect it defines is not
// one of the update's effect requests.
func TestItsOwnIsToldNotRevoked(t *testing.T) {
	ownJudged := func(m PluginManifest) PluginManifest {
		return withJudgement(m, false,
			answered("blobs", "notes/pages", kindNote),
			answered("effects", "notes.pinned", kindNote))
	}
	with := ownJudged(parseManifest(t, `{"id": "notes", "name": "Notes", "version": "1.0.0", "consumes": {
		"blobs": ["notes/pages"], "effects": [{"asserts": ["notes.pinned"], "user_visible_name": "Pinned"}]}}`))
	without := ownJudged(parseManifest(t, `{"id": "notes", "name": "Notes", "version": "2.0.0"}`))

	d := diffConsent(with, without)
	if d.contracts() || len(d.RemovedEffects) != 0 || !reflect.DeepEqual(d.OwnRemovedEffects, []string{"Pinned"}) {
		t.Errorf("dropping its own blob read and effect revokes nothing: %+v", d)
	}
	out := captureStdout(t, func() {
		if err := confirmUpdate(without, with, strings.NewReader(""), false, false, false); err != nil {
			t.Error(err)
		}
	})
	if strings.Contains(out, "will be revoked") || !strings.Contains(out, "notes/pages (its own; no switch)") || !strings.Contains(out, "effect: Pinned (its own; no switch)") {
		t.Errorf("update output:\n%s", out)
	}
	up := previewDiff(with, without, false)
	if len(up.Removed) != 1 || up.Removed[0].Kind != kindNote || len(up.RemovedEffects) != 0 {
		t.Errorf("removed rows: %+v, effects %v", up.Removed, up.RemovedEffects)
	}
	if add := diffConsent(without, with); len(add.AddedEffects) != 0 || len(add.OwnAddedEffects) != 1 || add.expands(true) {
		t.Errorf("adding its own effect asks nothing: %+v", add)
	}

	for _, disc := range previewDisclosures(with) {
		if disc.Axis == "blobs" && !reflect.DeepEqual(disc.Items, []string{"notes/pages (its own; no switch)"}) {
			t.Errorf("install disclosure: %+v", disc)
		}
	}
	// Unasked, the same read is a grant like any other.
	unasked := parseManifest(t, `{"id": "notes", "consumes": {"blobs": ["notes/pages"]}}`)
	for _, disc := range previewDisclosures(unasked) {
		if disc.Axis == "blobs" && !reflect.DeepEqual(disc.Items, []string{"notes/pages"}) {
			t.Errorf("unasked install disclosure: %+v", disc)
		}
	}
}

// With no app to ask, nothing is the plugin's own and every new request
// counts as switched on: the view can only ask more often than the app.
func TestWithNoAppEveryRequestCountsAsOn(t *testing.T) {
	t.Setenv("BRANCHKIT_APP_SUPPORT", t.TempDir())
	m, err := judged(parseManifest(t, notesManifest), "someone/notes")
	if err == nil || m.judgement != nil {
		t.Fatalf("no app is running, yet it was judged: %v", m.judgement)
	}
	if !grantsArriveOn(m) {
		t.Error("unasked, grants count as arriving on")
	}
	got := map[string][]string{}
	for _, d := range previewDisclosures(m) {
		got[d.Axis] = d.Items
	}
	if got["own_collections"] != nil || !reflect.DeepEqual(got["collection_writes"], []string{"notes"}) {
		t.Errorf("unasked, no collection is its own: %v", got)
	}
	if got["always_events"] != nil || len(got["events"]) != 2 {
		t.Errorf("unasked, every subscription is a switch: %v", got)
	}
	bare := parseManifest(t, `{"id": "notes", "version": "1.0.0"}`)
	ownBlob := parseManifest(t, `{"id": "notes", "consumes": {"blobs": ["notes/pages"]}}`)
	if !diffConsent(bare, ownBlob).expands(grantsArriveOn(ownBlob)) {
		t.Error("unasked, a new blob read asks")
	}
	ownEffect := parseManifest(t, `{"id": "notes", "consumes": {"effects": [{"asserts": ["notes.pinned"]}]}}`)
	if !diffConsent(bare, ownEffect).expands(grantsArriveOn(ownEffect)) {
		t.Error("unasked, a new effect asks")
	}
	out := captureStdout(t, func() { noteUnjudged(m, err) })
	if !strings.Contains(out, "could not be asked") {
		t.Errorf("the terminal is not told: %q", out)
	}
	if out := captureStdout(t, func() { noteUnjudged(withJudgement(m, false), nil) }); out != "" {
		t.Errorf("an answered question needs no note: %q", out)
	}
}

// An app that refuses the question (an older app without it, a manifest too
// large) or answers about another plugin leaves the safe view.
func TestARefusalOrAStrayAnswerIsTheSafeView(t *testing.T) {
	for name, answer := range map[string]func(judgeRequest) (int, any){
		"refused":     func(judgeRequest) (int, any) { return http.StatusUnauthorized, "unauthorized" },
		"other id":    func(judgeRequest) (int, any) { return http.StatusOK, judgement{PluginID: "someone-else"} },
		"not json":    func(judgeRequest) (int, any) { return http.StatusOK, "<html>" },
		"bad request": func(judgeRequest) (int, any) { return http.StatusBadRequest, "consent.judge: source is empty" },
	} {
		t.Run(name, func(t *testing.T) {
			fakeApp(t, answer)
			m, err := judged(parseManifest(t, notesManifest), "someone/notes")
			if err == nil || m.judgement != nil {
				t.Fatalf("judged from %s", name)
			}
		})
	}
}

// Both sides of an update are judged, or neither: one side in the app's
// view and the other in the safe view would show an unchanged member as
// moved between axes.
func TestAnUpdateIsJudgedOnBothSidesOrNeither(t *testing.T) {
	fakeApp(t, func(req judgeRequest) (int, any) {
		if strings.Contains(string(req.Manifest), `"1.0.0"`) {
			return http.StatusBadRequest, "no"
		}
		return http.StatusOK, judgement{PluginID: "notes", Members: []judgedMember{answered("own_collections", "notes", kindNote)}}
	})
	oldM := parseManifest(t, `{"id": "notes", "version": "1.0.0", "consumes": {"writes": ["notes"]}}`)
	newM := parseManifest(t, `{"id": "notes", "version": "2.0.0", "consumes": {"writes": ["notes"]}}`)
	n, o, err := judgedUpdate(newM, oldM, "someone/notes")
	if err == nil || n.judgement != nil || o.judgement != nil {
		t.Fatalf("one side judged: new=%v old=%v err=%v", n.judgement, o.judgement, err)
	}
	if d := diffConsent(o, n); len(d.Axes) != 0 {
		t.Errorf("an unchanged member moved: %+v", d.Axes)
	}

	// An old copy known only by name and version has nothing to judge.
	known := PluginManifest{ID: "notes", Version: "1.0.0"}
	n, o, err = judgedUpdate(parseManifest(t, `{"id": "notes", "version": "2.0.0"}`), known, "someone/notes")
	if err != nil || n.judgement == nil || o.judgement == nil {
		t.Fatalf("new=%v old=%v err=%v", n.judgement, o.judgement, err)
	}
}

// The catalog the app decides trust from is the app's to read: this CLI
// opens no catalog.yaml on this computer (it asks the app, consent_judge.go).
// The registry's online catalog is fetched by URL and cached under another
// name. A string naming a local catalog.yaml in any non-test source fails.
func TestTheCLIReadsNoLocalCatalog(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if strings.HasSuffix(s, "catalog.yaml") && !strings.HasPrefix(s, "https://") {
				t.Errorf("%s names a local catalog.yaml (%q): ask the app instead", fset.Position(lit.Pos()), s)
			}
			return true
		})
	}
}
