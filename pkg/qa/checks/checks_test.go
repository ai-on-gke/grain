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

package checks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/qa"
)

func TestIntegrityGuardZeroFalsePositiveOnGoTableTestsAndHelpers(t *testing.T) {
	ctx := context.Background()

	// Consolidating 3 separate Test* functions into 1 table-driven Test* with 3 cases
	// and delegating assertions to a local helper `assertSum(t, got, want)` MUST PASS (0% FP).
	baseTest := `package calc
import "testing"
func TestAddPos(t *testing.T) { if Add(1, 2) != 3 { t.Fatal("bad") } }
func TestAddZero(t *testing.T) { if Add(0, 0) != 0 { t.Fatal("bad") } }
func TestAddNeg(t *testing.T) { if Add(-1, -2) != -3 { t.Fatal("bad") } }
`
	headTest := `package calc
import "testing"

func TestAddTable(t *testing.T) {
	cases := []struct {
		name string
		a, b int
		want int
	}{
		{name: "pos", a: 1, b: 2, want: 3},
		{name: "zero", a: 0, b: 0, want: 0},
		{name: "neg", a: -1, b: -2, want: -3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSum(t, Add(tc.a, tc.b), tc.want)
		})
	}
}

func assertSum(t *testing.T, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
}
`
	diff := `diff --git a/pkg/calc/add.go b/pkg/calc/add.go
--- a/pkg/calc/add.go
+++ b/pkg/calc/add.go
@@ -1,3 +1,3 @@
 package calc
-func Add(a, b int) int { return a + b }
+func Add(a, b int) int { return b + a }
diff --git a/pkg/calc/add_test.go b/pkg/calc/add_test.go
--- a/pkg/calc/add_test.go
+++ b/pkg/calc/add_test.go
@@ -1,5 +1,24 @@
+package calc
`
	ws := &InMemoryWorkspace{
		Diff: diff,
		BaseFiles: map[string]string{
			"pkg/calc/add.go":      "package calc\nfunc Add(a, b int) int { return a + b }\n",
			"pkg/calc/add_test.go": baseTest,
		},
		HeadFiles: map[string]string{
			"pkg/calc/add.go":      "package calc\nfunc Add(a, b int) int { return b + a }\n",
			"pkg/calc/add_test.go": headTest,
		},
	}
	findings, err := CheckTestIntegrity(ctx, ws, ParseUnifiedDiff(diff))
	if err != nil {
		t.Fatalf("CheckTestIntegrity: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected 0 false positives on consolidated table test + helper, got %+v", findings)
	}
}

func TestIntegrityGuardCatchesTamperingEmptyTestsAndSkips(t *testing.T) {
	ctx := context.Background()

	// 1. Deleted test file without replacement + 2. Unannotated t.Skip + 3. Zero-assertion Go test + 4. Zero-assertion TS spec.
	headGoTest := `package calc
import "testing"

func TestFakeNoAssertions(t *testing.T) {
	_ = Add(2, 3)
}

func TestSkippedWithoutIssue(t *testing.T) {
	t.Skip("flaky right now")
	if Add(1, 1) != 2 {
		t.Fatal("math broke")
	}
}
`
	headTSSpec := `import { describe, it } from "vitest";
describe("Widget", () => {
  it("pretends to test widget", () => {
    const items = [1, 2];
    items.push(3);
  });
});
`
	diff := `diff --git a/pkg/calc/old_test.go b/pkg/calc/old_test.go
deleted file mode 100644
--- a/pkg/calc/old_test.go
+++ /dev/null
@@ -1,3 +0,0 @@
-package calc
-import "testing"
-func TestOld(t *testing.T) { t.Fatal("x") }
diff --git a/pkg/other/add_test.go b/pkg/other/add_test.go
new file mode 100644
--- /dev/null
+++ b/pkg/other/add_test.go
@@ -0,0 +1,12 @@
+package calc
+import "testing"
+
+func TestFakeNoAssertions(t *testing.T) {
+	_ = Add(2, 3)
+}
+
+func TestSkippedWithoutIssue(t *testing.T) {
+	t.Skip("flaky right now")
+	if Add(1, 1) != 2 {
+		t.Fatal("math broke")
+	}
+}
diff --git a/ui-ng/src/app/widget/widget.spec.ts b/ui-ng/src/app/widget/widget.spec.ts
new file mode 100644
--- /dev/null
+++ b/ui-ng/src/app/widget/widget.spec.ts
@@ -0,0 +1,7 @@
+import { describe, it } from "vitest";
+describe("Widget", () => {
+  it("pretends to test widget", () => {
+    const items = [1, 2];
+    items.push(3);
+  });
+});
`
	ws := &InMemoryWorkspace{
		Diff: diff,
		BaseFiles: map[string]string{
			"pkg/calc/old_test.go": "package calc\nimport \"testing\"\nfunc TestOld(t *testing.T) { t.Fatal(\"x\") }\n",
		},
		HeadFiles: map[string]string{
			"pkg/other/add_test.go":               headGoTest,
			"ui-ng/src/app/widget/widget.spec.ts": headTSSpec,
		},
	}

	findings, err := CheckTestIntegrity(ctx, ws, ParseUnifiedDiff(diff))
	if err != nil {
		t.Fatalf("CheckTestIntegrity: %v", err)
	}
	if len(findings) < 4 {
		t.Fatalf("expected >= 4 findings (deleted test, unannotated skip, empty Go test, empty TS spec), got %d: %+v", len(findings), findings)
	}
}

