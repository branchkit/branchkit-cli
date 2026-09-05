package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type TestResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type TestPhaseResult struct {
	Phase string       `json:"phase"`
	Tests []TestResult `json:"tests"`
}

func runStaticAnalysis(dir string) TestPhaseResult {
	phase := TestPhaseResult{Phase: "static_analysis"}

	manifestPath := filepath.Join(dir, "plugin.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		phase.Tests = append(phase.Tests, TestResult{
			Name: "manifest_readable", Status: "fail",
			Detail: fmt.Sprintf("cannot read plugin.json: %v", err),
		})
		return phase
	}

	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		phase.Tests = append(phase.Tests, TestResult{
			Name: "manifest_valid_json", Status: "fail",
			Detail: fmt.Sprintf("invalid JSON: %v", err),
		})
		return phase
	}
	phase.Tests = append(phase.Tests, TestResult{Name: "manifest_valid_json", Status: "pass"})

	phase.Tests = append(phase.Tests, checkRequiredFields(manifest)...)
	phase.Tests = append(phase.Tests, checkIDFormat(manifest))
	phase.Tests = append(phase.Tests, checkActionPrefix(manifest))
	phase.Tests = append(phase.Tests, checkActionTypes(manifest)...)
	phase.Tests = append(phase.Tests, checkSettingsTabs(manifest)...)
	phase.Tests = append(phase.Tests, checkCollectionDataFiles(dir, manifest)...)
	phase.Tests = append(phase.Tests, checkCommandGrammar(dir, manifest)...)
	phase.Tests = append(phase.Tests, checkKeybindBindings(manifest)...)
	phase.Tests = append(phase.Tests, checkProvidedCollections(manifest)...)
	phase.Tests = append(phase.Tests, checkConsumedCollections(manifest)...)
	phase.Tests = append(phase.Tests, checkCaptureReferences(dir, manifest)...)
	phase.Tests = append(phase.Tests, checkRunBinary(dir, manifest))

	return phase
}

// checkProvidedCollections validates each provides.collections entry: the
// preset must be one the platform knows, and a feeds_matching field
// reference must name a declared field — a typo there is a collection
// that silently never feeds the matcher. Field-reference problems are
// warns, not failures: the platform accepts the manifest and the record
// shape can be looser than `fields` at runtime.
func checkProvidedCollections(m map[string]any) []TestResult {
	provides, _ := m["provides"].(map[string]any)
	colls, _ := provides["collections"].(map[string]any)
	if len(colls) == 0 {
		return nil
	}

	knownPresets := map[string]bool{
		"tag": true, "log": true, "named_entities": true,
		"command_set": true, "data": true, "settings": true,
	}
	fieldRefKeys := []string{
		"key_field", "value_field", "aliases_field",
		"from_field", "to_field", "name_field",
	}

	var results []TestResult
	for name, v := range colls {
		schema, ok := v.(map[string]any)
		if !ok {
			results = append(results, TestResult{
				Name: "collection_" + name, Status: "fail",
				Detail: "collection schema must be an object",
			})
			continue
		}
		if preset, ok := schema["preset"].(string); ok && !knownPresets[preset] {
			results = append(results, TestResult{
				Name: "collection_" + name, Status: "fail",
				Detail: fmt.Sprintf("unknown preset %q (tag, log, named_entities, command_set, data, settings)", preset),
			})
			continue
		}

		declaredFields := map[string]bool{}
		if fields, ok := schema["fields"].([]any); ok {
			for _, f := range fields {
				if fm, ok := f.(map[string]any); ok {
					if key, ok := fm["key"].(string); ok {
						declaredFields[key] = true
					}
				}
			}
		}

		fm, _ := schema["feeds_matching"].(map[string]any)
		if len(declaredFields) > 0 && fm != nil {
			for _, refKey := range fieldRefKeys {
				ref, ok := fm[refKey].(string)
				if !ok || ref == "" {
					continue
				}
				if !declaredFields[ref] {
					results = append(results, TestResult{
						Name: "collection_" + name, Status: "warn",
						Detail: fmt.Sprintf("feeds_matching.%s %q is not a declared field — the matcher will find nothing under that key", refKey, ref),
					})
				}
			}
		}

		if excl, ok := schema["exclusive"].(bool); ok && excl {
			// The effective feeds type includes what the preset pins
			// when the manifest doesn't say: tag collections feed
			// as_gates by default.
			fmType, _ := fm["type"].(string)
			if fmType == "" {
				switch preset, _ := schema["preset"].(string); preset {
				case "tag":
					fmType = "as_gates"
				case "named_entities":
					fmType = "as_named_entities"
				case "command_set":
					fmType = "as_command_set"
				}
			}
			if fmType != "as_gates" {
				results = append(results, TestResult{
					Name: "collection_" + name, Status: "warn",
					Detail: "exclusive: true is read by the matcher only on gate collections (feeds_matching type as_gates); here it does nothing",
				})
			}
		}
	}
	if len(results) == 0 {
		results = append(results, TestResult{
			Name: "provided_collections", Status: "pass",
			Detail: fmt.Sprintf("%d collection(s)", len(colls)),
		})
	}
	return results
}

