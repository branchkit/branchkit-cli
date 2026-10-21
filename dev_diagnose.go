package main

// dev diagnose — one plugin-scoped snapshot of the running app, for an
// author (or the AI agent working for them) who wants the whole picture in
// one call before reaching for the narrower dev commands.
//
// It composes endpoints the other dev commands already use and adds none:
//
//	status    GET  /v1/plugins/{id}/status   (host token only)
//	warnings  GET  /v1/plugins/{id}/log?level=warn
//	events    POST /v1/events/query {plugin, severity: warn}
//	log       GET  /v1/plugins/{id}/log
//	received  GET  /v1/plugins/{id}/replay
//	chains    POST /v1/events/query {correlation_id} per id found above
//
// Every section is fetched on its own and degrades on its own: a failing
// endpoint puts its reason in that section and the rest still print. The
// same credentials as `dev plog` and `dev chain` apply, so it works on a
// Developer Access grant, where the status route (host-only) is skipped
// and says so.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Bounds on each section, so the snapshot stays one screen and one read.
const (
	diagnoseWarnLimit     = 20
	diagnoseEventLimit    = 20
	diagnoseLogLimit      = 20
	diagnoseReceivedLimit = 20
	diagnoseChainLimit    = 5
	diagnoseDefaultSince  = "1h"
)

type diagnoseStatus struct {
	Error           string `json:"error,omitempty"`
	Status          string `json:"status,omitempty"`
	Enabled         *bool  `json:"enabled,omitempty"`
	Version         string `json:"version,omitempty"`
	EventsDenied    uint64 `json:"events_denied_by_interaction"`
	EventsLostToLag uint64 `json:"events_lost_to_lag"`
}

type diagnoseLines struct {
	Error   string   `json:"error,omitempty"`
	Lines   []string `json:"lines"`
	Matched int      `json:"matched"`
}

type diagnoseRecord struct {
	TsUTC     string          `json:"ts_utc"`
	Severity  string          `json:"severity"`
	Source    string          `json:"source,omitempty"`
	Caller    string          `json:"caller,omitempty"`
	EventType string          `json:"event_type"`
	Params    json.RawMessage `json:"params,omitempty"`
}

type diagnoseRecords struct {
	Error   string           `json:"error,omitempty"`
	Records []diagnoseRecord `json:"records"`
}

