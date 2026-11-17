// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package checks

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"

	"github.com/ai-on-gke/grain/pkg/qa"
)

var defaultStrictDecoderKeywords = []string{
	"config", "decl", "schema", "staterepo", "settings", "manifest",
	"policy", "workflow", "qa", ".grain", "spec", "loader",
}

// CheckStrictConfigDecoder enforces golden:strict-config-decoder on modified non-test Go files in
// configuration, declaration, schema, state, or YAML-loading files: a yaml/json decoder added or
// touched in the diff that decodes into a struct-shaped target must enable KnownFields(true) or
// DisallowUnknownFields() (or be annotated with `// qa:allow-unknown-fields <reason>`).
//
// The check is type-aware without a type checker (it has to stay inside the S3 budget over the
// sandbox command runner). Unknown-field strictness only exists for struct fields, so a site is
// reported only when one of its Decode / yaml.Unmarshal targets resolves to:
//   - a struct type with at least one field (named, anonymous, or behind pointers and named
//     types, including aliases), or
//   - a slice, array or map whose element type is such a struct (strictness applies to nested
//     elements, so `map[string]Profile` drops unknown Profile keys exactly like `Profile` does).
//
// It is never reported for structural decoding: yaml.Node, json.RawMessage / json.Number,
// interfaces (`any`), maps or slices of non-struct values (`map[string]any`), scalars, empty
// structs, types from the standard library, decoders used only for Token() streaming, and
// decoders whose targets cannot be resolved (generic `dst any` helpers, call results, types from
// modules outside the repository). Unresolvable means "not proven", and a blocking rule must not
// block on what it cannot prove.
//
// Types are resolved from, in order: the function's parameters, results and local declarations;
// the file's package-level declarations; other files of the same package (found with one grep and
// one read per type name); and same-repository imported packages (matched by import path). Cross-
// file lookups are capped at maxStrictDecoderLookups per check run.
func CheckStrictConfigDecoder(ctx context.Context, ws WorkspaceReader, pd ParsedDiff, extraPaths []string) ([]Finding, error) {
	var findings []Finding
	res := newStrictTypeResolver(ctx, ws)

	for _, df := range pd.Files {
		if df.IsDeleted {
			continue
		}
		p := df.EffectivePath()
		if !strings.HasSuffix(p, ".go") || isTestPath(p) {
			continue
		}
		headBytes, err := ws.ReadFileAtHead(ctx, p)
		if err != nil {
			continue
		}
		headText := string(headBytes)
		if strings.Contains(headText, "qa:allow-unknown-fields") && !strings.Contains(headText, "NewDecoder") && !strings.Contains(headText, "Unmarshal") {
			continue
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, headBytes, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		if !isConfigDecoderTarget(p, f, extraPaths) {
			continue
		}
		fs := res.registerFile(p, f)

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			fnStart := fset.Position(fn.Pos()).Line
			fnEnd := fset.Position(fn.End()).Line
			if fnHasAllowComment(f.Comments, fset, fnStart, fnEnd) {
				continue
			}
			inDiff := func(pos token.Pos) bool {
				return df.IsNew || lineInHunks(fset.Position(pos).Line, df.Hunks, 3)
			}
			for _, s := range collectDecoderSites(fn, fs) {
				if s.strict || !inDiff(s.pos) {
					continue
				}
				target, ok := firstStructTarget(res, fs, fn, s.targets)
				if !ok {
					continue
				}
				remedy := "dec.KnownFields(true)"
				if s.pkg == "json" {
					remedy = "dec.DisallowUnknownFields()"
				}
				summary := fmt.Sprintf("%s.NewDecoder in %s decodes into %s without %s, silently dropping unknown configuration fields.", s.pkg, fn.Name.Name, target, remedy)
				if s.kind == "Unmarshal" {
					summary = fmt.Sprintf("yaml.Unmarshal in %s decodes into %s and silently drops unknown configuration fields; use yaml.NewDecoder with %s.", fn.Name.Name, target, remedy)
				}
				findings = append(findings, Finding{
					RuleID:   qa.RuleStrictConfigDecoder,
					Stage:    "S3",
					Severity: "blocking",
					FilePath: p,
					Line:     fset.Position(s.pos).Line,
					Summary:  summary,
					Evidence: fmt.Sprintf("Call %s before Decode(...) or document an explicit wire-compatibility exemption with `// qa:allow-unknown-fields <reason>`. Structural decodes (yaml.Node, map[string]any, json tokens) are not affected by this rule.", remedy),
				})
				break
			}
		}
	}
	return findings, nil
}

