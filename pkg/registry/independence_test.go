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

// Package registry holds the import-boundary enforcement tests for the grain
// registry (design/design/registry.design.md, "Package layout and boundary rules"):
//
//   - Packages outside cmd/grain-registry and pkg/registry/... may import only
//     pkg/registry/api, never pkg/registry/server, pkg/registry/reconciler,
//     pkg/registry/store, or pkg/registry/gcp.
//   - cmd/grain-registry and pkg/registry/... never import pkg/daemon,
//     pkg/orchestrator, pkg/agent, pkg/model, pkg/ui, or pkg/cli.
//   - pkg/registry/api imports no sibling pkg/registry/... packages and no
//     external cloud SDKs.
package registry

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const module = "github.com/ai-on-gke/grain/"

// registryAllowedModuleImports are the only in-module packages that
// cmd/grain-registry and pkg/registry/... may import.
var registryAllowedModuleImports = []string{
	"cmd/grain-registry",
	"pkg/registry",
	"pkg/registry/...",

	// Neutral leaf helpers holding no grain controller state.
	"pkg/apierror",
	"pkg/jwks",
	"pkg/redact",
	"pkg/tunnel",
	"pkg/tunnel/...",
	"pkg/version",
	"pkg/webauth",
	"pkg/webauth/...",
}

// grainControllerPackages are explicitly forbidden inside cmd/grain-registry
// and pkg/registry/... even if someone were tempted to add one to the allowlist.
var grainControllerPackages = []string{
	"pkg/daemon",
	"pkg/daemon/...",
	"pkg/orchestrator",
	"pkg/orchestrator/...",
	"pkg/agent",
	"pkg/agent/...",
	"pkg/model",
	"pkg/model/...",
	"pkg/ui",
	"pkg/ui/...",
	"pkg/cli",
	"pkg/cli/...",
}

func matches(pkg string, patterns []string) bool {
	for _, p := range patterns {
		if base, ok := strings.CutSuffix(p, "/..."); ok {
			if pkg == base || strings.HasPrefix(pkg, base+"/") {
				return true
			}
		} else if pkg == p {
			return true
		}
	}
	return false
}

func isRegistryPackage(pkg string) bool {
	return pkg == "cmd/grain-registry" ||
		strings.HasPrefix(pkg, "cmd/grain-registry/") ||
		pkg == "pkg/registry" ||
		strings.HasPrefix(pkg, "pkg/registry/")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// allImports runs a single `go list` over ./... returning each in-module
// package mapped to its direct imports (both stdlib/external and in-module).
func allImports(t *testing.T) map[string][]string {
	t.Helper()
	cmd := exec.Command("go", "list", "-f", `{{.ImportPath}} {{join .Imports ","}}`, "./...")
	cmd.Dir = repoRoot(t)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list ./...: %v\n%s", err, stderr.String())
	}
	out := map[string][]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		pkg, rawImports, _ := strings.Cut(line, " ")
		rel, ok := strings.CutPrefix(pkg, module)
		if !ok {
			continue
		}
		var imps []string
		if rawImports != "" {
			imps = strings.Split(rawImports, ",")
		}
		out[rel] = imps
	}
	return out
}

// transitiveRegistryDeps lists all in-module dependencies of cmd/grain-registry
// and pkg/registry/... that currently exist in the tree.
func transitiveRegistryDeps(t *testing.T) map[string][]string {
	t.Helper()
	root := repoRoot(t)
	out := map[string][]string{}
	for _, p := range []string{"cmd/grain-registry", "pkg/registry/..."} {
		if _, err := os.Stat(filepath.Join(root, strings.TrimSuffix(p, "/..."))); errors.Is(err, os.ErrNotExist) {
			continue
		}
		cmd := exec.Command("go", "list", "-deps", "-test=false", "./"+p)
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		b, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list -deps ./%s: %v\n%s", p, err, stderr.String())
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			if rel, ok := strings.CutPrefix(sc.Text(), module); ok {
				out[rel] = append(out[rel], p)
			}
		}
	}
	return out
}

