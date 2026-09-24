package main

// dev bisect — the interactive driver for the actuator's plugin bisect
// The actuator owns the search:
// this loop only shows what each round disabled, asks the one question, and
// posts the answer. The human is the oracle; this file is just the table
// they sit at.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type bisectStatus struct {
	Session       bool            `json:"session"`
	Interrupted   bool            `json:"interrupted"`
	Phase         json.RawMessage `json:"phase"`
	Round         int             `json:"round"`
	Suspects      []string        `json:"suspects"`
	Untestable    []string        `json:"untestable"`
	RoundDisabled []string        `json:"round_disabled"`
	Error         string          `json:"error"`
}

type bisectPhase struct {
	Phase   string `json:"phase"`
	Finding *struct {
		Kind         string   `json:"kind"`
		PluginID     string   `json:"plugin_id"`
		DisabledWith []string `json:"disabled_with"`
		Plugins      []string `json:"plugins"`
		Untested     []string `json:"untested"`
		Suspects     []string `json:"suspects"`
	} `json:"finding"`
}

// bisectCheck is a scripted oracle: a phrase and the plugin it must resolve
// to. When present, the CLI answers each round itself via the side-effect-free
// commands.resolve preview instead of asking a human — the routing symptom
// ("I say X and the wrong thing answers") is mechanically checkable.
type bisectCheck struct {
	phrase   string
	expected string
}

// parseCheckSpec parses "--check" syntax: "<phrase> => <plugin-id>" (or ->).
func parseCheckSpec(spec string) (bisectCheck, error) {
	sep := "=>"
	i := strings.Index(spec, sep)
	if i < 0 {
		sep = "->"
		i = strings.Index(spec, sep)
	}
	if i < 0 {
		return bisectCheck{}, fmt.Errorf("expected \"<phrase> => <plugin-id>\", got %q", spec)
	}
	c := bisectCheck{
		phrase:   strings.TrimSpace(spec[:i]),
		expected: strings.TrimSpace(spec[i+len(sep):]),
	}
	if c.phrase == "" || c.expected == "" {
		return bisectCheck{}, fmt.Errorf("expected \"<phrase> => <plugin-id>\", got %q", spec)
	}
	return c, nil
}

// checkAnswer turns a resolve verdict into the oracle's answer. The symptom
// is "the phrase does not route to the expected plugin" — so a match by the
// right owner means gone, and anything else (wrong owner, or no match at
// all) means still happening.
func checkAnswer(matched bool, owner, expected string) (answer, verdict string) {
	if matched && owner == expected {
		return "gone", fmt.Sprintf("routes to %s — gone", expected)
	}
	if matched {
		return "still_happening", fmt.Sprintf("routes to %s — still happening", owner)
	}
	return "still_happening", "no match — still happening"
}