// decoderSite is one yaml/json decoder (or yaml.Unmarshal call) and everything it decodes into.
type decoderSite struct {
	pos     token.Pos
	pkg     string // "yaml" or "json"
	kind    string // "NewDecoder" or "Unmarshal"
	strict  bool
	targets []ast.Expr
}

// collectDecoderSites finds decoders created in fn and the arguments of their Decode calls.
// Decoders are tracked by the identifier they are assigned to; a chained
// `yaml.NewDecoder(r).Decode(&v)` is its own site. Decoders that escape (returned, passed on)
// without a local Decode call have no targets and are therefore never reported.
func collectDecoderSites(fn *ast.FuncDecl, fs *strictFileScope) []*decoderSite {
	var sites []*decoderSite
	byVar := map[string]*decoderSite{}
	claimed := map[*ast.CallExpr]bool{}

	newDecoderPkg := func(e ast.Expr) (*ast.CallExpr, string) {
		call, ok := ast.Unparen(e).(*ast.CallExpr)
		if !ok {
			return nil, ""
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "NewDecoder" {
			return nil, ""
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return nil, ""
		}
		return call, fs.codecOf(id.Name)
	}
	bind := func(name string, rhs ast.Expr) {
		call, pkg := newDecoderPkg(rhs)
		if pkg == "" {
			return
		}
		claimed[call] = true
		s := &decoderSite{pos: call.Pos(), pkg: pkg, kind: "NewDecoder"}
		sites = append(sites, s)
		if name != "" && name != "_" {
			byVar[name] = s
		}
	}

	// Pass 1: decoder bindings, in source order.
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, rhs := range node.Rhs {
				name := ""
				if i < len(node.Lhs) {
					if id, ok := node.Lhs[i].(*ast.Ident); ok {
						name = id.Name
					}
				}
				bind(name, rhs)
			}
		case *ast.ValueSpec:
			for i, rhs := range node.Values {
				name := ""
				if i < len(node.Names) {
					name = node.Names[i].Name
				}
				bind(name, rhs)
			}
		}
		return true
	})

	// Pass 2: method calls on decoders, chained decoders and yaml.Unmarshal.
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if inner, pkg := newDecoderPkg(sel.X); pkg != "" && !claimed[inner] {
			// yaml.NewDecoder(r).Decode(&v): no chance to set strictness.
			claimed[inner] = true
			s := &decoderSite{pos: inner.Pos(), pkg: pkg, kind: "NewDecoder"}
			if sel.Sel.Name == "Decode" && len(call.Args) == 1 {
				s.targets = append(s.targets, call.Args[0])
			}
			sites = append(sites, s)
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok {
			if s := byVar[id.Name]; s != nil {
				switch sel.Sel.Name {
				case "Decode":
					if len(call.Args) == 1 {
						s.targets = append(s.targets, call.Args[0])
					}
				case "KnownFields":
					if len(call.Args) == 1 {
						if v, ok := call.Args[0].(*ast.Ident); ok && v.Name == "true" {
							s.strict = true
						}
					}
				case "DisallowUnknownFields":
					s.strict = true
				}
				return true
			}
			if sel.Sel.Name == "Unmarshal" && len(call.Args) == 2 && fs.codecOf(id.Name) == "yaml" {
				sites = append(sites, &decoderSite{pos: call.Pos(), pkg: "yaml", kind: "Unmarshal", targets: []ast.Expr{call.Args[1]}})
			}
		}
		return true
	})
	return sites
}

