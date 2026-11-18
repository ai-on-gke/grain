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
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/qa"
)

func resolveSandboxExec(ws WorkspaceReader, opts RunOptions) (func(ctx context.Context, dir string, argv []string, timeout time.Duration) (string, int, error), string) {
	var checkoutDir string
	if sw, ok := ws.(*SandboxWorkspace); ok {
		checkoutDir = sw.workDir()
		if opts.ExecInSandbox == nil && sw.RunCommand != nil {
			return func(ctx context.Context, dir string, argv []string, timeout time.Duration) (string, int, error) {
				cctx := ctx
				var cancel context.CancelFunc
				if timeout > 0 {
					cctx, cancel = context.WithTimeout(ctx, timeout)
					defer cancel()
				}
				targetDir := filepath.Join(checkoutDir, dir)
				var quoted []string
				for _, a := range argv {
					quoted = append(quoted, shellSingleQuote(a))
				}
				cmd := fmt.Sprintf("cd %s && %s", shellSingleQuote(targetDir), strings.Join(quoted, " "))
				out, isErr, err := sw.RunCommand(cctx, cmd)
				code := 0
				if isErr || err != nil {
					code = 1
				}
				return out, code, err
			}, checkoutDir
		}
	}
	return opts.ExecInSandbox, checkoutDir
}

// CheckMutationHardening generates bounded single-site mutations on added Go lines and runs go test.
func CheckMutationHardening(ctx context.Context, ws WorkspaceReader, pd ParsedDiff, opts RunOptions, params map[string]any) ([]Finding, error) {
	execFn, checkoutDir := resolveSandboxExec(ws, opts)
	if execFn == nil {
		return nil, nil
	}

	mutantsLimit := 25
	maxSeconds := 60
	if m, ok := params["mutants"].(int); ok && m > 0 {
		mutantsLimit = m
	} else if m, ok := params["mutants"].(float64); ok && m > 0 {
		mutantsLimit = int(m)
	}
	if s, ok := params["max-seconds"].(int); ok && s > 0 {
		maxSeconds = s
	} else if s, ok := params["max-seconds"].(float64); ok && s > 0 {
		maxSeconds = int(s)
	}

	deadline := time.Now().Add(time.Duration(maxSeconds) * time.Second)
	var findings []Finding
	totalMutants := 0

	for _, f := range pd.Files {
		if totalMutants >= mutantsLimit || time.Now().After(deadline) {
			break
		}
		p := f.EffectivePath()
		if isTestPath(p) || !strings.HasSuffix(p, ".go") || len(f.AddedLines) == 0 {
			continue
		}

		addedMap := make(map[int]bool, len(f.AddedLines))
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

		type mutation struct {
			node  *ast.BinaryExpr
			oldOp token.Token
			newOp token.Token
			line  int
		}
		var mutations []mutation

		ast.Inspect(fileAST, func(n ast.Node) bool {
			if n == nil {
				return false
			}
			line := fset.Position(n.Pos()).Line
			if !addedMap[line] {
				return true
			}
			be, ok := n.(*ast.BinaryExpr)
			if !ok {
				return true
			}
			var newOp token.Token
			switch be.Op {
			case token.EQL:
				newOp = token.NEQ
			case token.NEQ:
				newOp = token.EQL
			case token.LSS:
				newOp = token.LEQ
			case token.LEQ:
				newOp = token.LSS
			case token.GTR:
				newOp = token.GEQ
			case token.GEQ:
				newOp = token.GTR
			case token.LAND:
				newOp = token.LOR
			case token.LOR:
				newOp = token.LAND
			case token.ADD:
				newOp = token.SUB
			case token.SUB:
				newOp = token.ADD
			}
			if newOp != token.ILLEGAL {
				mutations = append(mutations, mutation{node: be, oldOp: be.Op, newOp: newOp, line: line})
			}
			return true
		})

		remaining := mutantsLimit - totalMutants
		if len(mutations) > remaining {
			mutations = mutations[:remaining]
		}
		if len(mutations) == 0 {
			continue
		}

		pkgDir := filepath.Dir(p)
		if _, exitCode, err := execFn(ctx, pkgDir, []string{"go", "test", "-count=1", "."}, time.Until(deadline)); err != nil || exitCode != 0 {
			continue
		}

		absPath := p
		if checkoutDir != "" && !filepath.IsAbs(p) {
			absPath = filepath.Join(checkoutDir, p)
		}

		func() {
			defer func() {
				_ = os.WriteFile(absPath, headBytes, 0o644)
			}()
			for _, mut := range mutations {
				if time.Now().After(deadline) {
					break
				}
				totalMutants++
				mut.node.Op = mut.newOp
				var buf bytes.Buffer
				_ = printer.Fprint(&buf, fset, fileAST)
				mut.node.Op = mut.oldOp

				if err := os.WriteFile(absPath, buf.Bytes(), 0o644); err != nil {
					continue
				}
				perMutant := 10 * time.Second
				if rem := time.Until(deadline); rem < perMutant {
					perMutant = rem
				}
				out, exitCode, _ := execFn(ctx, pkgDir, []string{"go", "test", "-count=1", "."}, perMutant)
				_ = os.WriteFile(absPath, headBytes, 0o644)

				if exitCode == 0 && !strings.Contains(out, "FAIL") {
					findings = append(findings, Finding{
						RuleID:   qa.RuleMutationHardening,
						Stage:    "S3.5",
						FilePath: p,
						Line:     mut.line,
						Summary:  fmt.Sprintf("Mutant survived: changing `%s` to `%s` kept `go test` green; add an assertion distinguishing this boundary.", mut.oldOp, mut.newOp),
					})
				}
			}
		}()
	}

	return findings, nil
}
