package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const goSDKModule = "github.com/branchkit/plugin-sdk-go"

// checkSDKRequiredFields finds Go request literals that leave out a field
// the platform refuses empty.
//
// Every generated wrapper takes one request struct, and Go does not enforce
// required fields in a struct literal: a forgotten `Name` compiles and sends
// "". The actuator refuses "" for a required parameter that names something
// (a path, an id, a collection) and says which field, but only when the
// call runs, so a request built on a path the author never exercised fails
// first on a user's machine. TypeScript and Python refuse a missing required
// field when they type-check; this is Go's equivalent.
//
// The rule comes from the SDK the plugin actually builds against: a field
// is required-non-empty when its generated doc carries the `non-empty`
// constraint note, which emit-sdk writes from the schema's minLength. So
// there is nothing here to keep in sync, and an SDK older than the note
// makes the check skip and say so rather than pass vacuously.
func checkSDKRequiredFields(dir string) TestResult {
	const name = "sdk_required_fields"
	modDir := goModuleDir(dir)
	if modDir == "" {
		return TestResult{Name: name, Status: "skip", Detail: "not a Go plugin"}
	}
	sdkDir, version, err := resolveGoSDK(modDir)
	if err != nil {
		return TestResult{Name: name, Status: "skip", Detail: err.Error()}
	}
	required, err := nonEmptyRequestFields(filepath.Join(sdkDir, "types_gen.go"))
	if err != nil {
		return TestResult{Name: name, Status: "skip", Detail: err.Error()}
	}
	if len(required) == 0 {
		return TestResult{Name: name, Status: "skip",
			Detail: fmt.Sprintf("%s %s documents no non-empty request fields (a newer SDK does)", goSDKModule, version)}
	}
	omissions, checked, err := scanRequestLiterals(modDir, required)
	if err != nil {
		return TestResult{Name: name, Status: "fail", Detail: err.Error()}
	}
	if len(omissions) > 0 {
		return TestResult{Name: name, Status: "fail",
			Detail: fmt.Sprintf("%d request field(s) left out, which Go sends as \"\" and the platform refuses:\n      %s",
				len(omissions), strings.Join(omissions, "\n      "))}
	}
	return TestResult{Name: name, Status: "pass",
		Detail: fmt.Sprintf("%d request literal(s) set every non-empty field", checked)}
}

// goModuleDir is the plugin's Go module root: the plugin directory or its
// src/ (the scaffold's layout). Empty when neither holds a go.mod.
func goModuleDir(dir string) string {
	for _, d := range []string{dir, filepath.Join(dir, "src")} {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	return ""
}

// resolveGoSDK finds the source directory of the plugin-sdk-go the module
// builds against. `go list -m` honours replace directives and go.work, so a
// plugin developed against a local SDK checkout reads that checkout.
func resolveGoSDK(modDir string) (dir, version string, err error) {
	if _, err := exec.LookPath("go"); err != nil {
		return "", "", fmt.Errorf("go is not on PATH, so the SDK the plugin builds against cannot be found")
	}
	type module struct {
		Version string
		Dir     string
		Replace *struct {
			Version string
			Dir     string
		}
	}
	list := func() (module, error) {
		var m module
		cmd := exec.Command("go", "list", "-m", "-json", goSDKModule)
		cmd.Dir = modDir
		out, err := cmd.Output()
		if err != nil {
			return m, fmt.Errorf("%s is not a dependency of this module", goSDKModule)
		}
		return m, json.Unmarshal(out, &m)
	}
	m, err := list()
	if err != nil {
		return "", "", err
	}
	if m.Replace != nil && m.Replace.Dir != "" {
		return m.Replace.Dir, "(replaced)", nil
	}
	if m.Dir == "" {
		// Not in the module cache yet (a fresh CI checkout): fetch it.
		dl := exec.Command("go", "mod", "download", goSDKModule)
		dl.Dir = modDir
		if out, err := dl.CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("downloading %s failed: %s", goSDKModule, strings.TrimSpace(string(out)))
		}
		if m, err = list(); err != nil {
			return "", "", err
		}
	}
	if m.Dir == "" {
		return "", "", fmt.Errorf("%s %s has no source directory", goSDKModule, m.Version)
	}
	return m.Dir, m.Version, nil
}

// nonEmptyRequestFields reads the SDK's generated types and returns, per
// `<Method>Request` struct, the fields whose doc comment carries the
// `non-empty` constraint. The note is its own ` · `-separated part of a
// doc line, so prose that merely says "non-empty" does not count.
func nonEmptyRequestFields(typesGen string) (map[string][]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), typesGen, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("reading the SDK's generated types: %v", err)
	}
	required := map[string][]string{}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range g.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || !strings.HasSuffix(ts.Name.Name, "Request") {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range st.Fields.List {
				if field.Doc == nil || !hasNonEmptyNote(field.Doc.Text()) {
					continue
				}
				for _, n := range field.Names {
					required[ts.Name.Name] = append(required[ts.Name.Name], n.Name)
				}
			}
		}
	}
	return required, nil
}