// consumedCollection is one `consumes.collections` entry in either of its
// two wire forms: a bare name, or a name plus the fields the plugin reads
// (docs/design/DESIGN_SHAPED_CONSUMPTION.md).
type consumedCollection struct {
	Name   string
	Fields []string
}

// parseConsumedCollections reads `consumes.collections`, accepting both the
// bare-string and the object form. Malformed entries are skipped here; the
// shape check reports them.
func parseConsumedCollections(m map[string]any) []consumedCollection {
	consumes, ok := m["consumes"].(map[string]any)
	if !ok {
		return nil
	}
	colls, ok := consumes["collections"].([]any)
	if !ok {
		return nil
	}
	var out []consumedCollection
	for _, c := range colls {
		switch v := c.(type) {
		case string:
			out = append(out, consumedCollection{Name: v})
		case map[string]any:
			name, _ := v["name"].(string)
			if name == "" {
				continue
			}
			entry := consumedCollection{Name: name}
			if raw, ok := v["fields"].([]any); ok {
				for _, f := range raw {
					if s, ok := f.(string); ok {
						entry.Fields = append(entry.Fields, s)
					}
				}
			}
			out = append(out, entry)
		}
	}
	return out
}

// checkConsumedCollections validates `consumes.collections` entries. The CLI
// sees only this manifest, so a cross-plugin shape can only be verified by
// the platform at load — but two things ARE checkable here, and both are
// silent failures otherwise: a malformed entry, and a self-consumed
// collection whose declared fields disagree with this manifest's own
// provides.collections schema.
func checkConsumedCollections(m map[string]any) []TestResult {
	consumes, ok := m["consumes"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := consumes["collections"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}

	var results []TestResult
	for i, c := range raw {
		switch v := c.(type) {
		case string:
			if v == "" {
				results = append(results, TestResult{
					Name: "consumed_collections", Status: "fail",
					Detail: fmt.Sprintf("entry %d is an empty name", i),
				})
			}
		case map[string]any:
			if name, _ := v["name"].(string); name == "" {
				results = append(results, TestResult{
					Name: "consumed_collections", Status: "fail",
					Detail: fmt.Sprintf("entry %d is an object without a non-empty %q", i, "name"),
				})
			}
		default:
			results = append(results, TestResult{
				Name: "consumed_collections", Status: "fail",
				Detail: fmt.Sprintf("entry %d must be a name or {name, fields}", i),
			})
		}
	}

	// Self-consumption is fully checkable: both halves are in this file.
	provides, _ := m["provides"].(map[string]any)
	provided, _ := provides["collections"].(map[string]any)
	shaped := 0
	for _, consumed := range parseConsumedCollections(m) {
		if len(consumed.Fields) == 0 {
			continue
		}
		shaped++
		schema, ok := provided[consumed.Name].(map[string]any)
		if !ok {
			continue // provider is another plugin — the platform checks it at load
		}
		declaredFields := map[string]bool{}
		if fields, ok := schema["fields"].([]any); ok {
			for _, f := range fields {
				if fm, ok := f.(map[string]any); ok {
					if key, ok := fm["key"].(string); ok {
						declaredFields[key] = true
					}
				}
			}
		}
		if len(declaredFields) == 0 {
			continue // nothing declared to check against
		}
		for _, want := range consumed.Fields {
			if !declaredFields[want] {
				results = append(results, TestResult{
					Name: "consumed_collections", Status: "fail",
					Detail: fmt.Sprintf("consumes %q with field %q, but this manifest's own provides.collections.%s declares no such field — reads of it will find nothing", consumed.Name, want, consumed.Name),
				})
			}
		}
	}

	if len(results) == 0 {
		detail := fmt.Sprintf("%d consumed collection(s)", len(raw))
		if shaped > 0 {
			detail += fmt.Sprintf(", %d with declared fields (cross-plugin shapes are checked at load)", shaped)
		}
		results = append(results, TestResult{
			Name: "consumed_collections", Status: "pass", Detail: detail,
		})
	}
	return results
}

// checkCaptureReferences resolves every <capture> in the voice-commands
// file against the collections this manifest provides or consumes. An
// undeclared capture is legal — the platform registers the command and
// classifies it dynamic — but it matches nothing until some plugin
// introduces the collection, which is the documented silent failure mode.
// So: warn, with the fix named.
func checkCaptureReferences(dir string, m map[string]any) []TestResult {
	declared := map[string]bool{}
	if provides, ok := m["provides"].(map[string]any); ok {
		if colls, ok := provides["collections"].(map[string]any); ok {
			for name := range colls {
				declared[name] = true
			}
		}
	}
	for _, c := range parseConsumedCollections(m) {
		declared[c.Name] = true
	}

	commands := loadVoiceCommands(dir, m)
	if commands == nil {
		return nil
	}

	var results []TestResult
	seen := map[string]bool{}
	for _, cmd := range commands {
		pattern, _ := cmd["pattern"].([]any)
		for _, tok := range flattenPatternTokens(pattern) {
			if !strings.HasPrefix(tok, "<") || !strings.HasSuffix(tok, ">") {
				continue
			}
			inner := tok[1 : len(tok)-1]
			// A leading name: prefix is a binding, not a collection.
			bound := false
			if i := strings.Index(inner, ":"); i >= 0 {
				inner = inner[i+1:]
				bound = true
			}
			// A BARE capture takes the collection name as its binding name,
			// and binding names cannot contain dots: the action-template
			// regex accepts [a-zA-Z_][a-zA-Z0-9_]* and `${a.b}` splits on the
			// first dot to separate binding from field. So a namespaced
			// collection captured bare produces a binding nothing can address
			// — `{plugin.acme.widgets}` stays a literal in the action params,
			// with no error at load or at match time.
			if !bound && strings.Contains(inner, ".") && !strings.Contains(inner, "${") {
				if !seen["bare."+inner] {
					seen["bare."+inner] = true
					results = append(results, TestResult{
						Name: "capture_binding_" + inner, Status: "error",
						Detail: fmt.Sprintf("capture %q is bare, so its binding name is the collection name %q — but binding names cannot contain dots, and the action template would keep {%s} as a literal. Give the capture an explicit binding name: <name:%s>", tok, inner, inner, inner),
					})
				}
			}
			for _, member := range strings.Split(inner, "|") {
				member = strings.TrimSpace(member)
				// Cross-plugin refs (<x@target>) have their own
				// validation; skip them here.
				if member == "" || strings.Contains(member, "@") || seen[member] {
					continue
				}
				seen[member] = true
				if !declared[member] {
					results = append(results, TestResult{
						Name: "capture_" + member, Status: "warn",
						Detail: fmt.Sprintf("capture references collection %q, which this manifest neither provides nor consumes — the capture matches nothing until a plugin introduces it; if it is the platform's or another plugin's, declare it under consumes.collections", member),
					})
				}
			}
		}
	}
	if len(results) == 0 && len(seen) > 0 {
		results = append(results, TestResult{
			Name: "capture_references", Status: "pass",
			Detail: fmt.Sprintf("%d capture collection(s) resolved", len(seen)),
		})
	}
	return results
}

// loadVoiceCommands reads the manifest's voice_commands collection_data
// file, or nil if there is none.
func loadVoiceCommands(dir string, m map[string]any) []map[string]any {
	data, _ := m["collection_data"].(map[string]any)
	vcFile, ok := data["voice_commands"].(string)
	if !ok {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, vcFile))
	if err != nil {
		return nil
	}
	var commands []map[string]any
	if err := json.Unmarshal(raw, &commands); err != nil {
		return nil
	}
	return commands
}

