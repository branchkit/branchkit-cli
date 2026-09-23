package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The wrapper-call scan is the whole derivation: miss a call shape and the
// report quietly under-claims, which is worse than saying nothing because the
// author reads it as "nothing to worry about on Linux".
func TestScanWrapperCalls(t *testing.T) {
	dir := t.TempDir()
	src := `package main

func run(p *Plugin) {
	p.NativeWarpCursor(1, 2)
	h.plugin.NativeKeyRepeatRate()
	_ = p.NotAWrapper()
	// p.NativeKeyRepeatDelay() in a comment still counts — the scan is
	// textual, and over-reporting a call the author can see is safer than
	// missing one they cannot.
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory the walk must skip, holding a call that must NOT appear.
	vendor := filepath.Join(dir, "vendor")
	if err := os.MkdirAll(vendor, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vendor, "v.go"),
		[]byte("package v\nfunc f(p *Plugin) { p.NativeListSpaces() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Not Go, so not scanned.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"),
		[]byte("p.NativeCursorInfo()"), 0o644); err != nil {
		t.Fatal(err)
	}

	table := map[string]availabilityOp{
		"NativeWarpCursor":     {Name: "native.warp_cursor"},
		"NativeKeyRepeatRate":  {Name: "native.key_repeat_rate"},
		"NativeKeyRepeatDelay": {Name: "native.key_repeat_delay"},
		"NativeListSpaces":     {Name: "native.list_spaces"},
		"NativeCursorInfo":     {Name: "native.cursor_info"},
	}

	got, err := scanWrapperCalls(dir, table)
	if err != nil {
		t.Fatalf("scanWrapperCalls: %v", err)
	}
	names := make([]string, len(got))
	for i, op := range got {
		names[i] = op.Name
	}

	want := []string{"native.key_repeat_delay", "native.key_repeat_rate", "native.warp_cursor"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v, want %v (sorted)", names, want)
		}
	}
}

func TestScanWrapperCallsRejectsNonDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f.go")
	if err := os.WriteFile(file, []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := scanWrapperCalls(file, nil); err == nil {
		t.Fatal("a file path should not be reported on as a plugin directory")
	}
}

// `undeclared` must not render as `yes`. An operation that has not said where
// it works is not the same claim as one that has, and collapsing the two is
// how an author ships against an assumption nobody made.
func TestUndeclaredHasItsOwnMark(t *testing.T) {
	if availabilityMark["undeclared"] == availabilityMark["yes"] {
		t.Fatal("undeclared renders identically to yes")
	}
	for _, state := range []string{"yes", "notyet", "never", "undeclared"} {
		if availabilityMark[state] == "" {
			t.Errorf("state %q has no mark", state)
		}
	}
}
