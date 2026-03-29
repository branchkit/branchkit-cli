package main

import "testing"

func TestParseGitHubSource(t *testing.T) {
	tests := []struct {
		input   string
		owner   string
		repo    string
		version string
		wantErr bool
	}{
		{"drew/branchkit-plugin-basetypes", "drew", "branchkit-plugin-basetypes", "", false},
		{"drew/branchkit-plugin-basetypes@v2.1.0", "drew", "branchkit-plugin-basetypes", "v2.1.0", false},
		{"branchkit/branchkit-plugin-voice@latest", "branchkit", "branchkit-plugin-voice", "latest", false},
		{"just-a-name", "", "", "", true},
		{"/repo", "", "", "", true},
		{"owner/", "", "", "", true},
		{"a/b/c", "", "", "", true},
		{"", "", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			s, err := parseGitHubSource(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for %q", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.input, err)
			}
			if s.Owner != tt.owner {
				t.Errorf("owner = %q, want %q", s.Owner, tt.owner)
			}
			if s.Repo != tt.repo {
				t.Errorf("repo = %q, want %q", s.Repo, tt.repo)
			}
			if s.Version != tt.version {
				t.Errorf("version = %q, want %q", s.Version, tt.version)
			}
		})
	}
}

func TestPluginNameFromRepo(t *testing.T) {
	tests := []struct{ repo, want string }{
		{"branchkit-plugin-basetypes", "basetypes"},
		{"branchkit-plugin-voice", "voice"},
		{"my-plugin", "my-plugin"},
		{"branchkit-plugin-", ""},
	}
	for _, tt := range tests {
		if got := pluginNameFromRepo(tt.repo); got != tt.want {
			t.Errorf("pluginNameFromRepo(%q) = %q, want %q", tt.repo, got, tt.want)
		}
	}
}
