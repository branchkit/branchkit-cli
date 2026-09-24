package main

// dev platforms — where will this plugin actually work?
//
// Derives, from a plugin's own source, which operations it calls and where
// each answers — told to the author at build time rather than learned from a
// user's bug report — finally in a tool the audience has. The derivation already existed as `just plugin-platforms`,
// which lives in the closed app repo: it served us and not the plugin authors
// whose day it is meant to change, which is the wrong way round for a thing
// whose entire purpose is telling an author what they can call. This is the
// same answer, reachable from an MIT CLI against the installed app.
//
// DERIVED, never declared. What a plugin calls is a fact in its source, and
// asking an author to maintain a list by hand makes them do a script's work
// and drifts the moment someone adds a call. The chain is mechanical: an SDK
// wrapper maps to an operation, and the operation states where it answers.
// `platform-availability.json` ships that mapping — each operation's typed
// wrapper and method constant in every SDK (go, ts, py) — precisely so a
// consumer never has to reimplement the emitters' naming rules. The Go
// wrapper for native.focused_window_id is NativeFocusedWindowID while its
// constant is MethodNativeFocusedWindowId: a rule guessed here would get one
// of the two wrong.
//
// It reads Go, TypeScript and Python — the three SDKs — and it SAYS what it
// read. It used to read .go files only and then print "runs anywhere" for a
// TypeScript or Python plugin, a clean bill issued for source it never
// opened. "Found nothing" is only reported as "runs anywhere" when the scan
// saw every source file in the plugin and could resolve every call form in
// it; otherwise the report names what it did not see.
//
// It does NOT judge. A plugin calling a macOS-only operation is not wrong; it
// may be a macOS plugin, or it may degrade deliberately. Only the author
// knows, which is why the required/optional split stays hand-written while
// this part does not.

import (
	"encoding/json"
	"fmt"
	"io"
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
	// Every other method the SDKs wrap. The matrix rates native operations;
	// these it says nothing about, and a plugin calling one must hear that
	// rather than nothing. Absent in data generated before the field existed.
	Unrated []availabilityOp `json:"unrated"`
}

type availabilityOp struct {
	Name         string            `json:"name"`
	Summary      string            `json:"summary,omitempty"`
	Wrappers     map[string]string `json:"wrappers"`
	Constants    map[string]string `json:"constants,omitempty"`
	Availability map[string]string `json:"availability,omitempty"`
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

// sourceLang is one SDK language the scan reads. `key` is the SDK key the
// availability data uses for its wrapper and constant names.
type sourceLang struct {
	key   string
	label string
	exts  []string
	// Generated files are skipped: a plugin's *_gen file registers handlers,
	// and a .d.ts declares types — neither is a call the plugin makes.
	generated func(name string) bool
	// Test files are skipped: they do not ship to the machine the question is
	// about, and a test that fakes a call would otherwise claim a need.
	test func(name string) bool
	// A typed wrapper call: `p.NativeWarpCursor(`, `plugin.nativeWarpCursor(`,
	// `plugin.native_warp_cursor(`. Match the selector and let the data decide
	// what is real — a name that is not a known wrapper is simply not in it.
	wrapper *regexp.Regexp
	// A method constant, wherever it appears: `branchkit.MethodNativeDarkMode`
	// handed to Call, or to a helper of the plugin's own.
	constant *regexp.Regexp
}

var sourceLangs = []sourceLang{
	{
		key:       "go",
		label:     "Go",
		exts:      []string{".go"},
		generated: func(n string) bool { return strings.HasSuffix(n, "_gen.go") },
		test:      func(n string) bool { return strings.HasSuffix(n, "_test.go") },
		wrapper:   regexp.MustCompile(`\.([A-Z]\w*)\(`),
		constant:  regexp.MustCompile(`\b(Method[A-Z]\w*)\b`),
	},
	{
		key:   "ts",
		label: "TypeScript",
		exts:  []string{".ts", ".tsx", ".mts", ".cts"},
		generated: func(n string) bool {
			base := strings.TrimSuffix(n, filepath.Ext(n))
			return strings.HasSuffix(base, "_gen") || strings.HasSuffix(base, ".d")
		},
		test: func(n string) bool {
			base := strings.TrimSuffix(n, filepath.Ext(n))
			return strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".spec")
		},
		wrapper:  regexp.MustCompile(`\.([a-z]\w*)\s*(?:<[^<>()]*>)?\(`),
		constant: regexp.MustCompile(`\b(Method[A-Z]\w*)\b`),
	},
	{
		key:       "py",
		label:     "Python",
		exts:      []string{".py"},
		generated: func(n string) bool { return strings.HasSuffix(n, "_gen.py") },
		test: func(n string) bool {
			return strings.HasPrefix(n, "test_") || strings.HasSuffix(n, "_test.py") || n == "conftest.py"
		},
		wrapper:  regexp.MustCompile(`\.([a-z_]\w*)\(`),
		constant: regexp.MustCompile(`\b(METHOD_[A-Z0-9_]+)\b`),
	},
}

