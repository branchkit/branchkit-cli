package main

import "testing"

func TestValidateID(t *testing.T) {
	valid := []string{"voice", "my-plugin", "browser-2", "wm", "a"}
	for _, id := range valid {
		if !validateID(id) {
			t.Errorf("validateID(%q) = false, want true", id)
		}
	}

	invalid := []string{"", "MyPlugin", "my_plugin", "has space", "UPPER", "a/b", "a@b"}
	for _, id := range invalid {
		if validateID(id) {
			t.Errorf("validateID(%q) = true, want false", id)
		}
	}
}
