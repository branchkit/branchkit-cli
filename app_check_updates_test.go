package main

// `app check-updates` against a fake api.github.com (asGitHub): every answer
// the latest-release endpoint can give, and what the app reads from each.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type checkRun struct {
	out      appUpdateCheck
	stderr   string
	requests []seen
}

// runCheck runs `app check-updates args...` on goos against a GitHub whose
// latest-release endpoint answers with handle.
func runCheck(t *testing.T, goos string, env map[string]string, handle http.HandlerFunc, args ...string) (checkRun, error) {
	t.Helper()
	setGitHubToken(t, "a token the check must never send")
	var log []seen
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log = append(log, seen{r.URL.Path, r.Header.Get("Accept"), r.Header.Get("Authorization")})
		handle(w, r)
	}))
	t.Cleanup(srv.Close)
	var stdout, stderr bytes.Buffer
	err := runAppCheckUpdates(args, appEnv{
		client: asGitHub(srv),
		stall:  2 * time.Second,
		goos:   goos,
		goarch: "arm64",
		getenv: func(k string) string { return env[k] },
		stdout: &stdout,
		stderr: &stderr,
	})
	run := checkRun{stderr: stderr.String(), requests: log}
	if err == nil {
		if jerr := json.Unmarshal(stdout.Bytes(), &run.out); jerr != nil {
			t.Fatalf("stdout is not the JSON: %v\n%s", jerr, stdout.String())
		}
	}
	return run, err
}

func latestIs(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+appReleaseRepo+"/releases/latest" {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.Write([]byte(body))
	}
}

func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }
}

// summary is the last line of stderr, which the app reads.
func (c checkRun) summary() string {
	lines := strings.Split(strings.TrimSpace(c.stderr), "\n")
	return lines[len(lines)-1]
}

const reachedLine, unreachedLine = "CHECKED reached=1 unreached=0", "CHECKED reached=0 unreached=1"

func TestAppCheckUpdatesFindsANewerRelease(t *testing.T) {
	run, err := runCheck(t, "darwin", nil, latestIs(`{"tag_name":"v0.2.0","html_url":"https://evil.example/x"}`), "--current", "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	want := appUpdateCheck{Current: "0.1.0", Latest: "0.2.0", Newer: true,
		NotesURL: "https://github.com/branchkit/releases/releases/tag/v0.2.0", Apply: applySelf}
	if run.out != want {
		t.Errorf("got %+v, want %+v", run.out, want)
	}
	if run.summary() != reachedLine {
		t.Errorf("summary %q", run.summary())
	}
	if len(run.requests) != 1 || run.requests[0].accept != "application/vnd.github+json" {
		t.Errorf("requests = %+v, want the latest release once", run.requests)
	}
}

func TestAppCheckUpdatesSameAndOlderAreNotNewer(t *testing.T) {
	for _, current := range []string{"0.2.0", "v0.2.0", "0.10.0", "1.0.0"} {
		run, err := runCheck(t, "windows", nil, latestIs(`{"tag_name":"v0.2.0"}`), "--current="+current)
		if err != nil {
			t.Fatal(err)
		}
		if run.out.Newer || run.out.Latest != "0.2.0" || run.summary() != reachedLine {
			t.Errorf("installed %s: %+v, %q", current, run.out, run.summary())
		}
	}
}

// branchkit/releases has no release (or does not exist yet): GitHub's 404
// is an answer.
func TestAppCheckUpdatesNoReleaseYetIsReached(t *testing.T) {
	run, err := runCheck(t, "darwin", nil, status(http.StatusNotFound), "--current", "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if run.out.Newer || run.out.Latest != "" || run.out.NotesURL != "" || run.summary() != reachedLine {
		t.Errorf("%+v, %q", run.out, run.summary())
	}
	// githubDo would have probed the repository and retried with the token.
	if len(run.requests) != 1 {
		t.Errorf("requests = %+v, want one", run.requests)
	}
}

func TestAppCheckUpdatesNeverSendsAToken(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"found":   latestIs(`{"tag_name":"v0.2.0"}`),
		"missing": status(http.StatusNotFound),
		// A spent anonymous limit, as GitHub reports it: githubDo would
		// retry this one with the person's token.
		"rate limited": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
		},
	} {
		run, _ := runCheck(t, "darwin", nil, h, "--current", "0.1.0")
		for _, r := range run.requests {
			if r.auth != "" {
				t.Errorf("%s: %s carried %q", name, r.path, r.auth)
			}
		}
	}
}

