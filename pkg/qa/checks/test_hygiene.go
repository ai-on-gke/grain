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
	"strings"

	"github.com/ai-on-gke/grain/pkg/qa"
)

// CheckTestHygiene analyzes added lines in _test.go files for anti-patterns.
func CheckTestHygiene(ctx context.Context, ws WorkspaceReader, pd ParsedDiff) ([]Finding, error) {
	var findings []Finding

	for _, f := range pd.Files {
		p := f.EffectivePath()
		if !strings.HasSuffix(p, "_test.go") {
			continue
		}
		if len(f.AddedLines) == 0 {
			continue
		}

		addedMap := make(map[int]bool)
		for _, l := range f.AddedLines {
			addedMap[l.Line] = true
		}

		headBytes, err := ws.ReadFileAtHead(ctx, p)
		if err != nil {
			continue
		}

		fset := token.NewFileSet()
		fileAST, err := parser.ParseFile(fset, p, headBytes, parser.ParseComments)
		if err != nil {
			continue
		}

		lines := strings.Split(string(headBytes), "\n")

		ast.Inspect(fileAST, func(n ast.Node) bool {
			if n == nil {
				return false
			}

			line := fset.Position(n.Pos()).Line

			// Check for time.Sleep
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "time" && sel.Sel.Name == "Sleep" {
						if addedMap[line] {
							if !hasQAAllowSleepOnLine(fileAST.Comments, fset, line, lines) {
								findings = append(findings, Finding{
									RuleID:   qa.RuleTestHygiene,
									Stage:    "S3",
									FilePath: p,
									Line:     line,
									Summary:  "Sleep-based timing detected in test. Use channels, condition polling, or fake clocks instead.",
								})
							}
						}
					}
				}
			}

			// Check for infinite loops
			if forStmt, ok := n.(*ast.ForStmt); ok {
				if addedMap[line] {
					if forStmt.Cond == nil || isTrueIdent(forStmt.Cond) {
						if !hasLoopBreaker(forStmt.Body) {
							findings = append(findings, Finding{
								RuleID:   qa.RuleTestHygiene,
								Stage:    "S3",
								FilePath: p,
								Line:     line,
								Summary:  "Unbounded constant-true loop detected. Ensure the loop has a break, return, t.Fatal*, ctx.Done(), or time.After deadline.",
							})
						}
					}
				}
			}
			return true
		})
	}
	return findings, nil
}

func isTrueIdent(n ast.Expr) bool {
	id, ok := n.(*ast.Ident)
	return ok && id.Name == "true"
}

func hasQAAllowSleepOnLine(comments []*ast.CommentGroup, fset *token.FileSet, targetLine int, lines []string) bool {
	for _, cg := range comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "qa:allow-sleep") {
				cLine := fset.Position(c.Pos()).Line
				if cLine == targetLine {
					return true
				}
				if cLine == targetLine-1 {
					// Check if the previous line contains anything other than whitespace and comments
					lineText := strings.TrimSpace(lines[cLine-1])
					if strings.HasPrefix(lineText, "//") || strings.HasPrefix(lineText, "/*") {
						return true
					}
				}
			}
		}
	}
	return false
}

func hasLoopBreaker(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.BranchStmt:
			if node.Tok == token.BREAK {
				found = true
			}
		case *ast.ReturnStmt:
			found = true
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
				name := sel.Sel.Name
				if strings.HasPrefix(name, "Fatal") {
					found = true
				} else if name == "Done" {
					if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "ctx" {
						found = true
					}
				} else if name == "After" {
					if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "time" {
						found = true
					}
				}
			}
		}
		return !found
	})
	return found
}
