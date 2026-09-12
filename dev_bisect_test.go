package main

import "testing"

func TestParseCheckSpec(t *testing.T) {
	cases := []struct {
		spec     string
		phrase   string
		expected string
		wantErr  bool
	}{
		{"next tab => browser", "next tab", "browser", false},
		{"next tab -> browser", "next tab", "browser", false},
		{"  copy that  =>  keyboard  ", "copy that", "keyboard", false},
		{"no separator here", "", "", true},
		{"=> browser", "", "", true},
		{"next tab =>", "", "", true},
	}
	for _, c := range cases {
		got, err := parseCheckSpec(c.spec)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected an error, got %+v", c.spec, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.spec, err)
			continue
		}
		if got.phrase != c.phrase || got.expected != c.expected {
			t.Errorf("%q: got (%q, %q), want (%q, %q)",
				c.spec, got.phrase, got.expected, c.phrase, c.expected)
		}
	}
}

func TestCheckAnswer(t *testing.T) {
	// The symptom is "does not route to the expected plugin": only a match
	// by the right owner reads as gone. A match by anyone else — and a
	// non-match, which is what a suppressing gate looks like — both read as
	// still happening.
	if a, _ := checkAnswer(true, "browser", "browser"); a != "gone" {
		t.Errorf("right owner: got %q, want gone", a)
	}
	if a, _ := checkAnswer(true, "snippets", "browser"); a != "still_happening" {
		t.Errorf("wrong owner: got %q, want still_happening", a)
	}
	if a, _ := checkAnswer(false, "", "browser"); a != "still_happening" {
		t.Errorf("no match: got %q, want still_happening", a)
	}
}
