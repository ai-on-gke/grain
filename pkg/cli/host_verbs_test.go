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

package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// Every subcommand `grain host` documents, and every word cmdHostds
// dispatches on, must reach the host registry (hostRegistryVerb) rather
// than start the host agent. `grain host delete-revoked` did not: it was
// in the usage and in cmdHostds but not in hostRegistryVerb, so on a
// workstation it answered "grain host: lstat /etc/grain/hostd.yaml: no
// such file or directory" and deleted nothing (docs/test-runs/
// 2026-11-07-hosts-enrollment-placement.md).
func TestEveryHostSubcommandReachesTheRegistry(t *testing.T) {
	var words []string
	for _, line := range strings.Split(hostRegistryUsage, "\n") {
		if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "   ") {
			continue
		}
		if f := strings.Fields(line); len(f) > 0 {
			words = append(words, f[0])
		}
	}
	if len(words) < 10 {
		t.Fatalf("read only %d subcommands from hostRegistryUsage (%v); has its layout changed?", len(words), words)
	}
	words = append(words, cmdHostdsCases(t)...)
	for _, w := range words {
		if strings.HasPrefix(w, "-") {
			continue
		}
		if !hostRegistryVerb(w) {
			t.Errorf("grain host %s starts the host agent instead of the host registry: add %q to hostRegistryVerb", w, w)
		}
	}
}

// cmdHostdsCases is every string case label of the switch in cmdHostds,
// read from hostds.go.
func cmdHostdsCases(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "hostds.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "cmdHostds" {
			continue
		}
		for _, st := range fn.Body.List {
			sw, ok := st.(*ast.SwitchStmt)
			if !ok {
				continue
			}
			for _, c := range sw.Body.List {
				for _, e := range c.(*ast.CaseClause).List {
					if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil {
							out = append(out, s)
						}
					}
				}
			}
		}
	}
	if len(out) < 10 {
		t.Fatalf("found only %d case labels in cmdHostds (%v); has its dispatch moved?", len(out), out)
	}
	return out
}
