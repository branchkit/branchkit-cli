package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
)

// cmdDevPlog queries the actuator's per-plugin log read endpoint
// (GET /v1/plugins/{id}/log) — the retrieval half of the plugin-logging
// design. Unlike `dev logs` (a live tail of actuator.log), this answers
// "what did plugin X log in the last N seconds?" as a one-shot query:
// the server does the clock math, so no timestamp regexes and no
// UTC-vs-local arithmetic on the caller's side.
//
//	branchkit-cli dev plog browser --since 30s --tag 'BK_GRAMMAR_*' --exclude BK_CS_BOOT
func cmdDevPlog(args []string) {
	pluginID := ""
	q := url.Values{}
	jsonMode := false

	flagInto := func(i *int, key string) {
		if *i+1 < len(args) {
			*i++
			q.Set(key, args[*i])
		}
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--since":
			flagInto(&i, "since")
		case "--tag":
			flagInto(&i, "tag")
		case "--exclude":
			flagInto(&i, "exclude")
		case "--level":
			flagInto(&i, "level")
		case "--limit":
			flagInto(&i, "limit")
		case "--json":
			jsonMode = true
		default:
			if pluginID == "" && len(args[i]) > 0 && args[i][0] != '-' {
				pluginID = args[i]
			}
		}
	}
	if pluginID == "" {
		fmt.Fprintln(os.Stderr, "Usage: branchkit-cli dev plog <plugin-id> [--since 30s|5m|ISO] [--tag GLOB[,GLOB]] [--exclude GLOB[,GLOB]] [--level warn] [--limit N] [--json]")
		fmt.Fprintln(os.Stderr, "One-shot query of plugin-logs/<id>.log via the running actuator.")
		os.Exit(1)
	}

	token := readHostToken()
	if token == "" {
		fmt.Fprintln(os.Stderr, "Error: no host.token — is BranchKit running?")
		os.Exit(1)
	}

	path := "/v1/plugins/" + url.PathEscape(pluginID) + "/log"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	raw, status, err := devHTTP("GET", path, token, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	switch status {
	case 200:
	case 404:
		fmt.Fprintf(os.Stderr, "No log file for plugin %q (nothing ever written, or the id is wrong)\n", pluginID)
		os.Exit(1)
	case 400:
		fmt.Fprintln(os.Stderr, "Bad query: --since takes 30s/5m/2h/1d or an ISO-8601 UTC prefix; --level takes trace..error")
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "Error: status=%d body=%s\n", status, string(raw))
		os.Exit(1)
	}

	if jsonMode {
		fmt.Println(string(raw))
		return
	}
	var resp struct {
		Lines   []string `json:"lines"`
		Matched int      `json:"matched"`
		Scanned int      `json:"scanned"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "Error: unparseable response: %v\n", err)
		os.Exit(1)
	}
	for _, line := range resp.Lines {
		fmt.Println(line)
	}
	if resp.Matched > len(resp.Lines) {
		fmt.Fprintln(os.Stderr, "("+strconv.Itoa(resp.Matched-len(resp.Lines))+" older matching lines truncated — raise --limit)")
	}
	fmt.Fprintf(os.Stderr, "(%d matched / %d scanned)\n", resp.Matched, resp.Scanned)
}
