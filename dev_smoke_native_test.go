package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// liveNativeBodies is what a healthy desktop answers to each probe.
var liveNativeBodies = map[string]string{
	"/v1/native/running-apps":   `{"apps":[{"name":"Finder"},{"name":"Terminal"}]}`,
	"/v1/native/displays":       `{"displays":[{"display_id":1,"w":1512,"h":982}]}`,
	"/v1/native/cursor-info":    `{"cursor_type":"arrow","x":10,"y":20}`,
	"/v1/native/input-sources":  `{"sources":[{"id":"us","name":"U.S.","is_active":true}]}`,
	"/v1/native/uptime-seconds": `{"seconds":86400}`,
	"/v1/native/computer-name":  `{"name":"a computer"}`,
	"/v1/native/frontmost-app":  `{"app":{"name":"Terminal"}}`,
	"/v1/native/volume":         `{"volume":0.5,"is_muted":false}`,
	"/v1/native/windows/ids":    `{"window_ids":["1","2"]}`,
}

// emptyNativeBodies is each probe's answer when the OS answers nothing.
var emptyNativeBodies = map[string]string{
	"/v1/native/running-apps":   `{"apps":[]}`,
	"/v1/native/displays":       `{"displays":[]}`,
	"/v1/native/input-sources":  `{"sources":[]}`,
	"/v1/native/uptime-seconds": `{"seconds":0}`,
	"/v1/native/frontmost-app":  `{"app":null}`,
	"/v1/native/windows/ids":    `{"window_ids":[]}`,
	// cursor-info, computer-name and volume answer 404 when they have
	// nothing, as the platform's getters do.
}

// nativeServer fakes the platform: each path answers from bodies, a path
// in statuses answers that status instead, and anything else 404s.
func nativeServer(t *testing.T, bodies map[string]string, statuses map[string]int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "GET" {
			t.Errorf("native probe used %s %s: every probe must be a read", req.Method, req.URL.Path)
		}
		if req.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("%s sent without the host token", req.URL.Path)
		}
		if code, ok := statuses[req.URL.Path]; ok {
			w.WriteHeader(code)
			return
		}
		body, ok := bodies[req.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	oldBase, oldSock, oldPipe := devBaseURL, devUnixSocket, devPipe
	devBaseURL, devUnixSocket, devPipe = srv.URL, "", ""
	t.Cleanup(func() {
		srv.Close()
		devBaseURL, devUnixSocket, devPipe = oldBase, oldSock, oldPipe
	})
}