func cmdDevBisect(args []string) {
	var pinned []string
	var check *bisectCheck
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "restore":
			devBisectRestore()
			return
		case "cancel":
			devBisectPost("/v1/bisect/cancel", nil)
			fmt.Println("Cancelled — your original enabled/disabled set is restored.")
			return
		case "--pin":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --pin needs a plugin id")
				os.Exit(1)
			}
			i++
			pinned = append(pinned, args[i])
		case "--check":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --check needs \"<phrase> => <plugin-id>\"")
				os.Exit(1)
			}
			i++
			c, err := parseCheckSpec(args[i])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: --check: %v\n", err)
				os.Exit(1)
			}
			check = &c
		case "help", "--help", "-h":
			printDevBisectUsage()
			return
		default:
			fmt.Fprintf(os.Stderr, "Unknown bisect argument: %s\n", args[i])
			printDevBisectUsage()
			os.Exit(1)
		}
	}
	if check != nil {
		// The expected owner must stay enabled for the check to mean
		// anything: disabling it would make every round read "not routing
		// to it" — the experiment breaking its own instrument. The pin is a
		// property of how the bisect is driven (design doc), and a
		// check-driven bisect is driven by this plugin's answers.
		pinned = append(pinned, check.expected)
	}

	// A crash's leftovers block a new start; say so plainly instead of 409ing.
	st := devBisectGet("/v1/bisect/status")
	if !st.Session && st.Interrupted {
		fmt.Println("An earlier bisect was interrupted and the fleet may still be half-disabled.")
		fmt.Println("Run `branchkit-cli dev bisect restore` first.")
		os.Exit(1)
	}

	if check != nil && !st.Session {
		// Baseline: confirm the symptom exists BEFORE touching the fleet.
		// A phrase that already routes where it should would send the
		// search chasing nothing — the honest output is "no symptom".
		matched, owner, err := resolveOwnerPreview(check.phrase)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: baseline resolve failed: %v\n", err)
			os.Exit(1)
		}
		answer, verdict := checkAnswer(matched, owner, check.expected)
		fmt.Printf("Baseline — %q %s\n", check.phrase, verdict)
		if answer == "gone" {
			fmt.Printf("No symptom: the phrase already routes to %s. Nothing to bisect.\n", check.expected)
			return
		}
	}

	var start bisectStatus
	if st.Session {
		// A live session survives the CLI exiting; rejoin it where it stands.
		fmt.Println("Rejoining the bisect already in progress.")
		start = st
	} else {
		start = devBisectPost("/v1/bisect/start", map[string]any{"pinned": pinned})
	}
	if len(start.Untestable) > 0 {
		fmt.Printf("Not testable this run (pinned, or a pinned plugin depends on them): %s\n",
			strings.Join(start.Untestable, ", "))
	}
	if check == nil {
		fmt.Println("Reproduce the symptom after each round, then answer the question.")
		fmt.Println("Answers: y = still happening · n = gone · u = not sure · q = quit (restores everything)")
	} else {
		fmt.Printf("Scripted oracle: does %q route to %s? Each round answers itself.\n",
			check.phrase, check.expected)
	}
	fmt.Println()

	in := bufio.NewScanner(os.Stdin)
	cur := start
	for {
		fmt.Printf("Round %d — disabled: %s\n", cur.Round, orNone(cur.RoundDisabled))
		fmt.Printf("         suspects: %s\n", orNone(cur.Suspects))
		var answer string
		if check != nil {
			matched, owner, err := resolveOwnerPreview(check.phrase)
			if err != nil {
				// Leave nothing half-applied on the way out: a scripted run
				// has no human to notice a modified fleet.
				fmt.Fprintf(os.Stderr, "Error: resolve failed mid-bisect: %v\n", err)
				fmt.Fprintln(os.Stderr, "Cancelling and restoring your original set.")
				devBisectPost("/v1/bisect/cancel", nil)
				os.Exit(1)
			}
			var verdict string
			answer, verdict = checkAnswer(matched, owner, check.expected)
			fmt.Printf("         %q %s\n", check.phrase, verdict)
		} else {
			fmt.Print("Is it still happening? [y/n/u/q] ")
			if !in.Scan() {
				fmt.Println("\nInput closed — cancelling and restoring your original set.")
				devBisectPost("/v1/bisect/cancel", nil)
				return
			}
			switch strings.ToLower(strings.TrimSpace(in.Text())) {
			case "y", "yes":
				answer = "still_happening"
			case "n", "no":
				answer = "gone"
			case "u", "unsure", "not sure":
				answer = "not_sure"
			case "q", "quit":
				devBisectPost("/v1/bisect/cancel", nil)
				fmt.Println("Cancelled — your original enabled/disabled set is restored.")
				return
			default:
				fmt.Println("Please answer y, n, u, or q.")
				continue
			}
		}
		cur = devBisectPost("/v1/bisect/answer", map[string]any{"answer": answer})
		var ph bisectPhase
		_ = json.Unmarshal(cur.Phase, &ph)
		if ph.Phase != "done" {
			fmt.Println()
			continue
		}
		fmt.Println()
		printBisectFinding(ph)
		return
	}
}

func printBisectFinding(ph bisectPhase) {
	f := ph.Finding
	if f == nil {
		fmt.Println("Finished, but the finding could not be read — check `dev bisect` against this app version.")
		return
	}
	switch f.Kind {
	case "culprit":
		fmt.Printf("Culprit: %s\n", f.PluginID)
		if len(f.DisabledWith) > 0 {
			fmt.Printf("(its dependents %s were disabled along with it — it cannot be isolated further)\n",
				strings.Join(f.DisabledWith, ", "))
		}
		fmt.Println("Disabling it made the symptom stop, verified in its own round.")
	case "indivisible":
		fmt.Printf("The culprit is inside a dependency-entangled group the search cannot split: %s\n",
			strings.Join(f.Plugins, ", "))
		fmt.Println("Disabling the whole group made the symptom stop; their dependency cycle is why no member can be tested alone.")
	case "none_of_them":
		fmt.Println("No plugin: the symptom reproduced with every testable plugin disabled.")
		if len(f.Untested) > 0 {
			fmt.Printf("It is the platform's fault, or hides in what could not be tested: %s\n",
				strings.Join(f.Untested, ", "))
		} else {
			fmt.Println("That points at the platform itself — worth reporting as a BranchKit bug.")
		}
	case "no_single_culprit":
		fmt.Println("The answers stopped supporting a single culprit — an intermittent symptom,")
		fmt.Println("or an interaction the experiment itself changes. No plugin is being named on a coin flip.")
		if len(f.Suspects) > 0 {
			fmt.Printf("The narrowing still points near: %s\n", strings.Join(f.Suspects, ", "))
		}
	default:
		fmt.Printf("Finished: %s\n", f.Kind)
	}
	fmt.Println("Your original enabled/disabled set is restored.")
}