// firstStructTarget returns a printable type for the first decode target that resolves to a
// struct shape, or ok=false when every target is structural or unresolvable.
func firstStructTarget(res *strictTypeResolver, fs *strictFileScope, fn *ast.FuncDecl, targets []ast.Expr) (string, bool) {
	if len(targets) == 0 {
		return "", false
	}
	vars := localVarTypes(fn, fs)
	for _, t := range targets {
		typ, tfs := targetType(res, fs, vars, t)
		if typ == nil {
			continue
		}
		if res.classify(typ, tfs, 0) == shapeStruct {
			return types2String(typ), true
		}
	}
	return "", false
}

// localVarTypes maps identifiers visible in fn to their declared or inferred type expressions:
// parameters, named results, `var x T`, and `x := T{}` / `&T{}` / `new(T)` / `make(T, ...)`. A
// name bound to two different inferred types is dropped (shadowing makes it ambiguous).
func localVarTypes(fn *ast.FuncDecl, fs *strictFileScope) map[string]ast.Expr {
	vars := map[string]ast.Expr{}
	ambiguous := map[string]bool{}
	set := func(name string, typ ast.Expr) {
		if name == "" || name == "_" || typ == nil {
			return
		}
		if prev, ok := vars[name]; ok && types2String(prev) != types2String(typ) {
			ambiguous[name] = true
			return
		}
		vars[name] = typ
	}
	for name, typ := range fs.pkgVars {
		vars[name] = typ
	}
	addFields := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			for _, n := range f.Names {
				set(n.Name, f.Type)
			}
		}
	}
	addFields(fn.Recv)
	addFields(fn.Type.Params)
	addFields(fn.Type.Results)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ValueSpec:
			for i, name := range node.Names {
				if node.Type != nil {
					set(name.Name, node.Type)
				} else if i < len(node.Values) {
					set(name.Name, inferExprType(node.Values[i]))
				}
			}
		case *ast.AssignStmt:
			if node.Tok != token.DEFINE || len(node.Lhs) != len(node.Rhs) {
				return true
			}
			for i, lhs := range node.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					set(id.Name, inferExprType(node.Rhs[i]))
				}
			}
		}
		return true
	})
	for name := range ambiguous {
		delete(vars, name)
	}
	return vars
}

// inferExprType infers the static type of a value expression from its syntax alone.
func inferExprType(e ast.Expr) ast.Expr {
	switch v := ast.Unparen(e).(type) {
	case *ast.CompositeLit:
		return v.Type
	case *ast.UnaryExpr:
		if v.Op == token.AND {
			if t := inferExprType(v.X); t != nil {
				return &ast.StarExpr{X: t}
			}
		}
	case *ast.CallExpr:
		if id, ok := v.Fun.(*ast.Ident); ok && len(v.Args) >= 1 {
			switch id.Name {
			case "new":
				return &ast.StarExpr{X: v.Args[0]}
			case "make":
				return v.Args[0]
			}
		}
	}
	return nil
}

// targetType returns the type of the value a Decode argument points into (the pointer is
// stripped later by classify), or nil when it cannot be inferred.
func targetType(res *strictTypeResolver, fs *strictFileScope, vars map[string]ast.Expr, arg ast.Expr) (ast.Expr, *strictFileScope) {
	switch v := ast.Unparen(arg).(type) {
	case *ast.UnaryExpr:
		if v.Op == token.AND {
			return targetType(res, fs, vars, v.X)
		}
	case *ast.Ident:
		if t, ok := vars[v.Name]; ok {
			return t, fs
		}
	case *ast.CompositeLit, *ast.CallExpr:
		if t := inferExprType(v); t != nil {
			return t, fs
		}
	case *ast.SelectorExpr:
		// &cfg.Section: the field's declared type inside the base's struct.
		baseType, bfs := targetType(res, fs, vars, v.X)
		if baseType == nil {
			return nil, nil
		}
		st, sfs := res.structOf(baseType, bfs, 0)
		if st == nil {
			return nil, nil
		}
		for _, field := range st.Fields.List {
			for _, n := range field.Names {
				if n.Name == v.Sel.Name {
					return field.Type, sfs
				}
			}
		}
	}
	return nil, nil
}

