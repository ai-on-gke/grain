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

package ports

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// allowed is a fixed port outside 42xx that non-test code may still
// name, and why. Path, when set, limits the entry to files under that
// slash-separated prefix of the repository; empty allows the port
// anywhere. Adding a line here is the decision docs/ports.md asks to be
// made deliberately: say who owns the port and why it cannot be in 42xx.
type allowed struct {
	port   int
	path   string
	reason string
}

var allowlist = []allowed{
	// Grandfathered sandbox-facing ports (docs/ports.md, "Grandfathered").
	// Baked into sandbox images, agent prompts and memory notes, and
	// held by running sandboxes across an upgrade, so moving them is a
	// breaking change the operator has to schedule.
	{9728, "", "-sandbox-port-base block: the git proxy, http://192.168.9.1:9728"},
	{9729, "", "sandbox port block offset 1 (package cache) and the model-proxy capability's first data-plane port"},
	{9733, "pkg/capability/intree/", "host-script capability's data plane, beside 9728/9729"},
	{38017, "pkg/orchestrator/", "first guest-loopback port a CI job's capability endpoints are forwarded to"},
	// The legacy UI address, kept so an old unit that names it keeps it.
	{8420, "pkg/hostinit/", "LegacyUIAddr: the UI's pre-4242 default, pinned into an older unit rather than moved"},
	// Well-known third-party ports: what the other program listens on,
	// not a choice grain made.
	{5432, "", "PostgreSQL"},
	{5000, "", "a local Docker registry (localhost:5000)"},
	{8443, "", "an example GitHub Enterprise host:port in help and validation text"},
	// Not grain's to move.
	{8080, "cmd/checkpost/", "checkpost is a separate service whose deployment publishes :8080"},
	{8080, "cmd/github-webhook/", "github-webhook is a separate gateway service whose deployment (deploy/kubernetes/github-webhook.yaml) publishes :8080"},
	{8080, "pkg/cli/ghwebhook_flags.go", "-github-webhook-url's example names the github-webhook gateway service's :8080"},
	{7443, "pkg/granule/", "granule listen mode's TCP port inside each kubernetes-granule sandbox pod (its own network namespace, never a host listener), which the relay and NetworkPolicy name"},
	{41234, "pkg/demo/", "a remote executor's address in demo fixtures, not a listener"},
}

// skipDirs are never linted wherever they are: other people's code and
// test data. skipRoots are skipped at the top of the repository only: the
// test-only tests/ tree and the frontend.
var (
	skipDirs  = map[string]bool{"node_modules": true, "third_party": true, "vendor": true, "testdata": true}
	skipRoots = map[string]bool{"tests": true, "ui": true}
)

// hostPort finds "host:port" and ":port" in a string literal: an
// optional host (name, dotted IPv4, bracketed IPv6), a colon and four or
// five digits that are not followed by more of a word. Ports below 1024
// are not reported -- 80, 443 and 53 are fixed by their protocols.
var hostPort = regexp.MustCompile(`(?:^|[^0-9A-Za-z_:])(?:[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*|\[[0-9A-Fa-f:.]*\])?:([0-9]{4,5})(?:$|[^0-9A-Za-z_])`)

