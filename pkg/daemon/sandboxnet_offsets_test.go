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

package daemon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/modelproxy"
)

// TestEveryServiceThisPackageServesHasAReservedOffset reads this
// package's own source for every fabric.Serve call and checks the name
// it serves under is in SandboxPortOffsets.
//
// A hand-kept list of what is published (pkg/cli's
// TestEveryServicePublishedTodayHasAReservedOffset) only covers what
// somebody remembered to add to it: "ci api" was served from runDaemon
// with no offset and no entry in that list, so every deployment with a
// port base refused to start its CI API listener and every CI job
// declaring api grants with it. Reading the calls themselves is what
// catches the next one.
func TestEveryServiceThisPackageServesHasAReservedOffset(t *testing.T) {
	offsets, err := SandboxPortOffsets()
	if err != nil {
		t.Fatalf("SandboxPortOffsets: %v", err)
	}

	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		files = append(files, file)
	}

	// The package's string constants, so a call naming its service by
	// one (CapabilityPortsService) is read as the string it is.
	consts := map[string]string{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if v, err := strconv.Unquote(lit.Value); err == nil {
							consts[name.Name] = v
						}
					}
				}
			}
		}
	}

	served := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			// Every publish in this package is on a SandboxFabric held
			// as "fabric"; the sanity check below fails if that stops
			// being so, rather than this quietly finding nothing.
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Serve" {
				return true
			}
			if recv, ok := sel.X.(*ast.Ident); !ok || recv.Name != "fabric" {
				return true
			}
			pos := fset.Position(call.Pos())
			switch arg := call.Args[0].(type) {
			case *ast.BasicLit:
				if arg.Kind != token.STRING {
					return true
				}
				name, _ := strconv.Unquote(arg.Value)
				served[name] = true
			case *ast.Ident:
				name, ok := consts[arg.Name]
				if !ok {
					t.Errorf("%s: Serve names its service by %s, which this test cannot resolve to a string -- "+
						"name it by a literal or a package constant", pos, arg.Name)
					return true
				}
				served[name] = true
			case *ast.BinaryExpr:
				// startModelProxy's vendor.Name+" proxy", inside its loop
				// over modelproxy.Vendors(): every vendor is checked below.
				if lit, ok := arg.Y.(*ast.BasicLit); ok && lit.Value == `" proxy"` {
					for _, vendor := range modelproxy.Vendors() {
						served[vendor.Name+" proxy"] = true
					}
					return true
				}
				t.Errorf("%s: Serve names its service by an expression this test does not know", pos)
			default:
				t.Errorf("%s: Serve names its service by an expression this test does not know", pos)
			}
			return true
		})
	}

	// Sanity: the scan found the services it is meant to be finding,
	// rather than passing on a package it no longer understands.
	for _, name := range []string{"git proxy", "package cache", "Actions cache", CapabilityPortsService, CIAPIService} {
		if !served[name] {
			t.Errorf("found no Serve call for %q -- has this package's wiring moved?", name)
		}
	}

	var names []string
	for name := range served {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := offsets[name]; !ok {
			t.Errorf("%q is served on the sandbox fabric but has no offset in SandboxPortOffsets: "+
				"on a deployment with -sandbox-port-base it fails to start", name)
		}
	}
}