type targetShape int

const (
	shapeUnknown targetShape = iota
	shapeStructural
	shapeStruct
)

// maxStrictDecoderLookups bounds cross-file type lookups (one grep plus one file read each) per
// check run, so a diff full of decoders cannot blow the S3 latency budget.
const maxStrictDecoderLookups = 8

const maxStrictTypeDepth = 8

type strictFileScope struct {
	dir     string
	imports map[string]string // local name -> import path
	pkgVars map[string]ast.Expr
}

// codecOf reports "yaml" or "json" when local names an imported YAML or encoding/json package.
// Files whose imports are unavailable fall back to the conventional names.
func (fs *strictFileScope) codecOf(local string) string {
	path, ok := fs.imports[local]
	if !ok {
		if local == "yaml" || local == "json" {
			return local
		}
		return ""
	}
	switch {
	case strings.Contains(path, "yaml"):
		return "yaml"
	case path == "encoding/json":
		return "json"
	}
	return ""
}

type strictTypeDecl struct {
	typ ast.Expr
	fs  *strictFileScope
}

type strictTypeResolver struct {
	ctx     context.Context
	ws      WorkspaceReader
	callers *goCallerResolver
	types   map[string]map[string]strictTypeDecl // dir -> type name -> decl
	files   map[string]bool                      // files whose declarations are registered
	missed  map[string]bool                      // dir+"."+name lookups that failed
	lookups int
}

func newStrictTypeResolver(ctx context.Context, ws WorkspaceReader) *strictTypeResolver {
	return &strictTypeResolver{
		ctx:     ctx,
		ws:      ws,
		callers: newGoCallerResolver(ws),
		types:   map[string]map[string]strictTypeDecl{},
		files:   map[string]bool{},
		missed:  map[string]bool{},
	}
}

// registerFile records f's imports, package-level vars and type declarations.
func (r *strictTypeResolver) registerFile(path string, f *ast.File) *strictFileScope {
	fs := &strictFileScope{dir: cleanDir(parentDir(path)), imports: map[string]string{}, pkgVars: map[string]ast.Expr{}}
	for _, imp := range f.Imports {
		ip := strings.Trim(imp.Path.Value, `"`)
		local := ip[strings.LastIndex(ip, "/")+1:]
		if strings.Contains(local, "yaml") || (strings.HasPrefix(local, "v") && strings.Contains(ip, "yaml")) {
			local = "yaml" // gopkg.in/yaml.v3, go.yaml.in/yaml/v3, github.com/goccy/go-yaml, sigs.k8s.io/yaml
		}
		if imp.Name != nil {
			local = imp.Name.Name
		}
		fs.imports[local] = ip
	}
	if r.files[path] {
		return fs
	}
	r.files[path] = true
	decls := r.types[fs.dir]
	if decls == nil {
		decls = map[string]strictTypeDecl{}
		r.types[fs.dir] = decls
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				if s.TypeParams == nil {
					decls[s.Name.Name] = strictTypeDecl{typ: s.Type, fs: fs}
				}
			case *ast.ValueSpec:
				for i, n := range s.Names {
					if s.Type != nil {
						fs.pkgVars[n.Name] = s.Type
					} else if i < len(s.Values) {
						if t := inferExprType(s.Values[i]); t != nil {
							fs.pkgVars[n.Name] = t
						}
					}
				}
			}
		}
	}
	return fs
}