func TestStrictConfigDecoderPairedPositiveAndNegative(t *testing.T) {
	ctx := context.Background()

	badCode := `package staterepo
import (
	"bytes"
	"gopkg.in/yaml.v3"
)
type Decl struct{ Name string }
func LoadDecl(raw []byte) error {
	var out Decl
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	return dec.Decode(&out)
}
`
	goodCode := `package staterepo
import (
	"bytes"
	"gopkg.in/yaml.v3"
)
type Decl struct{ Name string }
func LoadDecl(raw []byte) error {
	var out Decl
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	return dec.Decode(&out)
}
`
	diff := `diff --git a/pkg/staterepo/decl.go b/pkg/staterepo/decl.go
new file mode 100644
--- /dev/null
+++ b/pkg/staterepo/decl.go
@@ -0,0 +1,10 @@
+package staterepo
`
	badWS := &InMemoryWorkspace{
		Diff:      diff,
		HeadFiles: map[string]string{"pkg/staterepo/decl.go": badCode},
	}
	badFindings, err := CheckStrictConfigDecoder(ctx, badWS, ParseUnifiedDiff(diff), nil)
	if err != nil {
		t.Fatalf("CheckStrictConfigDecoder(bad): %v", err)
	}
	if len(badFindings) != 1 || !strings.Contains(badFindings[0].Summary, "KnownFields(true)") {
		t.Fatalf("expected 1 strict-config-decoder finding on missing KnownFields(true), got %+v", badFindings)
	}

	goodWS := &InMemoryWorkspace{
		Diff:      diff,
		HeadFiles: map[string]string{"pkg/staterepo/decl.go": goodCode},
	}
	goodFindings, err := CheckStrictConfigDecoder(ctx, goodWS, ParseUnifiedDiff(diff), nil)
	if err != nil {
		t.Fatalf("CheckStrictConfigDecoder(good): %v", err)
	}
	if len(goodFindings) != 0 {
		t.Fatalf("expected 0 findings when KnownFields(true) is called, got %+v", goodFindings)
	}
}

func TestSiblingExportHazardBlocksOnAttempt1AndClearsOnCallerNotesOrUpdate(t *testing.T) {
	ctx := context.Background()

	baseAPI := `package store
type Proposal struct {
	Title string
}
func RelayProposal(p Proposal) error { return nil }
`
	headAPI := `package store
type Proposal struct {
	Title string
}
func RelayProposal(p Proposal, strict bool) error { return nil }
`
	diff := `diff --git a/pkg/store/proposal.go b/pkg/store/proposal.go
--- a/pkg/store/proposal.go
+++ b/pkg/store/proposal.go
@@ -4,1 +4,1 @@
-func RelayProposal(p Proposal) error { return nil }
+func RelayProposal(p Proposal, strict bool) error { return nil }
`
	ws := &InMemoryWorkspace{
		Diff:      diff,
		BaseFiles: map[string]string{"pkg/store/proposal.go": baseAPI},
		HeadFiles: map[string]string{
			"pkg/store/proposal.go":    headAPI,
			"pkg/finish/relay.go":      "package finish\nimport \"example/pkg/store\"\nfunc run() { _ = store.RelayProposal(store.Proposal{}) }\n",
			"pkg/daemon/controller.go": "package daemon\nimport \"example/pkg/store\"\nfunc sync() { _ = store.RelayProposal(store.Proposal{}) }\n",
		},
	}
	pd := ParseUnifiedDiff(diff)

	// Attempt 1 without caller_notes -> blocks with <hazard_card> and reports 2 sibling packages.
	f1, hz, pkgCnt, err := CheckSiblingExportHazard(ctx, ws, pd, 1, "")
	if err != nil {
		t.Fatalf("CheckSiblingExportHazard(attempt 1): %v", err)
	}
	if len(f1) != 1 || pkgCnt != 2 || len(hz) != 2 {
		t.Fatalf("attempt 1 = (%d findings, %d pkgs, %d hz), want (1, 2, 2)", len(f1), pkgCnt, len(hz))
	}
	if !strings.Contains(f1[0].Evidence, "<hazard_card") {
		t.Fatalf("expected <hazard_card> in evidence, got %q", f1[0].Evidence)
	}

	// Attempt 1 WITH non-empty DB-only caller_notes -> clears cleanly!
	fWaived, _, pkgCnt2, err := CheckSiblingExportHazard(ctx, ws, pd, 1, "Callers updated in stacked child task")
	if err != nil {
		t.Fatalf("CheckSiblingExportHazard(waived): %v", err)
	}
	if len(fWaived) != 0 || pkgCnt2 != 2 {
		t.Fatalf("expected 0 findings with caller_notes while preserving pkgCnt=2, got findings=%+v pkgCnt=%d", fWaived, pkgCnt2)
	}
}

