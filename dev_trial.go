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
// was visible to any gate. This is that walk, as one command.
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
	network := false
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
		case "--network":
			network = true
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
	if listener && network {
		fmt.Fprintln(os.Stderr, "Error: --listener and --network each declare the manifest's network tier; run them separately")
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
	if network {
		id += "net"
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

	var probe *networkProbe
	cleanup := func() {
		if probe != nil {
			probe.close()
		}
		if keep {
			fmt.Printf("\nKept: %s (installed as %s)\n", dir, link)
			return
		}
		if installed {
			// Uninstall through the platform, not by unlinking: that path
			// stops the process, revokes the approval, reclaims the records
			// the scaffold seeded (its keybind), and reloads. Unlinking alone
			// left a `keybinds` record owned by a vanished plugin behind on
			// every run, which smoke's ownership check then flagged.
			if _, status, err := devHTTP("POST", "/settings/plugins/uninstall", token,
				map[string]any{"plugin_id": id}); err != nil || status >= 300 {
				os.Remove(link)
				devHTTP("POST", "/settings/reload-plugins", token, nil)
			}
			os.Remove(link) // a no-op after a clean uninstall; the fallback otherwise
			// The scaffold seeds one keybind into the keyboard plugin's
			// collection. After the uninstall that record is a departed
			// writer's group, retained for the grace window; reclaim it now
			// so a trial leaves nothing for smoke's ownership check to flag.
			devHTTP("POST", "/inspector/ownership/reclaim", token,
				map[string]any{"collection": "keybinds", "writer": id})
		}
		os.RemoveAll(parent)
		fmt.Printf("\nRemoved the trial plugin through the platform's uninstall (approval revoked, seeded records reclaimed).\n")
	}

	finish := func() {
		cleanup()
		if n := t.failed(); n > 0 {
			fmt.Printf("%d step(s) failed\n", n)
			os.Exit(1)
		}
		fmt.Println("Trial passed")
	}

	// 1. Scaffold, the way a new author does — with what the trial needs
	//    rendered in, not edited in afterwards.
	//
	//    Every scaffold would otherwise claim "hello branchkit" and
	//    alt+shift+h; two of them tie in voice's disambiguation and bind the
	//    same key twice. This used to be fixed by reading plugin.json back,
	//    editing a map and writing it out. Nothing does that any more: a
	//    round-trip through a Go map alphabetizes every key, and the fields
	//    at stake — `network`, `sockets` — are bounded-core security
	//    declarations that the signing chain hashes. A trial that mutates
	//    the manifest is validating bytes the author never shipped, which
	//    is the one thing this command exists not to do.
	initArgs := []string{"dev", "init", "--name", id, "--template", tmpl,
		"--description", "Scaffold trial (safe to delete)",
		"--phrase", id, "--keybind", trialKeybind}
	if listener {
		initArgs = append(initArgs, "--listener")
	}
	if network {
		// Before the scaffold, not after: the probe's ports are what the
		// manifest has to declare, so they must exist at render time.
		probe, err = startNetworkProbe()
		if !t.record("network probe listeners", err, "") {
			finish()
			return
		}
		initArgs = append(initArgs, "--network-hosts", strings.Join(probe.declaredHosts(), ","))
	}
	out, err := runSelf(self, parent, initArgs...)
	if !t.record("scaffold (dev init)", errWithTail(err, out), fmt.Sprintf("%q", id+" branchkit")) {
		finish()
		return
	}

	if listener {
		if err := writeListenerProbe(dir); !t.record("listener probe attached (/ping on the granted listener)", err, "") {
			finish()
			return
		}
		out, err = runSelf(self, dir, "dev", "build")
		if !t.record("rebuild with sockets.listen (Node engine)", errWithTail(err, out), lastLine(out)) {
			finish()
			return
		}
	}
	if network {
		err = writeProbeSource(dir, tmpl, probe)
		directHost := ""
		if probe != nil {
			directHost = "direct on " + probe.directHost
		}
		if !t.record("network probe attached (3 loopback ports + a direct one off loopback, 3 declared)", err, directHost) {
			finish()
			return
		}
		out, err = runSelf(self, dir, "dev", "build")
		if !t.record("rebuild with the probe", errWithTail(err, out), lastLine(out)) {
			finish()
			return
		}
	}

	// 3. Its own tests: static checks, harness conformance, unit tests.
	out, err = runSelf(self, dir, "dev", "test", ".")
	t.record("dev test", errWithTail(err, out), lastLine(out))

	// 4. Install by link and let the app discover it. Anything the network
	//    probe counted so far came from the unsandboxed test harness.
	if probe != nil {
		probe.reset()
	}
	os.Remove(link)
	err = linkPluginDir(dir, link)
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

	// 6a. A declared listener has to be reachable from OUTSIDE the sandbox.
	if listener {
		checkListener(t, dir)
	}

	// 6b. The network probe runs at on_ready; judge it by what arrived.
	if network {
		var doneErr error
		if !probe.waitDone(40 * time.Second) {
			doneErr = fmt.Errorf("the plugin never reported finishing — its last proxied dial did not arrive")
		}
		t.record("network: the probe ran to completion", doneErr, "")
		probe.verdict(t)
		probe.checkRecord(t, token, id)
	}

	// 7. Matching, without executing anything.
	// The phrase `dev init` rendered in: --phrase is the plugin id.
	matched, err := resolvePreview(token, []string{id, "branchkit"})
	if err == nil && !matched {
		err = fmt.Errorf("%q did not resolve", id+" branchkit")
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
	fmt.Println("Usage: branchkit-cli dev trial --template go|ts|py [--listener | --network] [--keep]")
	fmt.Println("  Scaffolds a plugin, runs its tests, installs it in the running dev app,")
	fmt.Println("  approves it, and checks that it starts, matches and renders — then")
	fmt.Println("  removes it. Executes no command. Needs a dev build of BranchKit running.")
	fmt.Println()
	fmt.Println("  --listener   TypeScript only: declare sockets.listen, so the Node engine is built")
	fmt.Println("  --network    declare a host allowlist and probe it: a declared host must be")
	fmt.Println("               reachable through the proxy, an undeclared one refused, and a raw")
	fmt.Println("               socket refused by the sandbox. Loopback only; needs no internet.")
	fmt.Println("  --keep       leave the plugin installed and its folder in place")
}

// trialKeybind is a combination no scaffold, first-party plugin or OS
// shortcut claims — two trials must not bind the same key.
const trialKeybind = "ctrl+alt+shift+f11"

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

func manifestPrivileges(dir string) []string {
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return nil
	}
	var m struct {
		// The request block. Reading `privileges` from the top level
		// silently returned nil after the move, and the approval call then
		// asked for nothing — HTTP 422, caught by `dev trial` rather than
		// by any compiler, because a local anonymous struct is neither a
		// typed access nor a string literal.
		Requires struct {
			Privileges []string `json:"privileges"`
		} `json:"requires"`
	}
	json.Unmarshal(raw, &m)
	return m.Requires.Privileges
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