// Source this scan does not read. Finding any of it means "found nothing" is
// not "calls nothing", and the report says so instead of issuing a clean bill.
var unscannedLangs = map[string]string{
	".js": "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript", ".jsx": "JavaScript",
	".rs": "Rust", ".lua": "Lua", ".swift": "Swift", ".rb": "Ruby",
	".java": "Java", ".kt": "Kotlin", ".cs": "C#", ".zig": "Zig", ".php": "PHP",
	".c": "C", ".h": "C", ".cc": "C++", ".cpp": "C++", ".cxx": "C++", ".hpp": "C++",
	".m": "Objective-C", ".mm": "Objective-C",
}

// A call that names its method as a string: `p.Call("native.dark_mode", …)`,
// `plugin.call("input.type_text", …)`, `plugin.call_sync('native.volume')`,
// `plugin.notify(…)`, and a plugin's own helper around them
// (`PluginCall(ctx, "collection.get", …)`). Any callee whose name contains
// call/notify qualifies, so a helper is not a blind spot; what keeps that
// broad shape honest is that the string must name a namespace the platform
// has — `callback("user.login")` is not counted.
var stringCallRe = regexp.MustCompile(
	"\\b\\w*(?:[cC]all|[nN]otify)\\w*\\s*(?:<[^<>()]*>)?\\(\\s*(?:\\w+\\s*,\\s*)?" +
		"[\"'`]([a-z_][a-z0-9_]*(?:\\.[a-z0-9_]+)+)[\"'`]")

// Directories never read: dependencies, build output, and copies of the SDK
// itself (a Python plugin vendors it with `pip install --target .`, and its
// generated wrappers would otherwise claim every operation there is).
var skippedDirs = map[string]bool{
	"vendor": true, "testdata": true, "node_modules": true, "dist": true,
	"__pycache__": true, "__tests__": true, "venv": true, "site-packages": true,
}

func skipDir(path, name string) bool {
	if skippedDirs[name] || strings.HasPrefix(name, ".") ||
		strings.HasSuffix(name, ".dist-info") || strings.HasSuffix(name, ".egg-info") {
		return true
	}
	for _, marker := range []string{"pyvenv.cfg", "contracts_gen.go", "contracts_gen.ts", "contracts_gen.py"} {
		if _, err := os.Stat(filepath.Join(path, marker)); err == nil {
			return true
		}
	}
	return false
}

// methodIndex is the availability data turned around for lookup: from what a
// plugin's source spells, to the operation it reaches.
type methodIndex struct {
	wrappers   map[string]map[string]string // sdk key → wrapper → method
	constants  map[string]map[string]string // sdk key → constant → method
	rated      map[string]availabilityOp
	known      map[string]bool // rated ∪ unrated method names
	namespaces map[string]bool
	hasUnrated bool
}

