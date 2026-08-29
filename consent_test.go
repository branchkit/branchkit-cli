package main

import (
	"strings"
	"testing"
)

// The consent moment: disclosure always, question only when interactive.
// Decline (or an empty/closed stdin) refuses the install.
func TestConfirmInstall(t *testing.T) {
	m := PluginManifest{
		ID: "example", Name: "Example", Version: "1.0.0",
		Privileges: []string{"dispatch"},
	}

	if err := confirmInstall(m, strings.NewReader(""), false); err != nil {
		t.Fatalf("non-interactive must not prompt or fail: %v", err)
	}
	for _, yes := range []string{"y\n", "Y\n", "yes\n", " YES \n"} {
		if err := confirmInstall(m, strings.NewReader(yes), true); err != nil {
			t.Fatalf("%q must accept: %v", yes, err)
		}
	}
	for _, no := range []string{"n\n", "\n", "nope\n", ""} {
		if err := confirmInstall(m, strings.NewReader(no), true); err == nil {
			t.Fatalf("%q must decline", no)
		}
	}
}
