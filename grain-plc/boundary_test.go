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

package grainplc_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	grainplc "github.com/ai-on-gke/grain/grain-plc"
)

const coreModulePrefix = "github.com/ai-on-gke/grain/"

var allowedCorePackages = map[string]bool{
	"pkg/hub/api":          true,
	"pkg/tunnel/wsupgrade": true,
	"pkg/tunnel/mux":       true,
	"pkg/apierror":         true,
}

func plcRootDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Dir(thisFile))
}

func runGoListIn(t *testing.T, dir string, args ...string) []string {
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

func TestGrainPLCHasIsolatedGoModule(t *testing.T) {
	lines := runGoListIn(t, plcRootDir(t), "-m")
	if len(lines) == 0 || lines[0] != grainplc.ModulePath {
		t.Fatalf("go list -m = %v, want [%s]", lines, grainplc.ModulePath)
	}
}

func TestGrainPLCImportsOnlyPublicCoreAPIs(t *testing.T) {
	deps := runGoListIn(t, plcRootDir(t), "-deps", "-test=false", "./...")
	var forbidden []string
	seen := map[string]bool{}
	for _, pkg := range deps {
		if pkg == grainplc.ModulePath || strings.HasPrefix(pkg, grainplc.ModulePath+"/") {
			continue
		}
		rel, ok := strings.CutPrefix(pkg, coreModulePrefix)
		if !ok {
			continue
		}
		if !allowedCorePackages[rel] && !seen[rel] {
			seen[rel] = true
			forbidden = append(forbidden, rel)
		}
	}
	sort.Strings(forbidden)
	if len(forbidden) > 0 {
		t.Fatalf("grain-plc imports internal core packages:\n\t%s\n\n"+
			"grain-plc may import only pkg/hub/api from github.com/ai-on-gke/grain.",
			strings.Join(forbidden, "\n\t"))
	}
}

func TestCoreDoesNotImportGrainPLC(t *testing.T) {
	coreRoot := filepath.Clean(filepath.Join(plcRootDir(t), ".."))
	if _, err := os.Stat(filepath.Join(coreRoot, "go.mod")); err != nil {
		t.Skipf("parent core go.mod not present at %s: %v", coreRoot, err)
	}
	lines := runGoListIn(t, coreRoot, "-f", `{{.ImportPath}}|{{join .Imports ","}},{{join .TestImports ","}},{{join .XTestImports ","}}`, "./...")
	var violations []string
	for _, line := range lines {
		pkgPath, rest, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		for _, imp := range strings.Split(rest, ",") {
			imp = strings.TrimSpace(imp)
			if imp == grainplc.ModulePath || strings.HasPrefix(imp, grainplc.ModulePath+"/") {
				violations = append(violations, pkgPath+" -> "+imp)
			}
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("parent core module imports grain-plc:\n\t%s", strings.Join(violations, "\n\t"))
	}
}