func newMethodIndex(doc *availabilityDoc) *methodIndex {
	idx := &methodIndex{
		wrappers:   map[string]map[string]string{},
		constants:  map[string]map[string]string{},
		rated:      map[string]availabilityOp{},
		known:      map[string]bool{},
		namespaces: map[string]bool{},
		hasUnrated: doc.Unrated != nil,
	}
	add := func(op availabilityOp) {
		idx.known[op.Name] = true
		if ns, _, ok := strings.Cut(op.Name, "."); ok {
			idx.namespaces[ns] = true
		}
		for sdk, w := range op.Wrappers {
			if idx.wrappers[sdk] == nil {
				idx.wrappers[sdk] = map[string]string{}
			}
			idx.wrappers[sdk][w] = op.Name
		}
		for sdk, c := range op.Constants {
			if idx.constants[sdk] == nil {
				idx.constants[sdk] = map[string]string{}
			}
			idx.constants[sdk][c] = op.Name
		}
	}
	for _, op := range doc.Operations {
		idx.rated[op.Name] = op
		add(op)
	}
	for _, op := range doc.Unrated {
		add(op)
	}
	return idx
}

// platformScan is what one plugin directory's source says it calls, and how
// much of that source the scan could actually read.
type platformScan struct {
	Dir string `json:"dir"`
	// Operations the matrix rates per platform, sorted.
	Operations []availabilityOp `json:"operations"`
	// Methods the plugin calls that the matrix does not rate, sorted.
	Unrated []string `json:"unrated"`
	// label → files read / skipped as tests / not readable by this scan.
	Scanned      map[string]int `json:"scanned"`
	TestsSkipped map[string]int `json:"tests_skipped,omitempty"`
	NotScanned   map[string]int `json:"not_scanned,omitempty"`
	// Languages whose typed wrappers the data could not name (data older
	// than the field), and whether the data could name unrated methods.
	Unresolved []string `json:"unresolved,omitempty"`
	// Calls that name their method as a string literal.
	StringCalls   int            `json:"string_calls"`
	StringMethods map[string]int `json:"string_methods,omitempty"`
	// String-named methods in a real namespace that name no known method: a
	// typo, or a method newer than this data.
	UnknownStrings []string `json:"unknown_strings,omitempty"`
	// True only when every source file was read and every call form in it
	// could be resolved. "Runs anywhere" is printed only then.
	Complete bool `json:"coverage_complete"`
}

