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
	"path"
	"regexp"
	"strings"

	"github.com/ai-on-gke/grain/pkg/qa"
)

var (
	unannotatedGoSkipRe = regexp.MustCompile(`\b(?:t|b)\.Skip(?:Now|f)?\s*\(`)
	unannotatedJSSkipRe = regexp.MustCompile(`\b(?:it\.skip|describe\.skip|test\.skip|xit|xdescribe|fit|fdescribe|it\.only|describe\.only|test\.only)\s*\(`)
	issueAnnotationRe   = regexp.MustCompile(`(?:#\d+|b/\d+|qa:allow-skip|TODO\([^)]+\))`)

	jsTestBlockStartRe = regexp.MustCompile(`^\s*(?:it|test)\s*\(\s*['"` + "`" + `]([^'"` + "`" + `]+)['"` + "`" + `]`)
	jsAssertionCallRe  = regexp.MustCompile(`\b(?:expect|assert\w*|fail|verify\w*|check\w*)\s*\(`)
)

// CheckTestIntegrity enforces golden:test-integrity-guard across modified Go and TS/JS test files:
//  1. Blocks net test file deletion unless accompanied by a rename or replacement test file in the directory.
//  2. Blocks newly added unannotated skips (t.Skip, it.skip, xit) and focused tests (fit, fdescribe, .only).
//  3. Blocks net drops in test-case count alongside production edits (counting table-test subcases so
//     consolidating 3 Test* functions into a 3-row table test counts as 3 -> 3 and passes cleanly).
//  4. Blocks newly added Go Test* functions or TS/JS it(...) blocks whose recursive AST (including
//     nested t.Run closures and same-package helper functions) contains zero failure or assertion calls.
func CheckTestIntegrity(ctx context.Context, ws WorkspaceReader, pd ParsedDiff) ([]Finding, error) {
	var findings []Finding
	prodModified := pd.NonTestAdded > 0 || pd.NonTestDeleted > 0

	addedTestDirs := map[string]bool{}
	for _, df := range pd.Files {
		if df.IsNew && isTestPath(df.NewPath) {
			addedTestDirs[path.Dir(df.NewPath)] = true
		}
	}

	totalBaseCases := 0
	totalHeadCases := 0

	for _, df := range pd.Files {
		eff := df.EffectivePath()
		if !isTestPath(eff) {
			continue
		}

		if df.IsDeleted && !df.IsRename {
			if !addedTestDirs[path.Dir(df.OldPath)] {
				findings = append(findings, Finding{
					RuleID:   qa.RuleTestIntegrityGuard,
					Stage:    "S3",
					Severity: "blocking",
					FilePath: df.OldPath,
					Line:     1,
					Summary:  fmt.Sprintf("Deleted test file %s without a replacement test file in %s.", df.OldPath, path.Dir(df.OldPath)),
					Evidence: "Restore the test file or migrate its coverage into a sibling test file in the same change.",
				})
			}
			if baseBytes, err := ws.ReadFileAtBase(ctx, df.OldPath); err == nil {
				totalBaseCases += countTestCases(df.OldPath, baseBytes)
			}
			continue
		}

		// Inspect newly added lines for unannotated skips or focused-only markers.
		for idx, al := range df.AddedLines {
			trimmed := strings.TrimSpace(al.Text)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
				continue
			}
			prevText := ""
			if idx > 0 && df.AddedLines[idx-1].Line == al.Line-1 {
				prevText = df.AddedLines[idx-1].Text
			}
			if strings.HasSuffix(eff, "_test.go") && unannotatedGoSkipRe.MatchString(al.Text) {
				if !issueAnnotationRe.MatchString(al.Text) && !issueAnnotationRe.MatchString(prevText) {
					findings = append(findings, Finding{
						RuleID:   qa.RuleTestIntegrityGuard,
						Stage:    "S3",
						Severity: "blocking",
						FilePath: eff,
						Line:     al.Line,
						Summary:  "Added unannotated t.Skip in Go test without an issue reference (#<num> or qa:allow-skip).",
						Evidence: strings.TrimSpace(al.Text),
					})
				}
			}
			if !strings.HasSuffix(eff, "_test.go") && unannotatedJSSkipRe.MatchString(al.Text) {
				isFocus := strings.Contains(al.Text, "fit(") || strings.Contains(al.Text, "fdescribe(") || strings.Contains(al.Text, ".only(")
				if isFocus || (!issueAnnotationRe.MatchString(al.Text) && !issueAnnotationRe.MatchString(prevText)) {
					findings = append(findings, Finding{
						RuleID:   qa.RuleTestIntegrityGuard,
						Stage:    "S3",
						Severity: "blocking",
						FilePath: eff,
						Line:     al.Line,
						Summary:  "Added focused (.only/fit/fdescribe) or unannotated skipped (.skip/xit) spec case.",
						Evidence: strings.TrimSpace(al.Text),
					})
				}
			}
		}

		headBytes, headErr := ws.ReadFileAtHead(ctx, eff)
		if headErr != nil {
			continue
		}
		var baseBytes []byte
		if !df.IsNew {
			basePath := df.OldPath
			if basePath == "" {
				basePath = eff
			}
			baseBytes, _ = ws.ReadFileAtBase(ctx, basePath)
		}

		totalBaseCases += countTestCases(eff, baseBytes)
		totalHeadCases += countTestCases(eff, headBytes)

		if strings.HasSuffix(eff, "_test.go") {
			findings = append(findings, checkNewGoTestsHaveAssertions(eff, baseBytes, headBytes)...)
		} else {
			findings = append(findings, checkNewJSTestsHaveAssertions(eff, df, string(headBytes))...)
		}
	}

	if prodModified && totalBaseCases > 0 && totalHeadCases < totalBaseCases {
		findings = append(findings, Finding{
			RuleID:   qa.RuleTestIntegrityGuard,
			Stage:    "S3",
			Severity: "blocking",
			FilePath: firstTestPath(pd),
			Line:     1,
			Summary:  fmt.Sprintf("Net test-case count decreased from %d to %d while production files changed.", totalBaseCases, totalHeadCases),
			Evidence: "Preserve existing test coverage when modifying production code.",
		})
	} else if !prodModified && len(addedTestDirs) == 0 && totalBaseCases >= 2 && totalBaseCases-totalHeadCases >= 2 {
		findings = append(findings, Finding{
			RuleID:   qa.RuleTestIntegrityGuard,
			Stage:    "S3",
			Severity: "blocking",
			FilePath: firstTestPath(pd),
			Line:     1,
			Summary:  fmt.Sprintf("Net test-case count dropped by %d (%d -> %d) without a replacement test file.", totalBaseCases-totalHeadCases, totalBaseCases, totalHeadCases),
			Evidence: "Migrate deleted test cases into a replacement test or table-driven subtest rather than dropping coverage.",
		})
	}
	return findings, nil
}

