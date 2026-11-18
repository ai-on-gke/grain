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
	"regexp"
	"strings"

	"github.com/ai-on-gke/grain/pkg/qa"
)

type ExportedSymbol struct {
	Name string
	File string
	Line int
}

// CheckExportedSurfaceDelta compares base and head to identify newly added exported surface area.
func CheckExportedSurfaceDelta(ctx context.Context, ws WorkspaceReader, pd ParsedDiff, opts RunOptions, params map[string]any) ([]Finding, error) {
	var findings []Finding
	var newSymbols []ExportedSymbol

	for _, f := range pd.Files {
		p := f.EffectivePath()
		if isTestPath(p) {
			continue
		}

		if strings.HasSuffix(p, ".go") {
			baseBytes, _ := ws.ReadFileAtBase(ctx, f.OldPath)
			headBytes, err := ws.ReadFileAtHead(ctx, p)
			if err != nil {
				continue
			}
			baseExports := parseGoExports(baseBytes, f.OldPath)
			headExports := parseGoExports(headBytes, p)

			baseSet := make(map[string]bool)
			for _, s := range baseExports {
				baseSet[s.Name] = true
			}

			for _, s := range headExports {
				if !baseSet[s.Name] {
					newSymbols = append(newSymbols, s)
				}
			}
		} else if strings.HasSuffix(p, ".ts") {
			baseBytes, _ := ws.ReadFileAtBase(ctx, f.OldPath)
			headBytes, err := ws.ReadFileAtHead(ctx, p)
			if err != nil {
				continue
			}
			baseExports := parseTSExports(baseBytes, f.OldPath)
			headExports := parseTSExports(headBytes, p)

			baseSet := make(map[string]bool)
			for _, s := range baseExports {
				baseSet[s.Name] = true
			}

			for _, s := range headExports {
				if !baseSet[s.Name] {
					newSymbols = append(newSymbols, s)
				}
			}
		}
	}

	if len(newSymbols) == 0 {
		return findings, nil
	}

	maxAdded := 25
	specBytes := opts.TaskSpecBytes
	if specBytes == 0 {
		specBytes = 600
	}
	if specBytes < 400 {
		maxAdded = 12
	}
	if pMax, ok := params["max-exports"].(int); ok {
		maxAdded = pMax
	} else if pMaxFloat, ok := params["max-exports"].(float64); ok {
		maxAdded = int(pMaxFloat)
	}

	if len(newSymbols) > maxAdded {
		var sb strings.Builder
		sb.WriteString("Added exported identifiers:\n")
		for _, s := range newSymbols {
			fmt.Fprintf(&sb, "- %s (%s:%d)\n", s.Name, s.File, s.Line)
		}
		findings = append(findings, Finding{
			RuleID:   qa.RuleExportedSurfaceDelta,
			Stage:    "S3",
			Summary:  fmt.Sprintf("Disproportionate burst of exported surface area: added %d new exported symbols (limit: %d).", len(newSymbols), maxAdded),
			Evidence: sb.String(),
		})
	}

	return findings, nil
}

func parseGoExports(content []byte, path string) []ExportedSymbol {
	var exports []ExportedSymbol
	if len(content) == 0 {
		return exports
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, content, 0)
	if err != nil {
		return exports
	}

	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return false
		}

		switch node := n.(type) {
		case *ast.FuncDecl:
			if node.Name.IsExported() {
				// Don't report methods, only top-level functions or types
				if node.Recv == nil {
					exports = append(exports, ExportedSymbol{Name: node.Name.Name, File: path, Line: fset.Position(node.Name.Pos()).Line})
				} else {
					exports = append(exports, ExportedSymbol{Name: node.Name.Name, File: path, Line: fset.Position(node.Name.Pos()).Line})
				}
			}
		case *ast.GenDecl:
			for _, spec := range node.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						exports = append(exports, ExportedSymbol{Name: s.Name.Name, File: path, Line: fset.Position(s.Name.Pos()).Line})
						if st, ok := s.Type.(*ast.StructType); ok && st.Fields != nil {
							for _, f := range st.Fields.List {
								for _, n := range f.Names {
									if n.IsExported() {
										exports = append(exports, ExportedSymbol{Name: s.Name.Name + "." + n.Name, File: path, Line: fset.Position(n.Pos()).Line})
									}
								}
							}
						} else if it, ok := s.Type.(*ast.InterfaceType); ok && it.Methods != nil {
							for _, m := range it.Methods.List {
								for _, n := range m.Names {
									if n.IsExported() {
										exports = append(exports, ExportedSymbol{Name: s.Name.Name + "." + n.Name, File: path, Line: fset.Position(n.Pos()).Line})
									}
								}
							}
						}
					}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if name.IsExported() {
							exports = append(exports, ExportedSymbol{Name: name.Name, File: path, Line: fset.Position(name.Pos()).Line})
						}
					}
				}
			}
		}
		return true
	})
	return exports
}

var tsExportRe = regexp.MustCompile(`^export\s+(?:function|class|interface|type|const)\s+([a-zA-Z0-9_]+)`)

func parseTSExports(content []byte, path string) []ExportedSymbol {
	var exports []ExportedSymbol
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		m := tsExportRe.FindStringSubmatch(line)
		if m != nil {
			exports = append(exports, ExportedSymbol{Name: m[1], File: path, Line: i + 1})
		}
	}
	return exports
}
