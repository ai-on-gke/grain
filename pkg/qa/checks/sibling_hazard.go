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
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/qa"
)

type modifiedExport struct {
	Symbol        string // display key: Name, Type.Method
	LookupSymbol  string // identifier callers reference: Name or Method
	TypeName      string // struct type name for field changes
	Fields        []string
	PkgDir        string
	PkgName       string
	File          string
	Line          int
	ChangeSummary string
	Breaking      bool
	Use           goUseKind
	IsTS          bool
}

var tsExportedDeclRe = regexp.MustCompile(`(?m)^\s*export\s+(?:function|interface|class|type)\s+([A-Za-z_][A-Za-z0-9_]*)\b([^\n{;]*)`)

// CheckSiblingExportHazard detects modified exported Go/TypeScript declarations referenced by
// sibling packages that have zero modified files in base...HEAD.
//
// Go exports are diffed per package (the union of the package's changed files at base and HEAD),
// so a declaration moved between files of one package is not a removal, and a brand-new package
// has nothing to remove. Callers are resolved through real import relationships: a file counts
// only if it imports the changed package by import path and references the export in a way the
// change can break (see sibling_callers.go). Adding an exported struct field is a hazard only for
// positional composite literals of that type.
//
// Always returns hazardSites and maxSiblingPkgCount (used by S1 BlastTier classification and S4
// Inquisitor grounding). Emits blocking <hazard_card> findings only on attempt <= 1 when callerNotes
// is empty, clearing cleanly once sibling callers are updated or explained via caller_notes.
func CheckSiblingExportHazard(
	ctx context.Context,
	ws WorkspaceReader,
	pd ParsedDiff,
	attempt int,
	callerNotes string,
) (findings []Finding, hazardSites map[string][]int, maxSiblingPkgs int, err error) {
	hazardSites = map[string][]int{}
	touchedDirs := map[string]bool{}
	for _, df := range pd.Files {
		if p := df.EffectivePath(); p != "" && !isTestPath(p) {
			touchedDirs[path.Dir(p)] = true
		}
	}

	changedExports := collectGoPackageChanges(ctx, ws, pd)
	for _, df := range pd.Files {
		if df.IsNew || df.IsDeleted {
			continue
		}
		p := df.EffectivePath()
		if !strings.HasSuffix(p, ".ts") || isTestPath(p) {
			continue
		}
		baseBytes, bErr := ws.ReadFileAtBase(ctx, nonEmptyPath(df.OldPath, p))
		headBytes, hErr := ws.ReadFileAtHead(ctx, p)
		if bErr == nil && hErr == nil {
			changedExports = append(changedExports, diffTSExports(p, string(baseBytes), string(headBytes))...)
		}
	}
	sort.SliceStable(changedExports, func(i, j int) bool {
		a, b := changedExports[i], changedExports[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Symbol < b.Symbol
	})

	hasWaiver := strings.TrimSpace(callerNotes) != ""
	seenSymbol := map[string]bool{}
	resolver := newGoCallerResolver(ws)

	for _, exp := range changedExports {
		ownDir := path.Dir(exp.File)
		if !exp.IsTS {
			ownDir = exp.PkgDir
		}
		key := ownDir + ":" + exp.Symbol
		if seenSymbol[key] {
			continue
		}
		seenSymbol[key] = true

		var sites []CallSite
		if exp.IsTS {
			sites = tsCallerSites(ctx, ws, resolver, exp)
		} else {
			for _, imp := range resolver.importersOf(ctx, exp.PkgDir, exp.PkgName) {
				sites = append(sites, imp.usageSites(exp)...)
			}
		}

		siblingAllDirs := map[string]bool{}
		untouchedDirs := map[string][]CallSite{}
		for _, site := range sites {
			if isTestPath(site.File) {
				continue
			}
			siteDir := path.Dir(site.File)
			if siteDir == ownDir {
				continue
			}
			siblingAllDirs[siteDir] = true
			if !touchedDirs[siteDir] {
				untouchedDirs[siteDir] = append(untouchedDirs[siteDir], site)
				hazardSites[site.File] = append(hazardSites[site.File], site.Line)
			}
		}

		if len(siblingAllDirs) > maxSiblingPkgs {
			maxSiblingPkgs = len(siblingAllDirs)
		}
		if len(untouchedDirs) == 0 {
			continue
		}
		// Cap noisy ubiquitous symbols (>25 sibling packages) to signature-breaking changes only.
		if len(untouchedDirs) > 25 && !exp.Breaking {
			continue
		}
		if attempt > 1 || hasWaiver {
			continue
		}

		var dirList []string
		for d := range untouchedDirs {
			dirList = append(dirList, d)
		}
		sort.Strings(dirList)
		var examples []string
		for _, d := range dirList {
			cs := untouchedDirs[d][0]
			examples = append(examples, fmt.Sprintf("%s:%d (%s)", cs.File, cs.Line, truncateLine(cs.Snippet, 90)))
			if len(examples) >= 5 {
				break
			}
		}

		findings = append(findings, Finding{
			RuleID:   qa.RuleSiblingExportHazard,
			Stage:    "S3",
			Severity: "blocking",
			FilePath: exp.File,
			Line:     exp.Line,
			Summary: fmt.Sprintf("Modified exported symbol %s (%s) has %d untouched sibling caller package(s).",
				exp.Symbol, exp.ChangeSummary, len(untouchedDirs)),
			Evidence: fmt.Sprintf("<hazard_card symbol=%q untouched_packages=%d>\n%s\nUpdate affected sibling callers or pass `caller_notes` to `open_pull_request` explaining why those callers remain compatible.\n</hazard_card>",
				exp.Symbol, len(untouchedDirs), strings.Join(examples, "\n")),
		})
	}

	for f := range hazardSites {
		sort.Ints(hazardSites[f])
	}
	return findings, hazardSites, maxSiblingPkgs, nil
}

// tsCallerSites returns lines naming exp.Symbol in files that import exp.File's module through a
// resolvable relative specifier. Candidates are the intersection of a grep for the symbol and a
// grep for the module's base name, so only plausible importers are read.
func tsCallerSites(ctx context.Context, ws WorkspaceReader, r *goCallerResolver, exp modifiedExport) []CallSite {
	symSites := r.grep(ctx, exp.Symbol)
	if len(symSites) == 0 {
		return nil
	}
	modFiles := map[string]bool{}
	for _, s := range r.grep(ctx, tsModuleBaseName(exp.File)) {
		modFiles[cleanRelPath(s.File)] = true
	}
	verdict := map[string]bool{}
	var out []CallSite
	for _, s := range symSites {
		f := cleanRelPath(s.File)
		if f == exp.File || !strings.HasSuffix(f, ".ts") || isVendoredPath(f) || !modFiles[f] {
			continue
		}
		ok, cached := verdict[f]
		if !cached {
			if src, err := ws.ReadFileAtHead(ctx, f); err == nil {
				ok = tsImportsModule(f, string(src), exp.File)
			}
			verdict[f] = ok
		}
		if ok {
			out = append(out, CallSite{File: f, Line: s.Line, Snippet: s.Snippet})
		}
	}
	return out
}

// goPkgSide accumulates the exported API of one package directory at one revision, restricted to
// the package's files that appear in the diff. Files outside the diff are identical at base and
// HEAD, so they cannot contribute a removal or change.
type goPkgSide struct {
	name    string
	exports map[string]goSymbolSnap
}

func (s *goPkgSide) merge(filePath string, src []byte) {
	name, exps := extractGoExports(filePath, src)
	if name == "" {
		return
	}
	if s.name == "" {
		s.name = name
	}
	if s.exports == nil {
		s.exports = map[string]goSymbolSnap{}
	}
	for k, v := range exps {
		if _, dup := s.exports[k]; !dup {
			s.exports[k] = v
		}
	}
}

func collectGoPackageChanges(ctx context.Context, ws WorkspaceReader, pd ParsedDiff) []modifiedExport {
	type goDiffFile struct {
		base, head string // "" when absent on that side
	}
	byBaseDir := map[string][]goDiffFile{}
	byHeadDir := map[string][]goDiffFile{}
	for _, df := range pd.Files {
		var basePath, headPath string
		if !df.IsNew {
			basePath = nonEmptyPath(df.OldPath, df.NewPath)
		}
		if !df.IsDeleted {
			headPath = df.NewPath
		}
		gf := goDiffFile{}
		if isGoSourcePath(basePath) {
			gf.base = basePath
			byBaseDir[cleanDir(path.Dir(basePath))] = append(byBaseDir[cleanDir(path.Dir(basePath))], gf)
		}
		if isGoSourcePath(headPath) {
			gf = goDiffFile{head: headPath}
			byHeadDir[cleanDir(path.Dir(headPath))] = append(byHeadDir[cleanDir(path.Dir(headPath))], gf)
		}
	}

	dirs := make([]string, 0, len(byBaseDir))
	for d := range byBaseDir {
		dirs = append(dirs, d) // packages with nothing at base (brand-new) cannot lose exports
	}
	sort.Strings(dirs)

	var out []modifiedExport
	for _, dir := range dirs {
		var base, head goPkgSide
		for _, gf := range byBaseDir[dir] {
			if b, err := ws.ReadFileAtBase(ctx, gf.base); err == nil {
				base.merge(gf.base, b)
			}
		}
		if len(base.exports) == 0 {
			continue
		}
		headFiles := map[string]bool{}
		for _, gf := range byHeadDir[dir] {
			headFiles[gf.head] = true
			if b, err := ws.ReadFileAtHead(ctx, gf.head); err == nil {
				head.merge(gf.head, b)
			}
		}
		pkgName := head.name
		if pkgName == "" {
			pkgName = base.name
		}
		out = append(out, diffGoPackageExports(dir, pkgName, base.exports, head.exports, headFiles)...)
	}
	return out
}

func isGoSourcePath(p string) bool {
	return p != "" && strings.HasSuffix(p, ".go") && !isTestPath(p) && !isVendoredPath(p)
}

type goSymbolSnap struct {
	LookupSymbol string
	TypeName     string
	Sig          string
	Fields       map[string]string
	File         string
	Line         int
	Kind         string // "func", "struct", "interface"
}

// diffGoPackageExports compares one package's exported API between base and HEAD.
// headFiles lists the package's HEAD files present in the diff (removed symbols are reported on
// the file they were declared in when it still exists, else on any changed HEAD file).
func diffGoPackageExports(dir, pkgName string, baseMap, headMap map[string]goSymbolSnap, headFiles map[string]bool) []modifiedExport {
	var out []modifiedExport
	anyHeadFile := ""
	for f := range headFiles {
		if anyHeadFile == "" || f < anyHeadFile {
			anyHeadFile = f
		}
	}
	names := make([]string, 0, len(baseMap))
	for n := range baseMap {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		bSnap := baseMap[name]
		lookup := bSnap.LookupSymbol
		if lookup == "" {
			lookup = name
		}
		mk := func(file string, line int, summary string, breaking bool, use goUseKind) modifiedExport {
			return modifiedExport{
				Symbol: name, LookupSymbol: lookup, TypeName: bSnap.TypeName,
				PkgDir: dir, PkgName: pkgName, File: file, Line: line,
				ChangeSummary: summary, Breaking: breaking, Use: use,
			}
		}
		hSnap, exists := headMap[name]
		if !exists {
			file := bSnap.File
			if !headFiles[file] && anyHeadFile != "" {
				file = anyHeadFile
			}
			use := goUseQualified
			if strings.Contains(name, ".") {
				use = goUseMethod
			}
			out = append(out, mk(file, 1,
				fmt.Sprintf("removed exported %s; declared at %s:%d in base", bSnap.Kind, bSnap.File, bSnap.Line),
				true, use))
			continue
		}
		if bSnap.Kind != hSnap.Kind {
			out = append(out, mk(hSnap.File, hSnap.Line,
				fmt.Sprintf("changed exported %s to %s", bSnap.Kind, hSnap.Kind), true, goUseQualified))
			continue
		}
		switch bSnap.Kind {
		case "func":
			if bSnap.Sig != hSnap.Sig {
				use := goUseQualified
				if strings.Contains(name, ".") {
					use = goUseMethod
				}
				out = append(out, mk(hSnap.File, hSnap.Line, "changed function/method signature", true, use))
			}
		case "interface":
			if bSnap.Sig != hSnap.Sig {
				out = append(out, mk(hSnap.File, hSnap.Line, "changed exported interface method set", true, goUseQualified))
			}
		case "struct":
			var broken, added []string
			for fName, bType := range bSnap.Fields {
				if hType, ok := hSnap.Fields[fName]; !ok || hType != bType {
					broken = append(broken, fName)
				}
			}
			for fName := range hSnap.Fields {
				if _, ok := bSnap.Fields[fName]; !ok {
					added = append(added, fName)
				}
			}
			sort.Strings(broken)
			sort.Strings(added)
			switch {
			case len(broken) > 0:
				reason := fmt.Sprintf("removed/renamed or re-typed struct field %s", strings.Join(broken, ", "))
				e := mk(hSnap.File, hSnap.Line, reason, true, goUseFields)
				e.TypeName, e.LookupSymbol, e.Fields = name, name, broken
				out = append(out, e)
			case len(added) > 0:
				reason := fmt.Sprintf("added exported struct field %s; breaks positional literals only", strings.Join(added, ", "))
				e := mk(hSnap.File, hSnap.Line, reason, false, goUsePositional)
				e.TypeName, e.LookupSymbol = name, name
				out = append(out, e)
			}
		}
	}
	return out
}

// extractGoExports returns the package clause name and the exported API declared in one file.
func extractGoExports(filePath string, src []byte) (string, map[string]goSymbolSnap) {
	out := map[string]goSymbolSnap{}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, src, parser.SkipObjectResolution)
	if err != nil {
		return "", out
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !ast.IsExported(d.Name.Name) {
				continue
			}
			symName := d.Name.Name
			mapKey := symName
			typeName := ""
			if d.Recv != nil && len(d.Recv.List) > 0 {
				recvType := exprString(fset, d.Recv.List[0].Type)
				recvBase := strings.TrimPrefix(recvType, "*")
				if idx := strings.IndexByte(recvBase, '['); idx > 0 {
					recvBase = recvBase[:idx]
				}
				if !ast.IsExported(recvBase) {
					continue
				}
				if isUbiquitousMethodName(symName) {
					continue
				}
				mapKey = recvBase + "." + symName
				typeName = recvBase
			}
			out[mapKey] = goSymbolSnap{
				LookupSymbol: symName,
				TypeName:     typeName,
				Sig:          funcTypeSignature(fset, d.Type),
				File:         filePath,
				Line:         fset.Position(d.Pos()).Line,
				Kind:         "func",
			}
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ast.IsExported(ts.Name.Name) {
					continue
				}
				switch typ := ts.Type.(type) {
				case *ast.StructType:
					fields := map[string]string{}
					if typ.Fields != nil {
						for _, fl := range typ.Fields.List {
							tStr := exprString(fset, fl.Type)
							if len(fl.Names) == 0 {
								if ast.IsExported(strings.TrimPrefix(tStr, "*")) {
									fields[tStr] = tStr
								}
								continue
							}
							for _, n := range fl.Names {
								if ast.IsExported(n.Name) {
									fields[n.Name] = tStr
								}
							}
						}
					}
					out[ts.Name.Name] = goSymbolSnap{
						TypeName: ts.Name.Name,
						Fields:   fields,
						File:     filePath,
						Line:     fset.Position(ts.Pos()).Line,
						Kind:     "struct",
					}
				case *ast.InterfaceType:
					out[ts.Name.Name] = goSymbolSnap{
						Sig:  exprString(fset, typ),
						File: filePath,
						Line: fset.Position(ts.Pos()).Line,
						Kind: "interface",
					}
				}
			}
		}
	}
	return f.Name.Name, out
}

