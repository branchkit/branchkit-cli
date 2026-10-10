package main

import (
	"embed"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConformanceFromCheckRuns(t *testing.T) {
	cases := []struct {
		name string
		runs []ghCheckRun
		want string
	}{
		{"no runs", nil, "unknown"},
		{"workflow name is not a check run name", []ghCheckRun{{Name: "build", Status: "completed", Conclusion: "success"}}, "unknown"},
		{"named job passed", []ghCheckRun{{Name: conformanceCheckName, Status: "completed", Conclusion: "success"}}, "passed"},
		{"named job failed", []ghCheckRun{{Name: conformanceCheckName, Status: "completed", Conclusion: "failure"}}, "failed"},
		{"named job running", []ghCheckRun{{Name: conformanceCheckName, Status: "in_progress"}}, "pending"},
		{"legacy unnamed job passed", []ghCheckRun{{Name: "conformance", Status: "completed", Conclusion: "success"}}, "passed"},
		{"named job wins over legacy", []ghCheckRun{
			{Name: "conformance", Status: "completed", Conclusion: "success"},
			{Name: conformanceCheckName, Status: "completed", Conclusion: "failure"},
		}, "failed"},
	}
	for _, tc := range cases {
		got := conformanceFromCheckRuns(tc.runs, "v1.0.0")
		if got.Status != tc.want {
			t.Errorf("%s: status %q, want %q", tc.name, got.Status, tc.want)
		}
	}
}

// GitHub names a check run after its job, so the scaffolded workflow's
// conformance job must carry the name the CLI looks the result up by.
func TestScaffoldConformanceJobNamedForLookup(t *testing.T) {
	for lang, fs := range map[string]embed.FS{"go": goTemplateFS, "ts": tsTemplateFS, "py": pyTemplateFS} {
		path := "templates/" + lang + "/.github/workflows/conformance.yml.tmpl"
		raw, err := fs.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var wf struct {
			Jobs map[string]struct {
				Name string `yaml:"name"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(raw, &wf); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		job, ok := wf.Jobs["conformance"]
		if !ok {
			t.Fatalf("%s: no conformance job", path)
		}
		if job.Name != conformanceCheckName {
			t.Errorf("%s: conformance job name %q, want %q", path, job.Name, conformanceCheckName)
		}
	}
}
