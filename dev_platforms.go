package main

// dev platforms — where will this plugin actually work?
//
// Step 0 of DESIGN_PLUGIN_CAPABILITY_DECLARATION.md, finally in a tool the
// audience has. The derivation already existed as `just plugin-platforms`,
// which lives in the closed app repo: it served us and not the plugin authors
// whose day it is meant to change, which is the wrong way round for a thing
// whose entire purpose is telling an author what they can call. This is the
// same answer, reachable from an MIT CLI against the installed app.
//
// DERIVED, never declared. What a plugin calls is a fact in its source, and
// asking an author to maintain a list by hand makes them do a script's work
// and drifts the moment someone adds a call. The chain is mechanical: an SDK
// wrapper maps to an operation, and the operation states where it answers.
// `platform-availability.json` ships that mapping — including each operation's
// Go wrapper name — precisely so a consumer never has to reimplement the
// emitters' acronym rules.
//
// It does NOT judge. A plugin calling a macOS-only operation is not wrong; it
// may be a macOS plugin, or it may degrade deliberately. Only the author
// knows, which is why the required/optional split stays hand-written while
// this part does not.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type availabilityDoc struct {
	GeneratedFrom    string            `json:"generated_from"`
	Platforms        []string          `json:"platforms"`
	SessionDependent map[string]string `json:"session_dependent"`
	Operations       []availabilityOp  `json:"operations"`
}

type availabilityOp struct {
	Name         string            `json:"name"`
	Summary      string            `json:"summary"`
	Wrappers     map[string]string `json:"wrappers"`
	Availability map[string]string `json:"availability"`
}

// How each availability state prints. `undeclared` is deliberately its own
// mark rather than being folded into "answers": an operation that has not
// stated where it works is not the same claim as one that has, and rendering
// the two identically is how an unstated assumption becomes a shipped one.
var availabilityMark = map[string]string{
	"yes":        "✅",
	"notyet":     "○",
	"never":      "—",
	"undeclared": "?",
}

var availabilityWhy = map[string]string{
	"notyet":     "not ported yet",
	"never":      "no equivalent on this platform",
	"undeclared": "availability not declared — assume macOS-first and verify",
}

// Wrapper calls look like `p.SomeWrapper(` or `h.plugin.SomeWrapper(`. Match
// the selector and let the wrapper table decide what is real, the same way
// the first-party script does — a name that is not a known wrapper is simply
// not in the map.
var wrapperCallRe = regexp.MustCompile(`\.([A-Z]\w*)\(`)

func cmdDevPlatforms(args []string) {
	jsonOut := false
	var dirs []string
	for _, a := range args {
		switch {
		case a == "--json":
			jsonOut = true
		case a == "--help" || a == "-h":
			printDevPlatformsUsage()
			return
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n", a)
			printDevPlatformsUsage()
			os.Exit(1)
		default:
			dirs = append(dirs, a)
		}
	}
	if len(dirs) == 0 {
		dirs = []string{"."}
	}

	doc, docPath, err := loadAvailability()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	// wrapper name → operation. Built from the shipped data, so the acronym
	// rules live in one place (the emitters) rather than being guessed here.
	wrapperToOp := map[string]availabilityOp{}
	for _, op := range doc.Operations {
		if w := op.Wrappers["go"]; w != "" {
			wrapperToOp[w] = op
		}
	}

	type pluginReport struct {
		Dir        string           `json:"dir"`
		Operations []availabilityOp `json:"operations"`
	}
	var reports []pluginReport
	exit := 0

	for _, dir := range dirs {
		used, err := scanWrapperCalls(dir, wrapperToOp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", dir, err)
			exit = 1
			continue
		}
		reports = append(reports, pluginReport{Dir: dir, Operations: used})
	}

	if jsonOut {
		out, _ := json.MarshalIndent(map[string]any{
			"source":            docPath,
			"generated_from":    doc.GeneratedFrom,
			"platforms":         doc.Platforms,
			"plugins":           reports,
			"session_dependent": doc.SessionDependent,
		}, "", "  ")
		fmt.Println(string(out))
		os.Exit(exit)
	}

	for _, r := range reports {
		printPlatformReport(r.Dir, r.Operations, doc)
	}
	fmt.Println()
	os.Exit(exit)
}