func firstTestPath(pd ParsedDiff) string {
	for _, df := range pd.Files {
		if p := df.EffectivePath(); isTestPath(p) {
			return p
		}
	}
	return ""
}

func countTestCases(filePath string, content []byte) int {
	if len(content) == 0 {
		return 0
	}
	if strings.HasSuffix(filePath, "_test.go") {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filePath, content, 0)
		if err != nil {
			return 0
		}
		total := 0
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil || !isGoTestFuncName(fn.Name.Name) {
				continue
			}
			subCount := countGoSubtests(fn.Body)
			if subCount > 0 {
				total += subCount
			} else {
				total++
			}
		}
		return total
	}

	// TS / JS spec files: count `it(` and `test(` invocations.
	count := 0
	for _, line := range strings.Split(string(content), "\n") {
		if jsTestBlockStartRe.MatchString(line) {
			count++
		}
	}
	return count
}

func countGoSubtests(body *ast.BlockStmt) int {
	// Count literal elements in table slices assigned inside the test function when paired with a range loop + t.Run,
	// or explicit non-loop t.Run invocations.
	sliceSizes := map[string]int{}
	tRunInRangeSizes := 0
	directTRuns := 0

	recordCompositeSize := func(ident *ast.Ident, expr ast.Expr) {
		if ident == nil || expr == nil {
			return
		}
		comp, ok := expr.(*ast.CompositeLit)
		if !ok || len(comp.Elts) == 0 {
			return
		}
		switch comp.Type.(type) {
		case *ast.ArrayType, *ast.MapType:
			sliceSizes[ident.Name] = len(comp.Elts)
		}
	}

	ast.Inspect(body, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			if len(stmt.Lhs) == 1 && len(stmt.Rhs) == 1 {
				if ident, ok := stmt.Lhs[0].(*ast.Ident); ok {
					recordCompositeSize(ident, stmt.Rhs[0])
				}
			}
		case *ast.ValueSpec:
			for i, ident := range stmt.Names {
				if i < len(stmt.Values) {
					recordCompositeSize(ident, stmt.Values[i])
				}
			}
		}
		return true
	})

	var walkStmts func(stmts []ast.Stmt, inRange bool)
	walkStmts = func(stmts []ast.Stmt, inRange bool) {
		for _, st := range stmts {
			switch s := st.(type) {
			case *ast.RangeStmt:
				tableLen := 0
				switch x := s.X.(type) {
				case *ast.Ident:
					tableLen = sliceSizes[x.Name]
				case *ast.CompositeLit:
					tableLen = len(x.Elts)
				}
				if hasTRunCall(s.Body) && tableLen > 0 {
					tRunInRangeSizes += tableLen
				} else if s.Body != nil {
					walkStmts(s.Body.List, true)
				}
			case *ast.BlockStmt:
				walkStmts(s.List, inRange)
			case *ast.IfStmt:
				if s.Body != nil {
					walkStmts(s.Body.List, inRange)
				}
			default:
				ast.Inspect(st, func(n ast.Node) bool {
					if _, isRange := n.(*ast.RangeStmt); isRange {
						return false
					}
					if call, ok := n.(*ast.CallExpr); ok && isMethodCall(call, "Run") && !inRange {
						directTRuns++
					}
					return true
				})
			}
		}
	}
	walkStmts(body.List, false)
	return tRunInRangeSizes + directTRuns
}