// TestNoFixedPortsOutsideTheConvention is the vet check the convention
// asks for: a fixed port literal in non-test Go code is in 42xx or on
// allowlist with a reason. It reads three shapes of literal:
//
//   - a string with a host:port or :port in it ("127.0.0.1:8420");
//   - an integer given to a name with "port" in it (const GitPort = 9728,
//     Port: 9728, port = 9728);
//   - an integer default of a flag whose name has "port" in it.
//
// Ports picked at run time never appear as literals, so they never trip
// it; neither do comments.
func TestNoFixedPortsOutsideTheConvention(t *testing.T) {
	root := repoRoot(t)
	var findings []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, skip := lintable(root, path, d)
		if skip != nil || rel == "" {
			return skip
		}
		for _, f := range lintFile(t, path) {
			if !permitted(f.port, rel) {
				findings = append(findings, fmt.Sprintf("%s:%d: port %d (%s)", rel, f.line, f.port, f.what))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(findings)
	if len(findings) > 0 {
		t.Errorf("fixed ports outside %d-%d (docs/ports.md): use a 42xx port from pkg/ports, or add an allowlist entry with a reason in pkg/ports/lint_test.go:\n  %s",
			ConventionMin, ConventionMax, strings.Join(findings, "\n  "))
	}
}

// TestAllowlistIsUsed keeps the allowlist honest: an entry nothing
// needs any more is one a later port could hide behind.
func TestAllowlistIsUsed(t *testing.T) {
	root := repoRoot(t)
	used := map[int]bool{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, skip := lintable(root, path, d)
		if skip != nil || rel == "" {
			return skip
		}
		for _, f := range lintFile(t, path) {
			for i, a := range allowlist {
				if a.port == f.port && strings.HasPrefix(rel, a.path) {
					used[i] = true
				}
			}
		}
		return nil
	})
	for i, a := range allowlist {
		if !used[i] {
			t.Errorf("allowlist entry for port %d (%q) matches nothing any more; remove it", a.port, a.reason)
		}
	}
}

func TestLintFindsEachShape(t *testing.T) {
	src := `package x
import "flag"
const GitPort = 9728
const bufSize = 9728
var addr = "127.0.0.1:8421"
var url = "http://grain.internal:8422/api"
var bare = ":8430"
var ok = "0.0.0.0:4242"
var sha = "sha256:12345abc"
var clock = "15:04:05"
type c struct{ Port int }
var v = c{Port: 9730}
func f() { var port int; port = 5555; _ = port; flag.Int("granule-base-port", 12000, ""); flag.Int("count", 9999, "") }
`
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, f := range lintFile(t, path) {
		got = append(got, f.port)
	}
	want := []int{9728, 8421, 8422, 8430, 4242, 9730, 5555, 12000}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("lintFile found %v, want %v", got, want)
	}
}

// lintable is the walk's filter: the repository-relative path of a
// non-test Go file to lint, or SkipDir for a directory not to descend.
func lintable(root, path string, d fs.DirEntry) (string, error) {
	rel, _ := filepath.Rel(root, path)
	rel = filepath.ToSlash(rel)
	if d.IsDir() {
		if path != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") || skipRoots[rel]) {
			return "", filepath.SkipDir
		}
		return "", nil
	}
	if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
		return "", nil
	}
	return rel, nil
}

type finding struct {
	line int
	port int
	what string
}

func permitted(port int, rel string) bool {
	if port < 1024 || InConvention(port) {
		return true
	}
	for _, a := range allowlist {
		if a.port == port && strings.HasPrefix(rel, a.path) {
			return true
		}
	}
	return false
}

func lintFile(t *testing.T, path string) []finding {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	var out []finding
	add := func(lit ast.Expr, what string) {
		bl, ok := lit.(*ast.BasicLit)
		if !ok || bl.Kind != token.INT {
			return
		}
		n, err := strconv.ParseInt(bl.Value, 0, 64)
		if err != nil || n < 1024 || n > 65535 {
			return
		}
		out = append(out, finding{fset.Position(bl.Pos()).Line, int(n), what})
	}
	portish := func(e ast.Expr) bool {
		var name string
		switch e := e.(type) {
		case *ast.Ident:
			name = e.Name
		case *ast.SelectorExpr:
			name = e.Sel.Name
		}
		return strings.Contains(strings.ToLower(name), "port")
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(n.Value)
			if err != nil {
				return true
			}
			for _, m := range hostPort.FindAllStringSubmatch(s, -1) {
				if p, err := strconv.Atoi(m[1]); err == nil && p >= 1024 && p <= 65535 {
					out = append(out, finding{fset.Position(n.Pos()).Line, p, "address in a string"})
				}
			}
		case *ast.ValueSpec:
			for i, name := range n.Names {
				if i < len(n.Values) && portish(name) {
					add(n.Values[i], "value of "+name.Name)
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range n.Lhs {
				if i < len(n.Rhs) && portish(lhs) {
					add(n.Rhs[i], "assigned to a port")
				}
			}
		case *ast.KeyValueExpr:
			if portish(n.Key) {
				add(n.Value, "port field")
			}
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Int" && sel.Sel.Name != "IntVar") {
				return true
			}
			nameAt, valueAt := 0, 1
			if sel.Sel.Name == "IntVar" {
				nameAt, valueAt = 1, 2
			}
			if len(n.Args) <= valueAt {
				return true
			}
			if name, ok := n.Args[nameAt].(*ast.BasicLit); ok && name.Kind == token.STRING && strings.Contains(strings.ToLower(name.Value), "port") {
				add(n.Args[valueAt], "default of flag "+name.Value)
			}
		}
		return true
	})
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's directory")
		}
		dir = parent
	}
}