func withBodies(base map[string]string, over map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		if v == "" {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	return out
}

func probeStatus(c smokeCheck, name string) string {
	for _, p := range c.Probes {
		if p.Name == name {
			return p.Status
		}
	}
	return "missing"
}

func TestNativeLivenessAllLivePasses(t *testing.T) {
	nativeServer(t, liveNativeBodies, nil)
	c := nativeLivenessCheck("tok")
	if c.Status != "pass" {
		t.Fatalf("status = %s (%s), want pass", c.Status, c.Detail)
	}
	if len(c.Probes) != len(nativeLivenessOps) {
		t.Fatalf("%d probes reported, want one per op (%d)", len(c.Probes), len(nativeLivenessOps))
	}
	for _, p := range c.Probes {
		if p.Status != "live" {
			t.Errorf("%s = %s (%s), want live", p.Name, p.Status, p.Detail)
		}
	}
	if len(c.NotCovered) == 0 {
		t.Error("the check must name what it does not probe")
	}
}

// The shape an obsoleted OS API makes: one read starts answering nothing
// while the rest still answer.
func TestNativeLivenessOneEmptyFails(t *testing.T) {
	nativeServer(t, withBodies(liveNativeBodies, map[string]string{
		"/v1/native/displays": `{"displays":[]}`,
	}), nil)
	c := nativeLivenessCheck("tok")
	if c.Status != "fail" {
		t.Fatalf("status = %s (%s), want fail", c.Status, c.Detail)
	}
	if !strings.Contains(c.Detail, "displays") {
		t.Errorf("detail %q does not name the dead op", c.Detail)
	}
	if got := probeStatus(c, "displays"); got != "empty" {
		t.Errorf("displays = %s, want empty", got)
	}
}

// A getter that answers 404 (its None) and one that errors both count dead.
func TestNativeLivenessNotFoundAndErrorFail(t *testing.T) {
	nativeServer(t, liveNativeBodies, map[string]int{
		"/v1/native/cursor-info":   404,
		"/v1/native/computer-name": 500,
	})
	c := nativeLivenessCheck("tok")
	if c.Status != "fail" {
		t.Fatalf("status = %s (%s), want fail", c.Status, c.Detail)
	}
	if got := probeStatus(c, "cursor"); got != "empty" {
		t.Errorf("cursor = %s, want empty", got)
	}
	if got := probeStatus(c, "computer name"); got != "error" {
		t.Errorf("computer name = %s, want error", got)
	}
}

func TestNativeLivenessAllEmptyWarns(t *testing.T) {
	nativeServer(t, emptyNativeBodies, nil)
	c := nativeLivenessCheck("tok")
	if c.Status != "warn" {
		t.Fatalf("status = %s (%s), want warn", c.Status, c.Detail)
	}
	for _, p := range c.Probes {
		if p.Status != "empty" {
			t.Errorf("%s = %s (%s), want empty", p.Name, p.Status, p.Detail)
		}
	}
}

// An op this OS or session does not offer (501) says nothing about
// liveness, and a read whose empty answer is a real state (nothing
// focused, no audio device) is not counted dead.
func TestNativeLivenessUnsupportedAndRealEmptyPass(t *testing.T) {
	nativeServer(t, withBodies(liveNativeBodies, map[string]string{
		"/v1/native/frontmost-app": `{"app":null}`,
		"/v1/native/volume":        "",
	}), map[string]int{
		"/v1/native/cursor-info": 501,
	})
	c := nativeLivenessCheck("tok")
	if c.Status != "pass" {
		t.Fatalf("status = %s (%s), want pass", c.Status, c.Detail)
	}
	if got := probeStatus(c, "cursor"); got != "unsupported" {
		t.Errorf("cursor = %s, want unsupported", got)
	}
	if !strings.Contains(c.Detail, "not offered here: cursor") {
		t.Errorf("detail %q does not name the op this OS lacks", c.Detail)
	}
	if got := probeStatus(c, "frontmost app"); got != "empty" {
		t.Errorf("frontmost app = %s, want empty", got)
	}
}

func TestNativeLivenessImplausibleAnswersAreEmpty(t *testing.T) {
	cases := map[string]string{
		"/v1/native/displays":    `{"displays":[{"w":0,"h":0}]}`,
		"/v1/native/volume":      `{"volume":-1}`,
		"/v1/native/cursor-info": `{"cursor_type":"arrow"}`,
	}
	for path, body := range cases {
		t.Run(path, func(t *testing.T) {
			nativeServer(t, withBodies(liveNativeBodies, map[string]string{path: body}), nil)
			c := nativeLivenessCheck("tok")
			for _, p := range c.Probes {
				if p.Path == path && p.Status != "empty" {
					t.Errorf("%s = %s (%s), want empty", path, p.Status, p.Detail)
				}
			}
		})
	}
}

// A machine with no desktop session (SSH, a VM with no seat): every
// desktop read answers nothing while uptime and the computer name still
// answer. That is the machine's state, not an obsoleted API, so it warns.
func TestNativeLivenessNoDesktopWarns(t *testing.T) {
	nativeServer(t, withBodies(emptyNativeBodies, map[string]string{
		"/v1/native/uptime-seconds": `{"seconds":120}`,
		"/v1/native/computer-name":  `{"name":"a computer"}`,
	}), nil)
	c := nativeLivenessCheck("tok")
	if c.Status != "warn" {
		t.Fatalf("status = %s (%s), want warn", c.Status, c.Detail)
	}
	if !strings.Contains(c.Detail, "no desktop") {
		t.Errorf("detail %q does not say the desktop is missing", c.Detail)
	}
}

// The system reads dying under a working desktop has no benign cause.
func TestNativeLivenessSystemDeadUnderDesktopFails(t *testing.T) {
	nativeServer(t, withBodies(liveNativeBodies, map[string]string{
		"/v1/native/uptime-seconds": `{"seconds":0}`,
		"/v1/native/computer-name":  "",
	}), nil)
	c := nativeLivenessCheck("tok")
	if c.Status != "fail" {
		t.Fatalf("status = %s (%s), want fail", c.Status, c.Detail)
	}
}

// PulseAudio and PipeWire report a sink amplified past 100% above 1.0.
func TestNativeLivenessAmplifiedVolumeIsLive(t *testing.T) {
	nativeServer(t, withBodies(liveNativeBodies, map[string]string{
		"/v1/native/volume": `{"volume":1.2,"is_muted":false}`,
	}), nil)
	c := nativeLivenessCheck("tok")
	if got := probeStatus(c, "volume"); got != "live" {
		t.Errorf("volume = %s, want live", got)
	}
}
