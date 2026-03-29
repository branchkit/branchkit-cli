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
	resp, err := client.Post("http://127.0.0.1:21551/settings/reload-plugins", "", nil)
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
