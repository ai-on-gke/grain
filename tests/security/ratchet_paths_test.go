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

package security

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This file holds the ratchet scanners for arguments that become a path
// or a command line: git-argv-separator and path-containment.

// goPackage is one directory's production Go files, parsed, with the
// names of the constants it declares at package level -- what lets a
// scanner tell `workflowDir` (fixed by the source) from `dir` (whatever
// the caller passed).
type goPackage struct {
	files  []goFile
	consts map[string]bool
}

type goFile struct {
	f    *ast.File
	fset *token.FileSet
	rel  string
}

func productionPackages(t *testing.T, root string) []*goPackage {
	t.Helper()
	byDir := map[string]*goPackage{}
	var order []string
	for _, path := range productionGoFiles(t, root) {
		dir := filepath.Dir(path)
		pkg, ok := byDir[dir]
		if !ok {
			pkg = &goPackage{consts: map[string]bool{}}
			byDir[dir] = pkg
			order = append(order, dir)
		}
		f, fset, rel := parseGo(t, root, path)
		pkg.files = append(pkg.files, goFile{f, fset, rel})
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				for _, name := range spec.(*ast.ValueSpec).Names {
					pkg.consts[name.Name] = true
				}
			}
		}
	}
	out := make([]*goPackage, 0, len(order))
	for _, dir := range order {
		out = append(out, byDir[dir])
	}
	return out
}

// isConstString reports whether e is a string the source fixes: a
// literal, a package-level constant, or a concatenation of those (with
// filepath.Separator and os.PathSeparator counted as constants too).
func isConstString(e ast.Expr, consts map[string]bool) bool {
	switch e := e.(type) {
	case *ast.BasicLit:
		return e.Kind == token.STRING
	case *ast.Ident:
		return consts[e.Name]
	case *ast.ParenExpr:
		return isConstString(e.X, consts)
	case *ast.BinaryExpr:
		return e.Op == token.ADD && isConstString(e.X, consts) && isConstString(e.Y, consts)
	case *ast.CallExpr:
		// string(filepath.Separator)
		if id, ok := e.Fun.(*ast.Ident); ok && id.Name == "string" && len(e.Args) == 1 {
			return isSeparator(e.Args[0])
		}
	case *ast.SelectorExpr:
		return isSeparator(e)
	}
	return false
}

func isSeparator(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && (sel.Sel.Name == "Separator" || sel.Sel.Name == "PathSeparator")
}

// leftmostLiteral is the string literal a concatenation starts with, if
// it starts with one.
func leftmostLiteral(e ast.Expr) (string, bool) {
	for {
		switch x := e.(type) {
		case *ast.BinaryExpr:
			if x.Op != token.ADD {
				return "", false
			}
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return "", false
			}
			s, err := strconv.Unquote(x.Value)
			return s, err == nil
		default:
			return "", false
		}
	}
}

// calleeName is the name a call is made through: f for f(...), Sel for
// x.Sel(...).
func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// gitValueFlags are git options -- global or a subcommand's -- whose
// value is the next argument rather than part of the same one, so a
// variable right after one of them is a value and not a revision or
// path git could take for an option.
var gitValueFlags = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
	"-m": true, "-F": true, "--message": true, "--file": true, "--author": true,
	"-b": true, "-B": true, "--branch": true, "-o": true, "--origin": true,
	"--depth": true, "--format": true, "--pretty": true, "--reference": true,
}

// isGitExecCall reports whether call starts a process named by the
// constant "git" -- exec.Command/CommandContext, or a command builder
// like upgrade's newCommand -- and where git's own argv starts.
func isGitExecCall(call *ast.CallExpr, execPkg string) (int, bool) {
	name := calleeName(call)
	isExec := execPkg != "" && isPkgSel(call.Fun, execPkg, "Command", "CommandContext")
	if !isExec && !strings.HasSuffix(strings.ToLower(name), "command") {
		return 0, false
	}
	for i, a := range call.Args {
		if s, ok := leftmostLiteral(a); ok && s == "git" {
			if _, isLit := a.(*ast.BasicLit); isLit {
				return i + 1, true
			}
		}
	}
	return 0, false
}

