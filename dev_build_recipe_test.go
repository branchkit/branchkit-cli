package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecipeStepHelper is not a test: a dev.build recipe step in the tests
// below runs this test binary with it selected, and it writes the file named
// after "--" into its working directory, proving the step ran and where.
func TestRecipeStepHelper(t *testing.T) {
	if os.Getenv("BK_RECIPE_HELPER") != "1" {
		t.Skip("run only as a dev.build recipe step")
	}
	args := os.Args
	for i, a := range args {
		if a == "--" && i+1 < len(args) {
			if err := os.WriteFile(args[i+1], []byte(os.Getenv(devBuildRecipeEnv)), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("no output name after --")
}

func writeRecipePlugin(t *testing.T, dev map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	m := map[string]any{"id": "recipe", "run": "./recipe-plugin", "dev": dev}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func helperStep(t *testing.T, out string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{exe, "-test.run=^TestRecipeStepHelper$", "--", out}
}

// A manifest that declares dev.build is built by it, in build_dir, in order.
func TestBuildPluginDirRunsTheManifestRecipe(t *testing.T) {
	t.Setenv("BK_RECIPE_HELPER", "1")
	t.Setenv(devBuildRecipeEnv, "")
	dir := writeRecipePlugin(t, map[string]any{
		"build":     [][]string{helperStep(t, "first.txt"), helperStep(t, "second.txt")},
		"build_dir": "work",
	})
	if err := os.Mkdir(filepath.Join(dir, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := buildPluginDir(dir, hostTarget()); err != nil {
		t.Fatalf("buildPluginDir: %v", err)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		got, err := os.ReadFile(filepath.Join(dir, "work", name))
		if err != nil {
			t.Fatalf("step writing %s did not run in build_dir: %v", name, err)
		}
		if string(got) != "1" {
			t.Errorf("%s: the step's environment lacks %s=1 (got %q)", name, devBuildRecipeEnv, got)
		}
	}
}

// A failing step stops the build and names the step.
func TestBuildPluginDirReportsAFailingRecipeStep(t *testing.T) {
	t.Setenv(devBuildRecipeEnv, "")
	dir := writeRecipePlugin(t, map[string]any{
		"build": [][]string{{"branchkit-no-such-program-xyz", "arg"}},
	})
	err := buildPluginDir(dir, hostTarget())
	if err == nil || !strings.Contains(err.Error(), "branchkit-no-such-program-xyz arg") {
		t.Fatalf("want an error naming the failed step, got %v", err)
	}
}

// The TypeScript scaffold's recipe is `branchkit-cli dev build`. Running the
// recipe from inside that command would never end, so it is recognised.
func TestRecipeRunsThisCommand(t *testing.T) {
	cases := []struct {
		steps [][]string
		want  bool
	}{
		{[][]string{{"branchkit-cli", "dev", "build"}}, true},
		{[][]string{{"/usr/local/bin/branchkit-cli", "dev", "build", "."}}, true},
		{[][]string{{"branchkit-cli.exe", "dev", "build"}}, true},
		{[][]string{{"go", "build", "-o", "../p-plugin", "."}}, false},
		{[][]string{{"branchkit-cli", "dev", "test"}}, false},
	}
	for _, c := range cases {
		if got := recipeRunsThisCommand(c.steps); got != c.want {
			t.Errorf("%v: got %v, want %v", c.steps, got, c.want)
		}
	}
}

// Inside a recipe step, the recipe is not run again (a step that reaches
// `dev build` through a script would otherwise recurse), and the build falls
// through to the layout — which here has none, so it says so.
func TestBuildPluginDirInsideARecipeUsesTheLayout(t *testing.T) {
	t.Setenv("BK_RECIPE_HELPER", "1")
	t.Setenv(devBuildRecipeEnv, "1")
	dir := writeRecipePlugin(t, map[string]any{
		"build": [][]string{helperStep(t, "ran.txt")},
	})
	err := buildPluginDir(dir, hostTarget())
	if err == nil || !strings.Contains(err.Error(), "unknown build system") {
		t.Fatalf("want the layout path's error, got %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "ran.txt")); serr == nil {
		t.Error("the recipe ran again inside a recipe step")
	}
}

// A recipe builds for this machine only, so a cross-build goes by layout.
func TestBuildPluginDirCrossBuildSkipsTheRecipe(t *testing.T) {
	t.Setenv("BK_RECIPE_HELPER", "1")
	t.Setenv(devBuildRecipeEnv, "")
	dir := writeRecipePlugin(t, map[string]any{
		"build": [][]string{helperStep(t, "ran.txt")},
	})
	other := buildTarget{"linux", "amd64"}
	if other.isHost() {
		other = buildTarget{"windows", "arm64"}
	}
	err := buildPluginDir(dir, other)
	if err == nil || !strings.Contains(err.Error(), "unknown build system") {
		t.Fatalf("want the layout path's error, got %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "ran.txt")); serr == nil {
		t.Error("the recipe ran for a cross-build")
	}
}
