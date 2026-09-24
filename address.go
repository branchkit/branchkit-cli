package main

// The one local address: every consumer resolves run/address.json (there is
// no default port) rather than assuming one.
//
// A running app writes <app support>/run/address.json with its UI port, the
// operator socket path once it exists, the dev listener's port under a dev
// build, and its pid. Every operator reads that file instead of guessing a
// port; there is no hardcoded default. Tokens are not
// in the file — host.token and the Developer Access files carry those.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Set alongside devBaseURL when the address file lists an operator socket:
// every request then dials the socket and devBaseURL is a placeholder host.
var devUnixSocket string

// devClient is the one HTTP client for operator calls: over the operator
// socket when the app offers one, over loopback TCP otherwise.
func devClient(timeout time.Duration) *http.Client {
	c := &http.Client{Timeout: timeout}
	if sock := devUnixSocket; sock != "" {
		c.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		}
	}
	return c
}

type appAddress struct {
	V         int   `json:"v"`
	PID       int   `json:"pid"`
	StartedAt int64 `json:"started_at"`
	UI        struct {
		Port int `json:"port"`
	} `json:"ui"`
	Operator *struct {
		Socket string `json:"socket"`
	} `json:"operator,omitempty"`
}

var errNoAddress = errors.New("no address file — is BranchKit running?")

// readAppAddress reads run/address.json and checks the writer is alive. A
// file whose pid is gone is a crash leftover and reads as absent.
func readAppAddress() (*appAddress, error) {
	path := filepath.Join(appSupportDir(), "run", "address.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errNoAddress
	}
	var a appAddress
	if err := json.Unmarshal(raw, &a); err != nil || a.UI.Port == 0 {
		return nil, fmt.Errorf("address file %s is unreadable", path)
	}
	if a.PID > 0 && !pidAlive(a.PID) {
		return nil, fmt.Errorf("address file %s names pid %d, which is not running (stale after a crash?)", path, a.PID)
	}
	return &a, nil
}

// pidAlive is per-OS: pid_alive_unix.go / pid_alive_windows.go. On Windows
// os.Process.Signal supports only Kill and Interrupt, so the Unix "signal 0"
// probe returned an error for EVERY pid and the CLI read every address file
// as a crash leftover — no Windows session could ever reach the app
// (found 2026-09-18, the first `dev smoke` on the Win11 VM).

// resolveDevBaseURL sets devBaseURL from the address file: the operator
// socket when the app offers one, else the UI port. A development build
// serves the full router on that port; a production install serves the UI
// routes plus the Developer Access operations there, and the socket.
func resolveDevBaseURL() error {
	a, err := readAppAddress()
	if err != nil {
		return err
	}
	if a.Operator != nil && a.Operator.Socket != "" {
		devUnixSocket = a.Operator.Socket
		devBaseURL = "http://branchkit"
		return nil
	}
	devUnixSocket = ""
	devBaseURL = fmt.Sprintf("http://127.0.0.1:%d", a.UI.Port)
	return nil
}