type diagnoseDelivered struct {
	DeliveredAt   string          `json:"delivered_at_utc"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	EventType     string          `json:"event_type"`
	Source        string          `json:"source"`
	Pattern       string          `json:"matched_subscription_pattern"`
	OnBehalfOf    string          `json:"on_behalf_of,omitempty"`
	Data          json.RawMessage `json:"data,omitempty"`
}

type diagnoseReceived struct {
	Error  string              `json:"error,omitempty"`
	Events []diagnoseDelivered `json:"events"`
	Total  int                 `json:"total"`
}

type diagnoseChain struct {
	CorrelationID string   `json:"correlation_id"`
	LastSeen      string   `json:"last_seen"`
	Error         string   `json:"error,omitempty"`
	Records       int      `json:"records"`
	MaxSeverity   string   `json:"max_severity,omitempty"`
	FirstEvent    string   `json:"first_event,omitempty"`
	Plugins       []string `json:"plugin_callers,omitempty"`
}

type diagnoseChains struct {
	Error  string          `json:"error,omitempty"`
	Chains []diagnoseChain `json:"chains"`
}

type diagnoseReport struct {
	Plugin string `json:"plugin"`
	// "host" on a host token, "developer_access" on a plugin's grant.
	Access   string           `json:"access"`
	Since    string           `json:"since"`
	Status   diagnoseStatus   `json:"status"`
	Warnings diagnoseLines    `json:"warnings"`
	Events   diagnoseRecords  `json:"events"`
	Log      diagnoseLines    `json:"log"`
	Received diagnoseReceived `json:"received"`
	Chains   diagnoseChains   `json:"chains"`
	Next     []string         `json:"next"`
}

// failed reports whether every section failed: then the app was not
// reachable at all, and the command exits non-zero.
func (r diagnoseReport) failed() bool {
	return r.Status.Error != "" && r.Warnings.Error != "" && r.Events.Error != "" &&
		r.Log.Error != "" && r.Received.Error != ""
}

func cmdDevDiagnose(args []string) {
	pluginID, since, jsonMode := "", "", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--since":
			if i+1 < len(args) {
				i++
				since = args[i]
			}
		case "--json":
			jsonMode = true
		case "--help", "-h":
			printDevDiagnoseUsage()
			return
		default:
			if pluginID == "" && !strings.HasPrefix(args[i], "-") {
				pluginID = args[i]
				continue
			}
			fmt.Fprintf(os.Stderr, "Unknown argument: %s\n", args[i])
			printDevDiagnoseUsage()
			os.Exit(1)
		}
	}
	if pluginID == "" {
		printDevDiagnoseUsage()
		os.Exit(1)
	}

	// A Developer Access grant answers for one plugin: try this one's first.
	devAccessPrefer = pluginID
	token := readHostToken()
	if token == "" {
		fmt.Fprintln(os.Stderr, "Error: no host token or Developer Access grant — is BranchKit running? Turn on Developer Access on the plugin's card in Settings.")
		os.Exit(1)
	}
	if devAccessScope != "" && devAccessScope != pluginID {
		fmt.Fprintf(os.Stderr, "Error: the Developer Access grant found is for plugin '%s', not '%s'. Turn on Developer Access on '%s''s card in Settings.\n",
			devAccessScope, pluginID, pluginID)
		os.Exit(1)
	}

	r := collectDiagnosis(pluginID, token, since, devAccessScope != "")
	if jsonMode {
		out, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(out))
	} else {
		renderDiagnosis(os.Stdout, r)
	}
	if r.failed() {
		os.Exit(1)
	}
}

func printDevDiagnoseUsage() {
	fmt.Fprintln(os.Stderr, `Usage: branchkit-cli dev diagnose <plugin-id> [--since 1h] [--json]
One snapshot of a plugin in the running app: its status, recent warnings and
errors, its log tail, what it last received, and the last chains (tr_ ids)
that touched it, followed by the commands to dig further.
  --since <bound>  window for warnings, events and the log (30s/5m/2h/1d or an
                   ISO prefix; default 1h for warnings and events, the log
                   tail is unbounded unless given)
  --json           one object with the same sections, for scripts and agents`)
}

// collectDiagnosis fetches every section through devHTTP. scoped is true on
// a Developer Access grant.
func collectDiagnosis(pluginID, token, since string, scoped bool) diagnoseReport {
	r := diagnoseReport{Plugin: pluginID, Access: "host", Since: since}
	if scoped {
		r.Access = "developer_access"
	}
	window := since
	if window == "" {
		window = diagnoseDefaultSince
		r.Since = window
	}
	esc := url.PathEscape(pluginID)

	if scoped {
		r.Status.Error = "not available on a Developer Access grant (the status route answers the app's host token only); the plugin's card in Settings shows it"
	} else {
		r.Status = diagnoseFetchStatus(esc, token)
	}

	q := url.Values{"level": {"warn"}, "limit": {fmt.Sprint(diagnoseWarnLimit)}, "since": {window}}
	r.Warnings = diagnoseFetchLog(esc, token, q)

	r.Events = diagnoseFetchEvents(token, map[string]any{
		"plugin": pluginID, "severity": "warn", "since": window, "limit": diagnoseEventLimit,
	})

	q = url.Values{"limit": {fmt.Sprint(diagnoseLogLimit)}}
	if since != "" {
		q.Set("since", since)
	}
	r.Log = diagnoseFetchLog(esc, token, q)

	r.Received = diagnoseFetchReplay(esc, token)
	r.Chains = diagnoseCollectChains(token, r)
	r.Next = diagnoseNext(r)
	return r
}

func diagnoseHTTPError(status int, raw []byte, err error) string {
	if err != nil {
		return err.Error()
	}
	body := strings.TrimSpace(string(raw))
	if len(body) > 200 {
		body = body[:200] + "…"
	}
	switch status {
	case 401, 403:
		return fmt.Sprintf("refused (HTTP %d): the credential in use cannot read this", status)
	}
	if body == "" {
		return fmt.Sprintf("HTTP %d", status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, body)
}

func diagnoseFetchStatus(esc, token string) diagnoseStatus {
	raw, status, err := devHTTP("GET", "/v1/plugins/"+esc+"/status", token, nil)
	if err != nil || status != 200 {
		if err == nil && status == 404 {
			return diagnoseStatus{Error: "no installed plugin has this id"}
		}
		return diagnoseStatus{Error: diagnoseHTTPError(status, raw, err)}
	}
	var s struct {
		Status  string `json:"status"`
		Enabled *bool  `json:"enabled"`
		Version string `json:"version"`
		Denied  uint64 `json:"events_denied_by_interaction"`
		Lag     uint64 `json:"events_lost_to_lag"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return diagnoseStatus{Error: "unparseable response: " + err.Error()}
	}
	return diagnoseStatus{Status: s.Status, Enabled: s.Enabled, Version: s.Version, EventsDenied: s.Denied, EventsLostToLag: s.Lag}
}

