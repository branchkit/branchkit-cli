package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func samePrinter(a, b func()) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// A help flag must win before any command runs: `dev init --help` used to
// scaffold and build a plugin, and `plugin install --help` looked up a plugin
// named "--help".
func TestUsageFor(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want func()
	}{
		{"init help scaffolds nothing", []string{"dev", "init", "--help"}, printDevUsage},
		{"install help installs nothing", []string{"plugin", "install", "--help"}, printPluginUsage},
		{"short flag", []string{"runtime", "install", "-h"}, printRuntimeUsage},
		{"flag after other flags", []string{"dev", "init", "--name", "x", "--help"}, printDevUsage},
		{"specific usage wins over the group", []string{"dev", "events", "--help"}, printDevEventsUsage},
		{"margins keeps its own help", []string{"dev", "margins", "-h"}, printDevMarginsUsage},
		{"platforms keeps its own help", []string{"dev", "platforms", "--help"}, printDevPlatformsUsage},
		{"unknown group falls back to top level", []string{"nonsense", "--help"}, printUsage},
		{"bare help flag", []string{"--help"}, printUsage},
	}
	for _, c := range cases {
		got := usageFor(c.args)
		if got == nil || !samePrinter(got, c.want) {
			t.Errorf("%s: usageFor(%v) picked the wrong printer", c.name, c.args)
		}
	}

	for _, args := range [][]string{
		{"dev", "init", "--name", "x"},
		{"dev", "say", "help me"},
		{"plugin", "list"},
		{},
	} {
		if usageFor(args) != nil {
			t.Errorf("usageFor(%v) asked for help; nothing in it does", args)
		}
	}
}

// The folder the app passes always wins; by hand the CLI targets the release
// install, and --dev / BRANCHKIT_DEV pick the development build's folder.
func TestAppSupportDirPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("APPDATA", "")
	t.Setenv("BRANCHKIT_APP_SUPPORT", "")
	t.Setenv("BRANCHKIT_DEV", "")
	defer func() { devFolder = false }()

	release := appSupportDir()
	devFolder = true
	dev := appSupportDir()
	devFolder = false
	if release == dev || filepath.Dir(release) != filepath.Dir(dev) {
		t.Fatalf("release %q and dev %q must be separate siblings", release, dev)
	}
	t.Setenv("BRANCHKIT_DEV", "1")
	if got := appSupportDir(); got != dev {
		t.Fatalf("BRANCHKIT_DEV: got %q, want %q", got, dev)
	}
	passed := filepath.Join(home, "told")
	t.Setenv("BRANCHKIT_APP_SUPPORT", passed)
	devFolder = true
	if got := appSupportDir(); got != passed {
		t.Fatalf("BRANCHKIT_APP_SUPPORT must win: got %q, want %q", got, passed)
	}
}

func TestStripGlobalFlags(t *testing.T) {
	defer func() { devFolder = false }()
	got := stripGlobalFlags([]string{"cli", "--dev", "dev", "say", "hi", "--", "--dev"})
	want := []string{"cli", "dev", "say", "hi", "--", "--dev"}
	if strings.Join(got, " ") != strings.Join(want, " ") || !devFolder {
		t.Fatalf("got %v devFolder=%v, want %v devFolder=true", got, devFolder, want)
	}
}