// What GitHub cannot answer is an unreached check: still JSON and exit 0,
// the summary saying it did not count.
func TestAppCheckUpdatesUnreached(t *testing.T) {
	stalled := func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}
	for name, h := range map[string]http.HandlerFunc{
		"server error":   status(http.StatusBadGateway),
		"unavailable":    status(http.StatusServiceUnavailable),
		"rate limited":   status(http.StatusForbidden),
		"malformed JSON": latestIs(`{"tag_name": "v0.2.0"`),
		"portal page":    latestIs(`<html>Sign in to the Wi-Fi</html>`),
		"no tag":         latestIs(`{}`),
		"stalled body":   stalled,
	} {
		run, err := runCheck(t, "darwin", nil, h, "--current", "0.1.0")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if run.out.Newer || run.out.Latest != "" || run.summary() != unreachedLine {
			t.Errorf("%s: %+v, %q", name, run.out, run.summary())
		}
		if !strings.Contains(run.stderr, "WARN: could not check branchkit/releases") {
			t.Errorf("%s: stderr %q names no reason", name, run.stderr)
		}
	}
}

func TestAppCheckUpdatesOffline(t *testing.T) {
	var stdout, stderr bytes.Buffer
	srv := httptest.NewTLSServer(status(http.StatusOK))
	client := asGitHub(srv)
	srv.Close() // nothing listens any more
	err := runAppCheckUpdates([]string{"--current", "0.1.0"}, appEnv{
		client: client, goos: "darwin", getenv: func(string) string { return "" },
		stdout: &stdout, stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.TrimSpace(stderr.String()), unreachedLine) {
		t.Errorf("stderr %q", stderr.String())
	}
	var out appUpdateCheck
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || out.Newer {
		t.Errorf("stdout %q (%v)", stdout.String(), err)
	}
}

// GitHub's latest endpoint skips pre-releases, but the check does not rely
// on it: a pre-release, a draft or a tag that is not vX.Y.Z is no update.
func TestAppCheckUpdatesIgnoresPrereleases(t *testing.T) {
	for _, body := range []string{
		`{"tag_name":"v0.3.0","prerelease":true}`,
		`{"tag_name":"v0.3.0","draft":true}`,
		`{"tag_name":"v0.3.0-rc.1"}`,
		`{"tag_name":"0.3.0"}`,
		`{"tag_name":"v0.3"}`,
	} {
		run, err := runCheck(t, "darwin", nil, latestIs(body), "--current", "0.1.0")
		if err != nil {
			t.Fatal(err)
		}
		if run.out.Newer || run.out.Latest != "" || run.summary() != reachedLine {
			t.Errorf("%s: %+v, %q", body, run.out, run.summary())
		}
	}
}

func TestAppCheckUpdatesApply(t *testing.T) {
	for _, tc := range []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"darwin", nil, applySelf},
		{"windows", nil, applySelf},
		{"linux", nil, applyPackageManager},
		{"linux", map[string]string{"APPIMAGE": "/home/t/BranchKit-0.1.0-x86_64.AppImage"}, applySelf},
	} {
		run, err := runCheck(t, tc.goos, tc.env, latestIs(`{"tag_name":"v0.2.0"}`), "--current", "0.1.0")
		if err != nil {
			t.Fatal(err)
		}
		if run.out.Apply != tc.want {
			t.Errorf("%s %v: apply %q, want %q", tc.goos, tc.env, run.out.Apply, tc.want)
		}
	}
}

func TestAppCheckUpdatesCurrent(t *testing.T) {
	run, err := runCheck(t, "darwin", map[string]string{"BRANCHKIT_APP_VERSION": "0.2.0"}, latestIs(`{"tag_name":"v0.2.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	if run.out.Current != "0.2.0" || run.out.Newer {
		t.Errorf("from the environment: %+v", run.out)
	}
	for _, args := range [][]string{
		nil,                       // unknown
		{"--current"},             // no value
		{"--current="},            // empty
		{"--current", "0.2"},      // not X.Y.Z
		{"--current", "0.2.0-rc"}, // a pre-release is never installed
		{"--version", "0.2.0"},    // not this command's flag
	} {
		run, err := runCheck(t, "darwin", nil, latestIs(`{"tag_name":"v0.2.0"}`), args...)
		if err == nil {
			t.Errorf("%v: accepted", args)
		}
		if len(run.requests) != 0 {
			t.Errorf("%v: asked GitHub before refusing", args)
		}
	}
}
