package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeHarness writes a script standing in for branchkit-test-harness: it
// records its arguments and exits with code.
func fakeHarness(t *testing.T, code string) (binary, argsFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake harness is a shell script")
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	binary = filepath.Join(dir, "harness")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\nexit " + code + "\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, argsFile
}

// The settings check runs the harness on the plugin folder with
// --settings-only, and --json when asked.
func TestSettingsCheckAsksTheHarnessForTheSettingsPhaseOnly(t *testing.T) {
	binary, argsFile := fakeHarness(t, "0")
	var out, errOut bytes.Buffer
	if code := runSettingsCheck(binary, "/plugins/demo", true, &out, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	got, _ := os.ReadFile(argsFile)
	if want := "/plugins/demo\n--settings-only\n--json\n"; string(got) != want {
		t.Errorf("harness args = %q, want %q", got, want)
	}
}

// A tab with a dead control fails the harness, and the command leaves with
// the harness's exit code; a harness that cannot run is a failure too.
func TestSettingsCheckFailsWhenTheHarnessDoes(t *testing.T) {
	binary, _ := fakeHarness(t, "1")
	var out, errOut bytes.Buffer
	if code := runSettingsCheck(binary, "/plugins/demo", false, &out, &errOut); code != 1 {
		t.Errorf("exit %d, want the harness's 1", code)
	}
	code := runSettingsCheck(filepath.Join(t.TempDir(), "missing"), "/plugins/demo", false, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "Error running harness") {
		t.Errorf("exit %d, stderr %q: a harness that cannot start must fail", code, errOut.String())
	}
}
