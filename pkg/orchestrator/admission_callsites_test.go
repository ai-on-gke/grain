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

package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestSandboxAcquiringCallSitesAreAllAccountedFor enumerates every production
// call site in the repository that acquires, serves, or readopts a sandbox and
// fails if a new path is added outside the metered and admission-controlled
// entry points (grain/task-487).
func TestSandboxAcquiringCallSitesAreAllAccountedFor(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	// 1. Direct backend Sandboxes.Acquire(ctx, name, shape) (3 arguments) may
	// only be called inside SandboxMeter.Acquire.
	allowedBackendAcquire := []string{
		"pkg/orchestrator/sandboxmeter.go",
	}

	// 2. Direct backend ServingSandboxes.Serve(ctx, spec) (2 arguments) may
	// only be called inside SandboxMeter.Serve.
	allowedBackendServe := []string{
		"pkg/orchestrator/sandboxmeter.go",
	}

	// 3. SandboxMeter.Acquire(ctx, sandboxes, kind, name, shape) (5 arguments)
	// may only be called from the documented admission-backed call sites.
	allowedMeterAcquire := []string{
		"pkg/orchestrator/cycle.go",                // agent run & recreate_sandbox (admitted in dispatch.Cycle / held by active run)
		"pkg/orchestrator/persistent_sandboxes.go", // persistent sandbox (reserved & admitted in PoolPersistent)
		"pkg/daemon/ci.go",                         // CI job sandbox (admitted in Engine.Jobs.Acquire in PoolCI)
	}

	// 4. SandboxMeter.Serve(ctx, serving, kind, spec) (4 arguments) may only be
	// called from the standing sandbox supervisor after admitting in PoolStanding.
	allowedMeterServe := []string{
		"pkg/orchestrator/standing_sandboxes.go",
	}

	// 5. Readopt(ctx, name) may only be called from adopt.go, which charges
	// adopted runs into PoolAgent via ResumeAdoptedRun / SyncSandboxLedger.
	allowedReadopt := []string{
		"pkg/orchestrator/adopt.go",
		"pkg/daemon/ci.go",
		"pkg/daemon/ci_statecheck.go",
	}

	// 6. ci.Sandboxes.Acquire(ctx, req) (2 arguments) may only be called from
	// Engine.runJob in pkg/ci/engine.go right after e.Jobs.Acquire, and
	// forwarded by StateCheckSandboxes in pkg/daemon/ci_statecheck.go.
	allowedCISandboxesAcquire := []string{
		"pkg/ci/engine.go",
		"pkg/daemon/ci_statecheck.go",
	}

	fset := token.NewFileSet()
	for _, root := range []string{"pkg", "cmd"} {
		walkRoot := filepath.Join(repoRoot, root)
		err := filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "demo" || d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)

			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", rel, err)
			}

			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pos := fset.Position(call.Pos())
				switch sel.Sel.Name {
				case "Acquire":
					switch len(call.Args) {
					case 3:
						// Sandboxes.Acquire(ctx, name, shape) -- exclude e.Admission.Acquire(ctx, request, waiting)
						if innerSel, ok := sel.X.(*ast.SelectorExpr); ok && innerSel.Sel.Name == "Admission" {
							break
						}
						if !slices.Contains(allowedBackendAcquire, rel) {
							t.Errorf("%s:%d: direct 3-arg Sandboxes.Acquire call outside %v", rel, pos.Line, allowedBackendAcquire)
						}
					case 5:
						// SandboxMeter.Acquire(ctx, sandboxes, kind, name, shape)
						if !slices.Contains(allowedMeterAcquire, rel) {
							t.Errorf("%s:%d: 5-arg SandboxMeter.Acquire call outside %v", rel, pos.Line, allowedMeterAcquire)
						}
					case 2:
						// Either ci.Sandboxes.Acquire(ctx, req), Gate.Acquire(ctx, req),
						// or a non-sandbox semaphore/lock Acquire. Check selector receiver name.
						if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "Sandboxes" {
							if !slices.Contains(allowedCISandboxesAcquire, rel) {
								t.Errorf("%s:%d: Sandboxes.Acquire(ctx, req) call outside %v", rel, pos.Line, allowedCISandboxesAcquire)
							}
						}
						if innerSel, ok := sel.X.(*ast.SelectorExpr); ok && innerSel.Sel.Name == "Sandboxes" {
							if !slices.Contains(allowedCISandboxesAcquire, rel) {
								t.Errorf("%s:%d: .Sandboxes.Acquire(ctx, req) call outside %v", rel, pos.Line, allowedCISandboxesAcquire)
							}
						}
					}
				case "Serve":
					switch len(call.Args) {
					case 2:
						// Distinguish ServingSandboxes.Serve(ctx, spec) from http.Server.Serve(ln)
						// or similar 1-arg / non-sandbox Serve calls.
						if ident, ok := sel.X.(*ast.Ident); ok && (ident.Name == "serving" || ident.Name == "sandboxes") {
							if !slices.Contains(allowedBackendServe, rel) {
								t.Errorf("%s:%d: direct 2-arg ServingSandboxes.Serve call outside %v", rel, pos.Line, allowedBackendServe)
							}
						}
					case 4:
						// SandboxMeter.Serve(ctx, serving, kind, spec) -- exclude mcp.Serve(ctx, registry, r, w)
						if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "mcp" {
							break
						}
						if !slices.Contains(allowedMeterServe, rel) {
							t.Errorf("%s:%d: 4-arg SandboxMeter.Serve call outside %v", rel, pos.Line, allowedMeterServe)
						}
					}
				case "Readopt":
					if !slices.Contains(allowedReadopt, rel) {
						t.Errorf("%s:%d: Readopt call outside %v", rel, pos.Line, allowedReadopt)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