func hasTRunCall(block *ast.BlockStmt) bool {
	if block == nil {
		return false
	}
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && isMethodCall(call, "Run") {
			found = true
			return false
		}
		return true
	})
	return found
}

func checkNewGoTestsHaveAssertions(filePath string, baseBytes, headBytes []byte) []Finding {
	baseFuncs := map[string]bool{}
	if len(baseBytes) > 0 {
		fsetBase := token.NewFileSet()
		if fb, err := parser.ParseFile(fsetBase, filePath, baseBytes, 0); err == nil {
			for _, d := range fb.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil {
					baseFuncs[fn.Name.Name] = true
				}
			}
		}
	}

	fset := token.NewFileSet()
	fh, err := parser.ParseFile(fset, filePath, headBytes, 0)
	if err != nil {
		return nil
	}

	// Index all functions in the file so recursive helper calls (`assertValid(t, got)`) resolve cleanly.
	localFuncs := map[string]*ast.FuncDecl{}
	for _, d := range fh.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Body != nil {
			localFuncs[fn.Name.Name] = fn
		}
	}

	var findings []Finding
	for _, d := range fh.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil || !isGoTestFuncName(fn.Name.Name) {
			continue
		}
		if baseFuncs[fn.Name.Name] {
			continue
		}
		visited := map[string]bool{fn.Name.Name: true}
		if !blockHasGoAssertion(fn.Body, localFuncs, visited) {
			line := fset.Position(fn.Pos()).Line
			findings = append(findings, Finding{
				RuleID:   qa.RuleTestIntegrityGuard,
				Stage:    "S3",
				Severity: "blocking",
				FilePath: filePath,
				Line:     line,
				Summary:  fmt.Sprintf("Newly added Go test %s contains no failure or assertion calls (including subtests and helpers).", fn.Name.Name),
				Evidence: "Call t.Fatalf / t.Errorf (or an assertion helper taking testing.TB) on unexpected outcomes so the test can fail.",
			})
		}
	}
	return findings
}

var goFailMethods = map[string]bool{
	"Error": true, "Errorf": true,
	"Fatal": true, "Fatalf": true,
	"Fail": true, "FailNow": true,
	"Equal": true, "NotEqual": true, "NoError": true, "ErrorIs": true,
	"True": true, "False": true, "Nil": true, "NotNil": true, "Contains": true,
}