func TestUIBudgetRatchetAppComponentGuardAndSpecFloor(t *testing.T) {
	ctx := context.Background()

	angularJSON := `{
		"projects": {
			"ui-ng": {
				"architect": {
					"build": {
						"configurations": {
							"production": {
								"budgets": [
									{"type": "anyComponentStyle", "maximumWarning": "8kB"}
								]
							}
						}
					}
				}
			}
		}
	}`
	// 1. Legacy oversized CSS that SHRINKS (9000 -> 8500 bytes) must NOT block (ratchet!).
	shrunkBaseCSS := strings.Repeat(".a{color:red}\n", 650) // 9100 bytes
	shrunkHeadCSS := strings.Repeat(".a{color:red}\n", 610) // 8540 bytes
	// 2. Growing CSS above 8000 bytes (7500 -> 8300 bytes) MUST block!
	grownBaseCSS := strings.Repeat(".b{color:blue}\n", 500) // 7500 bytes
	grownHeadCSS := strings.Repeat(".b{color:blue}\n", 560) // 8400 bytes

	diff := `diff --git a/ui-ng/src/app/legacy/legacy.css b/ui-ng/src/app/legacy/legacy.css
--- a/ui-ng/src/app/legacy/legacy.css
+++ b/ui-ng/src/app/legacy/legacy.css
@@ -1,1 +1,1 @@
+.a{color:red}
diff --git a/ui-ng/src/app/bloated/bloated.css b/ui-ng/src/app/bloated/bloated.css
--- a/ui-ng/src/app/bloated/bloated.css
+++ b/ui-ng/src/app/bloated/bloated.css
@@ -1,1 +1,1 @@
+.b{color:blue}
diff --git a/ui-ng/src/app/app.component.ts b/ui-ng/src/app/app.component.ts
--- a/ui-ng/src/app/app.component.ts
+++ b/ui-ng/src/app/app.component.ts
@@ -10,0 +11,1 @@
+import { TelemetryPageComponent } from "./telemetry/telemetry-page.component";
diff --git a/ui-ng/src/app/untested/untested.component.ts b/ui-ng/src/app/untested/untested.component.ts
new file mode 100644
--- /dev/null
+++ b/ui-ng/src/app/untested/untested.component.ts
@@ -0,0 +1,2 @@
+export class UntestedComponent {}
`
	ws := &InMemoryWorkspace{
		Diff: diff,
		BaseFiles: map[string]string{
			"ui-ng/src/app/legacy/legacy.css":   shrunkBaseCSS,
			"ui-ng/src/app/bloated/bloated.css": grownBaseCSS,
		},
		HeadFiles: map[string]string{
			"ui-ng/angular.json":                           angularJSON,
			"ui-ng/src/app/legacy/legacy.css":              shrunkHeadCSS,
			"ui-ng/src/app/bloated/bloated.css":            grownHeadCSS,
			"ui-ng/src/app/app.component.ts":               `import { TelemetryPageComponent } from "./telemetry/telemetry-page.component";`,
			"ui-ng/src/app/untested/untested.component.ts": `export class UntestedComponent {}`,
		},
	}

	findings, err := CheckUIBudgetAndSpecFloor(ctx, ws, ParseUnifiedDiff(diff))
	if err != nil {
		t.Fatalf("CheckUIBudgetAndSpecFloor: %v", err)
	}
	if len(findings) != 3 {
		t.Fatalf("expected 3 findings (bloated.css, app.component.ts eager import, untested.component.ts missing spec; NOT shrunk legacy.css), got %d: %+v", len(findings), findings)
	}
	for _, f := range findings {
		if strings.Contains(f.FilePath, "legacy.css") {
			t.Fatalf("shrinking legacy.css must not be blocked by ratchet: %+v", f)
		}
	}
}

