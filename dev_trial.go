package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// `dev trial` — run a freshly scaffolded plugin through the REAL app.
//
// The conformance suite and the plugin harness speak JSON-RPC to a plugin
// over stdio. Neither crosses the path a real plugin takes to get there:
// discovery, the privilege gate, the interpreter rewrite, the sandbox
// profile, the settings frame. The first time that path was walked for the
// Python and TypeScript scaffolds (2026-09-17) it found that every scaffold
// failed its own `dev test`, that the TypeScript scaffold could not start at
// all, and that the macOS `hosts` network tier had never loaded. None of it
// was visible to any gate. This is that walk, as one command
// (docs/design/PLAN_SCAFFOLD_TRIALS.md).
//
// It is written to run unchanged on macOS, Linux and Windows: it reaches the
// app through the same client every other `dev` command uses (operator
// socket or loopback TCP, whichever the address file offers) and shells out
// to nothing but this binary.
//
// It does NOT fire a real command. The scaffold's action types text at the
// cursor, which needs a safe focus target; matching is checked with a
// preview resolve instead.

type trialStep struct {
	name   string
	ok     bool
	detail string
}

type trialRun struct {
	steps []trialStep
}

func (t *trialRun) record(name string, err error, detail string) bool {
	step := trialStep{name: name, ok: err == nil, detail: detail}
	if err != nil {
		step.detail = err.Error()
	}
	t.steps = append(t.steps, step)
	mark := "✓"
	if !step.ok {
		mark = "✗"
	}
	if step.detail != "" {
		fmt.Printf("  %s %s — %s\n", mark, name, step.detail)
	} else {
		fmt.Printf("  %s %s\n", mark, name)
	}
	return step.ok
}

func (t *trialRun) failed() int {
	n := 0
	for _, s := range t.steps {
		if !s.ok {
			n++
		}
	}
	return n
}

func cmdDevTrial(args []string) {
	tmpl := ""
	keep := false
	listener := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--template":
			if i+1 < len(args) {
				i++
				tmpl = args[i]
			}
		case "--keep":
			keep = true
		case "--listener":
			listener = true
		}
	}
	if tmpl != "go" && tmpl != "ts" && tmpl != "py" {
		printDevTrialUsage()
		os.Exit(1)
	}
	if listener && tmpl != "ts" {
		fmt.Fprintln(os.Stderr, "Error: --listener exercises the TypeScript Node engine; use it with --template ts")
		os.Exit(1)
	}

	token := readHostToken()
	if token == "" {
		fmt.Fprintln(os.Stderr, "Error: the trial needs a running dev build of BranchKit (no host token found).")
		os.Exit(1)
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	id := "trial" + tmpl
	if listener {
		id += "listen"
	}
	parent, err := os.MkdirTemp("", "branchkit-trial-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	// The sandbox grants the plugin its REAL directory; a temp dir behind a
	// symlink (macOS /var → /private/var) must be named by where it is.
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		parent = resolved
	}
	dir := filepath.Join(parent, id)
	link := filepath.Join(appSupportDir(), "plugins", id)

	fmt.Printf("Scaffold trial: %s template as plugin %q\n", tmpl, id)
	t := &trialRun{}
	installed := false

	cleanup := func() {
		if keep {
			fmt.Printf("\nKept: %s (installed as %s)\n", dir, link)
			return
		}
		if installed {
			os.Remove(link)
			devHTTP("POST", "/settings/reload-plugins", token, nil)
		}
		os.RemoveAll(parent)
		fmt.Printf("\nRemoved the trial plugin. Its privilege approval stays on file until revoked in Settings → Plugins.\n")
	}

	finish := func() {
		cleanup()
		if n := t.failed(); n > 0 {
			fmt.Printf("%d step(s) failed\n", n)
			os.Exit(1)
		}
		fmt.Println("Trial passed")
	}

	// 1. Scaffold, the way a new author does.
	out, err := runSelf(self, parent, "dev", "init", "--name", id, "--template", tmpl,
		"--description", "Scaffold trial (safe to delete)")
	if !t.record("scaffold (dev init)", errWithTail(err, out), "") {
		finish()
		return
	}

	// 2. Make it unique. Every scaffold claims "hello branchkit" and
	//    alt+shift+h; two of them tie in voice's disambiguation and bind the
	//    same key twice.
	word := id
	err = uniquifyScaffold(dir, word, listener)
	if !t.record("unique phrase and keybind", err, fmt.Sprintf("%q", word+" branchkit")) {
		finish()
		return
	}
	if listener {
		out, err = runSelf(self, dir, "dev", "build")
		if !t.record("rebuild with sockets.listen (Node engine)", errWithTail(err, out), lastLine(out)) {
			finish()
			return
		}
	}

	// 3. Its own tests: static checks, harness conformance, unit tests.
	out, err = runSelf(self, dir, "dev", "test", ".")
	t.record("dev test", errWithTail(err, out), lastLine(out))

	// 4. Install by link and let the app discover it.
	os.Remove(link)
	err = os.Symlink(dir, link)
	if !t.record("install (symlink into plugins/)", err, "") {
		finish()
		return
	}
	installed = true
	_, status, err := devHTTP("POST", "/settings/reload-plugins", token, nil)
	if err == nil && status >= 300 {
		err = fmt.Errorf("HTTP %d", status)
	}
	if !t.record("discovered on reload", err, "") {
		finish()
		return
	}

	// 5. The privilege gate must stop it, then let it through once approved.
	state := waitForStatus(token, id, 10*time.Second, "Needs Approval", "Running")
	privileges := manifestPrivileges(dir)
	switch {
	case state == "Needs Approval":
		t.record("spawn gate held it for approval", nil, strings.Join(privileges, ", "))
		_, status, err = devHTTP("POST", "/settings/plugins/approve-privileges", token,
			map[string]any{"plugin_id": id, "privileges": privileges})
		if err == nil && status >= 300 {
			err = fmt.Errorf("HTTP %d", status)
		}
		t.record("approved its declared privileges", err, "")
	case state == "Running":
		t.record("spawn gate held it for approval", nil, "already approved from an earlier trial")
	default:
		t.record("spawn gate held it for approval", fmt.Errorf("status is %q", state), "")
	}

	// 6. It has to actually run, under the sandbox.
	//    "Running" has to HOLD: a plugin that crashes at startup is briefly
	//    reported Running between restarts, so one good poll proves nothing.
	state = waitForStatus(token, id, 30*time.Second, "Running")
	if state == "Running" {
		time.Sleep(4 * time.Second)
		state = waitForStatus(token, id, 0, "Running")
	}
	var runErr error
	if state != "Running" {
		runErr = fmt.Errorf("status is %q — read `branchkit-cli dev plog %s --since 2m` and the actuator log", state, id)
	}
	if !t.record("running under the sandbox", runErr, "") {
		finish()
		return
	}

	// 7. Matching, without executing anything.
	matched, err := resolvePreview(token, []string{word, "branchkit"})
	if err == nil && !matched {
		err = fmt.Errorf("%q did not resolve", word+" branchkit")
	}
	t.record("voice command resolves (preview)", err, "")

	// 8. The settings tab, rendered through the app.
	raw, status, err := devHTTP("GET", "/plugins/"+id+"/settings/getting_started/frame", token, nil)
	if err == nil && status != 200 {
		err = fmt.Errorf("HTTP %d", status)
	}
	if err == nil && !strings.Contains(string(raw), "bk-card") {
		err = fmt.Errorf("the frame rendered, but without the tab's content")
	}
	t.record("settings tab renders", err, fmt.Sprintf("%d bytes", len(raw)))

	finish()
}

