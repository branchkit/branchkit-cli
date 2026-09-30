package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func mcpRoundTrip(t *testing.T, connection string, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := serveMCP(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out, connection); err != nil {
		t.Fatal(err)
	}
	var replies []map[string]any
	dec := json.NewDecoder(&out)
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		replies = append(replies, m)
	}
	return replies
}

func TestMCPHandshakeListsToolsAndIgnoresNotifications(t *testing.T) {
	replies := mcpRoundTrip(t, "c",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"nope"}`,
	)
	if len(replies) != 3 {
		t.Fatalf("a notification gets no reply: want 3 replies, got %d", len(replies))
	}
	init := replies[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" {
		t.Errorf("the client's supported version is echoed, got %v", init["protocolVersion"])
	}
	tools := replies[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != len(mcpTools) {
		t.Fatalf("want %d tools, got %d", len(mcpTools), len(tools))
	}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		if _, leaked := tool["path"]; leaked {
			t.Error("internal route leaked into tools/list")
		}
	}
	if replies[2]["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Error("unknown method is -32601")
	}
}

// A tool call reads the connection's token from its discovery file, calls
// BranchKit with it, and a refusal comes back as a readable tool error.
func TestMCPToolCallUsesTheConnectionTokenAndSurfacesRefusals(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BRANCHKIT_APP_SUPPORT", dir)
	os.MkdirAll(filepath.Join(dir, "connections"), 0o755)
	os.WriteFile(filepath.Join(dir, "connections", "claude.json"),
		[]byte(`{"id":"claude","name":"Claude","token":"tok-123"}`), 0o600)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-123" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/connection/window-text":
			w.Write([]byte(`{"connection":"claude","read":"screen_text","data":{"text":"Disk not found"}}`))
		case "/v1/connection/run-command":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["words"] == "snap left" {
				w.Write([]byte(`{"outcome":"ran","detail":"ran 'snap left': done"}`))
			} else {
				w.Write([]byte(`{"outcome":"declined","detail":"the person declined; do not retry"}`))
			}
		case "/v1/connection/screenshot":
			w.WriteHeader(403)
			w.Write([]byte("Claude is not allowed to read 'screenshot'."))
		}
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	os.MkdirAll(filepath.Join(dir, "run"), 0o755)
	os.WriteFile(filepath.Join(dir, "run", "address.json"),
		[]byte(`{"v":1,"pid":0,"ui":{"port":`+strconv.Itoa(port)+`}}`), 0o600)

	replies := mcpRoundTrip(t, "claude",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"window_text","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"screenshot","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"run_command","arguments":{"words":"snap left"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"run_command","arguments":{"words":"empty trash"}}}`,
	)
	ok := replies[0]["result"].(map[string]any)
	if ok["isError"] != false || !strings.Contains(text(ok), "Disk not found") {
		t.Errorf("window_text: %v", ok)
	}
	refused := replies[1]["result"].(map[string]any)
	if refused["isError"] != true || !strings.Contains(text(refused), "not allowed") {
		t.Errorf("a refusal is a readable tool error: %v", refused)
	}
	ran := replies[2]["result"].(map[string]any)
	if ran["isError"] != false || !strings.Contains(text(ran), "ran") {
		t.Errorf("run_command passes the words and reports the run: %v", ran)
	}
	declined := replies[3]["result"].(map[string]any)
	if declined["isError"] != true || !strings.Contains(text(declined), "declined") {
		t.Errorf("a declined command is a tool error the AI must not retry: %v", declined)
	}
}

func TestMCPMissingConnectionIsAToolErrorNotACrash(t *testing.T) {
	t.Setenv("BRANCHKIT_APP_SUPPORT", t.TempDir())
	replies := mcpRoundTrip(t, "gone",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"frontmost_app"}}`,
	)
	res := replies[0]["result"].(map[string]any)
	if res["isError"] != true || !strings.Contains(text(res), "removed") {
		t.Errorf("%v", res)
	}
}

func text(result map[string]any) string {
	c := result["content"].([]any)
	return c[0].(map[string]any)["text"].(string)
}
