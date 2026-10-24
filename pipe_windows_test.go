//go:build windows

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

// devClient reaches an HTTP server on a named pipe the way it reaches the
// app's operator pipe.
func TestDevClientDialsTheOperatorPipe(t *testing.T) {
	name := fmt.Sprintf(`\\.\pipe\branchkit-cli-test-%d`, os.Getpid())
	l, err := winio.ListenPipe(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "over the pipe: %s", r.URL.Path)
	}))
	defer func(b, s, p string) { devBaseURL, devUnixSocket, devPipe = b, s, p }(devBaseURL, devUnixSocket, devPipe)
	devBaseURL, devUnixSocket, devPipe = "http://branchkit", "", name

	resp, err := devClient(5 * time.Second).Get(devBaseURL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "over the pipe: /v1/health" {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
}