// mentionsGit reports whether n contains a call isGitExecCall accepts or
// a []string literal that starts with "git" -- an argv about to run.
func mentionsGit(n ast.Node, execPkg string) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if _, ok := isGitExecCall(n, execPkg); ok {
				found = true
			}
		case *ast.CompositeLit:
			if len(n.Elts) > 0 {
				if s, ok := leftmostLiteral(n.Elts[0]); ok && s == "git" {
					if _, isLit := n.Elts[0].(*ast.BasicLit); isLit {
						found = true
					}
				}
			}
		}
		return !found
	})
	return found
}

// gitWrappers finds a package's own git helpers: functions whose last
// parameter is `args ...string` and that hand it to git -- directly, or
// through another of the package's helpers (gitAuthed -> run). The
// result maps a helper's name to how many fixed parameters precede the
// git arguments.
func gitWrappers(pkg *goPackage) map[string]int {
	type candidate struct {
		fn      *ast.FuncDecl
		param   string
		fixed   int
		execPkg string
	}
	var cands []candidate
	for _, gf := range pkg.files {
		for _, decl := range gf.f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || len(fn.Type.Params.List) == 0 {
				continue
			}
			last := fn.Type.Params.List[len(fn.Type.Params.List)-1]
			ell, ok := last.Type.(*ast.Ellipsis)
			if !ok || len(last.Names) != 1 {
				continue
			}
			if id, ok := ell.Elt.(*ast.Ident); !ok || id.Name != "string" {
				continue
			}
			fixed := -1
			for _, p := range fn.Type.Params.List {
				fixed += max(len(p.Names), 1)
			}
			cands = append(cands, candidate{fn, last.Names[0].Name, fixed, importName(gf.f, "os/exec")})
		}
	}
	wrappers := map[string]int{}
	for {
		grew := false
		for _, c := range cands {
			if _, done := wrappers[c.fn.Name.Name]; done {
				continue
			}
			is := mentionsGit(c.fn.Body, c.execPkg)
			ast.Inspect(c.fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if is || !ok {
					return !is
				}
				// Forwards its own arguments to a helper already known to be one.
				if _, known := wrappers[calleeName(call)]; known && call.Ellipsis.IsValid() {
					if id, ok := call.Args[len(call.Args)-1].(*ast.Ident); ok && id.Name == c.param {
						is = true
					}
				}
				return true
			})
			if is {
				wrappers[c.fn.Name.Name] = c.fixed
				grew = true
			}
		}
		if !grew {
			return wrappers
		}
	}
}

// gitArgvViolation returns the first argument in a git argv that is
// neither fixed by the source, nor a value for a flag, nor after a "--"
// or "--end-of-options" -- one git could read as an option -- or nil.
func gitArgvViolation(args []ast.Expr, consts map[string]bool) ast.Expr {
	prevFlag := ""
	for _, a := range args {
		if s, ok := leftmostLiteral(a); ok {
			if _, isLit := a.(*ast.BasicLit); isLit {
				if s == "--" || s == "--end-of-options" {
					return nil
				}
				prevFlag = s
				continue
			}
			// "refs/heads/"+branch or "--format="+f: a fixed start says
			// what kind of argument it is, whatever follows.
			if s != "" {
				prevFlag = ""
				continue
			}
		}
		if isConstString(a, consts) || gitValueFlags[prevFlag] {
			prevFlag = ""
			continue
		}
		return a
	}
	return nil
}

// scanGitArgv reports every git invocation -- exec of the constant
// "git", or a call to one of the package's own git helpers -- that
// passes a variable revision or path without a "--" or
// "--end-of-options" ahead of it. A call that spreads a slice (args...)
// is a helper forwarding its own arguments, and is checked where those
// are written instead.
func scanGitArgv(t *testing.T, root string) map[string][]string {
	out := map[string][]string{}
	for _, pkg := range productionPackages(t, root) {
		wrappers := gitWrappers(pkg)
		for _, gf := range pkg.files {
			execPkg := importName(gf.f, "os/exec")
			ast.Inspect(gf.f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || call.Ellipsis.IsValid() {
					return true
				}
				start, ok := isGitExecCall(call, execPkg)
				if !ok {
					fixed, isWrapper := wrappers[calleeName(call)]
					if !isWrapper || len(call.Args) <= fixed {
						return true
					}
					start = fixed
				}
				if bad := gitArgvViolation(call.Args[start:], pkg.consts); bad != nil {
					out[gf.rel] = append(out[gf.rel], fmt.Sprintf("%s:%d: git %s: %s is not after \"--\"",
						gf.rel, gf.fset.Position(bad.Pos()).Line, gitSubcommand(call.Args[start:]), types.ExprString(bad)))
				}
				return true
			})
		}
	}
	return out
}

