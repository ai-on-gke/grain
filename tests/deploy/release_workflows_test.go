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

package deploy

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

func TestAgentCommitStandardsDocumented(t *testing.T) {
	promptDoc := read(t, "PROMPT.md")
	settingsDoc := read(t, ".grain", "settings.yml")

	for _, expected := range []string{"fix:", "feat:", "refactor:", "test:", "chore:", "docs:", "Fixes:", "Backported-from:"} {
		if !strings.Contains(promptDoc, expected) {
			t.Errorf("PROMPT.md missing standard convention %q", expected)
		}
		if !strings.Contains(settingsDoc, expected) {
			t.Errorf(".grain/settings.yml missing standard convention %q", expected)
		}
	}
}

func TestScheduledDeclarationsTargetMain(t *testing.T) {
	root := repoRoot(t)
	schedulesDir := filepath.Join(root, ".grain", "schedules")

	// Every change lands on main (docs/design/release-from-trunk.md,
	// RFT-1): these schedules used to run on candidate.
	for _, filename := range []string{
		"nightly-features-test-plan.yml",
		"prune-packages.yml",
		"release-watchdog.yml",
		"staging-state-cleanup.yml",
		"staging-ui-exploration.yml",
		"ui-walkthrough.yml",
	} {
		path := filepath.Join(schedulesDir, filename)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("Failed to read schedule %s: %v", filename, err)
		}
		content := string(data)
		if !strings.Contains(content, "base: main") {
			t.Errorf("Schedule %s expected to target 'base: main', content:\n%s", filename, content)
		}
	}

	// The legacy cut and the candidate's duplicate workflow schedules are
	// gone, and nothing else is scheduled onto candidate.
	for _, gone := range []string{
		"release-candidate.yml", "nightly-candidate.yml", "gcp-smoke-candidate.yml", "live-agent-candidate.yml",
		"live-frameworks-candidate.yml", "live-secrets-repo-candidate.yml", "post-reset-e2e-candidate.yml",
	} {
		if _, err := os.Stat(filepath.Join(schedulesDir, gone)); err == nil {
			t.Errorf(".grain/schedules/%s still exists", gone)
		}
	}
	entries, err := os.ReadDir(schedulesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(schedulesDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if regexp.MustCompile(`(?m)^base: candidate$`).Match(data) {
			t.Errorf(".grain/schedules/%s still targets candidate", e.Name())
		}
	}
}

func TestDeepRCQualificationTemplates(t *testing.T) {
	root := repoRoot(t)
	templatesDir := filepath.Join(root, ".grain", "templates")

	expectedTemplates := []string{
		"qualification-base-triage.yml",
		"qualification-mergequeue.yml",
		"qualification-ci-diagnostics.yml",
		"qualification-system-smoke.yml",
	}

	for _, filename := range expectedTemplates {
		path := filepath.Join(templatesDir, filename)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("Qualification template %s not found: %v", filename, err)
		}
		content := string(data)
		// candidate is a pointer the release train force-moves and takes no
		// pull request, so qualification runs on main and keeps its
		// deliberate failures on a scratch branch.
		if !regexp.MustCompile(`(?m)^base: main$`).MatchString(content) {
			t.Errorf("Qualification template %s expected to declare 'base: main', content:\n%s", filename, content)
		}
		if !strings.Contains(content, "qualification/<this task id>") && filename != "qualification-system-smoke.yml" {
			t.Errorf("Qualification template %s expected to stage its experiments on a qualification/<this task id> scratch branch", filename)
		}
		if !strings.Contains(content, "auto-merge: true") {
			t.Errorf("Qualification template %s expected to declare 'auto-merge: true', content:\n%s", filename, content)
		}
	}

	// A run may push only its own task branch unless its template says
	// otherwise (docs/declarations.md, "Refs beyond the task's branch"),
	// so the three templates that create and delete a scratch branch
	// declare exactly that: qualification/* branches, created and
	// deleted, never force-moved. system-smoke stages nothing and pushes
	// nothing beyond its own branch.
	decl, err := repodecl.Load(os.DirFS(root), model.GrainSourceRepo())
	if err != nil {
		t.Fatalf("repodecl.Load: %v", err)
	}
	scratch := model.RefPushSet{{Pattern: "refs/heads/qualification/*", Delete: true}}
	wantPush := map[string]model.RefPushSet{
		"qualification-base-triage":    scratch,
		"qualification-mergequeue":     scratch,
		"qualification-ci-diagnostics": scratch,
		"qualification-system-smoke":   nil,
	}
	for _, tf := range decl.Templates {
		stem := strings.TrimSuffix(filepath.Base(tf.Path), ".yml")
		want, ok := wantPush[stem]
		if !ok {
			continue
		}
		delete(wantPush, stem)
		if !reflect.DeepEqual(tf.Push, want) {
			t.Errorf("%s declares push %v, want %v", tf.Path, tf.Push, want)
		}
		for _, ref := range []string{"refs/heads/main", "refs/heads/candidate"} {
			if matched, _, _ := tf.Push.Allows(ref); matched {
				t.Errorf("%s may push %s", tf.Path, ref)
			}
		}
	}
	for stem := range wantPush {
		t.Errorf("template %s not declared", stem)
	}
}

