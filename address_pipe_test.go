package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The CLI takes the owner-only endpoint the address file names: the
// operator socket, else (Windows) the operator pipe, else the UI port.
func TestResolveDevBaseURLPrefersTheOwnerOnlyEndpoint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BRANCHKIT_APP_SUPPORT", dir)
	if err := os.MkdirAll(filepath.Join(dir, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(operator string) {
		body := fmt.Sprintf(`{"v":1,"pid":%d,"ui":{"port":4321}%s}`, os.Getpid(), operator)
		if err := os.WriteFile(filepath.Join(dir, "run", "address.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	defer func(b, s, p string) { devBaseURL, devUnixSocket, devPipe = b, s, p }(devBaseURL, devUnixSocket, devPipe)

	write(`,"operator":{"pipe":"\\\\.\\pipe\\branchkit-operator-x"}`)
	if err := resolveDevBaseURL(); err != nil {
		t.Fatal(err)
	}
	if devPipe != `\\.\pipe\branchkit-operator-x` || devUnixSocket != "" || devBaseURL != "http://branchkit" {
		t.Fatalf("pipe: base=%q socket=%q pipe=%q", devBaseURL, devUnixSocket, devPipe)
	}

	write(`,"operator":{"socket":"/tmp/op.sock"}`)
	if err := resolveDevBaseURL(); err != nil {
		t.Fatal(err)
	}
	if devUnixSocket != "/tmp/op.sock" || devPipe != "" {
		t.Fatalf("socket: socket=%q pipe=%q", devUnixSocket, devPipe)
	}

	write(``)
	if err := resolveDevBaseURL(); err != nil {
		t.Fatal(err)
	}
	if devBaseURL != "http://127.0.0.1:4321" || devUnixSocket != "" || devPipe != "" {
		t.Fatalf("ui port: base=%q socket=%q pipe=%q", devBaseURL, devUnixSocket, devPipe)
	}
}
