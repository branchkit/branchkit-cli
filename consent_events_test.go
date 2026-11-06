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
	if !d.expands(true) {
		t.Fatal("widening a subscription to every sender must require fresh consent")
	}
	if got := subscriptionDisplay(ev.Removed[0]); got != "tempo.** (from metronome)" {
		t.Fatalf("display: %q", got)
	}

	two := eventsManifest(t, "drummer", `[{"pattern":"tempo.**","source":"metronome"},{"pattern":"tempo.**","source":"clock"}]`)
	if got := subscriptionKeys(two, true); len(got) != 2 {
		t.Fatalf("same pattern, different sources collapsed: %v", got)
	}
	if !diffConsent(old, two).expands(true) {
		t.Fatal("a second sender for the same pattern must require fresh consent")
	}
}

// Only subscriptions the app gives a switch are disclosed as grants:
// platform events and those only the plugin itself can send reach it with
// no switch, by the app's answer. Unasked, every subscription is a switch.
func TestOnlySwitchedSubscriptionsAreGrants(t *testing.T) {
	m := eventsManifest(t, "drummer", `[{"pattern":"_platform.window.**"},{"pattern":"drummer.*"},{"pattern":"voice.*"},{"pattern":"x.y","source":"drummer"}]`)
	if got := subscriptionKeys(m, true); len(got) != 4 {
		t.Fatalf("unasked, every subscription is a switch: %v", got)
	}
	m = withJudgement(m, false,
		answered("events", "voice.*", kindGrant),
		answered("always_events", "_platform.window.**", kindScope),
		answered("always_events", "drummer.*", kindScope),
		answered("always_events", "x.y (from drummer)", kindScope))
	if got := subscriptionKeys(m, true); len(got) != 1 || got[0] != "voice.*" {
		t.Fatalf("grant members: %v", got)
	}
	if got := subscriptionKeys(m, false); len(got) != 3 {
		t.Fatalf("always-delivered members: %v", got)
	}
	for _, ax := range consentAxes {
		if ax.name == "always_events" && ax.kind == kindGrant {
			t.Fatal("always-delivered events must not be disclosed as switches")
		}
	}
}
