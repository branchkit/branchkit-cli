package main

// dev platforms --write / --check: keep a manifest's
// requires.capabilities.uses equal to what the plugin's source calls.
//
// The derived list goes INTO the manifest because an installed plugin is a
// binary: the app deciding what will not work on a machine, before anything
// runs, has no source to scan. `required` and `dynamic` stay hand-written —
// only the author knows which calls have a way around, or that the plugin
// forwards calls its users' scripts name.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// capabilitiesDecl is requires.capabilities as the manifest schema defines it.
type capabilitiesDecl struct {
	Uses     []string `json:"uses,omitempty"`
	Required []string `json:"required,omitempty"`
	Dynamic  string   `json:"dynamic,omitempty"`
}

// derivedUses is every rated operation the scan saw the plugin call.
func derivedUses(r *platformScan) []string {
	out := make([]string, 0, len(r.Operations))
	for _, op := range r.Operations {
		out = append(out, op.Name)
	}
	sort.Strings(out)
	return out
}

// readCapabilities returns the manifest's requires.capabilities.
func readCapabilities(raw []byte) (capabilitiesDecl, error) {
	var m struct {
		Requires struct {
			Capabilities capabilitiesDecl `json:"capabilities"`
		} `json:"requires"`
	}
	err := json.Unmarshal(raw, &m)
	return m.Requires.Capabilities, err
}

// usesDrift compares the declared list with the derived one: operations the
// source calls that the manifest leaves out, and the reverse.
func usesDrift(declared, derived []string) (missing, extra []string) {
	for _, op := range derived {
		if !slices.Contains(declared, op) {
			missing = append(missing, op)
		}
	}
	for _, op := range declared {
		if !slices.Contains(derived, op) {
			extra = append(extra, op)
		}
	}
	return missing, extra
}

// member is one key of a JSON object and the byte span of its value.
type member struct {
	key        string
	keyStart   int // offset of the key's opening quote
	valueStart int
	valueEnd   int
}

// objectMembers lists the members of the object starting at raw[start]
// (which must be '{'), with byte offsets into raw, and the offset of its
// closing brace. Offsets let a caller splice one member and leave every
// other byte of a hand-formatted manifest alone.
func objectMembers(raw []byte, start int) ([]member, int, error) {
	dec := json.NewDecoder(bytes.NewReader(raw[start:]))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, 0, fmt.Errorf("expected an object at offset %d", start)
	}
	var out []member
	for dec.More() {
		keyEnd := start + int(dec.InputOffset())
		t, err := dec.Token()
		if err != nil {
			return nil, 0, err
		}
		key, _ := t.(string)
		keyStart := keyEnd + bytes.IndexByte(raw[keyEnd:], '"')
		afterKey := start + int(dec.InputOffset())
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, 0, err
		}
		end := start + int(dec.InputOffset())
		colon := afterKey + bytes.IndexByte(raw[afterKey:], ':')
		vs := colon + 1
		for vs < end && isSpace(raw[vs]) {
			vs++
		}
		out = append(out, member{key: key, keyStart: keyStart, valueStart: vs, valueEnd: end})
	}
	closeAt := start + int(dec.InputOffset())
	closeAt += bytes.IndexByte(raw[closeAt:], '}')
	return out, closeAt, nil
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// lineIndent is the whitespace that starts the line holding raw[at].
func lineIndent(raw []byte, at int) string {
	ls := bytes.LastIndexByte(raw[:at], '\n') + 1
	i := ls
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t') {
		i++
	}
	return string(raw[ls:i])
}

// renderAt marshals v indented to sit where a value starts on a line
// indented by indent, with unit as one level.
func renderAt(v any, indent, unit string) ([]byte, error) {
	b, err := json.MarshalIndent(v, indent, unit)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// indentUnit guesses the file's indentation step from its second line.
func indentUnit(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\n")[1:] {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && len(trimmed) < len(line) {
			return line[:len(line)-len(trimmed)]
		}
	}
	return "  "
}

