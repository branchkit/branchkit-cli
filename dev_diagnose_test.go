package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Recorded responses, in the shapes the app's endpoints return.
const (
	diagStatusBody = `{"id":"snippets","status":"running","enabled":true,"version":"0.3.0","events_denied_by_interaction":2,"events_lost_to_lag":0}`
	diagWarnBody   = `{"lines":["[2026-10-21T17:00:01.000Z] [WARN] [PACK_ENTRY_SKIPPED] {\"len\":0,\"tr\":\"tr_bbb\"}"],"matched":1,"scanned":40,"truncated":false}`
	diagLogBody    = `{"lines":["[2026-10-21T16:59:00.000Z] [INFO] [BOOT] {}","[2026-10-21T17:00:01.000Z] [WARN] [PACK_ENTRY_SKIPPED] {\"len\":0,\"tr\":\"tr_bbb\"}"],"matched":2,"scanned":40,"truncated":false}`
	diagReplayBody = `[{"delivered_at_utc":"2026-10-21T16:59:30.000Z","correlation_id":"tr_aaa","event_type":"_platform.collection.updated","source":"_platform","data":{"name":"snippets"},"matched_subscription_pattern":"_platform.collection.*"},{"delivered_at_utc":"2026-10-21T17:00:00.500Z","event_type":"_platform.app.focused","source":"_platform","data":{},"matched_subscription_pattern":"_platform.app.*"}]`
	diagEventsBody = `{"records":[{"ts_utc":"2026-10-21T17:00:02.000Z","source":"show-all","severity":"warn","caller":"snippets","event_type":"plugin.diagnostic","params":{"tag":"PACK_ENTRY_SKIPPED"}}],"chains":[],"plugin_callers":[],"truncated":false}`
)

func diagChainBody(tr string) string {
	return `{"records":[{"ts_utc":"t1","source":"show-all","severity":"info","caller":"voice","event_type":"command.matched","params":{}},{"ts_utc":"t2","source":"show-all","severity":"error","caller":"snippets","event_type":"dispatch.failed","params":{"tr":"` + tr + `"}}],"chains":[],"plugin_callers":["snippets"]}`
}

// diagServer answers the diagnose endpoints from the recorded bodies.
// fail names paths that answer 500 instead, to prove sections degrade
// on their own.
func diagServer(t *testing.T, fail map[string]bool, seen *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		key := req.Method + " " + req.URL.Path
		if req.URL.Path == "/v1/plugins/snippets/log" && req.URL.Query().Get("level") == "warn" {
			key += "?warn"
		}
		var body map[string]any
		if req.Method == "POST" {
			raw, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(raw, &body)
			if tr, ok := body["correlation_id"].(string); ok {
				key += "#" + tr
			}
		}
		*seen = append(*seen, key)
		if fail[key] {
			w.WriteHeader(500)
			_, _ = w.Write([]byte("boom"))
			return
		}
		switch {
		case key == "GET /v1/plugins/snippets/status":
			_, _ = w.Write([]byte(diagStatusBody))
		case key == "GET /v1/plugins/snippets/log?warn":
			_, _ = w.Write([]byte(diagWarnBody))
		case key == "GET /v1/plugins/snippets/log":
			_, _ = w.Write([]byte(diagLogBody))
		case key == "GET /v1/plugins/snippets/replay":
			_, _ = w.Write([]byte(diagReplayBody))
		case strings.HasPrefix(key, "POST /v1/events/query#"):
			_, _ = w.Write([]byte(diagChainBody(strings.TrimPrefix(key, "POST /v1/events/query#"))))
		case key == "POST /v1/events/query":
			if body["plugin"] != "snippets" || body["severity"] != "warn" {
				t.Errorf("events query body = %v", body)
			}
			_, _ = w.Write([]byte(diagEventsBody))
		default:
			w.WriteHeader(404)
		}
	}))
	oldBase, oldSock := devBaseURL, devUnixSocket
	devBaseURL, devUnixSocket = srv.URL, ""
	t.Cleanup(func() {
		srv.Close()
		devBaseURL, devUnixSocket = oldBase, oldSock
	})
	return srv
}

