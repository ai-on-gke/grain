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

package hub_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const (
	modulePrefix    = "github.com/ai-on-gke/grain/"
	plcModulePrefix = "github.com/ai-on-gke/grain/grain-plc"
)

// hubAllowed lists the only packages in this Go module that
// cmd/grain-hub, pkg/hub/..., and pkg/webauth may import (directly
// or transitively). Everything else in the grain daemon, sandbox, agent, and
// orchestrator trees is off-limits by default (hub.design.md §Import-boundary test).
var hubAllowed = []string{
	"cmd/grain-hub",
	"cmd/grain-hub/...",
	"cmd/grain-registry",
	"pkg/hubenv",
	"pkg/hub",
	"pkg/hub/...",
	"pkg/webauth",
	"pkg/webauth/...",
	"pkg/jwks",
	"pkg/tunnel",
	"pkg/tunnel/...",
	"pkg/version",
	"pkg/redact",
	"pkg/apierror",
}

// hubAPIAllowed lists the only packages in this module that
// pkg/hub/api may import (directly or transitively). External non-stdlib
// modules are also forbidden in pkg/hub/api.
var hubAPIAllowed = []string{
	"pkg/hub/api",
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
	return runGoListInDir(t, repoRootDir(t), args...)
}

func runGoListInDir(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s (dir=%s): %v\n%s", strings.Join(args, " "), dir, err, out)
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

func isHubOwnedPackage(rel string) bool {
	return matchesPattern(rel, []string{
		"cmd/grain-hub",
		"cmd/grain-hub/...",
		"cmd/grain-registry",
		"pkg/hub",
		"pkg/hub/...",
	})
}

func checkHubIsolation(deps []string) []string {
	var forbidden []string
	seen := map[string]bool{}
	for _, pkg := range deps {
		rel, ok := strings.CutPrefix(pkg, modulePrefix)
		if !ok {
			continue
		}
		if !matchesPattern(rel, hubAllowed) && !seen[rel] {
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

// hubInternalsTestOnlyCallers are packages outside the registry that
// may import its internals because they are not daemon or CLI code at all:
// tests/security holds every server's trust boundaries as tests (its only
// non-test file is doc.go), and the registry's consent surface is one of
// them, so it stands up a real pkg/hub Server over pkg/hub/store.
var hubInternalsTestOnlyCallers = map[string]bool{
	"tests/security": true,
}

func checkNonHubPackagesOnlyImportAPI(pkgs []pkgImports) []string {
	var violations []string
	for _, p := range pkgs {
		callerRel, ok := strings.CutPrefix(p.importPath, modulePrefix)
		if !ok || isHubOwnedPackage(callerRel) || hubInternalsTestOnlyCallers[callerRel] {
			continue
		}
		for _, imp := range p.imports {
			impRel, ok := strings.CutPrefix(imp, modulePrefix)
			if !ok {
				continue
			}
			if isHubOwnedPackage(impRel) && impRel != "pkg/hub/api" {
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

func checkHubAPIPurity(deps []depInfo) []string {
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
		if !matchesPattern(rel, hubAPIAllowed) {
			forbidden = append(forbidden, rel)
		}
	}
	sort.Strings(forbidden)
	return forbidden
}

func checkNoGrainPLCImports(pkgs []pkgImports) []string {
	var violations []string
	for _, p := range pkgs {
		callerRel := strings.TrimPrefix(p.importPath, modulePrefix)
		for _, imp := range p.imports {
			if imp == plcModulePrefix || strings.HasPrefix(imp, plcModulePrefix+"/") {
				violations = append(violations, callerRel+" -> "+imp)
			}
		}
	}
	sort.Strings(violations)
	return violations
}

func checkGrainPLCImportsOnlyHubAPI(deps []string) []string {
	var forbidden []string
	seen := map[string]bool{}
	for _, pkg := range deps {
		if pkg == plcModulePrefix || strings.HasPrefix(pkg, plcModulePrefix+"/") {
			continue
		}
		rel, ok := strings.CutPrefix(pkg, modulePrefix)
		if !ok {
			continue
		}
		if !matchesPattern(rel, hubAPIAllowed) && !seen[rel] {
			seen[rel] = true
			forbidden = append(forbidden, rel)
		}
	}
	sort.Strings(forbidden)
	return forbidden
}

func TestHubDoesNotImportDaemonPackages(t *testing.T) {
	roots := []string{"./pkg/hub/...", "./pkg/hubenv/...", "./pkg/webauth/..."}
	for _, cmdDir := range []string{"grain-hub", "grain-registry"} {
		if info, err := os.Stat(filepath.Join(repoRootDir(t), "cmd", cmdDir)); err == nil && info.IsDir() {
			roots = append(roots, "./cmd/"+cmdDir+"/...")
		}
	}
	args := append([]string{"-deps", "-test=false"}, roots...)
	deps := runGoList(t, args...)
	if bad := checkHubIsolation(deps); len(bad) > 0 {
		t.Fatalf("registry/webauth packages transitively import forbidden grain packages:\n\t%s\n\n"+
			"cmd/grain-hub, cmd/grain-registry, pkg/hub/..., pkg/hubenv, and pkg/webauth must remain isolated from daemon/sandbox/model packages "+
			"(design/design/hub.design.md §Import-boundary test).",
			strings.Join(bad, "\n\t"))
	}
}

func TestNonHubPackagesImportOnlyHubAPI(t *testing.T) {
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
	if bad := checkNonHubPackagesOnlyImportAPI(pkgs); len(bad) > 0 {
		t.Fatalf("packages outside cmd/grain-hub and pkg/hub/... import internal registry packages:\n\t%s\n\n"+
			"Daemon and CLI packages may import pkg/hub/api only.",
			strings.Join(bad, "\n\t"))
	}
}

func TestHubAPIDependsOnlyOnStdlibAndLeafPackages(t *testing.T) {
	lines := runGoList(t, "-deps", "-test=false", "-f", "{{.ImportPath}} {{.Standard}}", "./pkg/hub/api")
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
	if bad := checkHubAPIPurity(deps); len(bad) > 0 {
		t.Fatalf("pkg/hub/api depends on forbidden non-stdlib or sibling packages:\n\t%s\n\n"+
			"pkg/hub/api must depend only on stdlib, pkg/tunnel/wsupgrade, pkg/tunnel/mux, and pkg/apierror.",
			strings.Join(bad, "\n\t"))
	}
}

func TestCoreNeverImportsGrainPLC(t *testing.T) {
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
	if bad := checkNoGrainPLCImports(pkgs); len(bad) > 0 {
		t.Fatalf("core grain packages import grain-plc (one-way boundary violation):\n\t%s\n\n"+
			"Nothing in github.com/ai-on-gke/grain may import github.com/ai-on-gke/grain/grain-plc.",
			strings.Join(bad, "\n\t"))
	}
	goModBytes, err := os.ReadFile(filepath.Join(repoRootDir(t), "go.mod"))
	if err != nil {
		t.Fatalf("read root go.mod: %v", err)
	}
	if strings.Contains(string(goModBytes), plcModulePrefix) {
		t.Fatalf("root go.mod must not require or replace %s", plcModulePrefix)
	}
}

func TestGrainPLCImportsOnlyHubAPI(t *testing.T) {
	plcDir := filepath.Join(repoRootDir(t), "grain-plc")
	if info, err := os.Stat(filepath.Join(plcDir, "go.mod")); err != nil || info.IsDir() {
		t.Fatalf("grain-plc/go.mod not found at %s: %v", plcDir, err)
	}
	deps := runGoListInDir(t, plcDir, "-deps", "-test=false", "./...")
	if bad := checkGrainPLCImportsOnlyHubAPI(deps); len(bad) > 0 {
		t.Fatalf("grain-plc packages transitively import forbidden core grain packages:\n\t%s\n\n"+
			"grain-plc/ may import only pkg/hub/api (and its allowed leaf dependencies) from github.com/ai-on-gke/grain.",
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
		got := checkHubIsolation([]string{modulePrefix + "pkg/hub/server", modulePrefix + forbidden})
		if len(got) != 1 || got[0] != forbidden {
			t.Errorf("checkHubIsolation(%q) = %v, want [%s]", forbidden, got, forbidden)
		}
	}

	// 2. Non-registry -> registry internals catches server/store/reconcile/providers/root while allowing pkg/hub/api.
	reverse := checkNonHubPackagesOnlyImportAPI([]pkgImports{
		{
			importPath: modulePrefix + "pkg/daemon",
			imports: []string{
				modulePrefix + "pkg/hub/api",
				modulePrefix + "pkg/hub/server",
			},
		},
		{
			importPath: modulePrefix + "pkg/cli",
			imports: []string{
				modulePrefix + "pkg/hub",
				modulePrefix + "pkg/hub/store",
				modulePrefix + "pkg/hub/providers/gcp",
				modulePrefix + "cmd/grain-hub",
			},
		},
		{
			// tests/security exercises the registry's trust boundaries directly.
			importPath: modulePrefix + "tests/security",
			imports: []string{
				modulePrefix + "pkg/hub",
				modulePrefix + "pkg/hub/store",
			},
		},
		{
			// ...but no other tests/ package gets that exemption.
			importPath: modulePrefix + "tests/e2e",
			imports: []string{
				modulePrefix + "pkg/hub/store",
			},
		},
		{
			// Internal registry package importing sibling registry packages is allowed here.
			importPath: modulePrefix + "pkg/hub/server",
			imports: []string{
				modulePrefix + "pkg/hub/store",
				modulePrefix + "pkg/hub/api",
			},
		},
	})
	wantReverse := []string{
		"pkg/cli -> cmd/grain-hub",
		"pkg/cli -> pkg/hub",
		"pkg/cli -> pkg/hub/providers/gcp",
		"pkg/cli -> pkg/hub/store",
		"pkg/daemon -> pkg/hub/server",
		"tests/e2e -> pkg/hub/store",
	}
	if strings.Join(reverse, ",") != strings.Join(wantReverse, ",") {
		t.Fatalf("reverse violations = %v, want %v", reverse, wantReverse)
	}

	// 3. pkg/hub/api purity catches sibling packages, pkg/webauth, and external modules.
	purity := checkHubAPIPurity([]depInfo{
		{importPath: "net/http", standard: true},
		{importPath: modulePrefix + "pkg/tunnel/mux", standard: false},
		{importPath: modulePrefix + "pkg/tunnel/wsupgrade", standard: false},
		{importPath: modulePrefix + "pkg/apierror", standard: false},
		{importPath: modulePrefix + "pkg/webauth", standard: false},
		{importPath: modulePrefix + "pkg/hub/store", standard: false},
		{importPath: "cloud.google.com/go/compute/apiv1", standard: false},
	})
	wantPurity := []string{
		"cloud.google.com/go/compute/apiv1",
		"pkg/hub/store",
		"pkg/webauth",
	}
	if strings.Join(purity, ",") != strings.Join(wantPurity, ",") {
		t.Fatalf("purity violations = %v, want %v", purity, wantPurity)
	}

	// 4. Core -> grain-plc one-way check catches any import of grain-plc.
	plcViolations := checkNoGrainPLCImports([]pkgImports{
		{
			importPath: modulePrefix + "pkg/daemon",
			imports: []string{
				plcModulePrefix + "/pkg/grainclient",
				modulePrefix + "pkg/hub/api",
			},
		},
		{
			importPath: modulePrefix + "pkg/ui",
			imports: []string{
				plcModulePrefix,
			},
		},
	})
	wantPLCViolations := []string{
		"pkg/daemon -> " + plcModulePrefix + "/pkg/grainclient",
		"pkg/ui -> " + plcModulePrefix,
	}
	if strings.Join(plcViolations, ",") != strings.Join(wantPLCViolations, ",") {
		t.Fatalf("plc violations = %v, want %v", plcViolations, wantPLCViolations)
	}

	// 5. grain-plc -> core check allows pkg/hub/api and leaf deps while catching internal core packages.
	plcCoreForbidden := checkGrainPLCImportsOnlyHubAPI([]string{
		plcModulePrefix + "/pkg/grainclient",
		modulePrefix + "pkg/hub/api",
		modulePrefix + "pkg/tunnel/wsupgrade",
		modulePrefix + "pkg/tunnel/mux",
		modulePrefix + "pkg/apierror",
		modulePrefix + "pkg/model",
		modulePrefix + "pkg/ui",
		modulePrefix + "pkg/hub/server",
	})
	wantPLCCoreForbidden := []string{
		"pkg/hub/server",
		"pkg/model",
		"pkg/ui",
	}
	if strings.Join(plcCoreForbidden, ",") != strings.Join(wantPLCCoreForbidden, ",") {
		t.Fatalf("plc core forbidden = %v, want %v", plcCoreForbidden, wantPLCCoreForbidden)
	}
}