// lookupType finds the declaration of name in the package at dir, reading other files of that
// package on demand.
func (r *strictTypeResolver) lookupType(dir, name string) (strictTypeDecl, bool) {
	if d, ok := r.types[dir][name]; ok {
		return d, true
	}
	key := dir + "." + name
	if r.missed[key] || r.lookups >= maxStrictDecoderLookups {
		return strictTypeDecl{}, false
	}
	r.lookups++
	for _, site := range r.callers.grep(r.ctx, name) {
		if cleanDir(parentDir(site.File)) != dir || r.files[site.File] || isTestPath(site.File) || !strings.HasSuffix(site.File, ".go") {
			continue
		}
		if !looksLikeTypeSpec(site.Snippet, name) {
			continue
		}
		pf := r.callers.parseHead(r.ctx, site.File)
		if pf == nil {
			continue
		}
		r.registerFile(site.File, pf.File)
		if d, ok := r.types[dir][name]; ok {
			return d, true
		}
	}
	r.missed[key] = true
	return strictTypeDecl{}, false
}

// looksLikeTypeSpec reports whether a grep snippet can be the line that declares type name,
// either `type Name ...` or `Name ...` inside a `type (...)` block.
func looksLikeTypeSpec(snippet, name string) bool {
	s := strings.TrimSpace(snippet)
	s = strings.TrimSpace(strings.TrimPrefix(s, "type "))
	rest, ok := strings.CutPrefix(s, name)
	if !ok || rest == "" {
		return false
	}
	return rest[0] == ' ' || rest[0] == '\t' || rest[0] == '['
}

// dirForImport maps an import path to a repository directory when it belongs to the repository's
// module; otherwise "".
func (r *strictTypeResolver) dirForImport(fromDir, importPath string) (string, bool) {
	mod := r.callers.nearestModule(r.ctx, fromDir)
	if mod.ModulePath == "" {
		return "", false
	}
	if importPath == mod.ModulePath {
		return mod.Dir, true
	}
	rel, ok := strings.CutPrefix(importPath, mod.ModulePath+"/")
	if !ok {
		return "", false
	}
	if mod.Dir == "" {
		return cleanDir(rel), true
	}
	return cleanDir(mod.Dir + "/" + rel), true
}

// structuralExternal reports types from packages outside the repository module that are
// structural by construction (yaml.Node, encoding/json types) or are standard-library values.
func structuralExternal(importPath, name string) bool {
	if strings.Contains(importPath, "yaml") && name == "Node" {
		return true
	}
	if importPath == "encoding/json" {
		return true // RawMessage, Number, Token, Delim: no unknown-field semantics
	}
	// Standard-library types (time.Time, big.Int, netip.Addr, ...) are not configuration structs.
	first, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(first, ".")
}

var builtinStructural = map[string]bool{
	"any": true, "bool": true, "string": true, "byte": true, "rune": true, "error": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"float32": true, "float64": true, "complex64": true, "complex128": true,
}

// classify resolves typ (in the scope of fs) to a struct shape, a structural shape, or unknown.
func (r *strictTypeResolver) classify(typ ast.Expr, fs *strictFileScope, depth int) targetShape {
	if typ == nil || fs == nil || depth > maxStrictTypeDepth {
		return shapeUnknown
	}
	switch t := ast.Unparen(typ).(type) {
	case *ast.StarExpr:
		return r.classify(t.X, fs, depth+1)
	case *ast.StructType:
		if t.Fields == nil || len(t.Fields.List) == 0 {
			return shapeStructural
		}
		return shapeStruct
	case *ast.InterfaceType, *ast.FuncType, *ast.ChanType:
		return shapeStructural
	case *ast.ArrayType:
		return r.elemShape(t.Elt, fs, depth)
	case *ast.MapType:
		return r.elemShape(t.Value, fs, depth)
	case *ast.Ident:
		if d, ok := r.lookupType(fs.dir, t.Name); ok {
			return r.classify(d.typ, d.fs, depth+1)
		}
		if builtinStructural[t.Name] {
			return shapeStructural
		}
		return shapeUnknown
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		if !ok {
			return shapeUnknown
		}
		ip, ok := fs.imports[pkg.Name]
		if !ok {
			return shapeUnknown
		}
		if strings.Contains(ip, "yaml") && t.Sel.Name == "Node" {
			return shapeStructural
		}
		if dir, ok := r.dirForImport(fs.dir, ip); ok {
			if d, ok := r.lookupType(dir, t.Sel.Name); ok {
				return r.classify(d.typ, d.fs, depth+1)
			}
			return shapeUnknown
		}
		if structuralExternal(ip, t.Sel.Name) {
			return shapeStructural
		}
		return shapeUnknown
	case *ast.IndexExpr, *ast.IndexListExpr:
		return shapeUnknown // generic instantiation: not resolved without a type checker
	}
	return shapeUnknown
}

