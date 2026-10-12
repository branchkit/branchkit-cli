package main

import (
	"strings"
	"testing"
)

// The digest names what the consent panel shows: formatting and fields the
// panel never shows leave it alone, and anything the panel would show
// differently changes it.
func TestConsentDigestFollowsWhatThePanelShows(t *testing.T) {
	base := PluginManifest{ID: "weather", Name: "Weather", Version: "1.0.0"}
	base.Requires.Privileges = []string{"input"}
	same := base
	same.Version = "1.0.1"
	same.Description = "a new description"
	if consentDigest(base) != consentDigest(same) {
		t.Fatal("a change the panel does not show moved the digest")
	}
	more := base
	more.Requires.Privileges = []string{"input", "clipboard"}
	if consentDigest(base) == consentDigest(more) {
		t.Fatal("a new privilege did not move the digest")
	}
}

// A release that asks for more than its preview showed is refused; one that
// matches, or an install no preview preceded, goes ahead.
func TestInstallRefusesWhatThePreviewDidNotShow(t *testing.T) {
	shown := PluginManifest{ID: "weather", Name: "Weather"}
	shown.Requires.Privileges = []string{"input"}
	release := shown
	release.Requires.Privileges = []string{"input", "clipboard"}
	if err := checkExpectedConsent(release, consentDigest(shown)); err == nil || !strings.Contains(err.Error(), "did not show") {
		t.Fatalf("want refusal, got %v", err)
	}
	if err := checkExpectedConsent(shown, consentDigest(shown)); err != nil {
		t.Fatalf("matching release refused: %v", err)
	}
	if err := checkExpectedConsent(release, ""); err != nil {
		t.Fatalf("no preview, no check: %v", err)
	}
}
