package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func artifactFor(t *testing.T, platform, manifest string, files map[string]string) checkedArtifact {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, err := readManifest(filepath.Join(dir, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	return checkedArtifact{platform: platform, manifestBytes: []byte(manifest), manifest: m, dir: dir}
}

func failed(r releaseReport) []string {
	var out []string
	for _, c := range r.Checks {
		if !c.OK {
			out = append(out, c.Name)
		}
	}
	return out
}

const goodManifest = `{"id":"weather","name":"Weather","version":"1.2.0","run":"./weather","requires":{"network":{"hosts":["api.weather.gov"]}}}`

func TestAGoodReleasePasses(t *testing.T) {
	r := releaseReport{OK: true}
	assessRelease(&r, "weather", "v1.2.0", []checkedArtifact{
		artifactFor(t, "darwin-arm64", goodManifest, map[string]string{"main.js": "console.log('hi')"}),
		artifactFor(t, "linux-x86_64", goodManifest, nil),
		// A Windows checkout's CRLF line endings are not a different manifest.
		artifactFor(t, "windows-x86_64", strings.ReplaceAll("{\n"+goodManifest[1:], "\n", "\r\n"), nil),
	})
	if !r.OK {
		t.Fatalf("failed: %v", failed(r))
	}
	if strings.Join(r.Platforms, ",") != "darwin-arm64,linux-x86_64,windows-x86_64" || r.ConsentDigest == "" || r.PowerLine.Crosses {
		t.Fatalf("unexpected report: %+v", r)
	}
}

// Each of these fails the release, and names the check that failed.
func TestAReleaseFailsWhatItShould(t *testing.T) {
	cases := []struct {
		name      string
		tag       string
		artifacts func(t *testing.T) []checkedArtifact
		want      string
	}{
		{"no artifacts", "v1.2.0", func(t *testing.T) []checkedArtifact { return nil }, "platforms"},
		{"manifests differ by OS", "v1.2.0", func(t *testing.T) []checkedArtifact {
			return []checkedArtifact{
				artifactFor(t, "darwin-arm64", goodManifest, nil),
				artifactFor(t, "windows-x86_64", strings.Replace(goodManifest, `"hosts":["api.weather.gov"]`, `"hosts":["api.weather.gov","x.example"]`, 1), nil),
			}
		}, "one manifest"},
		{"version is not the tag", "v2.0.0", func(t *testing.T) []checkedArtifact {
			return []checkedArtifact{artifactFor(t, "darwin-arm64", goodManifest, nil)}
		}, "version matches tag"},
		{"id is not the repository", "v1.2.0", func(t *testing.T) []checkedArtifact {
			return []checkedArtifact{artifactFor(t, "darwin-arm64", strings.Replace(goodManifest, `"id":"weather"`, `"id":"voice"`, 1), nil)}
		}, "id matches repository"},
		{"raw IP host", "v1.2.0", func(t *testing.T) []checkedArtifact {
			return []checkedArtifact{artifactFor(t, "darwin-arm64", strings.Replace(goodManifest, "api.weather.gov", "203.0.113.7:443", 1), nil)}
		}, "network hosts are names"},
		{"a shipped secret", "v1.2.0", func(t *testing.T) []checkedArtifact {
			return []checkedArtifact{artifactFor(t, "darwin-arm64", goodManifest, map[string]string{"config.js": "const k = 'AKIAABCDEFGHIJKLMNOP'"})}
		}, "no secrets"},
		{"a hidden character", "v1.2.0", func(t *testing.T) []checkedArtifact {
			return []checkedArtifact{artifactFor(t, "darwin-arm64", goodManifest, map[string]string{"main.js": "if (ok ‮) {}"})}
		}, "no hidden characters"},
	}
	for _, c := range cases {
		r := releaseReport{OK: true}
		assessRelease(&r, "weather", c.tag, c.artifacts(t))
		got := failed(r)
		if r.OK || len(got) == 0 || !inList(got, c.want) {
			t.Errorf("%s: want %q failed, got %v", c.name, c.want, got)
		}
	}
}

// The power line is reported, not failed: crossing it means a person looks.
func TestThePowerLineIsReportedNotFailed(t *testing.T) {
	m := `{"id":"weather","name":"Weather","version":"1.2.0","run":"./w","requires":{"privileges":["input"]}}`
	r := releaseReport{OK: true}
	assessRelease(&r, "weather", "v1.2.0", []checkedArtifact{artifactFor(t, "darwin-arm64", m, nil)})
	if !r.OK || !r.PowerLine.Crosses {
		t.Fatalf("want ok and crossing, got %+v", r)
	}
}
