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

package hygiene_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnlyResolverReadsQAAssignment(t *testing.T) {
	cmd := exec.Command("git", "grep", "-E", "(ActiveQAAssignmentForTask|AssignOrLoadArm|qa\\.LookupFlow|checks\\.ResolveEffectiveFlow)", "--", "*.go")
	cmd.Dir = "../../"
	out, err := cmd.Output()
	if err != nil && cmd.ProcessState.ExitCode() != 1 {
		t.Fatalf("git grep failed: %v", err)
	}

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "pkg/qa/") &&
			!strings.HasPrefix(line, "pkg/model/qa") &&
			!strings.HasPrefix(line, "pkg/metrics/qa.go") &&
			!strings.HasPrefix(line, "tests/hygiene/qa_hygiene_test.go") &&
			!strings.HasPrefix(line, "pkg/daemon/mcpserver.go") {
			t.Errorf("Forbidden QA read outside pkg/qa/ and pkg/model/qa.go: %s", line)
		}
	}
}

func TestQAE2ESpecsPinDemoActor(t *testing.T) {
	matches, err := filepath.Glob("../../ui-ng/e2e/next-qa-*.spec.js")
	if err != nil {
		t.Fatalf("glob next-qa-*.spec.js: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected at least one ui-ng/e2e/next-qa-*.spec.js file")
	}
	for _, m := range matches {
		raw, err := os.ReadFile(m)
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		text := string(raw)
		if !strings.Contains(text, `demoArgs: ["-as"`) {
			t.Errorf("%s must configure test.use({ demoArgs: [\"-as\", ...] }) so QA E2E specs never leak the host OS username", filepath.Base(m))
		}
	}
}

func TestTestsWorkflowEnforcesGoVetAndMutatingRouteWithoutBypass(t *testing.T) {
	raw, err := os.ReadFile("../../.grain/workflows/tests.yml")
	if err != nil {
		t.Fatalf("read .grain/workflows/tests.yml: %v", err)
	}
	text := string(raw)
	if strings.Contains(text, "-vet=off") {
		t.Error(".grain/workflows/tests.yml must not disable go vet with -vet=off")
	}
	if !strings.Contains(text, "go vet") {
		t.Error(".grain/workflows/tests.yml must run go vet")
	}
	if !strings.Contains(text, "MutatingRoute") && !strings.Contains(text, "./pkg/ui/...") {
		t.Error(".grain/workflows/tests.yml must run pkg/ui MutatingRoute invariant tests")
	}
}