func devBisectRestore() {
	st := devBisectPost("/v1/bisect/restore", nil)
	if st.Error != "" {
		fmt.Println(st.Error)
		return
	}
	fmt.Println("Restored the enabled/disabled set from before the interrupted bisect.")
}

func orNone(v []string) string {
	if len(v) == 0 {
		return "(none)"
	}
	return strings.Join(v, ", ")
}

// resolveOwnerPreview asks the matcher, side-effect-free, who would claim
// the phrase right now. Distinct from dev_smoke's resolvePreview: the oracle
// needs the OWNER, not just whether something matched — "matched by the
// wrong plugin" is exactly the symptom.
func resolveOwnerPreview(phrase string) (matched bool, owner string, err error) {
	token := readHostToken()
	if token == "" {
		return false, "", fmt.Errorf("no host token or Developer Access grant — is BranchKit running?")
	}
	raw, status, err := devHTTP("POST", "/v1/commands/resolve", token, map[string]any{
		"words":   strings.Fields(phrase),
		"preview": true,
	})
	if err != nil {
		return false, "", err
	}
	if status != 200 {
		return false, "", fmt.Errorf("resolve returned %d: %s", status, strings.TrimSpace(string(raw)))
	}
	var result struct {
		Matched     bool   `json:"matched"`
		OwnerPlugin string `json:"owner_plugin"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return false, "", err
	}
	return result.Matched, result.OwnerPlugin, nil
}

func devBisectGet(path string) bisectStatus {
	return devBisectDo("GET", path, nil)
}

func devBisectPost(path string, body any) bisectStatus {
	return devBisectDo("POST", path, body)
}

func devBisectDo(method, path string, body any) bisectStatus {
	token := readHostToken()
	if token == "" {
		fmt.Fprintln(os.Stderr, "Error: no host token or Developer Access grant — is BranchKit running?")
		os.Exit(1)
	}
	if body == nil && method == "POST" {
		body = map[string]any{}
	}
	// Not devHTTP: its shared 10s client can lose the race against a round
	// whose apply legitimately runs toggles plus the full settle ceiling.
	// The session survives a timeout (rejoin works), but a driver should
	// not flake on its own tool's worst case.
	raw, status, err := bisectHTTP(method, path, token, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v — is BranchKit running?\n", err)
		os.Exit(1)
	}
	var st bisectStatus
	_ = json.Unmarshal(raw, &st)
	if status >= 400 {
		msg := st.Error
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		fmt.Fprintf(os.Stderr, "Error (%d): %s\n", status, msg)
		os.Exit(1)
	}
	return st
}

func bisectHTTP(method, path, token string, body any) ([]byte, int, error) {
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
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := devClient(30 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return raw, resp.StatusCode, err
}

func printDevBisectUsage() {
	fmt.Println("Usage: branchkit-cli dev bisect [--pin PLUGIN_ID]... [--check \"PHRASE => PLUGIN_ID\"]")
	fmt.Println("       branchkit-cli dev bisect restore")
	fmt.Println("       branchkit-cli dev bisect cancel")
	fmt.Println()
	fmt.Println("Find WHICH plugin causes a symptom no health check can see, by")
	fmt.Println("disabling dependency-closed halves and asking after each round.")
	fmt.Println("You are the oracle; reproduce the symptom before answering.")
	fmt.Println()
	fmt.Println("  --pin ID   Never disable this plugin (repeatable) — e.g. one you")
	fmt.Println("             need in order to reproduce the symptom at all.")
	fmt.Println("  --check \"next tab => browser\"")
	fmt.Println("             Scripted oracle for routing symptoms: each round asks")
	fmt.Println("             the matcher (side-effect-free) whether the phrase")
	fmt.Println("             resolves to that plugin, instead of asking you. The")
	fmt.Println("             expected plugin is pinned automatically; the run is")
	fmt.Println("             unattended and takes seconds. Refuses to start when")
	fmt.Println("             the phrase already routes correctly (no symptom).")
	fmt.Println("  restore    Recover after a crash left a bisect half-applied.")
	fmt.Println("  cancel     Abandon the running bisect and restore everything.")
}
