package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// GitHub credentials for private plugin repositories.
//
// Every request goes out anonymously first, so installing, previewing or
// checking updates for a public plugin never tells GitHub who is asking. A
// private repository answers an anonymous request with 404; only then, once
// the repository itself proves invisible, and only when the person has a
// token, the request is sent once more with it (githubDo). The repository
// installs for them and nobody else.
//
// The token goes only to GitHub's own hosts (githubAuthHosts). Release asset
// downloads redirect to a storage host with a signed URL; Go's client drops
// the Authorization header on a redirect to a different host, and
// TestAuthorizationIsNotForwardedAcrossHosts holds that.

// githubAuthHosts are the hosts a token is ever sent to.
var githubAuthHosts = map[string]bool{
	"api.github.com":            true,
	"raw.githubusercontent.com": true,
}

var (
	githubTokenOnce  sync.Once
	githubTokenValue string
)

// githubToken returns the person's GitHub token, or "" when there is none.
// It is read only when githubDo needs it.
//
// GITHUB_TOKEN, then GH_TOKEN, then the token the gh CLI stores. The last
// matters most: when the app checks plugin updates it runs this CLI in the
// app's environment, which carries no variables from the person's shell, so
// only gh's stored login reaches a private repository from there.
func githubToken() string {
	githubTokenOnce.Do(func() {
		for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
			if t := strings.TrimSpace(os.Getenv(name)); t != "" {
				githubTokenValue = t
				return
			}
		}
		githubTokenValue = ghStoredToken()
	})
	return githubTokenValue
}

// ghStoredToken asks the gh CLI for its stored token. An app launched from
// the Dock or a login item has a minimal PATH, so the usual install places are
// tried after PATH.
func ghStoredToken() string {
	candidates := []string{}
	if p, err := exec.LookPath("gh"); err == nil {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, "/opt/homebrew/bin/gh", "/usr/local/bin/gh", "/usr/bin/gh")
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".local", "bin", "gh"))
	}
	for _, gh := range candidates {
		if _, err := os.Stat(gh); err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := exec.CommandContext(ctx, gh, "auth", "token", "--hostname", "github.com").Output()
		cancel()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
		return "" // gh is installed but not signed in: no other copy will know more
	}
	return ""
}

// apiAssetURLKey carries an asset request's API endpoint, the one a token can
// download a private repository's asset from.
type apiAssetURLKey struct{}

// newGitHubRequest builds an anonymous GET with the CLI's User-Agent.
func newGitHubRequest(rawURL string) (*http.Request, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "branchkit-cli")
	return req, nil
}

// newAssetRequest builds the download request for a release asset: the
// public download URL, remembering the API endpoint for githubDo.
// browser_download_url serves a private repository's asset to a browser
// session only, never to a token, so the authenticated retry goes through
// the API endpoint (Accept: application/octet-stream redirects to the file).
func newAssetRequest(a ghAsset) (*http.Request, error) {
	req, err := newGitHubRequest(a.BrowserDownloadURL)
	if err != nil || a.URL == "" {
		return req, err
	}
	return req.WithContext(context.WithValue(req.Context(), apiAssetURLKey{}, a.URL)), nil
}

// githubDo sends a request built by newGitHubRequest or newAssetRequest,
// anonymously, and retries it once with the person's token only when that is
// the one way to an answer:
//
//   - 404 from a repository GitHub will not show anonymously. That is what
//     a private repository looks like; a public repository with no release,
//     a missing tag or a missing file answers 404 too, so the repository's
//     visibility is asked first (anonymously) and a visible one never gets
//     the token.
//   - 403 (or 429) because the anonymous rate limit is spent (60 an hour per
//     address), which no amount of waiting fixes inside one run. A spent
//     limit also answers the visibility probe, which then counts as private.
//
// A token GitHub refuses (401) is reported once, and the anonymous answer is
// returned so callers keep their usual messages.
func githubDo(client *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil {
		return resp, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		if githubToken() == "" || repoVisibleAnonymously(client, req.URL) {
			return resp, nil
		}
	case rateLimited(resp):
	default:
		return resp, nil
	}
	authed, ok := authenticatedRetry(req)
	if !ok {
		return resp, nil
	}
	resp.Body.Close()
	resp, err = client.Do(authed)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	resp.Body.Close()
	fmt.Fprintln(os.Stderr, "GitHub refused this machine's token (401). Run `gh auth login` or update GITHUB_TOKEN.")
	githubTokenValue = ""
	anon, err := newGitHubRequest(req.URL.String())
	if err != nil {
		return nil, err
	}
	for k, v := range req.Header {
		anon.Header[k] = v
	}
	return client.Do(anon)
}

// repoVisibleAnonymously reports whether the repository a GitHub URL names
// answers an anonymous request: true means public, so a 404 under it is a
// real absence, not privacy. URLs that name no repository count as visible,
// which keeps the token away from them.
func repoVisibleAnonymously(client *http.Client, u *url.URL) bool {
	owner, repo, ok := repoOf(u)
	if !ok {
		return true
	}
	req, err := newGitHubRequest(fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo))
	if err != nil {
		return true
	}
	resp, err := client.Do(req)
	if err != nil {
		return true
	}
	resp.Body.Close()
	// A spent anonymous quota answers the probe too: it says nothing about
	// visibility, and the anonymous 404 being asked about is then the only
	// answer, so the request goes on to the token as a rate limit does.
	if rateLimited(resp) {
		return false
	}
	return resp.StatusCode != http.StatusNotFound
}

// rateLimited reports whether GitHub refused a request for the anonymous
// rate limit: 403 with no requests remaining, or 429.
func rateLimited(resp *http.Response) bool {
	return resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0")
}

// repoOf reads owner and repository from an api.github.com /repos/ URL, a
// raw.githubusercontent.com URL or a github.com release download URL.
func repoOf(u *url.URL) (string, string, bool) {
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	switch u.Host {
	case "api.github.com":
		if len(parts) >= 3 && parts[0] == "repos" {
			return parts[1], parts[2], true
		}
	case "raw.githubusercontent.com", "github.com":
		if len(parts) >= 2 {
			return parts[0], parts[1], true
		}
	}
	return "", "", false
}

// authenticatedRetry is req with the token, or false when there is no token
// or the retry would leave GitHub's own hosts.
func authenticatedRetry(req *http.Request) (*http.Request, bool) {
	target := req.URL.String()
	accept := req.Header.Get("Accept")
	if api, ok := req.Context().Value(apiAssetURLKey{}).(string); ok && api != "" {
		target, accept = api, "application/octet-stream"
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || !githubAuthHosts[u.Host] {
		return nil, false
	}
	token := githubToken()
	if token == "" {
		return nil, false
	}
	authed, err := newGitHubRequest(target)
	if err != nil {
		return nil, false
	}
	if accept != "" {
		authed.Header.Set("Accept", accept)
	}
	authed.Header.Set("Authorization", "Bearer "+token)
	return authed, true
}
