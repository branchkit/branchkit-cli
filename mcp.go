package main

// `branchkit-cli mcp --connection <id>`: the bridge an AI app launches to
// read through BranchKit.
//
// It speaks the Model Context Protocol over stdio (newline-delimited
// JSON-RPC 2.0) and turns each tool call into one of BranchKit's
// connection reads, authenticated with that connection's own token. It
// never uses the host token: a connected app gets exactly what the person
// switched on for it in Settings > Connections, and every read is recorded
// on BranchKit's side.
//
// The token and the app's address are read again on every call, so the
// bridge keeps working across BranchKit restarts (tokens rotate per boot)
// and stops at once when the connection is removed.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// mcpProtocolVersions are the MCP revisions this bridge speaks, newest
// first. The client's requested version is echoed when it is one of these.
var mcpProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	// Not sent: the BranchKit route this tool calls.
	path string
	// Not sent: the result is a PNG to return as an image.
	image bool
	// Not sent: the call waits for the person to answer a question.
	waits bool
}

func noArgs() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

var mcpTools = []mcpTool{
	{
		Name:        "frontmost_app",
		Description: "Which app and window the person has in front right now (name, bundle id, window title).",
		InputSchema: noArgs(),
		path:        "/v1/connection/frontmost",
	},
	{
		Name:        "focused_element",
		Description: "The element that has keyboard focus: its role, name, value and state. A password field's value is never returned.",
		InputSchema: noArgs(),
		path:        "/v1/connection/focused-element",
	},
	{
		Name:        "selected_text",
		Description: "The text the person has selected in the app in front. Withheld in password fields.",
		InputSchema: noArgs(),
		path:        "/v1/connection/selected-text",
	},
	{
		Name:        "window_text",
		Description: "The text of the window in front, read from the accessibility tree (exact text, not OCR). Password fields are skipped. Use this to read an error, a page, or a dialog to the person.",
		InputSchema: noArgs(),
		path:        "/v1/connection/window-text",
	},
	{
		Name:        "sayable_commands",
		Description: "The BranchKit voice commands the person can say right now, grouped as BranchKit shows them.",
		InputSchema: noArgs(),
		path:        "/v1/connection/commands",
	},
	{
		Name: "run_command",
		Description: "Run one of the person's BranchKit commands, named in the words they would say " +
			"(\"snap left\", \"open safari\"). BranchKit matches the words to their commands, asks the person " +
			"in its own words, and runs it only if they allow it (at once, for a command they always allow); " +
			"this call waits for their answer (up to two minutes). Use sayable_commands to see what exists. If they decline or choose Not Now, do not retry.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"words": map[string]any{
					"type":        "string",
					"description": "The command, as the person would say it.",
				},
			},
			"required": []string{"words"},
		},
		path:  "/v1/connection/run-command",
		waits: true,
	},
	{
		Name: "propose_plan",
		Description: "Before a task of several commands, show the person the whole plan: every command you mean " +
			"to run, in order, each in the words they would say. BranchKit lists the steps in its own words and " +
			"asks once if the person checks in once per task; if they allow it, run each step with run_command " +
			"when you are ready, and every step that can be undone runs without asking again. A step that can't " +
			"be undone still asks; anything not in the plan, or out of order, asks again. Plans end after their " +
			"last step or in 10 minutes. This call waits for the answer (up to two minutes).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"steps": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "The commands, in order, as the person would say them.",
				},
			},
			"required": []string{"steps"},
		},
		path:  "/v1/connection/propose-plan",
		waits: true,
	},
	{
		Name:        "screenshot",
		Description: "A picture of the window in front. Off unless the person turned it on for this app; prefer window_text for reading.",
		InputSchema: noArgs(),
		path:        "/v1/connection/screenshot",
		image:       true,
	},
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func cmdMCP(args []string) {
	connection := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--connection" && i+1 < len(args):
			connection = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--connection="):
			connection = strings.TrimPrefix(args[i], "--connection=")
		}
	}
	if connection == "" {
		fmt.Fprintln(os.Stderr, "branchkit-cli mcp: --connection <id> is required (Settings > Connections shows it)")
		os.Exit(2)
	}
	if err := serveMCP(os.Stdin, os.Stdout, connection); err != nil {
		fmt.Fprintln(os.Stderr, "branchkit-cli mcp:", err)
		os.Exit(1)
	}
}

// serveMCP answers MCP messages from r on w until r closes.
func serveMCP(r io.Reader, w io.Writer, connection string) error {
	in := bufio.NewScanner(r)
	in.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	out := json.NewEncoder(w)
	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		reply := handleMCP([]byte(line), connection)
		if reply == nil {
			continue
		}
		if err := out.Encode(reply); err != nil {
			return err
		}
	}
	return in.Err()
}

// handleMCP answers one message, or returns nil for a notification.
func handleMCP(raw []byte, connection string) map[string]any {
	var msg rpcMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcError{-32700, "parse error"}}
	}
	if len(msg.ID) == 0 {
		return nil // a notification (e.g. notifications/initialized): no reply
	}
	result, rerr := dispatchMCP(msg, connection)
	reply := map[string]any{"jsonrpc": "2.0", "id": msg.ID}
	if rerr != nil {
		reply["error"] = rerr
	} else {
		reply["result"] = result
	}
	return reply
}

