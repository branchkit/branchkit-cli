package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The listener half of `dev trial --listener`: the scaffold declares
// sockets.listen and takes the granted listener, but nothing in it ever
// SERVED on that listener, so the trial proved that the plugin bound and
// nothing more. That is exactly the hole the Windows S5 verification had:
// the plugin self-bound inside its AppContainer, and no one connected from
// outside — where the connection times out (the AppContainer loopback
// exemption is outbound-only; measured 2026-09-18).
// So the trial injects a /ping route on the listener and, once the plugin is
// Running under the sandbox, connects to it from THIS process: 200 with the
// pairing token from connect.json, 401 without. On every OS.

// writeListenerProbe adds the /ping route to the TypeScript scaffold's entry.
func writeListenerProbe(dir string) error {
	entry := filepath.Join(dir, "src", "index.ts")
	if err := injectBefore(entry, "await plugin.run();",
		"// Written by branchkit-cli dev trial --listener.\n"+
			"const trialListener = await ListenLocal(plugin);\n"+
			"trialListener.handleFunc(\"GET\", \"/ping\", (_req, res) => { res.writeHead(200); res.end(\"pong\"); });\n"+
			"trialListener.serve();\n\n"); err != nil {
		return err
	}
	return injectBefore(entry, "import { Plugin }",
		"import { ListenLocal } from \"@branchkitdev/plugin-sdk-ts\";\n")
}

// checkListener reads the plugin's connect.json and connects from outside.
func checkListener(t *trialRun, dir string) {
	var info struct {
		Port  string `json:"port"`
		Token string `json:"token"`
	}
	var raw []byte
	var err error
	for i := 0; i < 20; i++ {
		raw, err = os.ReadFile(filepath.Join(dir, "connect.json"))
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err == nil {
		err = json.Unmarshal(raw, &info)
	}
	if err == nil && (info.Port == "" || info.Token == "") {
		err = fmt.Errorf("connect.json has no port/token: %s", strings.TrimSpace(string(raw)))
	}
	if !t.record("listener: the plugin published connect.json", err, "port "+info.Port) {
		return
	}
	client := &http.Client{Timeout: 8 * time.Second}
	url := "http://127.0.0.1:" + info.Port + "/ping"

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+info.Token)
	resp, err := client.Do(req)
	var detail string
	if err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		detail = fmt.Sprintf("HTTP %d %q", resp.StatusCode, strings.TrimSpace(string(body)))
		if resp.StatusCode != 200 || strings.TrimSpace(string(body)) != "pong" {
			err = fmt.Errorf("expected 200 \"pong\", got %s", detail)
		}
	} else {
		err = fmt.Errorf("could not connect from outside the sandbox: %v — the plugin bound the port but nothing outside its sandbox can reach it", err)
	}
	t.record("listener: reachable from outside the sandbox", err, detail)

	resp, err = client.Get(url)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != 401 {
			err = fmt.Errorf("expected 401 without the token, got HTTP %d", resp.StatusCode)
		}
	}
	t.record("listener: refuses a request without the token", err, "")
}
