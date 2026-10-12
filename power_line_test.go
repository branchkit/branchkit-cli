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