func hasNonEmptyNote(doc string) bool {
	for line := range strings.SplitSeq(doc, "\n") {
		for part := range strings.SplitSeq(line, " · ") {
			if strings.TrimSpace(part) == "non-empty" {
				return true
			}
		}
	}
	return false
}

// scanRequestLiterals walks the module's Go files for keyed composite
// literals of an SDK request type and reports each non-empty field left
// out. A literal assigned to a variable counts a field as set when the same
// function later assigns it (`req.Name = …`). A positional literal sets
// every field, so it is not checked.
func scanRequestLiterals(modDir string, required map[string][]string) (omissions []string, checked int, err error) {
	type omission struct {
		file string
		line int
		text string
	}
	var found []omission
	fset := token.NewFileSet()
	// A literal reached through its assignment is checked there, with the
	// assignment's context; Inspect then descends into it and must skip it.
	visited := map[*ast.CompositeLit]struct{}{}
	err = filepath.WalkDir(modDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); path != modDir && (n == "vendor" || n == "testdata" || strings.HasPrefix(n, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // the compiler reports syntax errors better than this can
		}
		alias := sdkImportName(f)
		if alias == "" {
			return nil
		}
		rel, _ := filepath.Rel(modDir, path)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			setLater := fieldAssignments(fn.Body)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				var target string
				var lit *ast.CompositeLit
				switch x := n.(type) {
				case *ast.AssignStmt:
					if len(x.Lhs) == 1 && len(x.Rhs) == 1 {
						if id, ok := x.Lhs[0].(*ast.Ident); ok {
							target, lit = id.Name, literalOf(x.Rhs[0])
						}
					}
				case *ast.ValueSpec:
					if len(x.Names) == 1 && len(x.Values) == 1 {
						target, lit = x.Names[0].Name, literalOf(x.Values[0])
					}
				case *ast.CompositeLit:
					lit = x
				}
				if lit == nil {
					return true
				}
				typeName, ok := sdkTypeName(lit, alias)
				if !ok {
					return true
				}
				fields, ok := required[typeName]
				if !ok {
					return true
				}
				have, positional := keyedFields(lit)
				if positional {
					return true
				}
				if target != "" {
					for f := range setLater[target] {
						have[f] = true
					}
				}
				if _, seen := visited[lit]; seen {
					return true
				}
				visited[lit] = struct{}{}
				checked++
				for _, field := range fields {
					if !have[field] {
						line := fset.Position(lit.Pos()).Line
						found = append(found, omission{rel, line, fmt.Sprintf("%s:%d: %s.%s{} leaves out %s",
							rel, line, alias, typeName, field)})
					}
				}
				return true
			})
		}
		return nil
	})
	// Source order, so the report reads down each file.
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].file != found[j].file {
			return found[i].file < found[j].file
		}
		return found[i].line < found[j].line
	})
	for _, o := range found {
		omissions = append(omissions, o.text)
	}
	return omissions, checked, err
}

// sdkImportName is the name the file refers to plugin-sdk-go by, or "" when
// it does not import it (or imports it with _ or .).
func sdkImportName(f *ast.File) string {
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if path != goSDKModule {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				return ""
			}
			return imp.Name.Name
		}
		return "branchkit"
	}
	return ""
}

// literalOf unwraps `&T{…}` and `T{…}`.
func literalOf(e ast.Expr) *ast.CompositeLit {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	lit, _ := e.(*ast.CompositeLit)
	return lit
}

func sdkTypeName(lit *ast.CompositeLit, alias string) (string, bool) {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != alias {
		return "", false
	}
	return sel.Sel.Name, true
}

func keyedFields(lit *ast.CompositeLit) (have map[string]bool, positional bool) {
	have = map[string]bool{}
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			return nil, true
		}
		if id, ok := kv.Key.(*ast.Ident); ok {
			have[id.Name] = true
		}
	}
	return have, false
}

// fieldAssignments maps each variable in body to the fields assigned on it
// anywhere in the function (`v.Field = …`).
func fieldAssignments(body *ast.BlockStmt) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, l := range as.Lhs {
			sel, ok := l.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if v, ok := sel.X.(*ast.Ident); ok {
				if out[v.Name] == nil {
					out[v.Name] = map[string]bool{}
				}
				out[v.Name][sel.Sel.Name] = true
			}
		}
		return true
	})
	return out
}
