package main

// cmdDevVocab — the grammar-membership oracle's CLI face
// (vocabulary.explain). Answers the question the first dogfood run's
// builder could not: "why isn't my word hearable?" Per word: committed to
// the running recognizer, staged in the union, or contributed-but-dropped —
// with the caller's own contributing sources named. On a Developer Access
// token the sources are scoped to that plugin.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func cmdDevVocab(args []string) {
	jsonMode := false
	words := []string{}
	for _, a := range args {
		switch a {
		case "--json":
			jsonMode = true
		case "--help", "-h":
			printDevVocabUsage()
			return
		default:
			words = append(words, a)
		}
	}
	if len(words) == 0 {
		printDevVocabUsage()
		os.Exit(1)
	}
	token := readHostToken()
	if token == "" {
		fmt.Fprintln(os.Stderr, "Error: no host token or Developer Access grant — is BranchKit running?")
		os.Exit(1)
	}
	raw, status, err := devHTTP("POST", "/v1/vocabulary/explain", token, map[string]any{
		"words": words,
	})
	if err != nil || status != 200 {
		fmt.Fprintf(os.Stderr, "Error: status=%d err=%v body=%s\n", status, err, strings.TrimSpace(string(raw)))
		os.Exit(1)
	}
	if jsonMode {
		fmt.Println(string(raw))
		return
	}
	var result struct {
		Words []struct {
			Word        string   `json:"word"`
			InEngine    bool     `json:"in_engine"`
			InUnion     bool     `json:"in_union"`
			YourSources []string `json:"your_sources"`
			Verdict     string   `json:"verdict"`
		} `json:"words"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		fmt.Println(string(raw))
		return
	}
	for _, w := range result.Words {
		fmt.Printf("%-18s %s\n", w.Word, w.Verdict)
		for _, s := range w.YourSources {
			fmt.Printf("%-18s   from %s\n", "", s)
		}
	}
}

func printDevVocabUsage() {
	fmt.Println("Usage: branchkit-cli dev vocab <word> [word...] [--json]")
	fmt.Println()
	fmt.Println("Why is (or isn't) a word in the recognition grammar? Per word:")
	fmt.Println("  hearable now  — committed to the running recognizer")
	fmt.Println("  staged        — in the union; lands at the next utterance boundary")
	fmt.Println("  dropped       — your record exists but the word never joined the union")
	fmt.Println("                  (not in the model's BPE lexicon, or the collection")
	fmt.Println("                  does not feed matching)")
	fmt.Println("  unknown       — nothing you contribute uses this word")
}
