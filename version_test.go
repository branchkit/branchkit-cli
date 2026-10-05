package main

import "testing"

func TestCliVersionPrefersStampedVersion(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = "v9.8.7"
	if got := cliVersion(); got != "v9.8.7" {
		t.Fatalf("cliVersion() = %q, want the stamped v9.8.7", got)
	}
}

func TestCliVersionWithoutStampIsNeverEmpty(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = ""
	if got := cliVersion(); got == "" {
		t.Fatal("cliVersion() is empty without a stamp")
	}
}
