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
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

// Caller resolution for golden:sibling-export-hazard.
//
// A sibling caller of a changed Go export is a non-test .go file in another directory that
// imports the changed package (by import path) AND references the export in a way the change can
// break. Matching a bare identifier across the repository is not enough: names such as Config,
// Unwrap, Validate or Stats exist in most packages of a module, and every one of them used to be
// counted as a caller.
//
// Usage rules (syntactic, no type checker; the check has to stay inside the S3 budget over the
// sandbox command runner):
//   - goUseQualified: `<pkg>.Name` selector on the importing file's identifier for the package
//     (alias-aware; bare `Name` for dot imports). Removed or re-signatured funcs, removed types,
//     changed interfaces.
//   - goUseMethod: the file imports the package and selects `.Method` on a value expression (not
//     on another imported package's identifier). Without type information the receiver type
//     cannot be proven, so precision is bounded by the import relationship.
//   - goUseFields: `.Field` selector on a value expression, a keyed composite literal of
//     `<pkg>.Type` naming the field, or a positional composite literal of `<pkg>.Type`. Removed
//     or re-typed struct fields.
//   - goUsePositional: positional (unkeyed, non-empty) composite literal of `<pkg>.Type`,
//     including elided element literals inside `[]<pkg>.Type{...}` / `map[K]<pkg>.Type{...}`.
//     Adding an exported field only breaks these; keyed literals, field reads and `<pkg>.Type{}`
//     keep compiling.

type goUseKind int

const (
	goUseQualified goUseKind = iota
	goUseMethod
	goUseFields
	goUsePositional
)

// goImporterFile is one parsed head-revision file that imports a changed package.
type goImporterFile struct {
	Path       string
	File       *ast.File
	Fset       *token.FileSet
	Lines      []string
	LocalNames map[string]bool // identifiers bound to the changed package ("." for dot import)
	PkgIdents  map[string]bool // identifiers bound to any import in this file
}

type goCallerResolver struct {
	ws         WorkspaceReader
	modCache   map[string]goModInfo         // dir -> nearest go.mod info
	importers  map[string][]*goImporterFile // package dir -> importer files
	parsed     map[string]*parsedGoFile     // file path -> parsed head file (nil when unreadable)
	grepCache  map[string][]CallSite
	readFailed map[string]bool
}

type goModInfo struct {
	Dir        string // directory holding go.mod ("" for repo root)
	ModulePath string // empty when no go.mod was found
}

type parsedGoFile struct {
	File  *ast.File
	Fset  *token.FileSet
	Lines []string
}

func newGoCallerResolver(ws WorkspaceReader) *goCallerResolver {
	return &goCallerResolver{
		ws:         ws,
		modCache:   map[string]goModInfo{},
		importers:  map[string][]*goImporterFile{},
		parsed:     map[string]*parsedGoFile{},
		grepCache:  map[string][]CallSite{},
		readFailed: map[string]bool{},
	}
}

// nearestModule walks from dir up to the repository root looking for go.mod at HEAD.
func (r *goCallerResolver) nearestModule(ctx context.Context, dir string) goModInfo {
	dir = cleanDir(dir)
	if info, ok := r.modCache[dir]; ok {
		return info
	}
	var info goModInfo
	if loc, ok := r.ws.(goModuleLocator); ok {
		if modDir, raw, found, err := loc.NearestGoModAtHead(ctx, dir); err == nil {
			if found {
				info = goModInfo{Dir: cleanDir(modDir), ModulePath: modfile.ModulePath(raw)}
			}
			r.modCache[dir] = info
			return info
		}
	}
	modPath := "go.mod"
	if dir != "" {
		modPath = dir + "/go.mod"
	}
	if raw, err := r.ws.ReadFileAtHead(ctx, modPath); err == nil {
		if mp := modfile.ModulePath(raw); mp != "" {
			info = goModInfo{Dir: dir, ModulePath: mp}
		}
	}
	if info.ModulePath == "" && dir != "" {
		info = r.nearestModule(ctx, parentDir(dir))
	}
	r.modCache[dir] = info
	return info
}

// importPathFor returns the Go import path of the package in dir, or "" when no go.mod resolves it.
func (r *goCallerResolver) importPathFor(ctx context.Context, dir string) string {
	dir = cleanDir(dir)
	mod := r.nearestModule(ctx, dir)
	if mod.ModulePath == "" {
		return ""
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(dir, mod.Dir), "/")
	if rel == "" {
		return mod.ModulePath
	}
	return mod.ModulePath + "/" + rel
}