func diagnoseFetchLog(esc, token string, q url.Values) diagnoseLines {
	raw, status, err := devHTTP("GET", "/v1/plugins/"+esc+"/log?"+q.Encode(), token, nil)
	if err != nil || status != 200 {
		switch {
		case err == nil && status == 404:
			return diagnoseLines{Error: "no log file: the plugin has not written to its log, or the id is wrong", Lines: []string{}}
		case err == nil && status == 400:
			return diagnoseLines{Error: "bad query: --since takes 30s/5m/2h/1d or an ISO-8601 UTC prefix", Lines: []string{}}
		}
		return diagnoseLines{Error: diagnoseHTTPError(status, raw, err), Lines: []string{}}
	}
	var resp struct {
		Lines   []string `json:"lines"`
		Matched int      `json:"matched"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return diagnoseLines{Error: "unparseable response: " + err.Error(), Lines: []string{}}
	}
	if resp.Lines == nil {
		resp.Lines = []string{}
	}
	return diagnoseLines{Lines: resp.Lines, Matched: resp.Matched}
}

type diagnoseQueryResult struct {
	Records       []diagnoseRecord `json:"records"`
	PluginCallers []string         `json:"plugin_callers"`
}

func diagnoseQuery(token string, body map[string]any) (diagnoseQueryResult, string) {
	raw, status, err := devHTTP("POST", "/v1/events/query", token, body)
	if err != nil || status != 200 {
		return diagnoseQueryResult{}, diagnoseHTTPError(status, raw, err)
	}
	var resp diagnoseQueryResult
	if err := json.Unmarshal(raw, &resp); err != nil {
		return diagnoseQueryResult{}, "unparseable response: " + err.Error()
	}
	return resp, ""
}

func diagnoseFetchEvents(token string, body map[string]any) diagnoseRecords {
	resp, errText := diagnoseQuery(token, body)
	if errText != "" {
		return diagnoseRecords{Error: errText, Records: []diagnoseRecord{}}
	}
	if resp.Records == nil {
		resp.Records = []diagnoseRecord{}
	}
	return diagnoseRecords{Records: resp.Records}
}

func diagnoseFetchReplay(esc, token string) diagnoseReceived {
	raw, status, err := devHTTP("GET", "/v1/plugins/"+esc+"/replay", token, nil)
	if err != nil || status != 200 {
		if err == nil && status == 404 {
			return diagnoseReceived{Error: "no installed plugin has this id", Events: []diagnoseDelivered{}}
		}
		return diagnoseReceived{Error: diagnoseHTTPError(status, raw, err), Events: []diagnoseDelivered{}}
	}
	var events []diagnoseDelivered
	if err := json.Unmarshal(raw, &events); err != nil {
		return diagnoseReceived{Error: "unparseable response: " + err.Error(), Events: []diagnoseDelivered{}}
	}
	total := len(events)
	if total > diagnoseReceivedLimit {
		events = events[total-diagnoseReceivedLimit:]
	}
	if events == nil {
		events = []diagnoseDelivered{}
	}
	return diagnoseReceived{Events: events, Total: total}
}

var diagnoseTrRe = regexp.MustCompile(`\btr_[A-Za-z0-9]+`)

// diagnoseLineTime returns the `[ts]` prefix of a plugin log line.
func diagnoseLineTime(line string) string {
	if strings.HasPrefix(line, "[") {
		if end := strings.Index(line, "]"); end > 0 {
			return line[1:end]
		}
	}
	return ""
}

// diagnoseCollectChains names the chains that touched the plugin: the
// correlation ids on what it received and any tr_ id its log lines carry,
// newest last, then asks the app for each one's records. On a Developer
// Access grant that answer is the plugin's own slice of the chain.
func diagnoseCollectChains(token string, r diagnoseReport) diagnoseChains {
	lastSeen := map[string]string{}
	see := func(id, ts string) {
		if id == "" {
			return
		}
		if prev, ok := lastSeen[id]; !ok || ts > prev {
			lastSeen[id] = ts
		}
	}
	for _, e := range r.Received.Events {
		see(e.CorrelationID, e.DeliveredAt)
	}
	for _, lines := range [][]string{r.Warnings.Lines, r.Log.Lines} {
		for _, l := range lines {
			for _, id := range diagnoseTrRe.FindAllString(l, -1) {
				see(id, diagnoseLineTime(l))
			}
		}
	}
	chains := make([]diagnoseChain, 0, len(lastSeen))
	for id, ts := range lastSeen {
		chains = append(chains, diagnoseChain{CorrelationID: id, LastSeen: ts})
	}
	sort.Slice(chains, func(i, j int) bool {
		if chains[i].LastSeen != chains[j].LastSeen {
			return chains[i].LastSeen < chains[j].LastSeen
		}
		return chains[i].CorrelationID < chains[j].CorrelationID
	})
	if len(chains) > diagnoseChainLimit {
		chains = chains[len(chains)-diagnoseChainLimit:]
	}
	if len(chains) == 0 {
		out := diagnoseChains{Chains: []diagnoseChain{}}
		if r.Received.Error != "" && r.Log.Error != "" {
			out.Error = "no source of correlation ids: the received events and the log could not be read"
		}
		return out
	}
	for i := range chains {
		resp, errText := diagnoseQuery(token, map[string]any{"correlation_id": chains[i].CorrelationID})
		if errText != "" {
			chains[i].Error = errText
			continue
		}
		chains[i].Records = len(resp.Records)
		chains[i].Plugins = resp.PluginCallers
		for _, rec := range resp.Records {
			if chains[i].FirstEvent == "" {
				chains[i].FirstEvent = rec.EventType
			}
			if severityRank(rec.Severity) > severityRank(chains[i].MaxSeverity) {
				chains[i].MaxSeverity = rec.Severity
			}
		}
	}
	return diagnoseChains{Chains: chains}
}

func severityRank(s string) int {
	switch strings.ToLower(s) {
	case "trace":
		return 1
	case "debug":
		return 2
	case "info":
		return 3
	case "warn", "warning":
		return 4
	case "error":
		return 5
	}
	return 0
}

// diagnoseNext names the exact follow-up commands, starting from what the
// snapshot found.
func diagnoseNext(r diagnoseReport) []string {
	id := r.Plugin
	var next []string
	if n := len(r.Chains.Chains); n > 0 {
		next = append(next, fmt.Sprintf("branchkit-cli dev chain %s    # the newest chain, record by record", r.Chains.Chains[n-1].CorrelationID))
	}
	next = append(next,
		fmt.Sprintf("branchkit-cli dev plog %s --since 60s    # everything it logged, not only warnings", id),
		fmt.Sprintf("branchkit-cli dev say \"<phrase>\" --simulate    # reproduce without speaking; nothing executes, then run dev diagnose %s again", id),
	)
	if r.Received.Error == "" && r.Received.Total == 0 {
		next = append(next, fmt.Sprintf("branchkit-cli dev events --plugin %s --since 10m    # nothing was delivered: compare with what was emitted (docs: how-to/debug/debug-event-delivery.md)", id))
	}
	if r.Status.EventsDenied > 0 {
		next = append(next, fmt.Sprintf("branchkit-cli dev events --source audit --plugin %s    # deliveries were denied: check the plugin's grants and privileges", id))
	}
	next = append(next, "branchkit-cli dev test .    # after a fix, from the plugin folder: the test harness, without the app")
	return next
}

func renderDiagnosis(w io.Writer, r diagnoseReport) {
	access := "host token"
	if r.Access == "developer_access" {
		access = "Developer Access grant"
	}
	fmt.Fprintf(w, "Plugin %s (via %s, window %s)\n", r.Plugin, access, r.Since)

	section := func(title string) { fmt.Fprintf(w, "\n== %s\n", title) }
	failed := func(msg string) { fmt.Fprintf(w, "  (unavailable: %s)\n", msg) }

	section("Status")
	if r.Status.Error != "" {
		failed(r.Status.Error)
	} else {
		enabled := "?"
		if r.Status.Enabled != nil {
			enabled = fmt.Sprint(*r.Status.Enabled)
		}
		version := r.Status.Version
		if version == "" {
			version = "-"
		}
		fmt.Fprintf(w, "  %s, enabled=%s, version %s\n", r.Status.Status, enabled, version)
		if r.Status.EventsDenied > 0 || r.Status.EventsLostToLag > 0 {
			fmt.Fprintf(w, "  this session: %d deliveries denied by grants or privileges, %d events lost because the plugin fell behind\n",
				r.Status.EventsDenied, r.Status.EventsLostToLag)
		}
	}

	printLines := func(l diagnoseLines, empty string) {
		if l.Error != "" {
			failed(l.Error)
			return
		}
		if len(l.Lines) == 0 {
			fmt.Fprintf(w, "  (%s)\n", empty)
			return
		}
		for _, line := range l.Lines {
			fmt.Fprintf(w, "  %s\n", line)
		}
		if l.Matched > len(l.Lines) {
			fmt.Fprintf(w, "  (%d older lines not shown)\n", l.Matched-len(l.Lines))
		}
	}

	section(fmt.Sprintf("Warnings and errors it logged (last %d, since %s)", diagnoseWarnLimit, r.Since))
	printLines(r.Warnings, "none")

	section(fmt.Sprintf("Events about it at warn or above (last %d, since %s)", diagnoseEventLimit, r.Since))
	if r.Events.Error != "" {
		failed(r.Events.Error)
	} else if len(r.Events.Records) == 0 {
		fmt.Fprintln(w, "  (none)")
	} else {
		for _, e := range r.Events.Records {
			fmt.Fprintf(w, "  %s %-5s %-28s %s\n", e.TsUTC, e.Severity, e.EventType, diagnoseClip(string(e.Params), 120))
		}
	}

	logTitle := fmt.Sprintf("Log tail (last %d lines)", diagnoseLogLimit)
	section(logTitle)
	printLines(r.Log, "empty")

	section(fmt.Sprintf("What it received (last %d)", diagnoseReceivedLimit))
	if r.Received.Error != "" {
		failed(r.Received.Error)
	} else if len(r.Received.Events) == 0 {
		fmt.Fprintln(w, "  (nothing delivered since the app started)")
	} else {
		for _, e := range r.Received.Events {
			tr := e.CorrelationID
			if tr == "" {
				tr = "-"
			}
			fmt.Fprintf(w, "  %s %-32s from %-12s %s (matched %s)\n", e.DeliveredAt, e.EventType, e.Source, tr, e.Pattern)
		}
		if r.Received.Total > len(r.Received.Events) {
			fmt.Fprintf(w, "  (%d older deliveries not shown)\n", r.Received.Total-len(r.Received.Events))
		}
	}

	section(fmt.Sprintf("Chains that touched it (last %d)", diagnoseChainLimit))
	if r.Chains.Error != "" {
		failed(r.Chains.Error)
	} else if len(r.Chains.Chains) == 0 {
		fmt.Fprintln(w, "  (no correlation ids on what it received or logged)")
	} else {
		for _, c := range r.Chains.Chains {
			if c.Error != "" {
				fmt.Fprintf(w, "  %s  %s  (unavailable: %s)\n", c.CorrelationID, c.LastSeen, c.Error)
				continue
			}
			sev := c.MaxSeverity
			if sev == "" {
				sev = "-"
			}
			fmt.Fprintf(w, "  %s  %s  %3d records  %-5s %s\n", c.CorrelationID, c.LastSeen, c.Records, sev, c.FirstEvent)
		}
	}

	fmt.Fprintln(w, "\nNext:")
	for _, n := range r.Next {
		fmt.Fprintf(w, "  %s\n", n)
	}
}

func diagnoseClip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
