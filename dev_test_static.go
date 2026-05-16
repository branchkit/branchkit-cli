package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	phase.Tests = append(phase.Tests, checkRunBinary(dir, manifest))

	return phase
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
			results = append(results, TestResult{
				Name: fmt.Sprintf("command_%d", i), Status: "fail",
				Detail: "command missing \"action\" object",
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