func nonRegistryViolations(importsByPkg map[string][]string) []string {
	var bad []string
	for pkg, imps := range importsByPkg {
		if isRegistryPackage(pkg) {
			continue
		}
		for _, imp := range imps {
			rel, ok := strings.CutPrefix(imp, module)
			if !ok {
				continue
			}
			if isRegistryPackage(rel) && rel != "pkg/registry/api" {
				bad = append(bad, pkg+" imports "+rel)
			}
		}
	}
	sort.Strings(bad)
	return bad
}

func registryDepsViolations(deps map[string][]string) []string {
	var bad []string
	for pkg, from := range deps {
		if matches(pkg, grainControllerPackages) || !matches(pkg, registryAllowedModuleImports) {
			bad = append(bad, pkg+" (via "+strings.Join(from, ", ")+")")
		}
	}
	sort.Strings(bad)
	return bad
}

// apiAllowedModuleImports are the stdlib-only leaf packages pkg/registry/api
// may import (design/design/registry.design.md, "Package layout and boundary rules" #3).
var apiAllowedModuleImports = []string{
	"pkg/registry/api",
	"pkg/tunnel/wsupgrade",
	"pkg/tunnel/mux",
	"pkg/apierror",
}

func apiImportsViolations(allDeps []string) []string {
	var bad []string
	for _, dep := range allDeps {
		if rel, ok := strings.CutPrefix(dep, module); ok {
			if !matches(rel, apiAllowedModuleImports) {
				bad = append(bad, dep)
			}
			continue
		}
		// Any external module dependency has a dot in its first path element
		// (unlike Go stdlib packages such as net/http, crypto/tls, encoding/json).
		first, _, _ := strings.Cut(dep, "/")
		if strings.Contains(first, ".") {
			bad = append(bad, dep)
		}
	}
	sort.Strings(bad)
	return bad
}

func TestOutsidePackagesImportOnlyRegistryAPI(t *testing.T) {
	for _, b := range nonRegistryViolations(allImports(t)) {
		t.Errorf("%s: packages outside cmd/grain-registry and pkg/registry/... may import only pkg/registry/api (design/design/registry.design.md)", b)
	}
}

func TestRegistryNeverImportsGrainController(t *testing.T) {
	for _, b := range registryDepsViolations(transitiveRegistryDeps(t)) {
		t.Errorf("registry links %s: cmd/grain-registry and pkg/registry/... must not import grain controller packages (design/design/registry.design.md)", b)
	}
}

func TestRegistryAPIStaysPureStdlib(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-test=false", "./pkg/registry/api")
	cmd.Dir = repoRoot(t)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps ./pkg/registry/api: %v\n%s", err, stderr.String())
	}
	var deps []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		deps = append(deps, strings.TrimSpace(sc.Text()))
	}
	for _, bad := range apiImportsViolations(deps) {
		t.Errorf("pkg/registry/api depends on %s; it must depend only on the Go standard library", bad)
	}
}

func TestBoundaryChecksCatchViolations(t *testing.T) {
	outside := map[string][]string{
		"pkg/daemon":      {module + "pkg/registry/api", module + "pkg/registry/server"},
		"pkg/cli":         {module + "pkg/registry/api"},
		"pkg/registry/db": {module + "pkg/registry/store"},
	}
	if got := nonRegistryViolations(outside); len(got) != 1 || got[0] != "pkg/daemon imports pkg/registry/server" {
		t.Errorf("nonRegistryViolations = %v, want [pkg/daemon imports pkg/registry/server]", got)
	}

	regDeps := map[string][]string{
		"pkg/registry/api": {"pkg/registry/..."},
		"pkg/model":        {"pkg/registry/..."},
		"pkg/daemon":       {"cmd/grain-registry"},
	}
	if got := registryDepsViolations(regDeps); len(got) != 2 {
		t.Errorf("registryDepsViolations = %v, want 2 violations", got)
	}

	apiDeps := []string{
		"net/http",
		module + "pkg/registry/api",
		module + "pkg/registry/store",
		"cloud.google.com/go/compute/apiv1",
	}
	if got := apiImportsViolations(apiDeps); len(got) != 2 {
		t.Errorf("apiImportsViolations = %v, want sibling and external SDK rejected", got)
	}
}