// scanPlatformCalls walks a plugin directory and returns every operation its
// Go, TypeScript and Python sources reach — through a typed wrapper, a method
// constant, or a method name spelled as a string.
func scanPlatformCalls(dir string, idx *methodIndex) (*platformScan, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory")
	}

	res := &platformScan{
		Dir:           dir,
		Scanned:       map[string]int{},
		TestsSkipped:  map[string]int{},
		NotScanned:    map[string]int{},
		StringMethods: map[string]int{},
	}
	rated := map[string]availabilityOp{}
	unrated := map[string]bool{}
	unknown := map[string]bool{}
	langSeen := map[string]bool{}

	reach := func(method string) {
		if op, ok := idx.rated[method]; ok {
			rated[method] = op
		} else {
			unrated[method] = true
		}
	}

	walkErr := filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // an unreadable corner should not fail the whole report
		}
		if fi.IsDir() {
			if path != dir && skipDir(path, fi.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		name := fi.Name()
		ext := strings.ToLower(filepath.Ext(name))
		var lang *sourceLang
		for i := range sourceLangs {
			for _, e := range sourceLangs[i].exts {
				if ext == e {
					lang = &sourceLangs[i]
				}
			}
		}
		if lang == nil {
			if l, ok := unscannedLangs[ext]; ok {
				res.NotScanned[l]++
			}
			return nil
		}
		if lang.generated(name) {
			return nil
		}
		if lang.test(name) {
			res.TestsSkipped[lang.label]++
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			res.NotScanned[lang.label+" (unreadable)"]++
			return nil
		}
		res.Scanned[lang.label]++
		langSeen[lang.key] = true

		for _, m := range lang.wrapper.FindAllSubmatch(src, -1) {
			if method, ok := idx.wrappers[lang.key][string(m[1])]; ok {
				reach(method)
			}
		}
		for _, m := range lang.constant.FindAllSubmatch(src, -1) {
			if method, ok := idx.constants[lang.key][string(m[1])]; ok {
				reach(method)
			}
		}
		for _, m := range stringCallRe.FindAllSubmatch(src, -1) {
			method := string(m[1])
			ns, _, _ := strings.Cut(method, ".")
			if !idx.namespaces[ns] {
				continue
			}
			res.StringCalls++
			res.StringMethods[method]++
			if idx.known[method] {
				reach(method)
			} else if idx.hasUnrated {
				unknown[method] = true
			} else {
				// Old data cannot tell a typo from a method it never listed,
				// so report the call rather than guess which it is.
				unrated[method] = true
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	for _, l := range sourceLangs {
		if langSeen[l.key] && len(idx.wrappers[l.key]) == 0 {
			res.Unresolved = append(res.Unresolved, l.label)
		}
	}

	res.Operations = make([]availabilityOp, 0, len(rated))
	for _, op := range rated {
		res.Operations = append(res.Operations, op)
	}
	sort.Slice(res.Operations, func(i, j int) bool { return res.Operations[i].Name < res.Operations[j].Name })
	res.Unrated = sortedKeys(unrated)
	res.UnknownStrings = sortedKeys(unknown)

	res.Complete = len(res.Scanned) > 0 && len(res.NotScanned) == 0 &&
		len(res.Unresolved) == 0 && idx.hasUnrated
	return res, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

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
	// Built from the shipped data, so the naming rules live in one place (the
	// emitters) rather than being guessed here.
	idx := newMethodIndex(doc)

	var reports []*platformScan
	exit := 0
	for _, dir := range dirs {
		r, err := scanPlatformCalls(dir, idx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", dir, err)
			exit = 1
			continue
		}
		reports = append(reports, r)
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
		printPlatformReport(os.Stdout, r, doc)
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

// countPhrase renders {"Go": 2, "Python": 1} as "2 Go files, 1 Python file".
func countPhrase(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		noun := "files"
		if m[k] == 1 {
			noun = "file"
		}
		parts = append(parts, fmt.Sprintf("%d %s %s", m[k], k, noun))
	}
	return strings.Join(parts, ", ")
}

func printPlatformReport(w io.Writer, r *platformScan, doc *availabilityDoc) {
	p := func(format string, a ...any) { fmt.Fprintf(w, format, a...) }

	// Plugins live at <name>/src, so the basename of the directory the author
	// points at is usually "src" — which names nothing. Use the parent.
	label := r.Dir
	if abs, err := filepath.Abs(r.Dir); err == nil {
		label = filepath.Base(abs)
		if label == "src" || label == "." {
			label = filepath.Base(filepath.Dir(abs))
		}
	}

	used := r.Operations
	if len(r.Unrated) > 0 {
		p("\n%s — %d rated platform operation(s), %d other method(s)\n", label, len(used), len(r.Unrated))
	} else {
		p("\n%s — %d platform operation(s)\n", label, len(used))
	}

	// What was read comes first: every line below is only as good as it.
	if len(r.Scanned) == 0 {
		p("  scanned: nothing — no Go, TypeScript or Python source found here\n")
	} else {
		line := "  scanned: " + countPhrase(r.Scanned)
		if len(r.TestsSkipped) > 0 {
			line += " (tests not counted: " + countPhrase(r.TestsSkipped) + ")"
		}
		p("%s\n", line)
	}
	if len(r.NotScanned) > 0 {
		p("  NOT scanned: %s — this scan reads Go, TypeScript and Python only;\n", countPhrase(r.NotScanned))
		p("     whatever those files call is missing from this report\n")
	}
	for _, l := range r.Unresolved {
		p("  %s typed wrappers NOT resolved: this availability data predates them.\n", l)
		p("     Run `branchkit-cli docs sync` or update BranchKit; string-named calls were still read\n")
	}
	if doc.Unrated == nil {
		p("  methods outside the native matrix NOT resolved: this availability data predates\n")
		p("     that list. Run `branchkit-cli docs sync` or update BranchKit\n")
	}

	if len(used) > 0 {
		printRatedMatrix(p, used, doc)
	}

	if len(r.Unrated) > 0 {
		p("\n  also calls %d method(s) the availability matrix does not rate per platform:\n", len(r.Unrated))
		for i, m := range r.Unrated {
			if i == 8 {
				p("     … and %d more\n", len(r.Unrated)-8)
				break
			}
			p("     %s\n", m)
		}
		p("     (the matrix covers native operations; it makes no claim about these either way)\n")
	}

	if r.StringCalls > 0 {
		names := make([]string, 0, len(r.StringMethods))
		for m := range r.StringMethods {
			names = append(names, m)
		}
		sort.Strings(names)
		if len(names) > 6 {
			names = append(names[:6], fmt.Sprintf("… %d more", len(names)-6))
		}
		p("\n  %d call(s) name the method as a string: %s\n", r.StringCalls, strings.Join(names, ", "))
		p("     counted above; the SDK's typed wrapper is the checked form of each\n")
	}
	if len(r.UnknownStrings) > 0 {
		p("  string method names that match no known method (a typo, or newer than this data):\n")
		for _, m := range r.UnknownStrings {
			p("     %s\n", m)
		}
	}

	if len(used) == 0 && len(r.Unrated) == 0 && len(r.UnknownStrings) == 0 {
		if r.Complete {
			p("  calls no platform operations; runs anywhere the platform does\n")
		} else {
			// The old report said "runs anywhere" here for every plugin it
			// could not read. Absence of evidence from a partial scan is not a
			// portability claim, so it is not made.
			p("  found no platform operations in what was scanned — not a claim that it\n")
			p("  runs anywhere, since the scan did not cover everything (see above)\n")
		}
	}
}

func printRatedMatrix(p func(string, ...any), used []availabilityOp, doc *availabilityDoc) {
	blocked := map[string][]string{}
	for _, op := range used {
		row := make([]string, 0, len(doc.Platforms))
		for _, pl := range doc.Platforms {
			state := op.Availability[pl]
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
				blocked[pl] = append(blocked[pl], fmt.Sprintf("%s  (%s)", op.Name, why))
			}
		}
		p("   %s   %s\n", strings.Join(row, " "), op.Name)
	}

	p("   %s\n", strings.Repeat("^  ", len(doc.Platforms)))
	p("   %s        ✅ answers   ○ not yet   — no equivalent   ? undeclared\n",
		strings.Join(doc.Platforms, " / "))

	for _, pl := range doc.Platforms {
		rows := blocked[pl]
		if len(rows) == 0 {
			continue
		}
		p("\n  on %s: %d of %d would refuse or are unstated\n", pl, len(rows), len(used))
		for i, r := range rows {
			if i == 6 {
				p("     … and %d more\n", len(rows)-6)
				break
			}
			p("     %s\n", r)
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
		p("\n  depends on the session, not just the platform:\n")
		sort.Strings(notes)
		for _, n := range notes {
			p("%s\n", n)
		}
	}
}

func printDevPlatformsUsage() {
	fmt.Println(`branchkit-cli dev platforms [DIR...] [--json]

Which platforms will this plugin actually work on?

Scans a plugin's Go, TypeScript and Python sources for the operations they
call — typed SDK wrappers, method constants, and method names passed as a
string to call/notify — then reports where each operation is available.
Derived from the plugin's own source and the platform's shipped availability
data — nothing to declare and nothing to keep in sync.

It says what it read. Test files, generated *_gen files, dependencies
(node_modules, vendor, venvs, a vendored SDK) and build output (dist) are
skipped. Source in any other language is listed as not scanned, and then
finding nothing is NOT reported as "runs anywhere". A method name computed at
run time is invisible to any source scan.

  DIR      plugin source directory (default: the current directory)
  --json   machine-readable output

Reads platform-availability.json from the docs tree: $BRANCHKIT_DOCS_DIR, the
synced cache (branchkit-cli docs sync), or the installed app bundle.`)
}
