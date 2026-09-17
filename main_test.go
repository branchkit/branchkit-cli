package main

import (
	"reflect"
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
