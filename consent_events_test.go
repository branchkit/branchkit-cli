package main

import "testing"

func eventsManifest(t *testing.T, id, events string) PluginManifest {
	t.Helper()
	return parseManifest(t, `{"id":"`+id+`","consumes":{"events":`+events+`}}`)
}

// A subscription's source is part of its grant: widening one from a single
// sender to every sender is a new grant (fresh consent) and the old one is
// revoked, and one pattern from two senders is two grants.
func TestEventSourceIsPartOfTheGrant(t *testing.T) {
	old := eventsManifest(t, "drummer", `[{"pattern":"tempo.**","source":"metronome"}]`)
	widened := eventsManifest(t, "drummer", `[{"pattern":"tempo.**"}]`)
	d := diffConsent(old, widened)
	ev := d.axis("events")
	if len(ev.Added) != 1 || ev.Added[0] != "tempo.**" || len(ev.Removed) != 1 || ev.Removed[0] != "tempo.**:metronome" {
		t.Fatalf("widening the source is not a grant change: %+v", ev)
	}
	if !d.expands() {
		t.Fatal("widening a subscription to every sender must require fresh consent")
	}
	if got := subscriptionDisplay(ev.Removed[0]); got != "tempo.** (from metronome)" {
		t.Fatalf("display: %q", got)
	}

	two := eventsManifest(t, "drummer", `[{"pattern":"tempo.**","source":"metronome"},{"pattern":"tempo.**","source":"clock"}]`)
	if got := subscriptionKeys(two, true); len(got) != 2 {
		t.Fatalf("same pattern, different sources collapsed: %v", got)
	}
	if !diffConsent(old, two).expands() {
		t.Fatal("a second sender for the same pattern must require fresh consent")
	}
}

// Only subscriptions the app gives a switch are disclosed as grants:
// platform events and those only the plugin itself can send are always
// delivered, so they are disclosed with the scope.
func TestOnlySwitchedSubscriptionsAreGrants(t *testing.T) {
	cases := []struct {
		pattern string
		source  string // "" = none
		grant   bool
	}{
		{"_platform.window.**", "", false},
		{"_platform.window.**", "voice", false},
		{"drummer.*", "", false},
		{"drummer.**", "", false},
		{"x.y", "drummer", false},
		{"ext.acme.*", "drummer.gaze", false},
		{"voice.*", "", true},
		{"*.focused", "", true},
		{"**", "", true},
		{"drummer.*", "voice", true},
		{"drummerx.a", "", true},
		{"x.y", "drummerx.gaze", true},
	}
	for _, c := range cases {
		e := SubscriptionDecl{Pattern: c.pattern}
		if c.source != "" {
			src := c.source
			e.Source = &src
		}
		if got := subscriptionIsGrant(e, "drummer"); got != c.grant {
			t.Errorf("%s from %q: grant = %v, want %v", c.pattern, c.source, got, c.grant)
		}
	}

	m := eventsManifest(t, "drummer", `[{"pattern":"_platform.window.**"},{"pattern":"drummer.*"},{"pattern":"voice.*"}]`)
	if got := subscriptionKeys(m, true); len(got) != 1 || got[0] != "voice.*" {
		t.Fatalf("grant members: %v", got)
	}
	if got := subscriptionKeys(m, false); len(got) != 2 {
		t.Fatalf("always-delivered members: %v", got)
	}
	for _, ax := range consentAxes {
		if ax.name == "always_events" && ax.grant {
			t.Fatal("always-delivered events must not be disclosed as switches")
		}
	}
}
