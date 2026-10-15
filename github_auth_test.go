package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// setGitHubToken fixes githubToken's answer for one test, so no test reads
// the developer's own environment or gh login.
func setGitHubToken(t *testing.T, token string) {
	t.Helper()
	githubTokenOnce.Do(func() {})
	prev := githubTokenValue
	githubTokenValue = token
	t.Cleanup(func() { githubTokenValue = prev })
}

// fakeGitHub answers like a private repository: 404 without a token, 200
// with one. It records each request's path, Accept and Authorization.
type seen struct{ path, accept, auth string }

func fakeGitHub(t *testing.T, status func(r *http.Request) int) (*httptest.Server, *[]seen) {
	t.Helper()
	var log []seen
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log = append(log, seen{r.URL.Path, r.Header.Get("Accept"), r.Header.Get("Authorization")})
		w.WriteHeader(status(r))
	}))
	t.Cleanup(srv.Close)
	return srv, &log
}

// asGitHub points a client at srv whatever host a request names, so a
// request to https://api.github.com reaches the fake.
func asGitHub(srv *httptest.Server) *http.Client {
	c := srv.Client()
	tr := c.Transport.(*http.Transport).Clone()
	addr := srv.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	tr.TLSClientConfig.InsecureSkipVerify = true
	c.Transport = tr
	return c
}

func private(r *http.Request) int {
	if r.Header.Get("Authorization") == "" {
		return http.StatusNotFound
	}
	return http.StatusOK
}

