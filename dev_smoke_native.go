package main

// The native liveness check of `dev smoke`: does the OS still answer?
//
// The capture check asks one family of native calls. Every other native
// call can stop answering the same way capture did on macOS 15.5: an OS
// API is obsoleted, still links, still returns, and returns nothing, which
// reads as an empty list or "no app". Proving an implementation exists
// cannot see that; only asking can. So this asks a set of reads that need
// no permission prompt and change nothing, and requires each answer to be
// plausible for a computer someone is sitting at (at least one display, at
// least one running app, a cursor somewhere).
//
// The signal is the capture check's split, taken within each group of
// reads that share a precondition. The desktop reads (apps, displays,
// cursor, input sources, windows) all need a desktop session; the system
// reads (uptime, computer name) answer without one, and so may the volume
// (Linux reads it from the audio server), which is a group of its own. A whole group dead is
// the machine's state (no desktop session; a build with no OS behind it),
// so it warns, except the system reads dying under a working desktop,
// which no machine state explains. Some reads of one group answering and
// some not is the shape an obsoleted API makes, and it fails.

import (
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strings"
)

// smokeGOOS is the OS the probed app runs on: the CLI's own, since it
// finds the app through a local address file. A variable so tests can set
// it.
var smokeGOOS = runtime.GOOS

// nativeProbe is one read's result, reported per op.
type nativeProbe struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// live: a plausible answer. empty: an answer of nothing. error: the
	// call failed. unsupported: this OS or session does not offer the op
	// (501), so it says nothing about liveness.
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	// MayBeEmpty marks a read whose empty answer is a real state (nothing
	// focused, no audio device), so an empty answer is not counted dead.
	MayBeEmpty bool `json:"may_be_empty,omitempty"`
}

type nativeLivenessOp struct {
	name string
	path string
	// group is the precondition the read shares with others: "desktop"
	// (a desktop session), "audio" (an audio service) or "system" (none).
	// The split is taken per group.
	group      string
	mayBeEmpty bool
	// emptyWith names another probe and emptyWithOS the OSes where, when
	// both answer empty, this one's empty is real. Linux and Windows list
	// the apps that have a window, so no window open means no app. Both
	// reads come from one window list there, so a window list that stopped
	// answering looks the same, and this check cannot tell them apart.
	// macOS lists apps from the workspace, where an empty list is never
	// real.
	emptyWith   string
	emptyWithOS []string
	// judge reads a 200 body and returns (plausible, detail).
	judge func(raw []byte) (bool, string)
}

// nativeLivenessOps are the reads the check asks. Each is a GET: no
// arguments, no state changed, no permission prompt on any OS (nothing
// here touches contacts, calendars, location, camera, microphone or
// Bluetooth).
var nativeLivenessOps = []nativeLivenessOp{
	{name: "running apps", path: "/v1/native/running-apps", group: "desktop", emptyWith: "window ids", emptyWithOS: []string{"linux", "windows"}, judge: judgeList("apps", "app")},
	{name: "displays", path: "/v1/native/displays", group: "desktop", judge: judgeDisplays},
	{name: "cursor", path: "/v1/native/cursor-info", group: "desktop", judge: judgeCursor},
	{name: "input sources", path: "/v1/native/input-sources", group: "desktop", judge: judgeList("sources", "source")},
	{name: "frontmost app", path: "/v1/native/frontmost-app", group: "desktop", mayBeEmpty: true, judge: judgeFrontmost},
	{name: "volume", path: "/v1/native/volume", group: "audio", mayBeEmpty: true, judge: judgeVolume},
	{name: "window ids", path: "/v1/native/windows/ids", group: "desktop", mayBeEmpty: true, judge: judgeList("window_ids", "window")},
	{name: "uptime", path: "/v1/native/uptime-seconds", group: "system", judge: judgeUptime},
	{name: "computer name", path: "/v1/native/computer-name", group: "system", judge: judgeNonEmptyString("name")},
}

