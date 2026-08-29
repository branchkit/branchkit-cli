package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"sort"
)

// cmdDevMargins reports the recognition-margin distribution of a keyed
// recognition log, split by verdict, to help site a confidence floor.
//
// It reads the collection's COMPACTED projection over HTTP
// (GET /v1/collections/{name}?compacted=true) — the actuator folds the keyed
// append log into one current-state record per key, so this command does no
// folding of its own. Each folded record carries its final verdict (empty =
// an unflagged match, "wrong" = a flagged mishear) and its worst-word margin;
// we bin those into a population-vs-confirmed-wrong split and report the gap a
// floor would sit in. See docs/design/DESIGN_LOG_ANNOTATION_PROJECTION.md.
//
// This replaces the recognition_log half of scripts/margin-distribution.py:
// the fold now lives only in the actuator, and this is a real consumer of it.
func cmdDevMargins(args []string) {
	collection := "plugin.voice.recognition_log"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--collection":
			if i+1 < len(args) {
				i++
				collection = args[i]
			}
		case "-h", "--help":
			fmt.Println("Usage: branchkit-cli dev margins [--collection <name>]")
			fmt.Println("  Reports the recognition-margin distribution (verdict-split) of a")
			fmt.Println("  keyed recognition log, read via its compacted projection.")
			return
		}
	}

	token := readHostToken()
	if token == "" {
		fmt.Fprintln(os.Stderr, "Error: no host.token — is BranchKit running?")
		os.Exit(1)
	}

	path := "/v1/collections/" + url.PathEscape(collection) + "?compacted=true"
	raw, status, err := devHTTP("GET", path, token, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: GET %s: %v\n", path, err)
		os.Exit(1)
	}
	if status == 404 {
		fmt.Fprintf(os.Stderr, "Error: collection %q not registered\n", collection)
		os.Exit(1)
	}
	if status == 400 {
		fmt.Fprintf(os.Stderr, "Error: %q is not a keyed (id_strategy: by_field) log — nothing to fold\n", collection)
		os.Exit(1)
	}
	if status != 200 {
		fmt.Fprintf(os.Stderr, "Error: GET %s returned status %d: %s\n", path, status, string(raw))
		os.Exit(1)
	}

	var resp struct {
		Records []struct {
			ID      string                 `json:"id"`
			Payload map[string]interface{} `json:"payload"`
		} `json:"records"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "Error: decoding response: %v\n", err)
		os.Exit(1)
	}

	// The records are already folded — one per key. Bin each folded record's
	// worst-word margin by its final verdict. Records with no margin (synthetic
	// / no-audio matches) carry no signal to place.
	var pop, wrong []float64
	for _, rec := range resp.Records {
		w, ok := rec.Payload["worst"].(float64)
		if !ok {
			continue
		}
		if v, _ := rec.Payload["verdict"].(string); v == "wrong" {
			wrong = append(wrong, w)
		} else {
			pop = append(pop, w)
		}
	}

	reportMargins(collection, len(resp.Records), pop, wrong)
}

func reportMargins(collection string, n int, pop, wrong []float64) {
	bar := "=================================================================="
	fmt.Printf("\n%s\n RECOGNITION LOG %s (compacted) — %d records\n%s\n", bar, collection, n, bar)
	if noMargin := n - len(pop) - len(wrong); noMargin > 0 {
		fmt.Printf("  (%d of %d carry no margin — synthetic / no-audio matches, not binned below)\n", noMargin, n)
	}
	if len(pop) == 0 && len(wrong) == 0 {
		fmt.Println("  (no margin-bearing records yet — real speech will populate this;")
		fmt.Println("   synthetic ingests carry no audio margin)")
		return
	}
	summarizeMargins("worst-word margin — population (unflagged matches)", pop)
	summarizeMargins("worst-word margin — CONFIRMED WRONG (flagged mishears)", wrong)
	if len(pop) > 0 {
		fmt.Println("\n  population worst-word margin:")
		histogramMargins(pop)
	}
	if len(wrong) > 0 {
		fmt.Println("\n  confirmed-wrong worst-word margin:")
		histogramMargins(wrong)
	}
	if len(pop) > 0 && len(wrong) > 0 {
		// The floor lives in the gap: above the worst a mishear reached, below
		// the lowest a real command reached. Report both edges.
		hiWrong := maxFloat(wrong)
		loPop := minFloat(pop)
		fmt.Printf("\n  floor-siting window: mishears reach up to %+.2f; real commands dip to %+.2f\n", hiWrong, loPop)
		if hiWrong < loPop {
			fmt.Printf("  -> clean separation: a floor in (%+.2f, %+.2f) drops every flagged mishear, keeps every real command.\n", hiWrong, loPop)
		} else {
			fmt.Printf("  -> OVERLAP of %+.2f: no single floor is perfect; pick the point that trades the fewest of each (per-context may split them).\n", hiWrong-loPop)
		}
	}
}

func summarizeMargins(label string, values []float64) {
	if len(values) == 0 {
		fmt.Printf("\n%s: (no data)\n", label)
		return
	}
	fmt.Printf("\n%s: n=%d  min=%+.2f  p05=%+.2f  p25=%+.2f  med=%+.2f  p75=%+.2f  max=%+.2f\n",
		label, len(values), minFloat(values), percentile(values, 0.05),
		percentile(values, 0.25), percentile(values, 0.5),
		percentile(values, 0.75), maxFloat(values))
}

func histogramMargins(values []float64) {
	const lo, hi, step = -12.0, 12.0, 1.0
	if len(values) == 0 {
		return
	}
	buckets := map[float64]int{}
	for _, v := range values {
		b := math.Max(lo, math.Min(hi, math.Round(v/step)*step))
		buckets[b]++
	}
	peak := 0
	for _, c := range buckets {
		if c > peak {
			peak = c
		}
	}
	const width = 40
	for b := lo; b <= hi; b += step {
		n := buckets[b]
		barLen := 0
		if peak > 0 {
			barLen = int(math.Round(float64(n) / float64(peak) * width))
		}
		bar := ""
		for i := 0; i < barLen; i++ {
			bar += "#"
		}
		fmt.Printf("  %+6.1f | %-*s %d\n", b, width, bar, n)
	}
}

// percentile returns the q-quantile (0..1) via linear interpolation, matching
// the Python analyzer's pct().
func percentile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), values...)
	sort.Float64s(s)
	if len(s) == 1 {
		return s[0]
	}
	pos := q * float64(len(s)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return s[lo]
	}
	frac := pos - float64(lo)
	return s[lo]*(1-frac) + s[hi]*frac
}

func minFloat(v []float64) float64 {
	m := v[0]
	for _, x := range v {
		if x < m {
			m = x
		}
	}
	return m
}

func maxFloat(v []float64) float64 {
	m := v[0]
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}
