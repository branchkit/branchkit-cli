package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// cmdDevEvents queries the actuator's structured event streams
// (POST /v1/events/query) — the CLI face of the event inspector, same
// filter dialect as the settings view. Three modes, mirroring the server:
// a --tr id returns its causal chain; any filter flag returns matching
// records; neither returns recent-chain summaries.
//
//	branchkit-cli dev events --types 'consent.*' --plugin scripts --since 1h
//	branchkit-cli dev events --source audit --types 'consent.decision'
//	branchkit-cli dev events --tr tr_a3K9zPqR4m
func cmdDevEvents(args []string) {
	body := map[string]any{}
	jsonMode := false

	flagInto := func(i *int, key string) {
		if *i+1 < len(args) {
			*i++
			body[key] = args[*i]
		}
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--tr":
			flagInto(&i, "correlation_id")
		case "--source":
			flagInto(&i, "source")
		case "--types":
			flagInto(&i, "types")
		case "--plugin":
			flagInto(&i, "plugin")
		case "--severity":
			flagInto(&i, "severity")
		case "--since":
			flagInto(&i, "since")
		case "--limit":
			if i+1 < len(args) {
				i++
				if n, err := strconv.Atoi(args[i]); err == nil {
					body["limit"] = n
				}
			}
		case "--json":
			jsonMode = true
		case "--help", "-h":
			printDevEventsUsage()
			return
		default:
			fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", args[i])
			printDevEventsUsage()
			os.Exit(1)
		}
	}

	token := readHostToken()
	if token == "" {
		fmt.Fprintln(os.Stderr, "Error: no host.token — is BranchKit running?")
		os.Exit(1)
	}

	raw, status, err := devHTTP("POST", "/v1/events/query", token, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if status != 200 {
		fmt.Fprintf(os.Stderr, "Error: status=%d body=%s\n", status, string(raw))
		os.Exit(1)
	}

	if jsonMode {
		fmt.Println(string(raw))
		return
	}
	var resp struct {
		Records []struct {
			TsUTC     string          `json:"ts_utc"`
			Severity  string          `json:"severity"`
			Caller    string          `json:"caller"`
			EventType string          `json:"event_type"`
			Params    json.RawMessage `json:"params"`
		} `json:"records"`
		Chains []struct {
			CorrelationID string   `json:"correlation_id"`
			When          string   `json:"when"`
			RecordCount   int      `json:"record_count"`
			Sources       []string `json:"sources"`
			MaxSeverity   string   `json:"max_severity"`
			HeadlineEvent string   `json:"headline_event"`
		} `json:"chains"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "Error: unparseable response: %v\n", err)
		os.Exit(1)
	}
	for _, r := range resp.Records {
		fmt.Printf("%s %-5s %-12s %-28s %s\n", r.TsUTC, r.Severity, r.Caller, r.EventType, string(r.Params))
	}
	for _, c := range resp.Chains {
		fmt.Printf("%s %s %-5s %3d records %-28s %v\n",
			c.When, c.CorrelationID, c.MaxSeverity, c.RecordCount, c.HeadlineEvent, c.Sources)
	}
	if resp.Truncated {
		fmt.Fprintln(os.Stderr, "(truncated — older matches beyond the read budget or --limit)")
	}
	if len(resp.Records) == 0 && len(resp.Chains) == 0 {
		fmt.Fprintln(os.Stderr, "(no matches — the audit stream is off by default in dev: BRANCHKIT_AUDIT=1)")
	}
}

func printDevEventsUsage() {
	fmt.Fprintln(os.Stderr, `Usage: branchkit-cli dev events [flags]
  --tr <tr_id>        the causal chain for one correlation id
  --source <name>     show-all (default) | audit
  --types <patterns>  comma-separated, * = one segment, ** = any (consent.*)
  --plugin <id>       records where this plugin is the caller or source
  --severity <lvl>    minimum severity: debug|info|warn|error
  --since <bound>     30s/5m/2h/1d relative, or ISO prefix (server does the math)
  --limit <n>         newest N records (default 200) / chain summaries (20)
  --json              raw response
With no flags: recent-chain summaries (discovery mode).`)
}
