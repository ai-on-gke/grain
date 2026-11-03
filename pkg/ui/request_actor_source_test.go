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

package ui_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// requestActorFile is the one file in pkg/ui allowed to read the
// deployment's own actor or decide who a request is from.
const requestActorFile = "request_actor.go"

// retiredActorHelpers are the helpers RequestActor replaced
// (operator-attribution spec §2.2). Each one decided who a request was
// from on its own, which is how a write path came to record the
// deployment's actor where it should have recorded the person.
var retiredActorHelpers = map[string]bool{
	"requestUser":    true,
	"actingUser":     true,
	"teamActingUser": true,
}

// TestNothingOutsideRequestActorReadsTheActor is spec §7.3's source-level
// half: outside request_actor.go, no non-test file in pkg/ui reads
// Config.Actor or names requestUser, actingUser or teamActingUser. A write
// that wants its actor asks RequestActor; a check that wants the signed-in
// person asks requestPerson. Comments are not code, so a comment naming
// Config.Actor is fine.
func TestNothingOutsideRequestActorReadsTheActor(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == requestActorFile {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		checked++
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if n.Sel.Name != "Actor" {
					return true
				}
				switch x := n.X.(type) {
				case *ast.SelectorExpr:
					if x.Sel.Name == "Config" {
						t.Errorf("%s reads Config.Actor: ask RequestActor (%s) instead", fset.Position(n.Pos()), requestActorFile)
					}
				case *ast.Ident:
					if x.Name == "Config" {
						t.Errorf("%s reads Config.Actor: ask RequestActor (%s) instead", fset.Position(n.Pos()), requestActorFile)
					}
				}
			case *ast.Ident:
				if retiredActorHelpers[n.Name] {
					t.Errorf("%s names %s, which RequestActor replaced: ask RequestActor or requestPerson (%s)",
						fset.Position(n.Pos()), n.Name, requestActorFile)
				}
			}
			return true
		})
	}
	if checked < 50 {
		t.Fatalf("checked only %d files: is this test running in pkg/ui?", checked)
	}
}
