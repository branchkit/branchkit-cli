package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The trial reaches a tab's frame the way the browser does: the tab page
// names the frame server's URL, its query escaped for the attribute, and
// the frame is fetched from there over plain TCP.
func TestFetchSettingsFrameFollowsTheTabPage(t *testing.T) {
	frames := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/f/tok/frame" || r.URL.Query().Get("tab") != "getting_started" || r.URL.Query().Get("view") != "v1" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`<div class="bk-card">hi</div>`))
	}))
	defer frames.Close()
	var src string
	ui := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tkn" || r.URL.Path != "/plugins/demo/settings/getting_started" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`<iframe src="` + src + `" data-plugin-id="demo" data-tab-key="getting_started"></iframe>`))
	}))
	defer ui.Close()
	defer func(b, s string) { devBaseURL, devUnixSocket = b, s }(devBaseURL, devUnixSocket)
	devBaseURL, devUnixSocket = ui.URL, ""

	src = frames.URL + "/f/tok/frame?tab=getting_started&amp;view=v1"
	raw, err := fetchSettingsFrame("demo", "getting_started", "tkn")
	if err != nil || !strings.Contains(string(raw), "bk-card") {
		t.Fatalf("got %q, %v", raw, err)
	}

	src = "about:blank"
	if _, err := fetchSettingsFrame("demo", "getting_started", "tkn"); err == nil {
		t.Fatal("a blank frame must be reported, not fetched")
	}
}
