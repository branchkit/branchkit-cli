package main

// The one local address (app docs/design/DESIGN_ONE_LOCAL_ADDRESS.md).
//
// A running app writes <app support>/run/address.json with its UI port, the
// operator socket path once it exists, the dev listener's port under a dev
// build, and its pid. Every operator reads that file instead of guessing a
// port; the old hardcoded 127.0.0.1:21551 default is gone. Tokens are not
// in the file — host.token and the Developer Access files carry those.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

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
	Dev *struct {
		Port int `json:"port"`
	} `json:"dev,omitempty"`
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

func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks existence without sending anything.
	return p.Signal(syscall.Signal(0)) == nil
}

// resolveDevBaseURL sets devBaseURL from the address file. The UI port
// serves the UI routes plus the Developer Access operations — what a
// production install offers. A dev build's listener on `dev.port` serves
// the full router (every /v1 and /dev route), so it is preferred when
// present; the operator socket takes that role once it exists
// (DESIGN_ONE_LOCAL_ADDRESS.md 3b).
func resolveDevBaseURL() error {
	a, err := readAppAddress()
	if err != nil {
		return err
	}
	port := a.UI.Port
	if a.Dev != nil && a.Dev.Port != 0 {
		port = a.Dev.Port
	}
	devBaseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	return nil
}