// elemShape classifies a container element: containers of structs are struct-shaped, containers
// of anything else are structural.
func (r *strictTypeResolver) elemShape(elt ast.Expr, fs *strictFileScope, depth int) targetShape {
	switch r.classify(elt, fs, depth+1) {
	case shapeStruct:
		return shapeStruct
	case shapeStructural:
		return shapeStructural
	}
	return shapeUnknown
}

// structOf follows pointers and named types to a struct literal type.
func (r *strictTypeResolver) structOf(typ ast.Expr, fs *strictFileScope, depth int) (*ast.StructType, *strictFileScope) {
	if typ == nil || fs == nil || depth > maxStrictTypeDepth {
		return nil, nil
	}
	switch t := ast.Unparen(typ).(type) {
	case *ast.StarExpr:
		return r.structOf(t.X, fs, depth+1)
	case *ast.StructType:
		return t, fs
	case *ast.Ident:
		if d, ok := r.lookupType(fs.dir, t.Name); ok {
			return r.structOf(d.typ, d.fs, depth+1)
		}
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		if !ok {
			return nil, nil
		}
		ip, ok := fs.imports[pkg.Name]
		if !ok {
			return nil, nil
		}
		if dir, ok := r.dirForImport(fs.dir, ip); ok {
			if d, ok := r.lookupType(dir, t.Sel.Name); ok {
				return r.structOf(d.typ, d.fs, depth+1)
			}
		}
	}
	return nil, nil
}

// types2String renders a type expression compactly for findings and comparisons.
func types2String(e ast.Expr) string {
	switch t := e.(type) {
	case nil:
		return ""
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + types2String(t.X)
	case *ast.SelectorExpr:
		return types2String(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + types2String(t.Elt)
		}
		return "[...]" + types2String(t.Elt)
	case *ast.MapType:
		return "map[" + types2String(t.Key) + "]" + types2String(t.Value)
	case *ast.StructType:
		return "struct{...}"
	case *ast.InterfaceType:
		return "interface{...}"
	case *ast.ParenExpr:
		return types2String(t.X)
	}
	return fmt.Sprintf("%T", e)
}

func isConfigDecoderTarget(relPath string, f *ast.File, extraPaths []string) bool {
	lower := strings.ToLower(relPath)
	for _, kw := range defaultStrictDecoderKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	for _, ep := range extraPaths {
		ep = strings.ToLower(strings.TrimSpace(ep))
		if ep != "" && strings.Contains(lower, ep) {
			return true
		}
	}
	for _, imp := range f.Imports {
		val := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(val, "yaml") {
			return true
		}
	}
	return false
}

func fnHasAllowComment(groups []*ast.CommentGroup, fset *token.FileSet, fnStart, fnEnd int) bool {
	for _, cg := range groups {
		cLine := fset.Position(cg.Pos()).Line
		if cLine >= fnStart-3 && cLine <= fnEnd {
			if strings.Contains(cg.Text(), "qa:allow-unknown-fields") {
				return true
			}
		}
	}
	return false
}

func lineInHunks(line int, hunks []qa.LineRange, margin int) bool {
	for _, h := range hunks {
		if line >= h.Start-margin && line <= h.End+margin {
			return true
		}
	}
	return false
}