// loadAvailability finds platform-availability.json in whichever docs tree
// this machine has — an explicit override, the synced cache, or the installed
// bundle. Same resolution every other docs-reading command uses.
func loadAvailability() (*availabilityDoc, string, error) {
	dir, source := resolveDocsDir()
	if dir == "" {
		return nil, "", fmt.Errorf(
			"no docs directory found — install BranchKit, run `branchkit-cli docs sync`, " +
				"or set BRANCHKIT_DOCS_DIR")
	}
	path := filepath.Join(dir, "platform-availability.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("reading %s: %w", path, err)
	}
	var doc availabilityDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, "", fmt.Errorf("parsing %s: %w", path, err)
	}
	return &doc, fmt.Sprintf("%s (%s)", path, source), nil
}

// scanWrapperCalls walks a plugin's Go sources and returns every operation it
// reaches through an SDK wrapper, sorted and deduplicated.
func scanWrapperCalls(dir string, wrapperToOp map[string]availabilityOp) ([]availabilityOp, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory")
	}

	seen := map[string]availabilityOp{}
	walkErr := filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // an unreadable corner should not fail the whole report
		}
		if fi.IsDir() {
			switch fi.Name() {
			case "vendor", "testdata", ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, m := range wrapperCallRe.FindAllSubmatch(src, -1) {
			if op, ok := wrapperToOp[string(m[1])]; ok {
				seen[op.Name] = op
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	out := make([]availabilityOp, 0, len(seen))
	for _, op := range seen {
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func printPlatformReport(dir string, used []availabilityOp, doc *availabilityDoc) {
	// Plugins live at <name>/src, so the basename of the directory the author
	// points at is usually "src" — which names nothing. Use the parent.
	label := dir
	if abs, err := filepath.Abs(dir); err == nil {
		label = filepath.Base(abs)
		if label == "src" || label == "." {
			label = filepath.Base(filepath.Dir(abs))
		}
	}

	fmt.Printf("\n%s — %d platform operation(s)\n", label, len(used))
	if len(used) == 0 {
		fmt.Println("  calls no platform operations; runs anywhere the platform does")
		return
	}

	blocked := map[string][]string{}
	for _, op := range used {
		row := make([]string, 0, len(doc.Platforms))
		for _, p := range doc.Platforms {
			state := op.Availability[p]
			mark := availabilityMark[state]
			if mark == "" {
				mark = "?"
			}
			row = append(row, mark)
			if state != "yes" {
				why := availabilityWhy[state]
				if why == "" {
					why = state
				}
				blocked[p] = append(blocked[p], fmt.Sprintf("%s  (%s)", op.Name, why))
			}
		}
		fmt.Printf("   %s   %s\n", strings.Join(row, " "), op.Name)
	}

	fmt.Printf("   %s\n", strings.Repeat("^  ", len(doc.Platforms)))
	fmt.Printf("   %s        ✅ answers   ○ not yet   — no equivalent   ? undeclared\n",
		strings.Join(doc.Platforms, " / "))

	for _, p := range doc.Platforms {
		rows := blocked[p]
		if len(rows) == 0 {
			continue
		}
		fmt.Printf("\n  on %s: %d of %d would refuse or are unstated\n", p, len(rows), len(used))
		for i, r := range rows {
			if i == 6 {
				fmt.Printf("     … and %d more\n", len(rows)-6)
				break
			}
			fmt.Printf("     %s\n", r)
		}
	}

	// Session-dependent operations answer on a platform but not in every
	// session on it — the sway/GNOME case. A per-platform mark cannot carry
	// that, so it is said in words rather than quietly rounded to "works".
	var notes []string
	for _, op := range used {
		if note, ok := doc.SessionDependent[op.Name]; ok {
			notes = append(notes, fmt.Sprintf("     %s — %s", op.Name, note))
		}
	}
	if len(notes) > 0 {
		fmt.Printf("\n  depends on the session, not just the platform:\n")
		sort.Strings(notes)
		for _, n := range notes {
			fmt.Println(n)
		}
	}
}

func printDevPlatformsUsage() {
	fmt.Println(`branchkit-cli dev platforms [DIR...] [--json]

Which platforms will this plugin actually work on?

Scans a plugin's Go sources for SDK wrapper calls, then reports where each
operation those calls reach is available. Derived from the plugin's own
source and the platform's shipped availability data — nothing to declare and
nothing to keep in sync.

  DIR      plugin source directory (default: the current directory)
  --json   machine-readable output

Reads platform-availability.json from the docs tree: $BRANCHKIT_DOCS_DIR, the
synced cache (branchkit-cli docs sync), or the installed app bundle.`)
}
