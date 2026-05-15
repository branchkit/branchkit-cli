package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

func cmdDevWatch(args []string) {
	dir := "."
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			dir = a
		}
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	manifestPath := filepath.Join(absDir, "plugin.json")
	if _, err := os.Stat(manifestPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: no plugin.json found in %s\n", absDir)
		os.Exit(1)
	}

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading plugin.json: %v\n", err)
		os.Exit(1)
	}
	var manifest struct {
		ID  string `json:"id"`
		Run string `json:"run"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing plugin.json: %v\n", err)
		os.Exit(1)
	}

	binaryName := manifest.ID + "-plugin"
	if manifest.Run != "" {
		binaryName = strings.TrimPrefix(manifest.Run, "./")
	}
	binaryPath := filepath.Join(absDir, binaryName)
	srcDir := filepath.Join(absDir, "src")

	if _, err := os.Stat(srcDir); err != nil {
		fmt.Fprintf(os.Stderr, "Error: no src/ directory found in %s\n", absDir)
		os.Exit(1)
	}

	fmt.Printf("Watching %s for changes (Ctrl+C to stop)...\n", manifest.ID)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-sigCh:
			fmt.Println("\nStopped watching.")
			return
		case <-ticker.C:
			if !hasChanges(absDir, srcDir, binaryPath) {
				continue
			}

			fmt.Printf("\nChange detected — rebuilding %s...\n", manifest.ID)
			if !buildPlugin(absDir, srcDir, binaryPath, binaryName) {
				fmt.Println("Build failed. Watching for next change...")
				continue
			}

			token := readHostToken()
			if token == "" {
				fmt.Println("Built. No host token — actuator will load the plugin on next start.")
				continue
			}

			if reloadViaEndpoint(manifest.ID, token) {
				fmt.Printf("Built and reloaded %s.\n", manifest.ID)
			} else {
				fmt.Println("Built. Could not notify actuator — plugin will load on next restart.")
			}
		}
	}
}

func hasChanges(pluginDir, srcDir, binaryPath string) bool {
	binaryStat, err := os.Stat(binaryPath)
	if err != nil {
		return true
	}
	binaryTime := binaryStat.ModTime()

	entries, _ := os.ReadDir(srcDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".templ") ||
			strings.HasSuffix(name, ".html") || strings.HasSuffix(name, ".css") ||
			strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".js") {
			info, err := e.Info()
			if err == nil && info.ModTime().After(binaryTime) {
				return true
			}
		}
	}

	entries, _ = os.ReadDir(pluginDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".json") {
			info, err := e.Info()
			if err == nil && info.ModTime().After(binaryTime) {
				return true
			}
		}
	}

	return false
}

func buildPlugin(absDir, srcDir, binaryPath, _ string) bool {
	if fileExists(filepath.Join(srcDir, "go.mod")) {
		cmd := exec.Command("go", "build", "-o", binaryPath, ".")
		cmd.Dir = srcDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run() == nil
	}

	pkgDir := srcDir
	if !fileExists(filepath.Join(srcDir, "package.json")) {
		pkgDir = absDir
	}
	if fileExists(filepath.Join(pkgDir, "package.json")) {
		bunPath := "bun"
		if managed := managedBunPath(); fileExists(managed) {
			bunPath = managed
		}
		cmd := exec.Command(bunPath, "install")
		cmd.Dir = pkgDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run() == nil
	}

	fmt.Fprintln(os.Stderr, "Unknown build system")
	return false
}

func readHostToken() string {
	path := filepath.Join(appSupportDir(), "host.token")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func reloadViaEndpoint(pluginID, token string) bool {
	client := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:21551/dev/plugins/%s/rebuild", pluginID)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &result) == nil && result.OK {
		return true
	}
	if result.Error != "" {
		fmt.Fprintf(os.Stderr, "Reload error: %s\n", result.Error)
	}
	return false
}