// setUses returns raw with requires.capabilities.uses replaced by uses,
// keeping `required`, `dynamic` and every other byte of the manifest.
func setUses(raw []byte, uses []string) ([]byte, error) {
	caps, err := readCapabilities(raw)
	if err != nil {
		return nil, err
	}
	caps.Uses = uses
	unit := indentUnit(raw)
	top := bytes.IndexByte(raw, '{')
	if top < 0 {
		return nil, fmt.Errorf("not a JSON object")
	}
	members, topClose, err := objectMembers(raw, top)
	if err != nil {
		return nil, err
	}
	splice := func(at, end int, with []byte) []byte {
		out := append([]byte{}, raw[:at]...)
		out = append(out, with...)
		return append(out, raw[end:]...)
	}
	// Insert `"key": value` as the last member of the object whose last
	// member ends at lastEnd (or which is empty) and which closes at close.
	insert := func(lastEnd, close int, memberIndent, key string, v any) ([]byte, error) {
		val, err := renderAt(v, memberIndent, unit)
		if err != nil {
			return nil, err
		}
		entry := fmt.Sprintf("\n%s%q: %s", memberIndent, key, val)
		if lastEnd >= 0 {
			return splice(lastEnd, lastEnd, append([]byte(","), entry...)), nil
		}
		closing := "\n" + lineIndent(raw, close)
		return splice(close, close, []byte(entry+closing)), nil
	}

	var req *member
	for i := range members {
		if members[i].key == "requires" {
			req = &members[i]
		}
	}
	if req == nil {
		lastEnd, memberIndent := -1, unit
		if n := len(members); n > 0 {
			lastEnd = members[n-1].valueEnd
			memberIndent = lineIndent(raw, members[n-1].keyStart)
		}
		return insert(lastEnd, topClose, memberIndent, "requires",
			map[string]any{"capabilities": caps})
	}
	reqMembers, reqClose, err := objectMembers(raw, req.valueStart)
	if err != nil {
		return nil, fmt.Errorf("requires: %w", err)
	}
	for _, m := range reqMembers {
		if m.key == "capabilities" {
			val, err := renderAt(caps, lineIndent(raw, m.keyStart), unit)
			if err != nil {
				return nil, err
			}
			return splice(m.valueStart, m.valueEnd, val), nil
		}
	}
	// A one-line `requires` becomes one member per line, each existing
	// value's bytes kept, rather than gaining a member on a line of its own.
	if !bytes.Contains(raw[req.valueStart:req.valueEnd], []byte("\n")) {
		outer := lineIndent(raw, req.keyStart)
		inner := outer + unit
		var b strings.Builder
		b.WriteString("{")
		for _, m := range reqMembers {
			fmt.Fprintf(&b, "\n%s%q: %s,", inner, m.key, raw[m.valueStart:m.valueEnd])
		}
		val, err := renderAt(caps, inner, unit)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "\n%s\"capabilities\": %s\n%s}", inner, val, outer)
		return splice(req.valueStart, req.valueEnd, []byte(b.String())), nil
	}
	lastEnd, memberIndent := -1, lineIndent(raw, req.keyStart)+unit
	if n := len(reqMembers); n > 0 {
		lastEnd = reqMembers[n-1].valueEnd
		memberIndent = lineIndent(raw, reqMembers[n-1].keyStart)
	}
	return insert(lastEnd, reqClose, memberIndent, "capabilities", caps)
}

// writeOrCheckUses applies --write or --check to one scanned plugin. It
// returns false when --check finds drift or the scan could not be trusted.
func writeOrCheckUses(r *platformScan, write bool) bool {
	path := filepath.Join(r.Dir, "plugin.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		return false
	}
	caps, err := readCapabilities(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		return false
	}
	derived := derivedUses(r)
	missing, extra := usesDrift(caps.Uses, derived)
	// A partial scan can say what it saw, never that nothing else is
	// called: writing from it would drop calls it could not read.
	if !r.Complete && caps.Dynamic == "" {
		fmt.Fprintf(os.Stderr, "%s: the scan could not read all of this plugin's source "+
			"(see the report); uses left unchanged\n", path)
		return !write && len(missing) == 0
	}
	if write {
		if len(missing) == 0 && len(extra) == 0 {
			return true
		}
		out, err := setUses(raw, derived)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			return false
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			return false
		}
		fmt.Printf("%s: requires.capabilities.uses now lists %d operations\n", path, len(derived))
		return true
	}
	ok := true
	if len(missing) > 0 {
		fmt.Printf("%s: called but not in requires.capabilities.uses: %s\n",
			path, strings.Join(missing, ", "))
		ok = false
	}
	if len(extra) > 0 && caps.Dynamic == "" {
		fmt.Printf("%s: in requires.capabilities.uses but never called: %s\n",
			path, strings.Join(extra, ", "))
		ok = false
	}
	if !ok {
		fmt.Printf("  fix: branchkit-cli dev platforms --write %s\n", r.Dir)
	}
	return ok
}