// flattenPatternTokens yields every string token in a pattern, entering
// one level of alternative-lists ([["focus","go to"], "<apps>"]).
func flattenPatternTokens(pattern []any) []string {
	var out []string
	for _, p := range pattern {
		switch t := p.(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, alt := range t {
				if s, ok := alt.(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func checkRequiredFields(m map[string]any) []TestResult {
	var results []TestResult
	for _, field := range []string{"id", "name", "version"} {
		val, ok := m[field]
		if !ok {
			results = append(results, TestResult{
				Name: "required_field_" + field, Status: "fail",
				Detail: fmt.Sprintf("missing required field %q", field),
			})
			continue
		}
		s, _ := val.(string)
		if s == "" {
			// id is always an error; name/version are warnings to match actuator severity
			severity := "warn"
			if field == "id" {
				severity = "fail"
			}
			results = append(results, TestResult{
				Name: "required_field_" + field, Status: severity,
				Detail: fmt.Sprintf("field %q is empty", field),
			})
			continue
		}
		results = append(results, TestResult{Name: "required_field_" + field, Status: "pass"})
	}
	return results
}

func checkIDFormat(m map[string]any) TestResult {
	id, _ := m["id"].(string)
	if id == "" {
		return TestResult{Name: "id_format", Status: "skip", Detail: "no id field"}
	}
	if !validateID(id) {
		return TestResult{
			Name: "id_format", Status: "fail",
			Detail: fmt.Sprintf("%q must be lowercase alphanumeric with hyphens", id),
		}
	}
	return TestResult{Name: "id_format", Status: "pass"}
}

func checkActionPrefix(m map[string]any) TestResult {
	prefix, ok := m["action_prefix"]
	if !ok || prefix == nil {
		return TestResult{Name: "action_prefix_format", Status: "skip", Detail: "no action_prefix declared"}
	}
	s, _ := prefix.(string)
	if s == "" {
		return TestResult{Name: "action_prefix_format", Status: "fail", Detail: "action_prefix is empty"}
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return TestResult{
				Name: "action_prefix_format", Status: "fail",
				Detail: fmt.Sprintf("%q contains invalid character %q — use lowercase alphanumeric or underscore", s, string(c)),
			}
		}
	}
	return TestResult{Name: "action_prefix_format", Status: "pass"}
}

var knownFieldTypes = map[string]bool{
	"string": true, "int": true, "number": true, "boolean": true,
	"string[]": true, "enum": true, "object": true, "json": true,
}

func checkActionTypes(m map[string]any) []TestResult {
	at, ok := m["action_types"]
	if !ok {
		return []TestResult{{Name: "action_types", Status: "skip", Detail: "no action_types declared"}}
	}
	types, ok := at.(map[string]any)
	if !ok {
		return []TestResult{{Name: "action_types", Status: "fail", Detail: "action_types must be an object"}}
	}

	var results []TestResult
	for name, v := range types {
		obj, ok := v.(map[string]any)
		if !ok {
			results = append(results, TestResult{
				Name: "action_type_" + name, Status: "fail",
				Detail: "action type must be an object",
			})
			continue
		}
		if fields, ok := obj["fields"].([]any); ok {
			for _, f := range fields {
				field, ok := f.(map[string]any)
				if !ok {
					continue
				}
				ft, _ := field["field_type"].(string)
				if ft != "" && !knownFieldTypes[ft] {
					key, _ := field["key"].(string)
					results = append(results, TestResult{
						Name: "action_type_" + name, Status: "fail",
						Detail: fmt.Sprintf("field %q has unknown field_type %q", key, ft),
					})
				}
			}
		}
		if len(results) == 0 || results[len(results)-1].Name != "action_type_"+name {
			results = append(results, TestResult{Name: "action_type_" + name, Status: "pass"})
		}
	}
	if len(types) == 0 {
		results = append(results, TestResult{Name: "action_types", Status: "pass", Detail: "0 types"})
	}
	return results
}

func checkSettingsTabs(m map[string]any) []TestResult {
	impl, ok := m["implements"]
	if !ok {
		return nil
	}
	implMap, ok := impl.(map[string]any)
	if !ok {
		return nil
	}
	tabs, ok := implMap["settings_tabs"]
	if !ok {
		return nil
	}
	tabList, ok := tabs.([]any)
	if !ok {
		return []TestResult{{Name: "settings_tabs", Status: "fail", Detail: "settings_tabs must be an array"}}
	}

	seen := map[string]bool{}
	var results []TestResult
	for _, t := range tabList {
		tab, ok := t.(map[string]any)
		if !ok {
			results = append(results, TestResult{Name: "settings_tab", Status: "fail", Detail: "tab entry must be an object"})
			continue
		}
		key, _ := tab["key"].(string)
		label, _ := tab["label"].(string)
		if key == "" {
			results = append(results, TestResult{Name: "settings_tab", Status: "fail", Detail: "tab missing required field \"key\""})
			continue
		}
		if label == "" {
			results = append(results, TestResult{
				Name: "settings_tab_" + key, Status: "fail",
				Detail: fmt.Sprintf("tab %q missing required field \"label\"", key),
			})
			continue
		}
		if seen[key] {
			results = append(results, TestResult{
				Name: "settings_tab_" + key, Status: "fail",
				Detail: fmt.Sprintf("duplicate tab key %q", key),
			})
			continue
		}
		seen[key] = true
	}
	if len(results) == 0 {
		results = append(results, TestResult{
			Name: "settings_tabs", Status: "pass",
			Detail: fmt.Sprintf("%d tab(s)", len(tabList)),
		})
	}
	return results
}

func checkCollectionDataFiles(dir string, m map[string]any) []TestResult {
	cd, ok := m["collection_data"]
	if !ok {
		return nil
	}
	data, ok := cd.(map[string]any)
	if !ok {
		return []TestResult{{Name: "collection_data", Status: "fail", Detail: "collection_data must be an object"}}
	}

	var results []TestResult
	for name, v := range data {
		filePath, ok := v.(string)
		if !ok {
			continue
		}
		fullPath := filepath.Join(dir, filePath)
		raw, err := os.ReadFile(fullPath)
		if err != nil {
			results = append(results, TestResult{
				Name: "collection_data_" + name, Status: "fail",
				Detail: fmt.Sprintf("cannot read %s: %v", filePath, err),
			})
			continue
		}
		if !json.Valid(raw) {
			results = append(results, TestResult{
				Name: "collection_data_" + name, Status: "fail",
				Detail: fmt.Sprintf("%s is not valid JSON", filePath),
			})
			continue
		}
		results = append(results, TestResult{
			Name: "collection_data_" + name, Status: "pass",
			Detail: filePath,
		})
	}
	return results
}

func checkCommandGrammar(dir string, m map[string]any) []TestResult {
	cd, ok := m["collection_data"]
	if !ok {
		return nil
	}
	data, ok := cd.(map[string]any)
	if !ok {
		return nil
	}

	vcFile, ok := data["voice_commands"].(string)
	if !ok {
		return nil
	}

	fullPath := filepath.Join(dir, vcFile)
	raw, err := os.ReadFile(fullPath)
	if err != nil {
		return nil
	}

	var commands []map[string]any
	if err := json.Unmarshal(raw, &commands); err != nil {
		return []TestResult{{
			Name: "command_grammar", Status: "fail",
			Detail: fmt.Sprintf("%s: expected JSON array of commands: %v", vcFile, err),
		}}
	}

	prefix, _ := m["action_prefix"].(string)
	var results []TestResult
	for i, cmd := range commands {
		pattern, _ := cmd["pattern"].([]any)
		if len(pattern) == 0 {
			results = append(results, TestResult{
				Name: fmt.Sprintf("command_%d", i), Status: "fail",
				Detail: "command missing \"pattern\" array",
			})
			continue
		}

		action, ok := cmd["action"].(map[string]any)
		if !ok {
			// Tag-only commands are a documented, first-party-used shape:
			// mode entry/exit carries sets_tags/clears_tags and no action
			// (the platform's own validator rates a bare command Warn, not
			// Error). This check used to hard-fail them — rejecting the
			// docs' own worked example — found by the first
			// differential-dogfood run, 2026-08-30.
			setsTags, _ := cmd["sets_tags"].([]any)
			clearsTags, _ := cmd["clears_tags"].([]any)
			if len(setsTags) > 0 || len(clearsTags) > 0 {
				continue // tag-only command: valid, nothing more to check
			}
			results = append(results, TestResult{
				Name: fmt.Sprintf("command_%d", i), Status: "warn",
				Detail: "command has no \"action\" and writes no tags — it will match but do nothing",
			})
			continue
		}

		actionType, _ := action["type"].(string)
		if actionType == "" {
			results = append(results, TestResult{
				Name: fmt.Sprintf("command_%d", i), Status: "fail",
				Detail: "action missing \"type\" field",
			})
			continue
		}

		// One params dialect (DESIGN_ONE_PARAMS_DIALECT.md): the action
		// envelope is closed, sequences included, and the check recurses
		// into sequence steps — a stray key is refused by the platform at
		// load, so refuse it here first, at author time.
		if errs := actionEnvelopeErrors(action); len(errs) > 0 {
			for _, e := range errs {
				results = append(results, TestResult{
					Name: fmt.Sprintf("command_%d", i), Status: "fail", Detail: e,
				})
			}
			continue
		}

		if prefix != "" && !strings.HasPrefix(actionType, prefix+".") {
			results = append(results, TestResult{
				Name: fmt.Sprintf("command_%d", i), Status: "warn",
				Detail: fmt.Sprintf("action type %q does not start with action_prefix %q", actionType, prefix),
			})
			continue
		}
	}

	if len(results) == 0 {
		results = append(results, TestResult{
			Name: "command_grammar", Status: "pass",
			Detail: fmt.Sprintf("%d command(s)", len(commands)),
		})
	}
	return results
}

// actionEnvelopeErrors validates one action object against the one-params
// dialect (DESIGN_ONE_PARAMS_DIALECT.md), mirroring the platform's
// parse_action_or_template: the envelope is type/params/phase; a sequence
// is exactly {"type": "sequence", "actions": […]} and the check recurses
// into each step; "params", when present, must be an object. Empty means
// valid.
func actionEnvelopeErrors(action map[string]any) []string {
	actionType, _ := action["type"].(string)
	if actionType == "" {
		return []string{"action missing \"type\" field"}
	}
	if actionType == "sequence" {
		var errs []string
		var unknown []string
		for k := range action {
			if k != "type" && k != "actions" {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			errs = append(errs, fmt.Sprintf(
				"sequence has unknown key(s) %v — a sequence is {\"type\": \"sequence\", \"actions\": […]}",
				unknown))
		}
		steps, ok := action["actions"].([]any)
		if !ok {
			errs = append(errs, "sequence requires an \"actions\" array")
			return errs
		}
		for j, step := range steps {
			stepObj, ok := step.(map[string]any)
			if !ok {
				errs = append(errs, fmt.Sprintf("sequence step %d must be an action object", j))
				continue
			}
			for _, e := range actionEnvelopeErrors(stepObj) {
				errs = append(errs, fmt.Sprintf("sequence step %d: %s", j, e))
			}
		}
		return errs
	}
	var errs []string
	var unknown []string
	for k := range action {
		switch k {
		case "type", "params", "phase":
		default:
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		errs = append(errs, fmt.Sprintf(
			"action has unknown key(s) %v — params nest under \"params\": {\"type\": %q, \"params\": {…}}",
			unknown, actionType))
	}
	if p, present := action["params"]; present {
		if _, ok := p.(map[string]any); !ok {
			errs = append(errs, fmt.Sprintf("action %q: \"params\" must be an object", actionType))
		}
	}
	return errs
}

// checkKeybindBindings enforces the one-params dialect on collection_data
// keybinds: a binding's envelope is closed (action / params) and the
// platform refuses stray keys at load — refuse them here first.
func checkKeybindBindings(m map[string]any) []TestResult {
	cd, _ := m["collection_data"].(map[string]any)
	kb, _ := cd["keybinds"].(map[string]any)
	if len(kb) == 0 {
		return nil
	}
	var results []TestResult
	combos := make([]string, 0, len(kb))
	for combo := range kb {
		combos = append(combos, combo)
	}
	sort.Strings(combos)
	for _, combo := range combos {
		binding, ok := kb[combo].(map[string]any)
		if !ok {
			results = append(results, TestResult{
				Name: "keybind_" + combo, Status: "fail",
				Detail: "binding must be an object: {\"action\": \"<dotted.type>\", \"params\": {…}}",
			})
			continue
		}
		if s, _ := binding["action"].(string); s == "" {
			results = append(results, TestResult{
				Name: "keybind_" + combo, Status: "fail",
				Detail: "binding missing \"action\"",
			})
			continue
		}
		var unknown []string
		for k := range binding {
			if k != "action" && k != "params" {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			results = append(results, TestResult{
				Name: "keybind_" + combo, Status: "fail",
				Detail: fmt.Sprintf(
					"binding has unknown key(s) %v — params nest under \"params\"", unknown),
			})
			continue
		}
		if p, present := binding["params"]; present {
			if _, ok := p.(map[string]any); !ok {
				results = append(results, TestResult{
					Name: "keybind_" + combo, Status: "fail",
					Detail: "binding \"params\" must be an object",
				})
			}
		}
	}
	if len(results) == 0 {
		results = append(results, TestResult{
			Name: "keybinds", Status: "pass",
			Detail: fmt.Sprintf("%d binding(s)", len(kb)),
		})
	}
	return results
}

func checkRunBinary(dir string, m map[string]any) TestResult {
	run, ok := m["run"]
	if !ok || run == nil {
		return TestResult{Name: "run_binary", Status: "skip", Detail: "no run command declared"}
	}
	s, _ := run.(string)
	if s == "" {
		return TestResult{Name: "run_binary", Status: "fail", Detail: "run field is empty"}
	}

	binary := strings.TrimPrefix(s, "./")
	binaryPath := filepath.Join(dir, binary)
	if _, err := os.Stat(binaryPath); err != nil {
		return TestResult{
			Name: "run_binary", Status: "warn",
			Detail: fmt.Sprintf("%s not found — run \"branchkit dev build\" first", binary),
		}
	}
	return TestResult{Name: "run_binary", Status: "pass", Detail: binary}
}

func printTestResults(phase TestPhaseResult, jsonOutput bool) int {
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(phase)
	} else {
		fmt.Printf("Phase: %s\n\n", phase.Phase)
		for _, t := range phase.Tests {
			icon := "?"
			switch t.Status {
			case "pass":
				icon = "\u2713"
			case "fail":
				icon = "\u2717"
			case "warn":
				icon = "!"
			case "skip":
				icon = "-"
			}
			line := fmt.Sprintf("  %s %s", icon, t.Name)
			if t.Detail != "" {
				line += " — " + t.Detail
			}
			fmt.Println(line)
		}
		fmt.Println()
	}

	failures := 0
	for _, t := range phase.Tests {
		if t.Status == "fail" {
			failures++
		}
	}
	return failures
}