func printDevTrialUsage() {
	fmt.Println("Usage: branchkit-cli dev trial --template go|ts|py [--listener] [--keep]")
	fmt.Println("  Scaffolds a plugin, runs its tests, installs it in the running dev app,")
	fmt.Println("  approves it, and checks that it starts, matches and renders — then")
	fmt.Println("  removes it. Executes no command. Needs a dev build of BranchKit running.")
	fmt.Println()
	fmt.Println("  --listener   TypeScript only: declare sockets.listen, so the Node engine is built")
	fmt.Println("  --keep       leave the plugin installed and its folder in place")
}

func runSelf(self, dir string, args ...string) (string, error) {
	cmd := exec.Command(self, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func errWithTail(err error, out string) error {
	if err == nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return fmt.Errorf("%v\n      %s", err, strings.Join(lines, "\n      "))
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// uniquifyScaffold gives the scaffold a phrase and a key combination nothing
// else uses, fixes its unit test to match, and optionally declares a listener.
func uniquifyScaffold(dir, word string, listener bool) error {
	cmdPath := filepath.Join(dir, "commands.json")
	raw, err := os.ReadFile(cmdPath)
	if err != nil {
		return err
	}
	var commands []map[string]any
	if err := json.Unmarshal(raw, &commands); err != nil {
		return err
	}
	for _, c := range commands {
		if pattern, ok := c["pattern"].([]any); ok && len(pattern) > 0 {
			pattern[0] = word
		}
	}
	if err := writeJSON(cmdPath, commands); err != nil {
		return err
	}

	manifestPath := filepath.Join(dir, "plugin.json")
	raw, err = os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	if data, ok := manifest["collection_data"].(map[string]any); ok {
		if binds, ok := data["keybinds"].(map[string]any); ok {
			renamed := map[string]any{}
			for _, binding := range binds {
				renamed["ctrl+alt+shift+f11"] = binding
			}
			data["keybinds"] = renamed
		}
	}
	if listener {
		manifest["network"] = "localhost"
		manifest["sockets"] = map[string]any{"listen": []any{map[string]any{"id": "trial", "port": 0}}}
	}
	if err := writeJSON(manifestPath, manifest); err != nil {
		return err
	}

	// The scaffold's own unit test asserts the phrase it shipped with.
	for _, rel := range []string{"src/main_test.go", "src/index.test.ts", "test_main.py"} {
		path := filepath.Join(dir, rel)
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		updated := strings.ReplaceAll(string(body), "hello branchkit", word+" branchkit")
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(path string, v any) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func manifestPrivileges(dir string) []string {
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return nil
	}
	var m struct {
		Privileges []string `json:"privileges"`
	}
	json.Unmarshal(raw, &m)
	return m.Privileges
}

// waitForStatus polls the plugin list until the plugin reports one of want,
// returning the last status it saw.
func waitForStatus(token, id string, timeout time.Duration, want ...string) string {
	deadline := time.Now().Add(timeout)
	last := "not listed"
	for {
		raw, status, err := devHTTP("GET", "/v1/plugins", token, nil)
		if err == nil && status == 200 {
			var plugins []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			}
			if json.Unmarshal(raw, &plugins) == nil {
				for _, p := range plugins {
					if p.ID == id {
						last = p.Status
					}
				}
			}
		}
		for _, w := range want {
			if last == w {
				return last
			}
		}
		if time.Now().After(deadline) {
			return last
		}
		time.Sleep(500 * time.Millisecond)
	}
}
