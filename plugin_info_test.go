package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintActionTypesNamesEachActionByItsRoutedName(t *testing.T) {
	m := &PluginManifest{
		ID:           "apps",
		ActionPrefix: "apps",
		ActionTypes: map[string]actionTypeDecl{
			"launch": {Label: "Open App", Fields: []actionFieldDecl{
				{Key: "app_id", FieldType: "string", Required: true, Placeholder: "an app id"},
				{Key: "new_instance", FieldType: "boolean"},
			}},
			"focus": {Label: "Focus App"},
		},
	}
	var b bytes.Buffer
	printActionTypes(&b, m)
	got := b.String()
	for _, want := range []string{
		"  apps.focus  Focus App\n",
		"  apps.launch  Open App\n",
		"      app_id (string, required)  an app id\n",
		"      new_instance (boolean, optional)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Index(got, "apps.focus") > strings.Index(got, "apps.launch") {
		t.Errorf("actions should be sorted:\n%s", got)
	}
}

func TestPrintActionTypesSkipsAPluginWithNoPrefix(t *testing.T) {
	m := &PluginManifest{ID: "x", ActionTypes: map[string]actionTypeDecl{"a": {}}}
	var b bytes.Buffer
	printActionTypes(&b, m)
	if b.Len() != 0 {
		t.Fatalf("a plugin without action_prefix routes no actions; printed:\n%s", b.String())
	}
}
