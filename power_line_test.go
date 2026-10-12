package main

import (
	"encoding/json"
	"testing"
)

func lineManifest(privs []string, network string) PluginManifest {
	m := PluginManifest{ID: "x"}
	m.Requires.Privileges = privs
	if network != "" {
		m.Requires.Network = json.RawMessage(network)
	}
	return m
}

// The same cases as the app's privileges::tests::the_power_line.
func TestThePowerLine(t *testing.T) {
	cases := []struct {
		privs   []string
		network string
		want    bool
	}{
		{[]string{"input"}, "", true},
		{[]string{"clipboard"}, "", false},
		{[]string{"clipboard"}, `"localhost"`, false},
		{[]string{"clipboard"}, `{"hosts":["sync.example.com"]}`, true},
		{[]string{"screenshot"}, `{"hosts":[],"requestable":true}`, true},
		{[]string{"clipboard"}, `"outbound"`, true},
		{[]string{"windows", "audio"}, `{"hosts":["api.weather.gov"],"requestable":true}`, false},
		{nil, `{"hosts":["api.weather.gov"]}`, false},
		{[]string{"no.such.privilege"}, "", true},
	}
	for _, c := range cases {
		got, reasons := crossesPowerLine(lineManifest(c.privs, c.network))
		if got != c.want {
			t.Errorf("%v %s: got %v (%v), want %v", c.privs, c.network, got, reasons, c.want)
		}
		if got != (len(reasons) > 0) {
			t.Errorf("%v %s: crosses %v but reasons %v", c.privs, c.network, got, reasons)
		}
	}
}

// Every grant a manifest can be given counts, not only required privileges:
// an optional privilege is held once granted, and the other grant axes that
// reach past the sandbox act or read on their own.
func TestThePowerLineReadsEveryGrant(t *testing.T) {
	parse := func(js string) PluginManifest {
		var m PluginManifest
		if err := json.Unmarshal([]byte(js), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	over := []string{
		`{"id":"x","requires":{"optional_privileges":["shell"]}}`,
		`{"id":"x","requires":{"optional_privileges":["screenshot"],"network":{"hosts":["e.example"]}}}`,
		`{"id":"x","requires":{"preferences":{"write":["defaults:com.apple.dock"]}}}`,
		`{"id":"x","requires":{"dbus":{"methods":["org.freedesktop.login1.Manager.PowerOff"]}}}`,
		`{"id":"x","consumes":{"dispatch":["tiling"]}}`,
		`{"id":"x","consumes":{"writes":["snippets"]}}`,
		`{"id":"x","provides":{"native_messaging":{"host_name":"com.x","chrome_extension_ids":["abcdefghijklmnopabcdefghijklmnop"]}}}`,
		`{"id":"x","requires":{"wmi":{"classes":["Win32_Process"]},"network":"outbound"}}`,
		`{"id":"x","requires":{"devices":{"hid":["046d:c52b"]},"network":{"hosts":[],"requestable":true}}}`,
		`{"id":"x","consumes":{"collections":["snippets"]},"requires":{"network":{"hosts":["collect.example.com"]}}}`,
		`{"id":"x","requires":{"protected_reads":["password_managers"],"network":{"hosts":["e.example"]}}}`,
		// A read served on a listen socket: anything local can take it.
		`{"id":"x","requires":{"privileges":["clipboard"],"network":"localhost","sockets":{"listen":[{"id":"s","port":8765}]}}}`,
		// Another program's local service, with a way out.
		`{"id":"x","requires":{"network":{"hosts":["localhost:8765","collect.example"]}}}`,
		`{"id":"x","consumes":{"events":[{"pattern":"tiling.**"}]},"requires":{"network":"outbound"}}`,
		`{"id":"x","consumes":{"blobs":["voice/audio"]},"requires":{"network":{"hosts":["e.example"]}}}`,
	}
	for _, js := range over {
		if got, _ := crossesPowerLine(parse(js)); !got {
			t.Errorf("should cross: %s", js)
		}
	}
	under := []string{
		`{"id":"x","requires":{"optional_privileges":["clipboard"]}}`,
		`{"id":"x","requires":{"preferences":{"read":["defaults:com.apple.dock"]}}}`,
		`{"id":"x","requires":{"wmi":{"classes":["Win32_Battery"]}}}`,
		`{"id":"x","requires":{"devices":{"hid":["046d:c52b"]},"network":"localhost"}}`,
		// Talking to a local service with no way out leaks nothing.
		`{"id":"x","requires":{"network":{"hosts":["localhost:8765"]}}}`,
		// A listen socket with nothing sensitive to serve.
		`{"id":"x","requires":{"sockets":{"listen":[{"id":"s","port":0}]}}}`,
	}
	for _, js := range under {
		if got, reasons := crossesPowerLine(parse(js)); got {
			t.Errorf("should not cross: %s (%v)", js, reasons)
		}
	}
}

// Every consent axis in the app's registry has a place on the line, and
// every place names a real axis: an axis added later fails here until it is
// placed, rather than staying under the line unread.
func TestEveryConsentAxisIsOnThePowerLine(t *testing.T) {
	names := map[string]bool{}
	for _, ax := range consentAxes {
		names[ax.name] = true
		switch powerLineAxes[ax.name] {
		case "privilege", "acts", "reads", "network", "way out", "neither":
		default:
			t.Errorf("consent axis %q has no place on the power line", ax.name)
		}
	}
	for name := range powerLineAxes {
		if !names[name] {
			t.Errorf("powerLineAxes names %q, which is not a consent axis", name)
		}
	}
}

// Every privilege the CLI validates against is placed on the line.
func TestEveryPrivilegeHasAReach(t *testing.T) {
	for _, p := range vocabulary.Privileges {
		switch vocabulary.PrivilegeReach[p] {
		case "acts", "reads", "neither":
		default:
			t.Errorf("privilege %q has no reach in manifest_vocabulary.json", p)
		}
	}
}