func blockHasGoAssertion(block *ast.BlockStmt, localFuncs map[string]*ast.FuncDecl, visited map[string]bool) bool {
	if block == nil {
		return false
	}
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if goFailMethods[fun.Sel.Name] {
				found = true
				return false
			}
			// Package-qualified assertion helpers (e.g. require.NoError, assert.Equal, check.That).
			if pkgIdent, ok := fun.X.(*ast.Ident); ok {
				lowerPkg := strings.ToLower(pkgIdent.Name)
				if lowerPkg == "assert" || lowerPkg == "require" || lowerPkg == "is" {
					found = true
					return false
				}
			}
		case *ast.Ident:
			name := fun.Name
			lower := strings.ToLower(name)
			if strings.HasPrefix(lower, "assert") || strings.HasPrefix(lower, "require") ||
				strings.HasPrefix(lower, "must") || strings.HasPrefix(lower, "expect") ||
				strings.HasPrefix(lower, "verify") || strings.HasPrefix(lower, "check") {
				if helper, exists := localFuncs[name]; exists {
					if !visited[name] {
						visited[name] = true
						if blockHasGoAssertion(helper.Body, localFuncs, visited) {
							found = true
							return false
						}
					}
				} else {
					// Same-package helper defined in a sibling *_test.go file in the package.
					if passesTestingTArg(call) {
						found = true
						return false
					}
				}
			}
			if helper, exists := localFuncs[name]; exists && !visited[name] {
				visited[name] = true
				if blockHasGoAssertion(helper.Body, localFuncs, visited) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

func passesTestingTArg(call *ast.CallExpr) bool {
	if len(call.Args) == 0 {
		return false
	}
	id, ok := call.Args[0].(*ast.Ident)
	return ok && (id.Name == "t" || id.Name == "tb" || id.Name == "subT")
}

func isGoTestFuncName(name string) bool {
	if !strings.HasPrefix(name, "Test") || name == "TestMain" {
		return false
	}
	if len(name) == 4 {
		return true
	}
	r := name[4]
	return !(r >= 'a' && r <= 'z')
}

func isMethodCall(call *ast.CallExpr, method string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == method
}

func checkNewJSTestsHaveAssertions(filePath string, df DiffFile, headText string) []Finding {
	addedLineSet := make(map[int]bool, len(df.AddedLines))
	for _, al := range df.AddedLines {
		addedLineSet[al.Line] = true
	}
	lines := strings.Split(headText, "\n")
	var findings []Finding

	for i := 0; i < len(lines); i++ {
		lineNum := i + 1
		if !addedLineSet[lineNum] {
			continue
		}
		m := jsTestBlockStartRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		testTitle := m[1]
		endIdx := scanBalancedBraceBlock(lines, i)
		bodySlice := strings.Join(lines[i:endIdx+1], "\n")
		if !jsAssertionCallRe.MatchString(bodySlice) {
			findings = append(findings, Finding{
				RuleID:   qa.RuleTestIntegrityGuard,
				Stage:    "S3",
				Severity: "blocking",
				FilePath: filePath,
				Line:     lineNum,
				Summary:  fmt.Sprintf("Newly added spec %q contains no expect(...) or assertion call.", testTitle),
				Evidence: "Add an explicit expect(...) assertion verifying component or service behavior.",
			})
		}
		i = endIdx
	}
	return findings
}

func scanBalancedBraceBlock(lines []string, startIdx int) int {
	braceDepth := 0
	parenDepth := 0
	openedParen := false
	openedBrace := false
	inBlockComment := false
	inTemplateLit := false
	for i := startIdx; i < len(lines); i++ {
		line := lines[i]
		inQuote := byte(0)
		for j := 0; j < len(line); j++ {
			ch := line[j]
			if inBlockComment {
				if ch == '*' && j+1 < len(line) && line[j+1] == '/' {
					inBlockComment = false
					j++
				}
				continue
			}
			if inTemplateLit {
				if ch == '\\' {
					j++
					continue
				}
				if ch == '`' {
					inTemplateLit = false
				}
				continue
			}
			if inQuote != 0 {
				if ch == '\\' {
					j++
					continue
				}
				if ch == inQuote {
					inQuote = 0
				}
				continue
			}
			if ch == '/' && j+1 < len(line) {
				if line[j+1] == '/' {
					break
				}
				if line[j+1] == '*' {
					inBlockComment = true
					j++
					continue
				}
			}
			if ch == '\'' || ch == '"' {
				inQuote = ch
				continue
			}
			if ch == '`' {
				inTemplateLit = true
				continue
			}
			switch ch {
			case '(':
				parenDepth++
				openedParen = true
			case ')':
				parenDepth--
				if openedParen && parenDepth <= 0 && braceDepth <= 0 {
					return i
				}
			case '{':
				braceDepth++
				openedBrace = true
			case '}':
				braceDepth--
				if !openedParen && openedBrace && braceDepth <= 0 {
					return i
				}
			}
		}
	}
	return len(lines) - 1
}