func funcTypeSignature(fset *token.FileSet, ft *ast.FuncType) string {
	if ft == nil {
		return ""
	}
	var params, results []string
	if ft.Params != nil {
		for _, p := range ft.Params.List {
			tStr := exprString(fset, p.Type)
			count := len(p.Names)
			if count == 0 {
				count = 1
			}
			for i := 0; i < count; i++ {
				params = append(params, tStr)
			}
		}
	}
	if ft.Results != nil {
		for _, r := range ft.Results.List {
			tStr := exprString(fset, r.Type)
			count := len(r.Names)
			if count == 0 {
				count = 1
			}
			for i := 0; i < count; i++ {
				results = append(results, tStr)
			}
		}
	}
	return "(" + strings.Join(params, ",") + ")->(" + strings.Join(results, ",") + ")"
}

func exprString(fset *token.FileSet, expr ast.Expr) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, expr); err != nil {
		return ""
	}
	return buf.String()
}

func isUbiquitousMethodName(name string) bool {
	switch name {
	case "String", "Error", "Close", "Read", "Write", "ServeHTTP", "MarshalJSON", "UnmarshalJSON", "Clone", "Equal", "IsZero":
		return true
	default:
		return false
	}
}

func diffTSExports(filePath, baseText, headText string) []modifiedExport {
	baseDecls := extractTSExports(baseText)
	headDecls := extractTSExports(headText)
	var out []modifiedExport
	for name, bSig := range baseDecls {
		hSig, ok := headDecls[name]
		if !ok {
			out = append(out, modifiedExport{
				Symbol:        name,
				File:          filePath,
				Line:          1,
				ChangeSummary: "removed TypeScript export",
				Breaking:      true,
				IsTS:          true,
			})
		} else if bSig.sig != hSig.sig {
			out = append(out, modifiedExport{
				Symbol:        name,
				File:          filePath,
				Line:          hSig.line,
				ChangeSummary: "changed TypeScript export signature",
				Breaking:      true,
				IsTS:          true,
			})
		}
	}
	return out
}

type tsSig struct {
	sig  string
	line int
}

func extractTSExports(src string) map[string]tsSig {
	out := map[string]tsSig{}
	locs := tsExportedDeclRe.FindAllStringSubmatchIndex(src, -1)
	for _, loc := range locs {
		name := src[loc[2]:loc[3]]
		sig := strings.TrimSpace(src[loc[4]:loc[5]])
		line := strings.Count(src[:loc[0]], "\n") + 1
		out[name] = tsSig{sig: sig, line: line}
	}
	return out
}

func nonEmptyPath(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func truncateLine(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
