package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A stand-in for plugin-sdk-go's types_gen.go: the doc-comment shapes
// emit-sdk writes, including a constraint note sharing a line and prose
// that mentions "non-empty" without being the note.
const fakeTypesGen = `package branchkit

type CollectionFetchRequest struct {
	// non-empty
	ID string ` + "`json:\"id\"`" + `
	// Collection name.
	// non-empty
	Name string ` + "`json:\"name\"`" + `
}

type InputTypeTextRequest struct {
	// Text to type. May be empty; it need not be non-empty.
	Text string ` + "`json:\"text\"`" + `
}

type BlobPublishRequest struct {
	// wire uint64 (64-bit) · min 0
	Length int ` + "`json:\"length\"`" + `
	// The blob's name.
	// wire string · non-empty
	Name string ` + "`json:\"name\"`" + `
}
`

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNonEmptyRequestFieldsReadsOnlyTheConstraintNote(t *testing.T) {
	p := filepath.Join(t.TempDir(), "types_gen.go")
	writeFile(t, p, fakeTypesGen)
	got, err := nonEmptyRequestFields(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got["CollectionFetchRequest"], ",") != "ID,Name" {
		t.Errorf("CollectionFetchRequest: got %v", got["CollectionFetchRequest"])
	}
	if strings.Join(got["BlobPublishRequest"], ",") != "Name" {
		t.Errorf("a note sharing a line with another constraint must count: %v", got["BlobPublishRequest"])
	}
	if _, ok := got["InputTypeTextRequest"]; ok {
		t.Errorf("prose mentioning non-empty is not the note: %v", got["InputTypeTextRequest"])
	}
}

const pluginSource = `package main

import sdk "github.com/branchkit/plugin-sdk-go"

func omitted(p *sdk.Plugin) {
	p.CollectionFetch(sdk.CollectionFetchRequest{ID: "x"})
}

func setAfterwards(p *sdk.Plugin, name string) {
	req := sdk.CollectionFetchRequest{ID: "x"}
	req.Name = name
	p.CollectionFetch(req)
}

func declaredWithVar(p *sdk.Plugin) {
	var req = &sdk.CollectionFetchRequest{Name: "n"}
	p.CollectionFetch(*req)
}

func complete(p *sdk.Plugin) {
	p.CollectionFetch(sdk.CollectionFetchRequest{ID: "x", Name: "n"})
	p.InputTypeText(sdk.InputTypeTextRequest{})
	p.CollectionFetch(sdk.CollectionFetchRequest{"x", "n"})
}
`

func TestScanRequestLiteralsReportsOnlyFieldsNeverSet(t *testing.T) {
	mod := t.TempDir()
	writeFile(t, filepath.Join(mod, "main.go"), pluginSource)
	required := map[string][]string{"CollectionFetchRequest": {"ID", "Name"}}

	omissions, checked, err := scanRequestLiterals(mod, required)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"main.go:6: sdk.CollectionFetchRequest{} leaves out Name", // omitted
		"main.go:16: sdk.CollectionFetchRequest{} leaves out ID",  // declaredWithVar
	}
	if strings.Join(omissions, "\n") != strings.Join(want, "\n") {
		t.Errorf("omissions:\n%s\nwant:\n%s", strings.Join(omissions, "\n"), strings.Join(want, "\n"))
	}
	// omitted, setAfterwards, declaredWithVar, complete's keyed literal.
	// The positional literal sets every field and is not counted; each
	// literal is counted once however it was reached.
	if checked != 4 {
		t.Errorf("checked %d literals, want 4", checked)
	}
}

func TestScanIgnoresFilesThatDoNotImportTheSDK(t *testing.T) {
	mod := t.TempDir()
	writeFile(t, filepath.Join(mod, "other.go"), `package main

type CollectionFetchRequest struct{ ID, Name string }

func f() { _ = CollectionFetchRequest{} }
`)
	omissions, checked, err := scanRequestLiterals(mod, map[string][]string{"CollectionFetchRequest": {"ID"}})
	if err != nil || len(omissions) != 0 || checked != 0 {
		t.Errorf("got %v, %d, %v", omissions, checked, err)
	}
}

// End to end through `go list -m`: the plugin's go.mod replaces the SDK
// with a local directory, the way a plugin developed beside an SDK checkout
// does, so the check must read THAT SDK's types.
func TestCheckSDKRequiredFieldsResolvesTheSDKTheModuleBuildsAgainst(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	root := t.TempDir()
	sdk := filepath.Join(root, "sdk")
	writeFile(t, filepath.Join(sdk, "go.mod"), "module github.com/branchkit/plugin-sdk-go\n\ngo 1.24\n")
	writeFile(t, filepath.Join(sdk, "types_gen.go"), fakeTypesGen)
	writeFile(t, filepath.Join(sdk, "plugin.go"), `package branchkit

type Plugin struct{}

func (p *Plugin) CollectionFetch(CollectionFetchRequest) {}
func (p *Plugin) InputTypeText(InputTypeTextRequest)     {}
`)
	plugin := filepath.Join(root, "plugin")
	writeFile(t, filepath.Join(plugin, "src", "go.mod"), `module example.com/plugin

go 1.24

require github.com/branchkit/plugin-sdk-go v0.0.0

replace github.com/branchkit/plugin-sdk-go => ../../sdk
`)
	writeFile(t, filepath.Join(plugin, "src", "main.go"), pluginSource)

	got := checkSDKRequiredFields(plugin)
	if got.Status != "fail" || !strings.Contains(got.Detail, "main.go:6: sdk.CollectionFetchRequest{} leaves out Name") {
		t.Errorf("got %s: %s", got.Status, got.Detail)
	}

	writeFile(t, filepath.Join(plugin, "src", "main.go"), `package main

import sdk "github.com/branchkit/plugin-sdk-go"

func f(p *sdk.Plugin) { p.CollectionFetch(sdk.CollectionFetchRequest{ID: "x", Name: "n"}) }
`)
	if got := checkSDKRequiredFields(plugin); got.Status != "pass" {
		t.Errorf("a complete literal: got %s: %s", got.Status, got.Detail)
	}
}

func TestCheckSDKRequiredFieldsSkipsWhatItCannotCheck(t *testing.T) {
	if got := checkSDKRequiredFields(t.TempDir()); got.Status != "skip" || got.Detail != "not a Go plugin" {
		t.Errorf("no go.mod: got %s: %s", got.Status, got.Detail)
	}
}
