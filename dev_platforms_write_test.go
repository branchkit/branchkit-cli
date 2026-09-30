package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// setUses changes requires.capabilities.uses and nothing else: a
// hand-formatted manifest keeps every other byte, and `required` and
// `dynamic`, which only the author can write, survive.
func TestSetUsesSplicesOnlyTheList(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "replaces an existing list, keeping required",
			in: `{
  "id": "x",
  "action_types": { "a": { "uses": ["native.mute"], "label": "A" } },
  "requires": {
    "privileges": ["input"],
    "capabilities": { "uses": ["native.old"], "required": ["native.mute"] }
  }
}
`,
			want: `{
  "id": "x",
  "action_types": { "a": { "uses": ["native.mute"], "label": "A" } },
  "requires": {
    "privileges": ["input"],
    "capabilities": {
      "uses": [
        "native.mute",
        "native.volume"
      ],
      "required": [
        "native.mute"
      ]
    }
  }
}
`,
		},
		{
			name: "adds capabilities to an existing requires",
			in: `{
  "id": "x",
  "requires": { "privileges": ["input"] }
}
`,
			want: `{
  "id": "x",
  "requires": {
    "privileges": ["input"],
    "capabilities": {
      "uses": [
        "native.mute",
        "native.volume"
      ]
    }
  }
}
`,
		},
		{
			name: "adds requires to a manifest without one",
			in: `{
    "id": "x",
    "name": "X"
}
`,
			want: `{
    "id": "x",
    "name": "X",
    "requires": {
        "capabilities": {
            "uses": [
                "native.mute",
                "native.volume"
            ]
        }
    }
}
`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := setUses([]byte(c.in), []string{"native.mute", "native.volume"})
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, c.want)
			}
			if !json.Valid(got) {
				t.Fatalf("not valid JSON:\n%s", got)
			}
			caps, err := readCapabilities(got)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(caps.Uses, []string{"native.mute", "native.volume"}) {
				t.Fatalf("uses = %v", caps.Uses)
			}
		})
	}
}

func TestSetUsesKeepsDynamic(t *testing.T) {
	in := `{"requires": {"capabilities": {"dynamic": "runs user scripts"}}}`
	got, err := setUses([]byte(in), []string{"hud.show"})
	if err != nil {
		t.Fatal(err)
	}
	caps, _ := readCapabilities(got)
	if caps.Dynamic != "runs user scripts" || len(caps.Uses) != 1 {
		t.Fatalf("got %+v from %s", caps, got)
	}
	if !strings.HasPrefix(string(got), `{"requires": {"capabilities": {`) {
		t.Fatalf("bytes before the splice changed: %s", got)
	}
}

func TestUsesDrift(t *testing.T) {
	missing, extra := usesDrift([]string{"a", "b"}, []string{"b", "c"})
	if !reflect.DeepEqual(missing, []string{"c"}) || !reflect.DeepEqual(extra, []string{"a"}) {
		t.Fatalf("missing=%v extra=%v", missing, extra)
	}
}
