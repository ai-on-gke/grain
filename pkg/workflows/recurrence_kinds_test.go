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

package workflows

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// RecurrenceKinds is what the tests that must cover every kind range
// over -- the state repository's round trip among them -- so a kind
// declared in recurrence.go and not listed there would slip past all of
// them. This reads the declarations themselves rather than trusting the
// list.
func TestRecurrenceKindsListsEveryDeclaredKind(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "recurrence.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[RecurrenceKind]bool{}
	for _, k := range RecurrenceKinds {
		if listed[k] {
			t.Errorf("RecurrenceKinds lists %q twice", k)
		}
		listed[k] = true
	}
	declared := 0
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			if ident, ok := vs.Type.(*ast.Ident); !ok || ident.Name != "RecurrenceKind" {
				continue
			}
			for i, name := range vs.Names {
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok {
					t.Fatalf("%s is not a string literal", name.Name)
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				declared++
				if !listed[RecurrenceKind(value)] {
					t.Errorf("%s (%q) is declared but not in RecurrenceKinds", name.Name, value)
				}
			}
		}
	}
	if declared != len(RecurrenceKinds) {
		t.Errorf("recurrence.go declares %d kinds, RecurrenceKinds lists %d", declared, len(RecurrenceKinds))
	}
}