func dispatchMCP(msg rpcMessage, connection string) (any, *rpcError) {
	switch msg.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		version := mcpProtocolVersions[0]
		for _, v := range mcpProtocolVersions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		instructions := "BranchKit lets you read what is on the person's screen, with their permission, " +
			"through exact accessibility text rather than pictures. Each tool is allowed only if the person " +
			"switched it on for this app in BranchKit Settings > Connections; a refusal says which switch."
		if allowed := sayHello(connection); len(allowed) > 0 {
			instructions += " Switched on for this app now: " + strings.Join(allowed, ", ") + "."
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "branchkit", "version": version},
			"instructions":    instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": mcpTools}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		for _, t := range mcpTools {
			if t.Name == p.Name {
				return callTool(t, connection, p.Arguments), nil
			}
		}
		return nil, &rpcError{-32602, fmt.Sprintf("unknown tool %q", p.Name)}
	default:
		return nil, &rpcError{-32601, fmt.Sprintf("method %q not found", msg.Method)}
	}
}

// toolText is a tool result the model reads as text.
func toolText(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

// connectionToken reads the connection's current token from its discovery
// file, which BranchKit rewrites at every start.
func connectionToken(connection string) (string, error) {
	if strings.ContainsAny(connection, `/\`) || connection == "." || connection == ".." {
		return "", fmt.Errorf("invalid connection id %q", connection)
	}
	raw, err := os.ReadFile(filepath.Join(appSupportDir(), "connections", connection+".json"))
	if err != nil {
		return "", fmt.Errorf("no connection %q in BranchKit (it may have been removed in Settings > Connections)", connection)
	}
	var d struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &d); err != nil || d.Token == "" {
		return "", fmt.Errorf("connection %q has no token yet; is BranchKit running?", connection)
	}
	return d.Token, nil
}

// sayHello tells BranchKit the app has started this connection, so its
// Settings page shows setup as finished before the first read, and returns
// the switches the person has on. Best effort: nil when BranchKit is not
// reachable, and the bridge works the same either way.
func sayHello(connection string) []string {
	token, err := connectionToken(connection)
	if err != nil {
		return nil
	}
	if resolveDevBaseURL() != nil {
		return nil
	}
	raw, status, err := devHTTPTimeout("POST", "/v1/connection/hello", token, map[string]any{}, 3*time.Second)
	if err != nil || status != 200 {
		return nil
	}
	var r struct {
		Allowed []string `json:"allowed"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	return r.Allowed
}

func callTool(t mcpTool, connection string, args map[string]any) map[string]any {
	token, err := connectionToken(connection)
	if err != nil {
		return toolText(err.Error(), true)
	}
	if err := resolveDevBaseURL(); err != nil {
		return toolText("BranchKit is not reachable: "+err.Error(), true)
	}
	body := map[string]any{}
	for k, v := range args {
		body[k] = v
	}
	// BranchKit holds the call open while the person answers: a request to
	// run a command always asks, and when the person chose to be asked
	// about every read too, so does a read.
	timeout := 150 * time.Second
	raw, status, err := devHTTPTimeout("POST", t.path, token, body, timeout)
	if err != nil {
		return toolText("BranchKit is not reachable: "+err.Error(), true)
	}
	if status != 200 {
		msg := strings.TrimSpace(string(raw))
		if status == 401 {
			msg = "BranchKit refused this connection's token; it may have been removed or BranchKit restarted mid-call. Try again."
		}
		return toolText(msg, true)
	}
	if t.waits {
		var r struct {
			Outcome string `json:"outcome"`
			Detail  string `json:"detail"`
		}
		if json.Unmarshal(raw, &r) != nil {
			return toolText("BranchKit sent an unreadable answer", true)
		}
		ok := r.Outcome == "ran" || r.Outcome == "allowed" || r.Outcome == "noted"
		return toolText(r.Detail, !ok)
	}
	var res struct {
		Data json.RawMessage `json:"data"`
		// Set when BranchKit asked the person about this read and they did
		// not allow it: what to tell them.
		Refused string `json:"refused"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return toolText("BranchKit sent an unreadable answer", true)
	}
	if res.Refused != "" {
		return toolText(res.Refused, true)
	}
	if t.image {
		var img struct {
			ImageBase64 string `json:"image_base64"`
		}
		if json.Unmarshal(res.Data, &img) == nil && img.ImageBase64 != "" {
			return map[string]any{
				"content": []map[string]any{{"type": "image", "data": img.ImageBase64, "mimeType": "image/png"}},
				"isError": false,
			}
		}
		return toolText("no window could be captured", true)
	}
	return toolText(string(res.Data), false)
}

func printMCPUsage() {
	fmt.Println("Usage: branchkit-cli mcp --connection <id>")
	fmt.Println()
	fmt.Println("Serve BranchKit's connection reads to an AI app over MCP (stdio).")
	fmt.Println("Add a connection in BranchKit Settings > Connections first; it shows the id")
	fmt.Println("and the exact line to paste into your AI app's MCP configuration.")
}
