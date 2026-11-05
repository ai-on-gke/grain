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

package registry_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const modulePrefix = "github.com/ai-on-gke/grain/"

// registryAllowed lists the only packages in this Go module that
// cmd/grain-registry, pkg/registry/..., and pkg/webauth may import (directly
// or transitively). Everything else in the grain daemon, sandbox, agent, and
// orchestrator trees is off-limits by default (registry.design.md §Import-boundary test).
var registryAllowed = []string{
	"cmd/grain-registry",
	"cmd/grain-registry/...",
	"pkg/registry",
	"pkg/registry/...",
	"pkg/webauth",
	"pkg/webauth/...",
	"pkg/jwks",
	"pkg/tunnel",
	"pkg/tunnel/...",
	"pkg/version",
	"pkg/redact",
	"pkg/apierror",
}

// registryAPIAllowed lists the only packages in this module that
// pkg/registry/api may import (directly or transitively). External non-stdlib
// modules are also forbidden in pkg/registry/api.
var registryAPIAllowed = []string{
	"pkg/registry/api",
	"pkg/tunnel/wsupgrade",
	"pkg/tunnel/mux",
	"pkg/apierror",
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

func runGoList(t *testing.T, args ...string) []string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = repoRootDir(t)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func matchesPattern(rel string, patterns []string) bool {
	for _, pat := range patterns {
		if pat == rel {
			return true
		}
		if root, ok := strings.CutSuffix(pat, "/..."); ok {
			if rel == root || strings.HasPrefix(rel, root+"/") {
				return true
			}
		}
	}
	return false
}

func isRegistryOwnedPackage(rel string) bool {
	return matchesPattern(rel, []string{
		"cmd/grain-registry",
		"cmd/grain-registry/...",
		"pkg/registry",
		"pkg/registry/...",
	})
}

func checkRegistryIsolation(deps []string) []string {
	var forbidden []string
	seen := map[string]bool{}
	for _, pkg := range deps {
		rel, ok := strings.CutPrefix(pkg, modulePrefix)
		if !ok {
			continue
		}
		if !matchesPattern(rel, registryAllowed) && !seen[rel] {
			seen[rel] = true
			forbidden = append(forbidden, rel)
		}
	}
	sort.Strings(forbidden)
	return forbidden
}

type pkgImports struct {
	importPath string
	imports    []string
}

func checkNonRegistryPackagesOnlyImportAPI(pkgs []pkgImports) []string {
	var violations []string
	for _, p := range pkgs {
		callerRel, ok := strings.CutPrefix(p.importPath, modulePrefix)
		if !ok || isRegistryOwnedPackage(callerRel) {
			continue
		}
		for _, imp := range p.imports {
			impRel, ok := strings.CutPrefix(imp, modulePrefix)
			if !ok {
				continue
			}
			if isRegistryOwnedPackage(impRel) && impRel != "pkg/registry/api" {
				violations = append(violations, callerRel+" -> "+impRel)
			}
		}
	}
	sort.Strings(violations)
	return violations
}

type depInfo struct {
	importPath string
	standard   bool
}

func checkRegistryAPIPurity(deps []depInfo) []string {
	var forbidden []string
	for _, d := range deps {
		if d.standard {
			continue
		}
		rel, ok := strings.CutPrefix(d.importPath, modulePrefix)
		if !ok {
			// Third-party module dependency (cloud SDK, jwt, oauth2, etc.).
			forbidden = append(forbidden, d.importPath)
			continue
		}
		if !matchesPattern(rel, registryAPIAllowed) {
			forbidden = append(forbidden, rel)
		}
	}
	sort.Strings(forbidden)
	return forbidden
}

func TestRegistryDoesNotImportDaemonPackages(t *testing.T) {
	roots := []string{"./pkg/registry/...", "./pkg/webauth/..."}
	if info, err := os.Stat(filepath.Join(repoRootDir(t), "cmd", "grain-registry")); err == nil && info.IsDir() {
		roots = append(roots, "./cmd/grain-registry/...")
	}
	args := append([]string{"-deps", "-test=false"}, roots...)
	deps := runGoList(t, args...)
	if bad := checkRegistryIsolation(deps); len(bad) > 0 {
		t.Fatalf("registry/webauth packages transitively import forbidden grain packages:\n\t%s\n\n"+
			"cmd/grain-registry, pkg/registry/..., and pkg/webauth must remain isolated from daemon/sandbox/model packages "+
			"(design/design/registry.design.md §Import-boundary test).",
			strings.Join(bad, "\n\t"))
	}
}

func TestNonRegistryPackagesImportOnlyRegistryAPI(t *testing.T) {
	lines := runGoList(t, "-f", `{{.ImportPath}}|{{join .Imports ","}},{{join .TestImports ","}},{{join .XTestImports ","}}`, "./...")
	var pkgs []pkgImports
	for _, line := range lines {
		pkgPath, rest, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		var imps []string
		seen := map[string]bool{}
		for _, imp := range strings.Split(rest, ",") {
			imp = strings.TrimSpace(imp)
			if imp != "" && !seen[imp] {
				seen[imp] = true
				imps = append(imps, imp)
			}
		}
		pkgs = append(pkgs, pkgImports{importPath: pkgPath, imports: imps})
	}
	if bad := checkNonRegistryPackagesOnlyImportAPI(pkgs); len(bad) > 0 {
		t.Fatalf("packages outside cmd/grain-registry and pkg/registry/... import internal registry packages:\n\t%s\n\n"+
			"Daemon and CLI packages may import pkg/registry/api only.",
			strings.Join(bad, "\n\t"))
	}
}

func TestRegistryAPIDependsOnlyOnStdlibAndLeafPackages(t *testing.T) {
	lines := runGoList(t, "-deps", "-test=false", "-f", "{{.ImportPath}} {{.Standard}}", "./pkg/registry/api")
	var deps []depInfo
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("unexpected go list output line %q", line)
		}
		deps = append(deps, depInfo{
			importPath: fields[0],
			standard:   fields[1] == "true",
		})
	}
	if bad := checkRegistryAPIPurity(deps); len(bad) > 0 {
		t.Fatalf("pkg/registry/api depends on forbidden non-stdlib or sibling packages:\n\t%s\n\n"+
			"pkg/registry/api must depend only on stdlib, pkg/tunnel/wsupgrade, pkg/tunnel/mux, and pkg/apierror.",
			strings.Join(bad, "\n\t"))
	}
}