func TestDiagnoseComposesEverySection(t *testing.T) {
	var seen []string
	diagServer(t, nil, &seen)
	r := collectDiagnosis("snippets", "tok", "", false)

	if r.Status.Error != "" || r.Status.Status != "running" || r.Status.EventsDenied != 2 {
		t.Errorf("status = %+v", r.Status)
	}
	if r.Since != "1h" {
		t.Errorf("default window = %q, want 1h", r.Since)
	}
	if len(r.Warnings.Lines) != 1 || len(r.Log.Lines) != 2 || len(r.Events.Records) != 1 {
		t.Errorf("warnings %d, log %d, events %d", len(r.Warnings.Lines), len(r.Log.Lines), len(r.Events.Records))
	}
	if r.Received.Total != 2 {
		t.Errorf("received total = %d", r.Received.Total)
	}
	// Chains come from the replay's correlation ids and the log's tr_ ids,
	// newest last, each looked up once.
	var ids []string
	for _, c := range r.Chains.Chains {
		ids = append(ids, c.CorrelationID)
		if c.Records != 2 || c.MaxSeverity != "error" || c.FirstEvent != "command.matched" {
			t.Errorf("chain summary = %+v", c)
		}
	}
	if strings.Join(ids, ",") != "tr_aaa,tr_bbb" {
		t.Errorf("chains = %v, want tr_aaa,tr_bbb (newest last)", ids)
	}
	lookups := map[string]int{}
	for _, k := range seen {
		if strings.Contains(k, "#tr_") {
			lookups[k]++
		}
	}
	if len(lookups) != 2 || lookups["POST /v1/events/query#tr_aaa"] != 1 || lookups["POST /v1/events/query#tr_bbb"] != 1 {
		t.Errorf("each chain should be looked up once: %v", lookups)
	}

	var out bytes.Buffer
	renderDiagnosis(&out, r)
	text := out.String()
	for _, want := range []string{
		"Plugin snippets (via host token, window 1h)",
		"running, enabled=true, version 0.3.0",
		"2 deliveries denied",
		"PACK_ENTRY_SKIPPED",
		"_platform.collection.updated",
		"tr_bbb  2026-10-21T17:00:01.000Z    2 records  error command.matched",
		"Next:",
		"branchkit-cli dev chain tr_bbb",
		"branchkit-cli dev plog snippets --since 60s",
		`branchkit-cli dev say "<phrase>" --simulate`,
		"branchkit-cli dev events --source audit --plugin snippets",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	// Newest last: the earlier delivery prints before the later one.
	if strings.Index(text, "_platform.collection.updated") > strings.Index(text, "_platform.app.focused") {
		t.Error("received events are not newest last")
	}
}

func TestDiagnoseSectionsDegradeIndependently(t *testing.T) {
	var seen []string
	diagServer(t, map[string]bool{
		"GET /v1/plugins/snippets/replay":   true,
		"GET /v1/plugins/snippets/log?warn": true,
		"POST /v1/events/query#tr_bbb":      true,
	}, &seen)
	r := collectDiagnosis("snippets", "tok", "5m", false)

	if !strings.Contains(r.Received.Error, "HTTP 500") || !strings.Contains(r.Warnings.Error, "HTTP 500") {
		t.Errorf("failing sections should carry their reason: received %q, warnings %q", r.Received.Error, r.Warnings.Error)
	}
	if r.Status.Error != "" || len(r.Log.Lines) != 2 || len(r.Events.Records) != 1 {
		t.Errorf("healthy sections must still fill: %+v", r)
	}
	// With the replay down, the log's tr_ id still names a chain, and its
	// failed lookup is reported on that chain alone.
	if len(r.Chains.Chains) != 1 || r.Chains.Chains[0].CorrelationID != "tr_bbb" || r.Chains.Chains[0].Error == "" {
		t.Errorf("chains = %+v", r.Chains.Chains)
	}
	if r.failed() {
		t.Error("a partial snapshot is not a failed run")
	}
	var out bytes.Buffer
	renderDiagnosis(&out, r)
	if n := strings.Count(out.String(), "\n  (unavailable: HTTP 500"); n != 2 {
		t.Errorf("want 2 unavailable sections, got %d:\n%s", n, out.String())
	}
	if !strings.Contains(out.String(), "tr_bbb  2026-10-21T17:00:01.000Z  (unavailable: HTTP 500") {
		t.Errorf("failed chain lookup not shown:\n%s", out.String())
	}
	for _, k := range seen {
		if k == "GET /v1/plugins/snippets/log" {
			return
		}
	}
	t.Error("the log tail was not fetched")
}

func TestDiagnoseOnDeveloperAccessSkipsStatus(t *testing.T) {
	var seen []string
	diagServer(t, nil, &seen)
	r := collectDiagnosis("snippets", "tok", "", true)
	if r.Access != "developer_access" || !strings.Contains(r.Status.Error, "Developer Access") {
		t.Errorf("status on a grant = %+v", r.Status)
	}
	for _, k := range seen {
		if strings.HasSuffix(k, "/status") {
			t.Error("the host-only status route was called on a grant")
		}
	}
	if len(r.Log.Lines) == 0 || len(r.Chains.Chains) == 0 {
		t.Error("the grant's own sections should still fill")
	}
}

func TestDiagnoseUnreachableAppFails(t *testing.T) {
	var seen []string
	srv := diagServer(t, nil, &seen)
	srv.Close()
	r := collectDiagnosis("snippets", "tok", "", false)
	if !r.failed() {
		t.Errorf("every section failed, so the run should fail: %+v", r)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	// The JSON form keeps every section present, with its error.
	for _, k := range []string{`"status"`, `"warnings"`, `"events"`, `"log"`, `"received"`, `"chains"`, `"next"`} {
		if !bytes.Contains(raw, []byte(k)) {
			t.Errorf("JSON lacks %s", k)
		}
	}
}

func TestSayTraceHintNamesTheChain(t *testing.T) {
	got := sayTraceHint([]byte(`{"ingested":true,"correlation_id":"tr_abc123DEF45"}`))
	if got != "Trace: branchkit-cli dev chain tr_abc123DEF45" {
		t.Errorf("got %q", got)
	}
	if got := sayTraceHint([]byte(`{"ingested":true}`)); !strings.Contains(got, "dev chain") {
		t.Errorf("older app: got %q", got)
	}
}