func TestRunS3OverRealGitMirrorAndProfiler(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	originDir := filepath.Join(tmp, "origin.git")
	workDir := filepath.Join(tmp, "work")

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "safe.bareRepository=all"}, args...)...)
		if dir != "" {
			cmd.Dir = dir
		}
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}

	runGit("", "init", "--bare", "--initial-branch=main", originDir)
	runGit("", "clone", originDir, workDir)
	if err := os.MkdirAll(filepath.Join(workDir, "pkg/cfg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "pkg/cfg/load.go"), []byte("package cfg\nfunc Name() string { return \"v1\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(workDir, "add", ".")
	runGit(workDir, "commit", "-m", "initial commit")
	runGit(workDir, "push", "origin", "main")

	runGit(workDir, "checkout", "-b", "grain-qa-stage/task-42")
	badLoader := `package cfg
import (
	"bytes"
	"gopkg.in/yaml.v3"
)
type Settings struct{ Name string }
func Load(raw []byte) error {
	var m Settings
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	return dec.Decode(&m)
}
`
	if err := os.WriteFile(filepath.Join(workDir, "pkg/cfg/load.go"), []byte(badLoader), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(workDir, "add", ".")
	runGit(workDir, "commit", "-m", "add loader")
	runGit(workDir, "push", "origin", "grain-qa-stage/task-42")

	mirror, err := OpenMirrorWorkspace(ctx, filepath.Join(tmp, "cache"), originDir, "", "main", "grain-qa-stage/task-42")
	if err != nil {
		t.Fatalf("OpenMirrorWorkspace: %v", err)
	}

	perfLog := filepath.Join(tmp, "perf", "qa-checks.jsonl")
	prof := qa.NewProfiler(perfLog)
	flow, _ := qa.LookupFlow(model.QAFlowGoldenV1Minimal)

	rep, err := RunS3(ctx, mirror, flow, RunOptions{
		TaskID:  "task-42",
		RunID:   "run-1",
		Arm:     model.QAArmTreatment,
		Attempt: 1,
	}, prof)
	if err != nil {
		t.Fatalf("RunS3: %v", err)
	}
	if rep.Passed || len(rep.Findings) != 1 || rep.Findings[0].RuleID != qa.RuleStrictConfigDecoder {
		t.Fatalf("expected RunS3 to block on missing KnownFields(true), got %+v", rep)
	}
	perfBytes, err := os.ReadFile(perfLog)
	if err != nil || !strings.Contains(string(perfBytes), `"checkId":"s3-suite"`) {
		t.Fatalf("expected profiler JSONL output, got err=%v content=%q", err, string(perfBytes))
	}
}

func TestRound2HardeningRegressions(t *testing.T) {
	ctx := context.Background()

	// 1. cleanRelPath rejects absolute and traversal paths.
	if got := cleanRelPath("/etc/passwd"); got != "" {
		t.Fatalf("cleanRelPath(/etc/passwd) = %q, want empty", got)
	}
	if got := cleanRelPath("../../etc/passwd"); got != "" {
		t.Fatalf("cleanRelPath(../../etc/passwd) = %q, want empty", got)
	}

	// 2. ParseRepoFlowYAML enforces tighten-only floors, and ResolveEffectiveFlow reads from BaseFiles.
	if _, err := qa.ParseRepoFlowYAML([]byte("disable-rules:\n  - golden:test-integrity-guard\n")); err == nil {
		t.Fatal("expected ParseRepoFlowYAML to reject disabling golden:test-integrity-guard")
	}
	if _, err := qa.ParseRepoFlowYAML([]byte("disable-rules:\n  - golden:strict-config-decoder\n")); err != nil {
		t.Fatalf("expected ParseRepoFlowYAML to allow disabling repo-opinionated golden:strict-config-decoder, got %v", err)
	}
	if _, err := qa.ParseRepoFlowYAML([]byte("enable-s4: false\n")); err == nil {
		t.Fatal("expected ParseRepoFlowYAML to reject disabling enable-s4")
	}
	flowWS := &InMemoryWorkspace{
		BaseFiles: map[string]string{
			".grain/qa-flow.yml": "strict-decoder-paths:\n  - custom/cfg\n",
		},
		HeadFiles: map[string]string{
			".grain/qa-flow.yml": "disable-rules:\n  - golden:sibling-export-hazard\n",
		},
	}
	effFlow, err := ResolveEffectiveFlow(ctx, flowWS, model.QAFlowRepoCustom)
	if err != nil {
		t.Fatalf("ResolveEffectiveFlow: %v", err)
	}
	if !effFlow.HasRule(qa.RuleSiblingExportHazard) || len(effFlow.StrictDecoderPaths) != 1 {
		t.Fatalf("expected ResolveEffectiveFlow to read BaseFiles (not HeadFiles), got %+v", effFlow)
	}

	// 3. CheckTestIntegrity catches non-asserting setupHelper(t), supports var/map table tests, and handles JS template braces.
	goTestHead := `package foo
import "testing"
func setupEnv(t *testing.T) { _ = t.TempDir() }
func TestFakePassViaSetupHelper(t *testing.T) {
	setupEnv(t)
}
func TestMapTable(t *testing.T) {
	var cases = map[string]int{"a": 1, "b": 2, "c": 3}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			if want <= 0 { t.Fatalf("bad want %d", want) }
		})
	}
}
`
	jsSpecHead := "it('handles brace strings', () => {\n  const tpl = `unbalanced } brace`;\n  expect(tpl.length).toBeGreaterThan(0);\n});\n"
	tiDiff := `diff --git a/pkg/foo/foo_test.go b/pkg/foo/foo_test.go
new file mode 100644
--- /dev/null
+++ b/pkg/foo/foo_test.go
@@ -0,0 +1,14 @@
+func TestFakePassViaSetupHelper(t *testing.T) {}
diff --git a/ui-ng/src/app/foo.spec.ts b/ui-ng/src/app/foo.spec.ts
new file mode 100644
--- /dev/null
+++ b/ui-ng/src/app/foo.spec.ts
@@ -0,0 +1,4 @@
+it('handles brace strings', () => {
+  const tpl = ` + "`unbalanced } brace`" + `;
+  expect(tpl.length).toBeGreaterThan(0);
+});
`
	tiWS := &InMemoryWorkspace{
		Diff: tiDiff,
		HeadFiles: map[string]string{
			"pkg/foo/foo_test.go":       goTestHead,
			"ui-ng/src/app/foo.spec.ts": jsSpecHead,
		},
	}
	tiFindings, err := CheckTestIntegrity(ctx, tiWS, ParseUnifiedDiff(tiDiff))
	if err != nil {
		t.Fatalf("CheckTestIntegrity: %v", err)
	}
	if len(tiFindings) != 1 || !strings.Contains(tiFindings[0].Summary, "TestFakePassViaSetupHelper") {
		t.Fatalf("expected exactly 1 finding for TestFakePassViaSetupHelper (and 0 for JS brace template), got %+v", tiFindings)
	}
	if gotCases := countTestCases("pkg/foo/foo_test.go", []byte(goTestHead)); gotCases != 4 {
		t.Fatalf("countTestCases(var map[string]int with 3 entries + 1 plain test) = %d, want 4", gotCases)
	}

	// 4. CheckStrictConfigDecoder catches a second unstrict decoder in the same function AND yaml.Unmarshal.
	decHead := `package config
import (
	"bytes"
	"encoding/json"
	"gopkg.in/yaml.v3"
)
type Both struct{ Name string }
func LoadBoth(j, y []byte) error {
	jd := json.NewDecoder(bytes.NewReader(j))
	jd.DisallowUnknownFields()
	var a Both
	_ = jd.Decode(&a)

	yd := yaml.NewDecoder(bytes.NewReader(y))
	return yd.Decode(&a)
}
func LoadRaw(y []byte) error {
	var a map[string]Both
	return yaml.Unmarshal(y, &a)
}
`
	decDiff := `diff --git a/pkg/config/both.go b/pkg/config/both.go
new file mode 100644
--- /dev/null
+++ b/pkg/config/both.go
@@ -0,0 +1,20 @@
+package config
`
	decWS := &InMemoryWorkspace{
		Diff:      decDiff,
		HeadFiles: map[string]string{"pkg/config/both.go": decHead},
	}
	decFindings, err := CheckStrictConfigDecoder(ctx, decWS, ParseUnifiedDiff(decDiff), nil)
	if err != nil {
		t.Fatalf("CheckStrictConfigDecoder: %v", err)
	}
	if len(decFindings) != 2 {
		t.Fatalf("expected 2 strict-decoder findings (second decoder + yaml.Unmarshal), got %+v", decFindings)
	}

	// 5. CheckSiblingExportHazard distinguishes two receiver methods with the same name in one file.
	baseMethods := `package model
type Alpha struct{}
func (a *Alpha) Validate() error { return nil }
type Beta struct{}
func (b *Beta) Validate() error { return nil }
`
	headMethods := `package model
type Alpha struct{}
func (a *Alpha) Validate(strict bool) error { return nil }
type Beta struct{}
func (b *Beta) Validate() error { return nil }
`
	methDiff := `diff --git a/pkg/model/types.go b/pkg/model/types.go
--- a/pkg/model/types.go
+++ b/pkg/model/types.go
@@ -3,1 +3,1 @@
-func (a *Alpha) Validate() error { return nil }
+func (a *Alpha) Validate(strict bool) error { return nil }
`
	methWS := &InMemoryWorkspace{
		Diff:      methDiff,
		BaseFiles: map[string]string{"pkg/model/types.go": baseMethods},
		HeadFiles: map[string]string{
			"pkg/model/types.go": headMethods,
			"pkg/api/handler.go": "package api\nimport \"example/pkg/model\"\nfunc Check(a *model.Alpha) error { return a.Validate() }\n",
		},
	}
	methFindings, _, _, err := CheckSiblingExportHazard(ctx, methWS, ParseUnifiedDiff(methDiff), 1, "")
	if err != nil {
		t.Fatalf("CheckSiblingExportHazard(methods): %v", err)
	}
	if len(methFindings) != 1 || !strings.Contains(methFindings[0].Summary, "Alpha.Validate") {
		t.Fatalf("expected Alpha.Validate hazard finding despite unchanged Beta.Validate, got %+v", methFindings)
	}
}

func TestRunnerRespectsDisabledNonFloorCheck(t *testing.T) {
	ws := &InMemoryWorkspace{Diff: "empty"}
	profiler := qa.NewProfiler("")
	flow := qa.FlowSpec{
		Rules: []qa.RuleSpec{
			{ID: qa.RuleTestIntegrityGuard, Enabled: true},
			{ID: qa.RuleSiblingExportHazard, Enabled: false},
		},
	}
	opts := RunOptions{TaskID: "1", RunID: "2", Arm: "treatment", Attempt: 1}

	report, err := RunS3(context.Background(), ws, flow, opts, profiler)
	if err != nil {
		t.Fatalf("RunS3: %v", err)
	}
	for _, res := range report.Results {
		if res.CheckID == qa.RuleSiblingExportHazard {
			t.Fatalf("RuleSiblingExportHazard was executed despite being disabled")
		}
	}
}

func TestTestIntegrityGuardPlaywrightDestructuredAndTaggedFixtures(t *testing.T) {
	specCode := `import { test, expect } from "@playwright/test";

test("destructured fixture passes with expect in body", async ({ page, request }) => {
  await page.goto("/next/context");
  await expect(page.locator("h1")).toBeVisible();
  expect(request).toBeTruthy();
});

test("tagged fixture passes with expect in body", { tag: "@smoke" }, async ({ page }: { page: unknown }) => {
  await expect(page as never).toBeDefined();
});

test("destructured fixture with zero assertions still fails", async ({ page }) => {
  await page.goto("/next/context");
});
`
	var diffLines []string
	diffLines = append(diffLines,
		"diff --git a/ui-ng/e2e/context.spec.ts b/ui-ng/e2e/context.spec.ts",
		"new file mode 100644",
		"--- /dev/null",
		"+++ b/ui-ng/e2e/context.spec.ts",
		"@@ -0,0 +1,15 @@",
	)
	for _, ln := range strings.Split(strings.TrimSuffix(specCode, "\n"), "\n") {
		diffLines = append(diffLines, "+"+ln)
	}
	diff := strings.Join(diffLines, "\n") + "\n"
	ws := &InMemoryWorkspace{
		Diff:      diff,
		HeadFiles: map[string]string{"ui-ng/e2e/context.spec.ts": specCode},
	}
	findings, err := CheckTestIntegrity(context.Background(), ws, ParseUnifiedDiff(diff))
	if err != nil {
		t.Fatalf("CheckTestIntegrity: %v", err)
	}
	if len(findings) != 1 || !strings.Contains(findings[0].Summary, "zero assertions") {
		t.Fatalf("expected exactly 1 finding on the assertion-free Playwright test, got %d: %+v", len(findings), findings)
	}
}

func TestOpenMirrorWorkspaceResolvesMergeBaseWithoutRESTAndNeverFallsBackToRoot(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	originDir := filepath.Join(tmp, "origin.git")
	workDir := filepath.Join(tmp, "work")

	runGit := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		if dir != "" {
			cmd.Dir = dir
		}
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=tester",
			"GIT_AUTHOR_EMAIL=tester@example.com",
			"GIT_COMMITTER_NAME=tester",
			"GIT_COMMITTER_EMAIL=tester@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	runGit("", "init", "--bare", "--initial-branch=main", originDir)
	runGit("", "clone", originDir, workDir)

	// Commit 1 (initial root commit).
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(workDir, "add", ".")
	runGit(workDir, "commit", "-m", "initial commit")
	rootSHA := runGit(workDir, "rev-parse", "HEAD")

	// Commit 2 (pre-existing repo files on main, including code that would trip
	// repo-opinionated rules if evaluated from the initial commit).
	if err := os.MkdirAll(filepath.Join(workDir, "pkg/existing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "pkg/existing/big.go"), []byte("package existing\n// pre-existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(workDir, "add", ".")
	runGit(workDir, "commit", "-m", "baseline files on main")
	mainBaseSHA := runGit(workDir, "rev-parse", "HEAD")
	runGit(workDir, "push", "origin", "main")

	// Branch grain-qa-stage/task-10 cleanly from main and touch only 1 file.
	runGit(workDir, "checkout", "-b", "grain-qa-stage/task-10")
	if err := os.MkdirAll(filepath.Join(workDir, "pkg/feature"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "pkg/feature/hello.go"), []byte("package feature\nfunc Hello() string { return \"hi\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(workDir, "add", ".")
	runGit(workDir, "commit", "-m", "add feature")
	runGit(workDir, "push", "origin", "grain-qa-stage/task-10")

	// Advance main by one unrelated commit to ensure merge-base (not main tip) is computed.
	runGit(workDir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(workDir, "CHANGELOG.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(workDir, "add", ".")
	runGit(workDir, "commit", "-m", "advance main")
	runGit(workDir, "push", "origin", "main")

	// 1. Empty baseBranch (typical task.Base == "", zero REST lookup):
	// must resolve remote HEAD -> origin/main and compute merge-base == mainBaseSHA, NEVER rootSHA.
	mirror, err := OpenMirrorWorkspace(ctx, filepath.Join(tmp, "cache"), originDir, "", "", "grain-qa-stage/task-10")
	if err != nil {
		t.Fatalf("OpenMirrorWorkspace with empty baseBranch: %v", err)
	}
	if mirror.BaseSHA == rootSHA {
		t.Fatalf("mirror.BaseSHA fell back to initial/root commit %s; want merge-base %s", rootSHA, mainBaseSHA)
	}
	if mirror.BaseSHA != mainBaseSHA {
		t.Fatalf("mirror.BaseSHA = %s, want merge-base %s", mirror.BaseSHA, mainBaseSHA)
	}
	diff, err := mirror.BaseHeadDiff(ctx)
	if err != nil {
		t.Fatalf("BaseHeadDiff: %v", err)
	}
	pd := ParseUnifiedDiff(diff)
	if paths := pd.AllPaths(); len(paths) != 1 || paths[0] != "pkg/feature/hello.go" {
		t.Fatalf("expected only [pkg/feature/hello.go] in diff, got %v", paths)
	}

	// 2. Non-existent base branch must return ErrBaseResolutionFailed instead of falling back to rootSHA.
	if _, err := OpenMirrorWorkspace(ctx, filepath.Join(tmp, "cache"), originDir, "", "nonexistent-base", "grain-qa-stage/task-10"); !errors.Is(err, ErrBaseResolutionFailed) {
		t.Fatalf("expected ErrBaseResolutionFailed for missing base branch, got %v", err)
	}

	// 3. Unrelated orphan branch with no common ancestor must return ErrBaseResolutionFailed instead of root commit.
	runGit(workDir, "checkout", "--orphan", "grain-qa-stage/task-orphan")
	runGit(workDir, "rm", "-rf", ".")
	if err := os.WriteFile(filepath.Join(workDir, "orphan.txt"), []byte("orphan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(workDir, "add", ".")
	runGit(workDir, "commit", "-m", "orphan commit")
	runGit(workDir, "push", "origin", "grain-qa-stage/task-orphan")
	if _, err := OpenMirrorWorkspace(ctx, filepath.Join(tmp, "cache"), originDir, "", "", "grain-qa-stage/task-orphan"); !errors.Is(err, ErrBaseResolutionFailed) {
		t.Fatalf("expected ErrBaseResolutionFailed for unrelated orphan history, got %v", err)
	}

	// 4. SandboxWorkspace must also never fall back to root commit when no base ref exists.
	orphanClone := filepath.Join(tmp, "orphan-clone")
	runGit("", "clone", "--single-branch", "--branch", "grain-qa-stage/task-orphan", originDir, orphanClone)
	sw := &SandboxWorkspace{
		CheckoutDir: orphanClone,
		BaseBranch:  "nonexistent-base",
		RunCommand: func(c context.Context, command string) (string, bool, error) {
			cmd := exec.CommandContext(c, "sh", "-c", command)
			out, runErr := cmd.CombinedOutput()
			return string(out), runErr != nil, nil
		},
	}
	if _, err := sw.BaseSHA(ctx); !errors.Is(err, ErrBaseResolutionFailed) {
		t.Fatalf("SandboxWorkspace.BaseSHA expected ErrBaseResolutionFailed, got %v", err)
	}
}

func TestStandardWorkflowSafeOnGenericGoAndAngularRepo(t *testing.T) {
	ctx := context.Background()

	// Construct a diff on a generic Go + Angular repository that intentionally violates
	// Grain's repo-specific conventions (non-strict JSON decoder, >8kB Angular component CSS,
	// eager app.component.ts import, component without .spec.ts) while keeping tests clean.
	bigCSS := strings.Repeat(".panel { color: inherit; }\n", 350) // > 8000 bytes
	goLoader := `package cfg
import (
	"bytes"
	"encoding/json"
)
type AppConfig struct {
	Port int ` + "`json:\"port\"`" + `
}
func Load(raw []byte) (AppConfig, error) {
	var c AppConfig
	err := json.NewDecoder(bytes.NewReader(raw)).Decode(&c)
	return c, err
}
`
	appComp := `import { Component } from "@angular/core";
import { StudioComponent } from "./studio/studio.component";
@Component({ selector: "app-root", template: "" })
export class AppComponent { readonly ref = StudioComponent; }
`
	studioComp := `import { Component } from "@angular/core";
@Component({ selector: "app-studio", template: "<div>ok</div>" })
export class StudioComponent {}
`

	diff := strings.Join([]string{
		"diff --git a/pkg/config/loader.go b/pkg/config/loader.go",
		"new file mode 100644",
		"--- /dev/null",
		"+++ b/pkg/config/loader.go",
		"@@ -0,0 +1,13 @@",
		"+" + strings.ReplaceAll(strings.TrimSuffix(goLoader, "\n"), "\n", "\n+"),
		"diff --git a/ui-ng/src/app/app.component.ts b/ui-ng/src/app/app.component.ts",
		"--- a/ui-ng/src/app/app.component.ts",
		"+++ b/ui-ng/src/app/app.component.ts",
		"@@ -1,2 +1,4 @@",
		" import { Component } from \"@angular/core\";",
		"+import { StudioComponent } from \"./studio/studio.component\";",
		" @Component({ selector: \"app-root\", template: \"\" })",
		"+export class AppComponent { readonly ref = StudioComponent; }",
		"diff --git a/ui-ng/src/app/studio/studio.component.ts b/ui-ng/src/app/studio/studio.component.ts",
		"new file mode 100644",
		"--- /dev/null",
		"+++ b/ui-ng/src/app/studio/studio.component.ts",
		"@@ -0,0 +1,3 @@",
		"+" + strings.ReplaceAll(strings.TrimSuffix(studioComp, "\n"), "\n", "\n+"),
		"diff --git a/ui-ng/src/app/studio/studio.component.css b/ui-ng/src/app/studio/studio.component.css",
		"new file mode 100644",
		"--- /dev/null",
		"+++ b/ui-ng/src/app/studio/studio.component.css",
		"@@ -0,0 +1,350 @@",
		"+" + strings.ReplaceAll(strings.TrimSuffix(bigCSS, "\n"), "\n", "\n+"),
	}, "\n") + "\n"

	ws := &InMemoryWorkspace{
		Head: "1111111111111111111111111111111111111111",
		Diff: diff,
		HeadFiles: map[string]string{
			"pkg/config/loader.go":                      goLoader,
			"ui-ng/src/app/app.component.ts":            appComp,
			"ui-ng/src/app/studio/studio.component.ts":  studioComp,
			"ui-ng/src/app/studio/studio.component.css": bigCSS,
		},
	}

	stdBundle, ok := qa.LookupBuiltInOrRegisteredVersion("standard-workflow@1")
	if !ok {
		t.Fatal("standard-workflow@1 not found")
	}
	rep, err := RunS3(ctx, ws, stdBundle.Resolved, RunOptions{
		TaskID:  "generic-1",
		Arm:     model.QAArmTreatment,
		Attempt: 1,
	}, nil)
	if err != nil {
		t.Fatalf("RunS3(standard-workflow): %v", err)
	}
	if !rep.Passed {
		t.Fatalf("standard-workflow blocked PR creation on generic Go/Angular repo conventions:\n%s", rep.FormatAgentFailure())
	}
	if len(rep.Findings) == 0 {
		t.Fatalf("expected non-blocking warn findings to still be surfaced on standard-workflow, got 0 findings")
	}
	for _, f := range rep.Findings {
		if f.Severity != "warn" {
			t.Errorf("expected finding %s to have severity=warn under default standard-workflow, got %q", f.RuleID, f.Severity)
		}
	}

	// Explicit repo opt-in to blocking strict-config-decoder & ui-budget-and-spec-floor MUST block.
	optIn := stdBundle.Resolved
	optIn.Rules = append([]qa.RuleSpec(nil), optIn.Rules...)
	for i := range optIn.Rules {
		if optIn.Rules[i].ID == qa.RuleStrictConfigDecoder || optIn.Rules[i].ID == qa.RuleUIBudgetAndSpecFloor {
			optIn.Rules[i].Severity = "blocking"
		}
	}
	repOptIn, err := RunS3(ctx, ws, optIn, RunOptions{
		TaskID:  "generic-optin",
		Arm:     model.QAArmTreatment,
		Attempt: 1,
	}, nil)
	if err != nil {
		t.Fatalf("RunS3(opt-in): %v", err)
	}
	if repOptIn.Passed {
		t.Fatal("expected explicit blocking opt-in to block PR creation")
	}
}

func TestMirrorWorkspaceRejectsInvalidBranchesAndStripsURLCredentials(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	for _, bad := range []string{"-evil", "foo:refs/heads/bar", "../escape", "bad..ref", "bad@{0}", "has space"} {
		if _, err := OpenMirrorWorkspace(ctx, tmp, "/tmp/unused.git", "", bad, "main"); err == nil {
			t.Errorf("expected OpenMirrorWorkspace to reject baseBranch %q", bad)
		}
		if _, err := OpenMirrorWorkspace(ctx, tmp, "/tmp/unused.git", "", "main", bad); err == nil {
			t.Errorf("expected OpenMirrorWorkspace to reject headBranch %q", bad)
		}
	}
	got := mirrorStableURL("https://x-access-token:ghs_supersecret@github.com/acme/widgets.git")
	if strings.Contains(got, "ghs_supersecret") || strings.Contains(got, "x-access-token") {
		t.Fatalf("mirrorStableURL leaked embedded credentials: %q", got)
	}
	if got != "https://github.com/acme/widgets.git" {
		t.Fatalf("mirrorStableURL = %q, want https://github.com/acme/widgets.git", got)
	}
	gotLocal := mirrorStableURL("http://x-access-token:secret@127.0.0.1:41234/acme/widgets.git")
	if gotLocal != "gitproxy/acme/widgets.git" {
		t.Fatalf("mirrorStableURL(local) = %q, want gitproxy/acme/widgets.git", gotLocal)
	}

	clean, auth, pass := stripURLCredentials("https://alice:topsecret_pw@github.com/acme/widgets.git", "")
	if clean != "https://github.com/acme/widgets.git" || auth == "" || pass != "topsecret_pw" {
		t.Fatalf("stripURLCredentials = (%q, %q, %q)", clean, auth, pass)
	}
	redacted := redactMirrorOutput("fatal: unable to access 'https://alice:topsecret_pw@github.com/acme/widgets.git/': topsecret_pw rejected", "", pass)
	if strings.Contains(redacted, "topsecret_pw") || strings.Contains(redacted, "alice:") {
		t.Fatalf("redactMirrorOutput leaked embedded URL secret: %q", redacted)
	}
}
