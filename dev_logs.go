package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

func cmdDevLogs(args []string) {
	pluginID := ""
	source := ""
	jsonMode := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--source":
			if i+1 < len(args) {
				i++
				source = strings.ToUpper(args[i])
			}
		case "--json":
			jsonMode = true
		default:
			if !strings.HasPrefix(args[i], "-") {
				pluginID = args[i]
			}
		}
	}

	var logPath string
	if jsonMode {
		logPath = filepath.Join(appSupportDir(), "show-all.current.jsonl")
	} else {
		logPath = filepath.Join(appSupportDir(), "actuator.log")
	}

	if _, err := os.Stat(logPath); err != nil {
		fmt.Fprintf(os.Stderr, "Log file not found: %s\n", logPath)
		fmt.Fprintln(os.Stderr, "Is BranchKit running?")
		os.Exit(1)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	f, err := os.Open(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening %s: %v\n", logPath, err)
		os.Exit(1)
	}
	defer f.Close()

	// Seek to end to tail new lines only
	f.Seek(0, 2)

	if jsonMode {
		fmt.Fprintf(os.Stderr, "Streaming %s", logPath)
	} else {
		fmt.Fprintf(os.Stderr, "Tailing %s", logPath)
	}
	if pluginID != "" {
		fmt.Fprintf(os.Stderr, " (filter: %s)", pluginID)
	}
	if source != "" {
		fmt.Fprintf(os.Stderr, " (source: %s)", source)
	}
	fmt.Fprintln(os.Stderr)

	scanner := bufio.NewScanner(f)
	for {
		select {
		case <-sigCh:
			fmt.Fprintln(os.Stderr, "\nStopped.")
			return
		default:
		}

		if scanner.Scan() {
			line := scanner.Text()
			if matchesFilter(line, pluginID, source, jsonMode) {
				fmt.Println(line)
			}
		} else {
			time.Sleep(200 * time.Millisecond)
			scanner = bufio.NewScanner(f)
		}
	}
}

func matchesFilter(line, pluginID, source string, jsonMode bool) bool {
	if pluginID == "" && source == "" {
		return true
	}

	if jsonMode {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			return false
		}
		if source != "" {
			s, _ := entry["source"].(string)
			if !strings.EqualFold(s, source) {
				return false
			}
		}
		if pluginID != "" {
			s, _ := entry["source"].(string)
			if !strings.EqualFold(s, pluginID) {
				evt, _ := entry["event"].(map[string]any)
				pluginField, _ := evt["plugin"].(string)
				if !strings.EqualFold(pluginField, pluginID) {
					raw := line
					if !strings.Contains(strings.ToLower(raw), strings.ToLower(pluginID)) {
						return false
					}
				}
			}
		}
		return true
	}

	upper := strings.ToUpper(line)
	if source != "" && !strings.Contains(upper, "["+source+"]") {
		return false
	}
	if pluginID != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(pluginID)) {
		return false
	}
	return true
}
