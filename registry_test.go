package main

import "testing"

func TestIsShortName(t *testing.T) {
	short := []string{"voice", "tiling", "my-plugin", "browser-2"}
	for _, s := range short {
		if !isShortName(s) {
			t.Errorf("isShortName(%q) = false, want true", s)
		}
	}

	notShort := []string{
		"drew/branchkit-plugin-basetypes", // owner/repo
		"./my-plugin",                      // local path
		"/usr/local/plugin",                // absolute path
		"../plugin",                        // relative path
	}
	for _, s := range notShort {
		if isShortName(s) {
			t.Errorf("isShortName(%q) = true, want false", s)
		}
	}
}