func (r *goCallerResolver) grep(ctx context.Context, needle string) []CallSite {
	if sites, ok := r.grepCache[needle]; ok {
		return sites
	}
	sites, err := r.ws.GrepSymbolSites(ctx, needle)
	if err != nil {
		sites = nil
	}
	r.grepCache[needle] = sites
	return sites
}

func (r *goCallerResolver) parseHead(ctx context.Context, file string) *parsedGoFile {
	if pf, ok := r.parsed[file]; ok {
		return pf
	}
	var pf *parsedGoFile
	if src, err := r.ws.ReadFileAtHead(ctx, file); err == nil {
		fset := token.NewFileSet()
		if f, pErr := parser.ParseFile(fset, file, src, parser.SkipObjectResolution); pErr == nil {
			pf = &parsedGoFile{File: f, Fset: fset, Lines: strings.Split(string(src), "\n")}
		}
	}
	r.parsed[file] = pf
	return pf
}

// importersOf returns the head-revision non-test Go files outside pkgDir that import the package
// in pkgDir. pkgName is the package clause name (used when the import has no alias).
//
// With a resolvable go.mod the candidate set is a fixed-string grep for the quoted import path;
// otherwise (fixtures, GOPATH-style trees) candidates come from a grep for the directory's base
// name and are accepted only when an import path equals the directory or ends with "/<dir>".
func (r *goCallerResolver) importersOf(ctx context.Context, pkgDir, pkgName string) []*goImporterFile {
	pkgDir = cleanDir(pkgDir)
	if imps, ok := r.importers[pkgDir]; ok {
		return imps
	}
	importPath := r.importPathFor(ctx, pkgDir)
	var needle string
	matchImport := func(p string) bool { return p == importPath }
	if importPath != "" {
		needle = strconv.Quote(importPath)
	} else {
		needle = path.Base(pkgDir)
		if pkgDir == "" || needle == "." || needle == "/" {
			r.importers[pkgDir] = nil
			return nil
		}
		matchImport = func(p string) bool { return p == pkgDir || strings.HasSuffix(p, "/"+pkgDir) }
	}

	candidates := map[string]bool{}
	for _, s := range r.grep(ctx, needle) {
		f := cleanRelPath(s.File)
		if !strings.HasSuffix(f, ".go") || isTestPath(f) || isVendoredPath(f) || path.Dir(f) == pkgDir {
			continue
		}
		candidates[f] = true
	}
	files := make([]string, 0, len(candidates))
	for f := range candidates {
		files = append(files, f)
	}
	sort.Strings(files)

	var out []*goImporterFile
	for _, f := range files {
		pf := r.parseHead(ctx, f)
		if pf == nil {
			continue
		}
		imp := &goImporterFile{
			Path:       f,
			File:       pf.File,
			Fset:       pf.Fset,
			Lines:      pf.Lines,
			LocalNames: map[string]bool{},
			PkgIdents:  map[string]bool{},
		}
		for _, spec := range pf.File.Imports {
			p, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			local := path.Base(p)
			if spec.Name != nil {
				local = spec.Name.Name
			}
			if matchImport(p) {
				if spec.Name == nil {
					local = pkgName
				}
				if local != "_" {
					imp.LocalNames[local] = true
				}
			}
			if local != "_" && local != "." {
				imp.PkgIdents[local] = true
			}
		}
		if len(imp.LocalNames) > 0 {
			out = append(out, imp)
		}
	}
	r.importers[pkgDir] = out
	return out
}

