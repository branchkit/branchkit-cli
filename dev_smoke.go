package main

// dev smoke / dev say / dev chain — dev-loop verification against the
// RUNNING BranchKit instance on :21551.
//
// `dev smoke` is entirely side-effect-free: every "would this match?"
// assertion goes through commands.resolve with preview=true (computes the
// decision, commits nothing), and the one transcript it injects is a
// sentinel that is pre-checked NOT to match any command before it is sent
// (and aborts if it would). Nothing scrolls, clicks, or types.
//
// `dev say` is the deliberate opposite — it injects a real transcript and
// matched commands REALLY execute. It exists for manual dev-loop
// verification, not for tests.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const devBaseURL = "http://127.0.0.1:21551"

func devHTTP(method, path, token string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, devBaseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return raw, resp.StatusCode, err
}

// resolvePreview runs commands.resolve with preview=true — the
// verify-don't-execute mode. Returns whether the words matched.
func resolvePreview(token string, words []string) (bool, error) {
	raw, status, err := devHTTP("POST", "/v1/commands/resolve", token, map[string]any{
		"words":   words,
		"preview": true,
	})
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("resolve returned %d: %s", status, strings.TrimSpace(string(raw)))
	}
	var result struct {
		Matched bool `json:"matched"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return false, err
	}
	return result.Matched, nil
}

// literalPattern reports whether a display pattern is plain literal words —
// no captures (<...>), alternations ((a|b)), or quantifiers — so the pattern
// string itself is a speakable utterance.
var literalPatternRe = regexp.MustCompile(`^[a-z0-9 _-]+$`)

func literalPattern(p string) bool {
	return literalPatternRe.MatchString(p)
}

// tailContains reads the last maxBytes of path and reports whether needle
// appears in it.
func tailContains(path, needle string, maxBytes int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	start := info.Size() - maxBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), needle)
}

type smokeCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass | warn | fail
	Detail string `json:"detail"`
}

func cmdDevSmoke(args []string) {
	jsonOutput := false
	for _, a := range args {
		if a == "--json" {
			jsonOutput = true
		}
	}

	var checks []smokeCheck
	add := func(name, status, detail string) {
		checks = append(checks, smokeCheck{Name: name, Status: status, Detail: detail})
		if !jsonOutput {
			mark := map[string]string{"pass": "✓", "warn": "⚠", "fail": "✗"}[status]
			fmt.Printf("  %s %-22s %s\n", mark, name, detail)
		}
	}
	finish := func() {
		failures := 0
		for _, c := range checks {
			if c.Status == "fail" {
				failures++
			}
		}
		if jsonOutput {
			out, _ := json.MarshalIndent(map[string]any{"checks": checks, "failures": failures}, "", "  ")
			fmt.Println(string(out))
		} else if failures > 0 {
			fmt.Printf("\n%d check(s) failed\n", failures)
		} else {
			fmt.Println("\nAll smoke checks passed")
		}
		if failures > 0 {
			os.Exit(1)
		}
	}

	if !jsonOutput {
		fmt.Println("BranchKit smoke (side-effect-free — preview resolves + one non-matching sentinel)")
	}

	// --- 1. Connectivity + auth ---
	token := readHostToken()
	if token == "" {
		add("connectivity", "fail", "no host.token — is BranchKit running?")
		finish()
		return
	}
	if _, status, err := devHTTP("GET", "/plugins", "", nil); err != nil || status != 200 {
		add("connectivity", "fail", fmt.Sprintf("GET /plugins: status=%d err=%v", status, err))
		finish()
		return
	}
	add("connectivity", "pass", "app up, host token present")

	// --- 2. Layer-2 state: matchable inspector ---
	raw, status, err := devHTTP("GET", "/inspector/matchable", "", nil)
	var matchable struct {
		ActiveTags          []string       `json:"active_tags"`
		ExclusiveTags       []string       `json:"exclusive_tags"`
		ExclusiveNamespaces []string       `json:"exclusive_namespaces"`
		EligibleCount       int            `json:"eligible_count"`
		GatedCount          int            `json:"gated_count"`
		Eligible            []matchableCmd `json:"eligible"`
		Gated               []matchableCmd `json:"gated"`
	}
	if err != nil || status != 200 || json.Unmarshal(raw, &matchable) != nil {
		add("matchable", "fail", fmt.Sprintf("GET /inspector/matchable: status=%d err=%v", status, err))
		finish()
		return
	}
	if matchable.EligibleCount == 0 {
		add("matchable", "fail", "0 eligible commands — registry empty or every command gated")
	} else {
		add("matchable", "pass", fmt.Sprintf("%d eligible / %d gated, %d active tags (%d exclusive)",
			matchable.EligibleCount, matchable.GatedCount, len(matchable.ActiveTags), len(matchable.ExclusiveTags)))
	}

	// --- 3. Layer-1 state: vocabulary lag ---
	raw, status, err = devHTTP("GET", "/inspector/vocabulary", "", nil)
	var vocab struct {
		InSync        bool     `json:"in_sync"`
		EverCommitted bool     `json:"ever_committed"`
		Lag           []string `json:"lag"`
		DivergedAgeMs *int64   `json:"diverged_age_ms"`
	}
	if err != nil || status != 200 || json.Unmarshal(raw, &vocab) != nil {
		add("vocabulary", "fail", fmt.Sprintf("GET /inspector/vocabulary: status=%d err=%v", status, err))
	} else if !vocab.EverCommitted {
		add("vocabulary", "warn", "no grammar ever committed — recognizer not warmed yet?")
	} else if vocab.InSync {
		add("vocabulary", "pass", "committed grammar in sync with desired union")
	} else if vocab.DivergedAgeMs != nil && *vocab.DivergedAgeMs > 5000 {
		add("vocabulary", "fail", fmt.Sprintf("grammar lag stuck for %dms (%d words) — commit backstop not firing?",
			*vocab.DivergedAgeMs, len(vocab.Lag)))
	} else {
		add("vocabulary", "warn", fmt.Sprintf("transient grammar lag (%d words) — normal during collection churn", len(vocab.Lag)))
	}

	// --- 4. Transport sentinel: pipeline event → voice plugin, no match ---
	// The sentinel is pre-checked against the live matcher: if it would match
	// anything, abort rather than execute a command.
	sentinel := fmt.Sprintf("smoke sentinel %d", time.Now().UnixNano()%1000000)
	if matched, err := resolvePreview(token, strings.Fields(sentinel)); err != nil {
		add("transport", "fail", fmt.Sprintf("sentinel pre-check: %v", err))
	} else if matched {
		add("transport", "fail", fmt.Sprintf("sentinel %q would MATCH a command — not injecting", sentinel))
	} else {
		_, status, err := devHTTP("POST", "/v1/pipelines/ingest-transcript", token, map[string]any{
			"name": "command_recognition",
			"text": sentinel,
		})
		if err != nil || status != 200 {
			add("transport", "fail", fmt.Sprintf("ingest-transcript: status=%d err=%v", status, err))
		} else {
			// The voice plugin logs every command_recognition transcript it
			// handles (orchestrated / dropped) — either line proves delivery.
			logPath := filepath.Join(appSupportDir(), "actuator.log")
			seen := false
			for i := 0; i < 15; i++ {
				if tailContains(logPath, sentinel, 512*1024) {
					seen = true
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			if seen {
				add("transport", "pass", "sentinel reached the voice plugin (event → orchestration seam live)")
			} else {
				add("transport", "fail", "sentinel never appeared in voice plugin logs — plugin down, subscription broken, or a dictation/calibration session is swallowing transcripts")
			}
		}
	}

	// --- 5. Registry↔matcher agreement: every eligible literal pattern must
	// preview-resolve. Derived from the live registry — no fixture to drift.
	literals := 0
	skipped := 0
	var failedPatterns []string
	for _, cmd := range matchable.Eligible {
		if !literalPattern(cmd.Pattern) {
			skipped++
			continue
		}
		literals++
		matched, err := resolvePreview(token, strings.Fields(cmd.Pattern))
		if err != nil || !matched {
			failedPatterns = append(failedPatterns, fmt.Sprintf("%s (%s)", cmd.Pattern, cmd.Owner))
		}
	}
	if literals == 0 {
		add("resolve-sweep", "warn", "no fully-literal eligible patterns to sweep")
	} else if len(failedPatterns) == 0 {
		add("resolve-sweep", "pass", fmt.Sprintf("%d/%d eligible literal patterns preview-resolve (%d with captures/alternations skipped)",
			literals, literals, skipped))
	} else {
		sample := failedPatterns
		if len(sample) > 5 {
			sample = sample[:5]
		}
		add("resolve-sweep", "fail", fmt.Sprintf("%d/%d eligible literal patterns did NOT resolve: %s",
			len(failedPatterns), literals, strings.Join(sample, ", ")))
	}

	// --- 6. Prefix-collision lint: an UNGATED literal command that is a
	// word-prefix of a GATED command reachable in the same context can
	// never execute there — the matcher's gated-Partial-suppresses-
	// ungated-exact discipline (matching.rs, the cold-startup hint-race
	// design) swallows it into completion mode. That swallow is
	// load-bearing (continuous decoding segments on ~100ms VAD silences,
	// so eager exact-match would misfire; the fix belongs in the
	// VOCABULARY). Precision notes, each mirroring a matcher rule:
	//   - gated exact beats gated partial, so gated shorts are safe
	//     ("up" resolves under "up level");
	//   - only a LITERAL continuation swallows — a capture next-token
	//     does not ("scroll down" resolves under "scroll down <number>");
	//   - an exclusive mode suppresses everything outside it, so
	//     commands are co-reachable only with EQUAL exclusive-gate sets.
	// See branchkit-extension docs/design/PLAN_RELIABILITY_CONSOLIDATION.md
	// (prefix-free vocabulary arc). Warn-level while the first-party
	// vocabulary pass is open.
	collisions := prefixCollisions(append(matchable.Eligible, matchable.Gated...), matchable.ExclusiveNamespaces)
	if len(collisions) == 0 {
		add("prefix-lint", "pass", fmt.Sprintf("no prefix collisions among co-reachable commands (%d commands checked)",
			len(matchable.Eligible)+len(matchable.Gated)))
	} else {
		sample := collisions
		if len(sample) > 4 {
			sample = sample[:4]
		}
		add("prefix-lint", "warn", fmt.Sprintf("%d shadowed command(s) — unreachable where their extensions are live: %s",
			len(collisions), strings.Join(sample, "; ")))
	}

	// --- 7. Collection-ownership guard: cross-owner rehydrate refused, or
	// orphaned data (no live introducer). Reinstalling a plugin — or a
	// different plugin reusing a collection name — must never silently inherit
	// the old owner's on-disk data (storage is keyed by name alone). The
	// reconciliation pass in build_collections refuses the mismatch and
	// classifies orphans against uninstall markers + grace. `refused` is the
	// last reconciliation; `orphans` are surfaced-not-deleted; `quarantined`
	// is the durable collection_logs/orphaned/ scan. See
	// DESIGN_PLUGIN_DATA_LIFECYCLE.md.
	raw, status, err = devHTTP("GET", "/inspector/ownership", "", nil)
	var ownership struct {
		RefusedCount       int  `json:"refused_count"`
		OrphanCount        int  `json:"orphan_count"`
		OrphansReclaimable int  `json:"orphans_reclaimable"`
		OrphansRetained    int  `json:"orphans_retained"`
		OrphansUnmarked    int  `json:"orphans_unmarked"`
		QuarantinedCount   int  `json:"quarantined_count"`
		UnownedGroupCount  int  `json:"unowned_group_count"`
		GroupCount         int  `json:"group_count"`
		UnattributedRecs   int  `json:"unattributed_records"`
		Clean              bool `json:"clean"`
		Refused            []struct {
			Collection        string `json:"collection"`
			Storage           string `json:"storage"`
			StoredIntroducer  string `json:"stored_introducer"`
			CurrentIntroducer string `json:"current_introducer"`
		} `json:"refused"`
		UnownedGroups []struct {
			Collection string `json:"collection"`
			Writer     string `json:"writer"`
			Count      int    `json:"count"`
		} `json:"unowned_groups"`
	}
	if err != nil || status != 200 || json.Unmarshal(raw, &ownership) != nil {
		add("ownership", "fail", fmt.Sprintf("GET /inspector/ownership: status=%d err=%v", status, err))
	} else if ownership.Clean {
		detail := fmt.Sprintf(
			"no cross-owner conflicts, orphans, quarantined data, or unowned record groups (%d group(s) inventoried)",
			ownership.GroupCount)
		// Pre-Record.writer records: unreachable by any writer filter, but not
		// a lost plugin. Reported so they stay visible as they age out.
		if ownership.UnattributedRecs > 0 {
			detail += fmt.Sprintf("; %d unattributed record(s) predate the writer field", ownership.UnattributedRecs)
		}
		add("ownership", "pass", detail)
	} else {
		var sample []string
		for _, r := range ownership.Refused {
			sample = append(sample, fmt.Sprintf("%s (%s: %s→%s)", r.Collection, r.Storage, r.StoredIntroducer, r.CurrentIntroducer))
			if len(sample) >= 3 {
				break
			}
		}
		// An unowned group is records whose writer is gone — nothing will ever
		// replace them, and the collection-level orphan pass cannot see it when
		// the collection itself is alive under another owner.
		for _, g := range ownership.UnownedGroups {
			sample = append(sample, fmt.Sprintf("%s: %d record(s) owned by absent %q", g.Collection, g.Count, g.Writer))
			if len(sample) >= 5 {
				break
			}
		}
		// A cross-owner refusal is the guard working (data protected, not
		// applied); orphans are surfaced-not-deleted per design. Warn so both
		// are visible without failing the sweep.
		detail := fmt.Sprintf("%d refused, %d orphan(s) [%d reclaimable / %d retained / %d unmarked], %d quarantined file(s), %d unowned group(s)",
			ownership.RefusedCount, ownership.OrphanCount,
			ownership.OrphansReclaimable, ownership.OrphansRetained, ownership.OrphansUnmarked,
			ownership.QuarantinedCount, ownership.UnownedGroupCount)
		if len(sample) > 0 {
			detail += ": " + strings.Join(sample, "; ")
		}
		add("ownership", "warn", detail)
	}

	finish()
}

// matchableCmd is the per-command shape of /inspector/matchable entries the
// smoke checks consume.
type matchableCmd struct {
	Owner        string   `json:"owner"`
	Pattern      string   `json:"pattern"`
	RequiresTags []string `json:"requires_tags"`
}

// exclusiveKey canonicalizes the subset of a command's required tags that
// fall under a declared-exclusive namespace. Two commands can be
// simultaneously matchable only if these subsets are EQUAL: while an
// exclusive gate is active the matcher suppresses every command that
// doesn't require it, and while it's inactive the commands requiring it
// are gated.
func exclusiveKey(tags, exclusiveNamespaces []string) string {
	var ex []string
	for _, t := range tags {
		for _, ns := range exclusiveNamespaces {
			if t == ns || strings.HasPrefix(t, ns+".") {
				ex = append(ex, t)
				break
			}
		}
	}
	sort.Strings(ex)
	return strings.Join(ex, ",")
}

// prefixCollisions reports complete literal commands that are word-prefixes
// of a longer command requiring the same exclusive gates (i.e. reachable in
// the same context). One entry per shadowed short command, e.g.
// "'pause' ⊂ 'pause video' (+2 more)".
func prefixCollisions(cmds []matchableCmd, exclusiveNamespaces []string) []string {
	literalTok := func(tok string) bool {
		return !strings.ContainsAny(tok, "<{([|")
	}
	type short struct {
		words []string
		key   string
	}
	shorts := make(map[string]short) // pattern -> tokenized short candidate
	for _, c := range cmds {
		// Only UNGATED commands can be swallowed (a gated exact beats a
		// gated partial), so only they are collision candidates.
		if len(c.RequiresTags) > 0 {
			continue
		}
		words := strings.Fields(c.Pattern)
		ok := len(words) > 0
		for _, w := range words {
			if !literalTok(w) {
				ok = false
				break
			}
		}
		if ok {
			shorts[c.Pattern] = short{words: words, key: exclusiveKey(c.RequiresTags, exclusiveNamespaces)}
		}
	}
	shadowedBy := make(map[string][]string) // short pattern -> extension patterns
	for _, c := range cmds {
		ext := strings.Fields(c.Pattern)
		for pat, s := range shorts {
			if len(ext) <= len(s.words) {
				continue
			}
			match := true
			for i, w := range s.words {
				if !literalTok(ext[i]) || ext[i] != w {
					match = false
					break
				}
			}
			// Only a LITERAL continuation swallows the short command into
			// completion mode — a capture-shaped next token does not block
			// the exact match (observed: "scroll down" resolves fine under
			// "scroll down <number>", while "copy" is swallowed by
			// "copy url").
			if match && !literalTok(ext[len(s.words)]) {
				match = false
			}
			// Only a GATED extension suppresses the ungated short (an
			// ungated partial never suppresses an exact match).
			if match && len(c.RequiresTags) > 0 && exclusiveKey(c.RequiresTags, exclusiveNamespaces) == s.key {
				shadowedBy[pat] = append(shadowedBy[pat], c.Pattern)
			}
		}
	}
	var out []string
	var keys []string
	for k := range shadowedBy {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		exts := shadowedBy[k]
		sort.Strings(exts)
		entry := fmt.Sprintf("%q ⊂ %q", k, exts[0])
		if len(exts) > 1 {
			entry += fmt.Sprintf(" (+%d more)", len(exts)-1)
		}
		out = append(out, entry)
	}
	return out
}

func cmdDevSay(args []string) {
	pipeline := "command_recognition"
	var text string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pipeline":
			if i+1 < len(args) {
				i++
				pipeline = args[i]
			}
		default:
			if text == "" {
				text = args[i]
			} else {
				text += " " + args[i]
			}
		}
	}
	if text == "" {
		fmt.Fprintln(os.Stderr, "Usage: branchkit-cli dev say <text> [--pipeline name]")
		fmt.Fprintln(os.Stderr, "Injects a synthetic transcript — matched commands REALLY execute.")
		os.Exit(1)
	}
	token := readHostToken()
	if token == "" {
		fmt.Fprintln(os.Stderr, "Error: no host.token — is BranchKit running?")
		os.Exit(1)
	}
	raw, status, err := devHTTP("POST", "/v1/pipelines/ingest-transcript", token, map[string]any{
		"name": pipeline,
		"text": text,
	})
	if err != nil || status != 200 {
		fmt.Fprintf(os.Stderr, "Error: status=%d err=%v body=%s\n", status, err, strings.TrimSpace(string(raw)))
		os.Exit(1)
	}
	fmt.Println(strings.TrimSpace(string(raw)))
	fmt.Println("Trace: branchkit-cli dev chain   (or grep the text in actuator.log)")
}

func cmdDevChain(args []string) {
	limit := 20
	var tr string
	jsonOutput := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "--limit":
			if i+1 < len(args) {
				i++
				if n, err := strconv.Atoi(args[i]); err == nil {
					limit = n
				}
			}
		default:
			if strings.HasPrefix(args[i], "tr_") {
				tr = args[i]
			}
		}
	}
	token := readHostToken()
	if token == "" {
		fmt.Fprintln(os.Stderr, "Error: no host.token — is BranchKit running?")
		os.Exit(1)
	}

	body := map[string]any{}
	if tr != "" {
		body["correlation_id"] = tr
	} else {
		body["limit"] = limit
	}
	raw, status, err := devHTTP("POST", "/v1/events/query", token, body)
	if err != nil || status != 200 {
		fmt.Fprintf(os.Stderr, "Error: status=%d err=%v body=%s\n", status, err, strings.TrimSpace(string(raw)))
		os.Exit(1)
	}
	if jsonOutput {
		fmt.Println(string(raw))
		return
	}

	var result struct {
		Records []struct {
			TsUTC     string          `json:"ts_utc"`
			Source    string          `json:"source"`
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
		PluginCallers []string `json:"plugin_callers"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		fmt.Println(string(raw))
		return
	}

	if tr != "" {
		if len(result.Records) == 0 {
			fmt.Printf("No records for %s in the hot window (older chains live in the show-all archives)\n", tr)
			return
		}
		for _, r := range result.Records {
			params := string(r.Params)
			if len(params) > 120 {
				params = params[:120] + "…"
			}
			fmt.Printf("%s  %-8s %-10s %-24s %s\n", r.TsUTC, r.Severity, r.Source, r.EventType, params)
		}
		if len(result.PluginCallers) > 0 {
			fmt.Printf("\nPlugin callers in chain: %s — sub-warn plugin logs don't reach the bus; see `branchkit-cli dev logs`\n",
				strings.Join(result.PluginCallers, ", "))
		}
	} else {
		for _, c := range result.Chains {
			fmt.Printf("%s  %s  %2d records  %-8s %s\n",
				c.CorrelationID, c.When, c.RecordCount, c.MaxSeverity, c.HeadlineEvent)
		}
	}
}
