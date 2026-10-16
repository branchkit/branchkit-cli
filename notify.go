package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

// notifyPlatform tells a running platform to reload plugins.
// Silently succeeds if the platform is not running.
func notifyPlatform() {
	// readHostToken resolves the app's address first; without it there is
	// no URL to build — and no operator socket for the client to dial. The
	// client used to be built BEFORE this line, so it never got the socket
	// transport, every call failed, and an install into a running app always
	// reported "BranchKit is not running".
	token := readHostToken()
	client := devClient(5 * time.Second)
	if devBaseURL == "" {
		fmt.Println("BranchKit is not running — plugin will load on next start.")
		return
	}
	req, err := http.NewRequest(http.MethodPost, devBaseURL+"/settings/reload-plugins", nil)
	if err != nil {
		return
	}
	// The reload endpoint is host-token-gated like every mutating route.
	// This call shipped tokenless and therefore 401'd on every install —
	// "plugin will load immediately" had never once been true from the CLI.
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("BranchKit is not running — plugin will load on next start.")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Println("BranchKit notified — plugin will load immediately.")
	} else {
		fmt.Fprintf(os.Stderr, "BranchKit returned error %d — plugin will load on next restart.\n", resp.StatusCode)
	}
}