func TestAPublicRepositoryNeverSeesTheToken(t *testing.T) {
	setGitHubToken(t, "secret")
	srv, log := fakeGitHub(t, func(*http.Request) int { return http.StatusOK })
	req, _ := newGitHubRequest("https://api.github.com/repos/o/r/releases/latest")
	resp, err := githubDo(asGitHub(srv), req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(*log) != 1 || (*log)[0].auth != "" {
		t.Fatalf("requests = %+v, want one anonymous request", *log)
	}
}

func TestAPrivateRepositoryIsRetriedWithTheToken(t *testing.T) {
	setGitHubToken(t, "secret")
	srv, log := fakeGitHub(t, private)
	req, _ := newGitHubRequest("https://api.github.com/repos/o/r/releases/latest")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := githubDo(asGitHub(srv), req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	want := []seen{
		{"/repos/o/r/releases/latest", "application/vnd.github+json", ""},
		{"/repos/o/r", "", ""}, // is the repository itself visible? no: private
		{"/repos/o/r/releases/latest", "application/vnd.github+json", "Bearer secret"},
	}
	if !equalSeen(*log, want) {
		t.Fatalf("requests = %+v, want %+v", *log, want)
	}
}

func equalSeen(a, b []seen) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAPrivateAssetIsRetriedThroughTheAPIEndpoint(t *testing.T) {
	setGitHubToken(t, "secret")
	srv, log := fakeGitHub(t, private)
	req, _ := newAssetRequest(ghAsset{
		Name:               "x.tar.gz",
		BrowserDownloadURL: "https://github.com/o/r/releases/download/v1/x.tar.gz",
		URL:                "https://api.github.com/repos/o/r/releases/assets/7",
	})
	resp, err := githubDo(asGitHub(srv), req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	want := []seen{
		{"/o/r/releases/download/v1/x.tar.gz", "", ""},
		{"/repos/o/r", "", ""},
		{"/repos/o/r/releases/assets/7", "application/octet-stream", "Bearer secret"},
	}
	if !equalSeen(*log, want) {
		t.Fatalf("requests = %+v, want %+v", *log, want)
	}
}

func TestNoTokenMeansNoRetry(t *testing.T) {
	setGitHubToken(t, "")
	srv, log := fakeGitHub(t, private)
	req, _ := newGitHubRequest("https://api.github.com/repos/o/r")
	resp, err := githubDo(asGitHub(srv), req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || len(*log) != 1 {
		t.Fatalf("status %d after %d requests, want one 404", resp.StatusCode, len(*log))
	}
}

// A public repository with no release, a missing tag or a missing file also
// answers 404. Its visibility answers anonymously, so no token is sent.
func TestAPublicRepositorysMissingReleaseNeverSeesTheToken(t *testing.T) {
	setGitHubToken(t, "secret")
	srv, log := fakeGitHub(t, func(r *http.Request) int {
		if r.URL.Path == "/repos/o/r" {
			return http.StatusOK
		}
		return http.StatusNotFound
	})
	req, _ := newGitHubRequest("https://api.github.com/repos/o/r/releases/latest")
	resp, err := githubDo(asGitHub(srv), req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, s := range *log {
		if s.auth != "" {
			t.Fatalf("the token went to a public repository: %+v", *log)
		}
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want the 404", resp.StatusCode)
	}
}

func TestASpentAnonymousRateLimitRetriesWithTheToken(t *testing.T) {
	setGitHubToken(t, "secret")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	req, _ := newGitHubRequest("https://api.github.com/repos/o/r/releases/latest")
	resp, err := githubDo(asGitHub(srv), req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200 after the authenticated retry", resp.StatusCode)
	}
}

// A refused token (expired, revoked) must leave the anonymous answer in
// place, so the caller still shows its usual message.
func TestARefusedTokenReturnsTheAnonymousAnswer(t *testing.T) {
	setGitHubToken(t, "stale")
	srv, _ := fakeGitHub(t, func(r *http.Request) int {
		if r.Header.Get("Authorization") != "" {
			return http.StatusUnauthorized
		}
		return http.StatusNotFound
	})
	req, _ := newGitHubRequest("https://api.github.com/repos/o/r/releases/latest")
	resp, err := githubDo(asGitHub(srv), req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want the anonymous 404", resp.StatusCode)
	}
	if githubToken() != "" {
		t.Fatal("the refused token is still in use")
	}
}

func TestTheTokenNeverLeavesGitHubsHosts(t *testing.T) {
	setGitHubToken(t, "secret")
	for _, u := range []string{
		"https://github.com/o/r/releases/download/v1/a.tar.gz",
		"https://objects.githubusercontent.com/signed",
		"https://api.github.com.evil.example/repos",
		"http://api.github.com/repos/o/r",
		"https://example.com/a",
	} {
		req, _ := newGitHubRequest(u)
		if _, ok := authenticatedRetry(req); ok {
			t.Errorf("%s: would retry with the token", u)
		}
	}
	for _, u := range []string{
		"https://api.github.com/repos/o/r",
		"https://raw.githubusercontent.com/o/r/v1/plugin.json",
	} {
		req, _ := newGitHubRequest(u)
		if _, ok := authenticatedRetry(req); !ok {
			t.Errorf("%s: would not retry with the token", u)
		}
	}
}

// The API asset endpoint redirects to a storage host with a signed URL. The
// token must not travel with that redirect; Go's client drops Authorization
// when the redirect changes host, and this holds the CLI to it.
func TestAuthorizationIsNotForwardedAcrossHosts(t *testing.T) {
	var storageAuth string
	storageHit := false
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		storageHit = true
		storageAuth = r.Header.Get("Authorization")
		w.Write([]byte("file"))
	}))
	defer storage.Close()
	// The API answers on 127.0.0.1 and redirects to localhost: another host.
	target := strings.Replace(storage.URL, "127.0.0.1", "localhost", 1) + "/signed"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("the first hop lost its authorization")
		}
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer api.Close()

	req, _ := http.NewRequest("GET", api.URL+"/asset", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !storageHit {
		t.Fatal("the redirect never reached the storage host")
	}
	if storageAuth != "" {
		t.Fatalf("authorization forwarded to the redirect host: %q", storageAuth)
	}
}