func TestBoundaryCheckersCatchViolations(t *testing.T) {
	// 1. Registry -> daemon isolation catches every forbidden daemon subtree and prefix lookalikes.
	for _, forbidden := range []string{
		"pkg/orchestrator",
		"pkg/agent/loop",
		"pkg/dispatch",
		"pkg/sandboxaccess",
		"pkg/sandboxaddr",
		"pkg/sandboxcli",
		"pkg/sandboximage",
		"pkg/granule/vmm",
		"pkg/hostd",
		"pkg/modelproxy",
		"pkg/ui",
		"pkg/daemon",
		"pkg/ci/runner",
		"pkg/mcp",
		"pkg/capability/gcpkey",
		"pkg/model",
		"pkg/webauth2",
		"pkg/tunnelvision",
		"pkg/registry2",
	} {
		got := checkRegistryIsolation([]string{modulePrefix + "pkg/registry/server", modulePrefix + forbidden})
		if len(got) != 1 || got[0] != forbidden {
			t.Errorf("checkRegistryIsolation(%q) = %v, want [%s]", forbidden, got, forbidden)
		}
	}

	// 2. Non-registry -> registry internals catches server/store/reconcile/providers/root while allowing pkg/registry/api.
	reverse := checkNonRegistryPackagesOnlyImportAPI([]pkgImports{
		{
			importPath: modulePrefix + "pkg/daemon",
			imports: []string{
				modulePrefix + "pkg/registry/api",
				modulePrefix + "pkg/registry/server",
			},
		},
		{
			importPath: modulePrefix + "pkg/cli",
			imports: []string{
				modulePrefix + "pkg/registry",
				modulePrefix + "pkg/registry/store",
				modulePrefix + "pkg/registry/providers/gcp",
				modulePrefix + "cmd/grain-registry",
			},
		},
		{
			// Internal registry package importing sibling registry packages is allowed here.
			importPath: modulePrefix + "pkg/registry/server",
			imports: []string{
				modulePrefix + "pkg/registry/store",
				modulePrefix + "pkg/registry/api",
			},
		},
	})
	wantReverse := []string{
		"pkg/cli -> cmd/grain-registry",
		"pkg/cli -> pkg/registry",
		"pkg/cli -> pkg/registry/providers/gcp",
		"pkg/cli -> pkg/registry/store",
		"pkg/daemon -> pkg/registry/server",
	}
	if strings.Join(reverse, ",") != strings.Join(wantReverse, ",") {
		t.Fatalf("reverse violations = %v, want %v", reverse, wantReverse)
	}

	// 3. pkg/registry/api purity catches sibling packages, pkg/webauth, and external modules.
	purity := checkRegistryAPIPurity([]depInfo{
		{importPath: "net/http", standard: true},
		{importPath: modulePrefix + "pkg/tunnel/mux", standard: false},
		{importPath: modulePrefix + "pkg/tunnel/wsupgrade", standard: false},
		{importPath: modulePrefix + "pkg/apierror", standard: false},
		{importPath: modulePrefix + "pkg/webauth", standard: false},
		{importPath: modulePrefix + "pkg/registry/store", standard: false},
		{importPath: "cloud.google.com/go/compute/apiv1", standard: false},
	})
	wantPurity := []string{
		"cloud.google.com/go/compute/apiv1",
		"pkg/registry/store",
		"pkg/webauth",
	}
	if strings.Join(purity, ",") != strings.Join(wantPurity, ",") {
		t.Fatalf("purity violations = %v, want %v", purity, wantPurity)
	}
}