// gitSubcommand names the git command an argv runs, for the report.
func gitSubcommand(args []ast.Expr) string {
	skip := false
	for _, a := range args {
		lit, ok := a.(*ast.BasicLit)
		if !ok {
			if skip {
				skip = false
				continue
			}
			return "..."
		}
		s, _ := strconv.Unquote(lit.Value)
		switch {
		case skip:
			skip = false
		case gitValueFlags[s]:
			skip = true
		case !strings.HasPrefix(s, "-"):
			return s
		}
	}
	return "..."
}

// containmentPathFuncs are the filepath functions whose result is a
// filesystem path built to be compared against a root.
var containmentPathFuncs = []string{"Clean", "Join", "Abs", "EvalSymlinks", "Dir"}

// scanPathContainment reports every strings.HasPrefix that asks whether
// a filesystem path lies under a root it was not given as a constant:
// one where either side is a filepath.Clean/Join/Abs/EvalSymlinks/Dir
// result -- called right there, or assigned to a variable in the same
// function -- or where the prefix ends in filepath.Separator. Such a
// check is lexical, so a symlink under the root walks straight past it;
// safepath.Within is the check that resolves them. A prefix the source
// fixes (".."+sep after filepath.Rel, "tables/") is a check on a name's
// shape rather than on where it leads, and is left alone.
func scanPathContainment(t *testing.T, root string) map[string][]string {
	out := map[string][]string{}
	for _, pkg := range productionPackages(t, root) {
		for _, gf := range pkg.files {
			scanFileContainment(gf, pkg.consts, out)
		}
	}
	return out
}

func scanFileContainment(gf goFile, consts map[string]bool, out map[string][]string) {
	f, fset, rel := gf.f, gf.fset, gf.rel
	if strings.HasPrefix(rel, "pkg/safepath/") {
		return
	}
	stringsPkg := importName(f, "strings")
	fpPkg := importName(f, "path/filepath")
	if stringsPkg == "" || fpPkg == "" {
		return
	}
	isPathCall := func(e ast.Expr) bool {
		call, ok := e.(*ast.CallExpr)
		return ok && isPkgSel(call.Fun, fpPkg, containmentPathFuncs...)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		// Variables the function assigns a filepath result to.
		pathVars := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range as.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if len(as.Rhs) == len(as.Lhs) && isPathCall(as.Rhs[i]) {
					pathVars[id.Name] = true
				}
				if len(as.Rhs) == 1 && len(as.Lhs) == 2 && i == 0 && isPathCall(as.Rhs[0]) {
					pathVars[id.Name] = true // abs, err := filepath.Abs(p)
				}
			}
			return true
		})
		isPath := func(e ast.Expr) bool {
			if isPathCall(e) {
				return true
			}
			if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.ADD {
				e = b.X
			}
			id, ok := e.(*ast.Ident)
			return ok && pathVars[id.Name]
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isPkgSel(call.Fun, stringsPkg, "HasPrefix") || len(call.Args) != 2 {
				return true
			}
			p, prefix := call.Args[0], call.Args[1]
			if isConstString(prefix, consts) {
				return true
			}
			if isPath(p) || isPath(prefix) || endsInSeparator(prefix) {
				out[rel] = append(out[rel], fmt.Sprintf("%s:%d: %s: strings.HasPrefix(%s, %s)",
					rel, fset.Position(call.Pos()).Line, fn.Name.Name, types.ExprString(p), types.ExprString(prefix)))
			}
			return true
		})
	}
}

// endsInSeparator reports whether e is `x + string(filepath.Separator)`
// -- a root being given its boundary. A plain `x + "/"` is not enough on
// its own (a test name's subtests are matched that way); it counts when
// x is a filepath result, through isPath.
func endsInSeparator(e ast.Expr) bool {
	b, ok := e.(*ast.BinaryExpr)
	if !ok || b.Op != token.ADD {
		return false
	}
	if call, ok := b.Y.(*ast.CallExpr); ok && len(call.Args) == 1 {
		return isSeparator(call.Args[0])
	}
	return isSeparator(b.Y)
}