// nativeNotProbed names what a side-effect-free sweep deliberately leaves
// out, so the gap is printed rather than hidden: an op that acts cannot be
// asked without acting, and a read that prompts would put a dialog in
// front of the person.
var nativeNotProbed = []string{
	"input: type, key, click, scroll, drag, warp cursor",
	"windows: move, resize, raise, focus, minimize, close",
	"apps: launch, activate, hide, quit",
	"system setters: volume, mute, brightness, dark mode, input source, do not disturb",
	"clipboard write, menu clicks, accessibility actions and attribute writes",
	"files: write, copy, move, delete",
	"reads that prompt: contacts, calendar, location, camera, microphone, Bluetooth",
	"reads that take arguments (a window, an app, a path)",
}

func pluralize(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}

func judgeList(field, noun string) func([]byte) (bool, string) {
	return func(raw []byte) (bool, string) {
		var body map[string]json.RawMessage
		if json.Unmarshal(raw, &body) != nil {
			return false, "not a JSON object"
		}
		var items []json.RawMessage
		if json.Unmarshal(body[field], &items) != nil {
			return false, fmt.Sprintf("no %q list", field)
		}
		return len(items) > 0, pluralize(len(items), noun)
	}
}

func judgeDisplays(raw []byte) (bool, string) {
	var body struct {
		Displays []struct {
			W int `json:"w"`
			H int `json:"h"`
		} `json:"displays"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return false, "not a display list"
	}
	if len(body.Displays) == 0 {
		return false, "0 displays"
	}
	for _, d := range body.Displays {
		if d.W <= 0 || d.H <= 0 {
			return false, fmt.Sprintf("a display of %dx%d", d.W, d.H)
		}
	}
	return true, pluralize(len(body.Displays), "display")
}

func judgeCursor(raw []byte) (bool, string) {
	var body struct {
		X *int `json:"x"`
		Y *int `json:"y"`
	}
	if json.Unmarshal(raw, &body) != nil || body.X == nil || body.Y == nil {
		return false, "no position"
	}
	return true, "position read"
}

func judgeUptime(raw []byte) (bool, string) {
	var body struct {
		Seconds uint64 `json:"seconds"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Seconds == 0 {
		return false, "0 seconds"
	}
	return true, fmt.Sprintf("%d s", body.Seconds)
}

func judgeNonEmptyString(field string) func([]byte) (bool, string) {
	return func(raw []byte) (bool, string) {
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil {
			return false, "not a JSON object"
		}
		s, _ := body[field].(string)
		if strings.TrimSpace(s) == "" {
			return false, "empty " + field
		}
		// The value itself is the person's: say it was read, not what it is.
		return true, "read"
	}
}

func judgeFrontmost(raw []byte) (bool, string) {
	var body struct {
		App json.RawMessage `json:"app"`
	}
	if json.Unmarshal(raw, &body) != nil || len(body.App) == 0 || string(body.App) == "null" {
		return false, "none"
	}
	return true, "read"
}

func judgeVolume(raw []byte) (bool, string) {
	var body struct {
		Volume *float64 `json:"volume"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Volume == nil {
		return false, "no level"
	}
	// Above 1 is real: PulseAudio and PipeWire amplify past 100%.
	if *body.Volume < 0 {
		return false, fmt.Sprintf("level %v below 0", *body.Volume)
	}
	return true, "level read"
}

// probeNative asks one op and classifies the answer.
func probeNative(token string, op nativeLivenessOp) nativeProbe {
	p := nativeProbe{Name: op.name, Path: op.path, MayBeEmpty: op.mayBeEmpty}
	raw, status, err := devHTTP("GET", op.path, token, nil)
	switch {
	case err != nil:
		p.Status, p.Detail = "error", err.Error()
	case status == 501:
		// The platform answers 501 before the handler runs for an op its
		// OS or session tables refuse (the cursor on a Wayland session).
		p.Status, p.Detail = "unsupported", "not offered on this OS or session"
	case status == 404:
		// A getter with nothing to report answers 404 with no body.
		p.Status, p.Detail = "empty", "no answer (404)"
	case status != 200:
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 120 {
			msg = msg[:120] + "…"
		}
		p.Status, p.Detail = "error", fmt.Sprintf("HTTP %d %s", status, msg)
	default:
		ok, detail := op.judge(raw)
		p.Detail = detail
		if ok {
			p.Status = "live"
		} else {
			p.Status = "empty"
		}
	}
	return p
}

// nativeLivenessCheck runs every probe and returns the check with each
// probe's result and the list of what was deliberately not probed.
func nativeLivenessCheck(token string) smokeCheck {
	type tally struct {
		live int
		dead []string
	}
	groups := map[string]*tally{"desktop": {}, "audio": {}, "system": {}}
	var probes []nativeProbe
	status := map[string]string{}
	for _, op := range nativeLivenessOps {
		p := probeNative(token, op)
		probes = append(probes, p)
		status[op.name] = p.Status
	}
	var unsupported []string
	for i, op := range nativeLivenessOps {
		p := &probes[i]
		if p.Status == "empty" && op.emptyWith != "" && slices.Contains(op.emptyWithOS, smokeGOOS) &&
			status[op.emptyWith] == "empty" {
			p.MayBeEmpty = true
			p.Detail += " (and no " + op.emptyWith + ")"
		}
		g := groups[op.group]
		switch {
		case p.Status == "live":
			g.live++
		case p.Status == "unsupported":
			unsupported = append(unsupported, p.Name)
		case p.Status == "empty" && p.MayBeEmpty:
			// A real state, so neither live nor dead.
		default:
			g.dead = append(g.dead, p.Name)
		}
	}
	desktop, audio, system := groups["desktop"], groups["audio"], groups["system"]

	c := smokeCheck{Name: "native", Probes: probes, NotCovered: nativeNotProbed}
	note := ""
	if len(unsupported) > 0 {
		note = fmt.Sprintf("; not offered here: %s", strings.Join(unsupported, ", "))
	}
	// Within one group, some answering and some not.
	var split []string
	splitTotal := 0
	for _, g := range []*tally{desktop, audio, system} {
		if g.live > 0 && len(g.dead) > 0 {
			split = append(split, g.dead...)
			splitTotal += g.live + len(g.dead)
		}
	}
	live := desktop.live + audio.live + system.live
	switch {
	case len(split) > 0:
		c.Status = "fail"
		c.Detail = fmt.Sprintf(
			"%d of %d read-only ops returned nothing while others needing the same answered (%s) — an OS path has stopped answering%s",
			len(split), splitTotal, strings.Join(split, ", "), note)
	case live == 0 && len(desktop.dead)+len(audio.dead)+len(system.dead) == 0:
		c.Status = "warn"
		c.Detail = "no read-only op could be judged" + note
	case live == 0:
		c.Status = "warn"
		c.Detail = fmt.Sprintf(
			"no read-only op answered (%s) — the native layer is not answering at all%s",
			strings.Join(append(append(desktop.dead, audio.dead...), system.dead...), ", "), note)
	case len(system.dead) > 0:
		// No machine state explains this one: other reads answer, uptime does not.
		c.Status = "fail"
		c.Detail = fmt.Sprintf(
			"no system read answered (%s) while other reads answered — an OS path has stopped answering%s",
			strings.Join(system.dead, ", "), note)
	case len(audio.dead) > 0:
		// The volume is may-be-empty, so it is dead only when the call
		// failed, which no machine state explains while others answer.
		c.Status = "fail"
		c.Detail = fmt.Sprintf(
			"the volume read failed while other reads answered — an OS path has stopped answering%s", note)
	case len(desktop.dead) > 0:
		c.Status = "warn"
		c.Detail = fmt.Sprintf(
			"no desktop read answered (%s) while the system reads did — no desktop session?%s",
			strings.Join(desktop.dead, ", "), note)
	default:
		c.Status = "pass"
		c.Detail = fmt.Sprintf("all %d judged read-only ops answered%s", live, note)
	}
	return c
}

// printNativeProbes is the human rendering of a native check's probes and
// of what it did not cover, printed under the check's own line.
func printNativeProbes(c smokeCheck) {
	for _, p := range c.Probes {
		status := p.Status
		if p.Status == "empty" && p.MayBeEmpty {
			status = "empty (can be real)"
		}
		fmt.Printf("      %-20s %-15s %s\n", status, p.Name, p.Detail)
	}
	if len(c.NotCovered) > 0 {
		fmt.Println("      not probed, because asking would act or prompt:")
		for _, n := range c.NotCovered {
			fmt.Printf("        - %s\n", n)
		}
	}
}
