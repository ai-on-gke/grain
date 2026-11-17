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
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/qa"
)

// CheckBoundaryFuzz synthesizes a bounded boundary/fuzz test harness for newly added Go byte/string/offset functions.
func CheckBoundaryFuzz(ctx context.Context, ws WorkspaceReader, pd ParsedDiff, opts RunOptions, params map[string]any) ([]Finding, error) {
	execFn, checkoutDir := resolveSandboxExec(ws, opts)
	if execFn == nil {
		return nil, nil
	}

	maxSeconds := 15
	if s, ok := params["max-seconds"].(int); ok && s > 0 {
		maxSeconds = s
	} else if s, ok := params["max-seconds"].(float64); ok && s > 0 {
		maxSeconds = int(s)
	}
	deadline := time.Now().Add(time.Duration(maxSeconds) * time.Second)
	var findings []Finding

	for _, f := range pd.Files {
		if time.Now().After(deadline) {
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

		for _, d := range fileAST.Decls {
			if time.Now().After(deadline) {
				break
			}
			decl, ok := d.(*ast.FuncDecl)
			if !ok || decl.Recv != nil || decl.Type.Params == nil {
				continue
			}
			line := fset.Position(decl.Pos()).Line
			if !addedMap[line] {
				continue
			}

			var fuzzParams []string
			var callArgs []string
			var paramTypes []string
			canFuzz := true
			hasFuzzableType := false

			for i, field := range decl.Type.Params.List {
				typeStr := ""
				if ident, ok := field.Type.(*ast.Ident); ok {
					typeStr = ident.Name
				} else if arr, ok := field.Type.(*ast.ArrayType); ok && arr.Len == nil {
					if ident, ok := arr.Elt.(*ast.Ident); ok && ident.Name == "byte" {
						typeStr = "[]byte"
					}
				}
				switch typeStr {
				case "int", "int64", "uint", "uint64", "string", "[]byte":
					hasFuzzableType = true
				default:
					canFuzz = false
				}
				if !canFuzz {
					break
				}
				count := len(field.Names)
				if count == 0 {
					count = 1
				}
				for j := 0; j < count; j++ {
					argName := fmt.Sprintf("arg%d_%d", i, j)
					fuzzParams = append(fuzzParams, fmt.Sprintf("%s %s", argName, typeStr))
					callArgs = append(callArgs, argName)
					paramTypes = append(paramTypes, typeStr)
				}
			}
			if !canFuzz || !hasFuzzableType {
				continue
			}

			pkgDir := filepath.Dir(p)
			absPkgDir := pkgDir
			if checkoutDir != "" && !filepath.IsAbs(pkgDir) {
				absPkgDir = filepath.Join(checkoutDir, pkgDir)
			}

			fuzzTestName := "FuzzGrainQA_" + decl.Name.Name
			var b bytes.Buffer
			fmt.Fprintf(&b, "package %s\n\nimport (\n\t\"math\"\n\t\"testing\"\n)\n\nvar _ = math.MaxInt32\n\n", fileAST.Name.Name)
			fmt.Fprintf(&b, "func %s(f *testing.F) {\n", fuzzTestName)
			for _, seedVariant := range []int{0, 1, 2, 3} {
				b.WriteString("\tf.Add(")
				for idx, pt := range paramTypes {
					if idx > 0 {
						b.WriteString(", ")
					}
					b.WriteString(boundarySeedExpr(pt, seedVariant))
				}
				b.WriteString(")\n")
			}
			fmt.Fprintf(&b, "\tf.Fuzz(func(t *testing.T, %s) {\n", strings.Join(fuzzParams, ", "))
			fmt.Fprintf(&b, "\t\t%s(%s)\n", decl.Name.Name, strings.Join(callArgs, ", "))
			b.WriteString("\t})\n}\n")

			fuzzPath := filepath.Join(absPkgDir, "zz_grain_qa_fuzz_test.go")
			if err := os.WriteFile(fuzzPath, b.Bytes(), 0o644); err != nil {
				continue
			}
			func() {
				defer func() {
					_ = os.Remove(fuzzPath)
					_ = os.RemoveAll(filepath.Join(absPkgDir, "testdata", "fuzz", fuzzTestName))
				}()
				seedTimeout := 8 * time.Second
				if rem := time.Until(deadline); rem < seedTimeout {
					seedTimeout = rem
				}
				out, exitCode, _ := execFn(ctx, pkgDir, []string{"go", "test", "-count=1", "-run=^" + fuzzTestName + "$", "."}, seedTimeout)
				if exitCode == 0 && time.Until(deadline) >= 6*time.Second {
					fuzzTimeout := 12 * time.Second
					if rem := time.Until(deadline); rem < fuzzTimeout {
						fuzzTimeout = rem
					}
					out, exitCode, _ = execFn(ctx, pkgDir, []string{"go", "test", "-run=^$", "-fuzz=^" + fuzzTestName + "$", "-fuzztime=500ms", "."}, fuzzTimeout)
				}
				if exitCode != 0 && (strings.Contains(out, "panic:") || strings.Contains(out, "runtime error:")) {
					findings = append(findings, Finding{
						RuleID:   qa.RuleBoundaryFuzz,
						Stage:    "S3.5",
						FilePath: p,
						Line:     line,
						Summary:  fmt.Sprintf("Boundary fuzz harness triggered a panic in `%s` on edge/boundary inputs.", decl.Name.Name),
						Evidence: trimEvidence(out, 600),
					})
				}
			}()
		}
	}

	return findings, nil
}

func boundarySeedExpr(paramType string, variant int) string {
	switch paramType {
	case "int":
		return []string{"0", "-1", "int(math.MaxInt32)", "64"}[variant%4]
	case "int64":
		return []string{"int64(0)", "int64(-1)", "int64(math.MaxInt64)", "int64(64)"}[variant%4]
	case "uint":
		return []string{"uint(0)", "uint(1)", "uint(math.MaxUint32)", "uint(64)"}[variant%4]
	case "uint64":
		return []string{"uint64(0)", "uint64(1)", "uint64(math.MaxUint64)", "uint64(64)"}[variant%4]
	case "[]byte":
		return []string{"[]byte(nil)", "[]byte{}", "[]byte{0x00, 0xff}", "[]byte(\"boundary\")"}[variant%4]
	default:
		return []string{`""`, `"\x00"`, `"\xff\xfe"`, `"boundary"`}[variant%4]
	}
}

func trimEvidence(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
