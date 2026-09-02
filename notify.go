package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

// notifyActuator tells a running actuator to reload plugins.
// Silently succeeds if the actuator is not running.
func notifyActuator() {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPost, devBaseURL+"/settings/reload-plugins", nil)
	if err != nil {
		return
	}
	// The reload endpoint is host-token-gated like every mutating route.
	// This call shipped tokenless and therefore 401'd on every install —
	// "plugin will load immediately" had never once been true from the CLI.
	if token := readHostToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Actuator is not running — plugin will load on next start.")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Println("Actuator notified — plugin will load immediately.")
	} else {
		fmt.Fprintf(os.Stderr, "Actuator returned error %d — plugin will load on next restart.\n", resp.StatusCode)
	}
}