// usageSites returns the lines in imp that reference exp in a way the change can break.
func (imp *goImporterFile) usageSites(exp modifiedExport) []CallSite {
	var lines []int
	seen := map[int]bool{}
	add := func(n ast.Node) {
		ln := imp.Fset.Position(n.Pos()).Line
		if ln > 0 && !seen[ln] {
			seen[ln] = true
			lines = append(lines, ln)
		}
	}
	dot := imp.LocalNames["."]

	isPkgQualified := func(e ast.Expr, name string) bool {
		switch t := e.(type) {
		case *ast.SelectorExpr:
			if id, ok := t.X.(*ast.Ident); ok && imp.LocalNames[id.Name] && t.Sel.Name == name {
				return true
			}
		case *ast.Ident:
			return dot && t.Name == name
		}
		return false
	}
	isTypeRef := func(e ast.Expr, name string) bool {
		return isPkgQualified(unwrapTypeExpr(e), name)
	}
	isValueSelector := func(sel *ast.SelectorExpr, name string) bool {
		if sel.Sel.Name != name {
			return false
		}
		if id, ok := sel.X.(*ast.Ident); ok && imp.PkgIdents[id.Name] {
			return false // `otherpkg.Name` is a package-qualified identifier, not a field/method.
		}
		return true
	}
	isPositional := func(cl *ast.CompositeLit) bool {
		if len(cl.Elts) == 0 {
			return false
		}
		_, keyed := cl.Elts[0].(*ast.KeyValueExpr)
		return !keyed
	}
	namesField := func(cl *ast.CompositeLit) bool {
		for _, el := range cl.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok && containsString(exp.Fields, id.Name) {
					return true
				}
			}
		}
		return false
	}
	// litMatches reports whether a composite literal of the export's type breaks under the change.
	litMatches := func(cl *ast.CompositeLit) bool {
		switch exp.Use {
		case goUsePositional:
			return isPositional(cl)
		case goUseFields:
			return isPositional(cl) || namesField(cl)
		}
		return false
	}

	ast.Inspect(imp.File, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.ImportSpec:
			return false
		case *ast.SelectorExpr:
			switch exp.Use {
			case goUseQualified:
				if isPkgQualified(t, exp.LookupSymbol) {
					add(t)
				}
			case goUseMethod:
				if isValueSelector(t, exp.LookupSymbol) {
					add(t)
				}
			case goUseFields:
				for _, f := range exp.Fields {
					if isValueSelector(t, f) {
						add(t)
						break
					}
				}
			}
		case *ast.Ident:
			if exp.Use == goUseQualified && dot && t.Name == exp.LookupSymbol {
				add(t)
			}
		case *ast.CompositeLit:
			if exp.Use != goUsePositional && exp.Use != goUseFields {
				return true
			}
			if t.Type != nil && isTypeRef(t.Type, exp.TypeName) && litMatches(t) {
				add(t)
			}
			// Elided element types: []pkg.T{{...}}, map[K]pkg.T{k: {...}}, [N]*pkg.T{{...}}.
			if elt := compositeElemType(t.Type); elt != nil && isTypeRef(elt, exp.TypeName) {
				for _, el := range t.Elts {
					v := el
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						v = kv.Value
					}
					if u, ok := v.(*ast.UnaryExpr); ok && u.Op == token.AND {
						v = u.X
					}
					if inner, ok := v.(*ast.CompositeLit); ok && inner.Type == nil && litMatches(inner) {
						add(inner)
					}
				}
			}
		}
		return true
	})

	sort.Ints(lines)
	out := make([]CallSite, 0, len(lines))
	for _, ln := range lines {
		snippet := ""
		if ln-1 < len(imp.Lines) {
			snippet = strings.TrimSpace(imp.Lines[ln-1])
		}
		out = append(out, CallSite{File: imp.Path, Line: ln, Snippet: snippet})
	}
	return out
}

func unwrapTypeExpr(e ast.Expr) ast.Expr {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		default:
			return e
		}
	}
}

func compositeElemType(e ast.Expr) ast.Expr {
	switch t := e.(type) {
	case *ast.ArrayType:
		return t.Elt
	case *ast.MapType:
		return t.Value
	}
	return nil
}

// TypeScript caller resolution: a caller must import the changed module through a relative
// specifier that resolves to it ('./core', '../x/core.js', or the directory for index.ts).
// Non-relative specifiers (tsconfig path aliases, packages) are not resolved and are not counted.

var tsImportSpecRe = regexp.MustCompile(`(?:\bfrom|\bimport)\s*\(?\s*['"]([^'"\n]+)['"]`)

func tsModuleBaseName(file string) string {
	base := strings.TrimSuffix(path.Base(file), ".ts")
	if base == "index" {
		return path.Base(path.Dir(file))
	}
	return base
}

func tsImportsModule(importerPath, src, modulePath string) bool {
	mod := strings.TrimSuffix(modulePath, ".ts")
	isIndex := path.Base(mod) == "index"
	for _, m := range tsImportSpecRe.FindAllStringSubmatch(src, -1) {
		spec := m[1]
		if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
			continue
		}
		resolved := path.Clean(path.Join(path.Dir(importerPath), spec))
		resolved = strings.TrimSuffix(strings.TrimSuffix(resolved, ".js"), ".ts")
		if resolved == mod || (isIndex && resolved == path.Dir(mod)) {
			return true
		}
	}
	return false
}

func isVendoredPath(p string) bool {
	return strings.HasPrefix(p, "vendor/") || strings.Contains(p, "/vendor/") || strings.Contains(p, "node_modules/")
}

func cleanDir(d string) string {
	d = strings.Trim(path.Clean("/"+d), "/")
	return d
}

func parentDir(d string) string {
	p := path.Dir(d)
	if p == "." || p == "/" {
		return ""
	}
	return p
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