// TestNoTemplateTargetsCandidate: no template may open a pull request
// against candidate, which refuses them.
func TestNoTemplateTargetsCandidate(t *testing.T) {
	templatesDir := filepath.Join(repoRoot(t), ".grain", "templates")
	entries, err := os.ReadDir(templatesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(templatesDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if regexp.MustCompile(`(?m)^base: candidate$`).Match(data) {
			t.Errorf(".grain/templates/%s still targets candidate", e.Name())
		}
	}
}

func TestBranchSpecificSuitesSplit(t *testing.T) {
	root := repoRoot(t)
	suitesDir := filepath.Join(root, ".grain", "suites")

	candidateSuites := map[string][]string{
		"release-ui-walkthrough.yml": {
			"ui-walkthrough-tasks",
			"ui-walkthrough-boards",
			"ui-walkthrough-repos",
			"ui-walkthrough-automation",
			"ui-walkthrough-settings",
			"ui-walkthrough-infrastructure",
			"ui-walkthrough-operations",
			"ui-walkthrough-shell",
		},
		"release-qualification.yml": {
			"qualification-mergequeue",
			"qualification-ci-diagnostics",
			"qualification-base-triage",
			"qualification-system-smoke",
		},
	}

	for filename, expectedTemplates := range candidateSuites {
		path := filepath.Join(suitesDir, filename)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("Candidate suite %s not found: %v", filename, err)
		}
		content := string(data)
		for _, tmpl := range expectedTemplates {
			if !strings.Contains(content, tmpl) {
				t.Errorf("Candidate suite %s expected to run template %s, got:\n%s", filename, tmpl, content)
			}
		}
	}

	mainSuites := map[string][]string{
		"main-ui-smoke.yml": {
			"ui-walkthrough-tasks",
			"ui-walkthrough-shell",
			"ui-walkthrough-settings",
			"ui-walkthrough-infrastructure",
			"ui-walkthrough-boards",
		},
		"staging-ui-exploration.yml": {
			"staging-ui-exploration",
		},
	}

	for filename, expectedTemplates := range mainSuites {
		path := filepath.Join(suitesDir, filename)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("Main suite %s not found: %v", filename, err)
		}
		content := string(data)
		for _, tmpl := range expectedTemplates {
			if !strings.Contains(content, tmpl) {
				t.Errorf("Main suite %s expected to run template %s, got:\n%s", filename, tmpl, content)
			}
		}
	}
}

// TestReleaseQualification verifies the release qualification templates
// specified in docs/test-plans/2026-09-29.md §5. The cross-branch porting
// that plan also covered is retired (docs/design/release-from-trunk.md,
// RFT-1), and TestScheduledDeclarationsTargetMain checks the schedules.
func TestReleaseQualification(t *testing.T) {
	templatesDir := filepath.Join(repoRoot(t), ".grain", "templates")
	for _, filename := range []string{
		"qualification-base-triage.yml",
		"qualification-mergequeue.yml",
		"qualification-ci-diagnostics.yml",
		"qualification-system-smoke.yml",
	} {
		data, err := os.ReadFile(filepath.Join(templatesDir, filename))
		if err != nil {
			t.Fatalf("Qualification template %s not found: %v", filename, err)
		}
		if !strings.Contains(string(data), "auto-merge: true") {
			t.Errorf("Qualification template %s expected 'auto-merge: true', got:\n%s", filename, data)
		}
	}
}
