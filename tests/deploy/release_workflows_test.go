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
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
	"gopkg.in/yaml.v3"
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

	// Every change lands on main (design/design/release-from-trunk.design.md,
	// RFT-1): these schedules used to run on candidate.
	for _, filename := range []string{
		"nightly-features-test-plan.yml",
		"prune-packages.yml",
		"release-watchdog.yml",
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
		// A qualification run that opens a pull request fails Stage 2
		// (design/playbook/releases.playbook.md §2), so none auto-merges.
		if !strings.Contains(content, "auto-merge: false") {
			t.Errorf("Qualification template %s expected to declare 'auto-merge: false', content:\n%s", filename, content)
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
// that plan also covered is retired (design/design/release-from-trunk.design.md,
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
		// Stage 2 is judged objectively (design/playbook/releases.playbook.md
		// §2): a qualification run that opens a pull request fails, so none
		// of them auto-merges, every one is filed for a candidate, and every
		// one ends with the footer the reconciler reads.
		for _, want := range []string{
			"auto-merge: false",
			"- name: rc_tag", "- name: rc_sha",
			"Qualification-Verdict: PASS", "Qualified-Tag: {{rc_tag}}", "Qualified-Commit: {{rc_sha}}",
		} {
			if !strings.Contains(string(data), want) {
				t.Errorf("Qualification template %s expected %q, got:\n%s", filename, want, data)
			}
		}
		// Only {{name}} is filled in (model.SubstituteFields): a single-brace
		// {rc_tag} reaches the run verbatim.
		if m := regexp.MustCompile(`(^|[^{])\{rc_(tag|sha)\}`).Find(data); m != nil {
			t.Errorf("Qualification template %s has a placeholder grain does not fill: %q", filename, m)
		}
		// nothing_needed closes the task, and a closed item fails
		// (model.JudgeQualificationAttempt), so a template may not offer it.
		if strings.Contains(string(data), "(or `nothing_needed`)") {
			t.Errorf("Qualification template %s offers nothing_needed as a way to finish", filename)
		}
		if filename != "qualification-system-smoke.yml" &&
			!strings.Contains(string(data), "git push origin :refs/heads/qualification/<this task id>") {
			t.Errorf("Qualification template %s does not delete its scratch branch", filename)
		}
	}

	suite, err := os.ReadFile(filepath.Join(repoRoot(t), ".grain", "suites", "release-qualification.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mode: until_clean", "max-passes: 1", "auto-merge: false"} {
		if !strings.Contains(string(suite), want) {
			t.Errorf("release-qualification suite expected %q, got:\n%s", want, suite)
		}
	}
	// The reconciler files the suite's last item only after the others pass.
	if !regexp.MustCompile(`qualification-system-smoke\s*\n#`).Match(suite) &&
		!regexp.MustCompile(`- qualification-system-smoke\s*\n[a-z#]`).Match(suite) {
		t.Errorf("qualification-system-smoke must be the suite's last template, got:\n%s", suite)
	}
}

// TestQualificationTemplatesFitAutoSubmitFanOut checks that no qualification
// template asks its run to propose more scratch-branch fixtures than a single
// task may self-approve (model.DefaultAutoSubmitFanOut, task 2862). Scratch
// fixtures go through AutoSubmitLimits: a proposal past FanOut waits for a
// person with AutoSubmitLimitNote, and releasequalification's children check
// counts that unqueued task in OpenChildren, so the item cannot pass
// unattended.
func TestQualificationTemplatesFitAutoSubmitFanOut(t *testing.T) {
	decl, err := repodecl.Load(os.DirFS(repoRoot(t)), model.GrainSourceRepo())
	if err != nil {
		t.Fatalf("repodecl.Load: %v", err)
	}
	cardinal := map[string]int{
		"one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
		"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
	}
	fileCount := regexp.MustCompile(`(?i)\bfile\s+(one|two|three|four|five|six|seven|eight|nine|ten|\d+)\b[^.\n]*\bfixtures?\b`)
	gitPushScratch := regexp.MustCompile(`git push origin ([^\s` + "`" + `]*refs/heads/qualification/[^\s` + "`" + `.]*)`)
	directCommitPush := regexp.MustCompile(`(?i)\bby pushing\b[^.\n]*\bdirectly to\s+` + "`?" + `refs/heads/qualification/`)

	foundMergequeue := false
	for _, tf := range decl.Templates {
		stem := model.DeclarationStem(tf.Path)
		if !strings.HasPrefix(stem, "qualification-") {
			continue
		}
		total := 0
		for _, m := range fileCount.FindAllStringSubmatch(tf.Template.Body, -1) {
			word := strings.ToLower(m[1])
			if n, ok := cardinal[word]; ok {
				total += n
			} else if n, err := strconv.Atoi(word); err == nil {
				total += n
			}
		}
		if total > model.DefaultAutoSubmitFanOut {
			t.Errorf("%s asks its run to file %d scratch fixtures across its steps, exceeding DefaultAutoSubmitFanOut (%d)",
				tf.Path, total, model.DefaultAutoSubmitFanOut)
		}
		// gitproxy's onHistory check only allows declared pushes on non-default
		// branches to move refs onto commits the repository already has (create
		// at {{rc_sha}} and delete), never commits carried by the push.
		for _, m := range gitPushScratch.FindAllStringSubmatch(tf.Template.Body, -1) {
			switch m[1] {
			case "{{rc_sha}}:refs/heads/qualification/<this", ":refs/heads/qualification/<this":
			default:
				t.Errorf("%s instructs `git push origin %s`, which gitproxy refuses if it carries new commits", tf.Path, m[1])
			}
		}
		if directCommitPush.MatchString(tf.Template.Body) {
			t.Errorf("%s instructs pushing a commit directly to refs/heads/qualification/*, which gitproxy refuses on declared non-default branches", tf.Path)
		}
		if stem == "qualification-mergequeue" {
			foundMergequeue = true
			if total != model.DefaultAutoSubmitFanOut {
				t.Errorf("%s matched %d fixture-filing directives, want %d; check the template and test pattern",
					tf.Path, total, model.DefaultAutoSubmitFanOut)
			}
			if !strings.Contains(tf.Template.Body, "at most five") {
				t.Errorf("%s should state the five-fixture self-approval cap so runs do not file a sixth", tf.Path)
			}
		}
	}
	if !foundMergequeue {
		t.Fatal("qualification-mergequeue template not found")
	}
}

// TestReleaseWorkflowChecksReviewedMergeBeforeBuilding verifies that
// .github/workflows/release.yml runs the reviewed-merge gate in its own fast
// job before the ~40-minute image build (`images` needs `reviewed-merge`),
// prints every refused PR into $GITHUB_STEP_SUMMARY, keeps minimal read-only
// permissions on the check job, retains check_reviewed_merge in
// scripts/release-images.sh for image attestation, and points the release
// manager template at pipeline.unreviewed-merge.
func TestReleaseWorkflowChecksReviewedMergeBeforeBuilding(t *testing.T) {
	raw := read(t, ".github", "workflows", "release.yml")

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("parse release.yml: %v", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		t.Fatal("release.yml root is not a YAML mapping")
	}

	var jobsNode *yaml.Node
	rootMap := doc.Content[0]
	for i := 0; i+1 < len(rootMap.Content); i += 2 {
		if rootMap.Content[i].Value == "jobs" {
			jobsNode = rootMap.Content[i+1]
			break
		}
	}
	if jobsNode == nil || jobsNode.Kind != yaml.MappingNode {
		t.Fatal("release.yml has no jobs mapping")
	}

	var jobOrder []string
	for i := 0; i+1 < len(jobsNode.Content); i += 2 {
		jobOrder = append(jobOrder, jobsNode.Content[i].Value)
	}
	reviewedIdx, imagesIdx := -1, -1
	for i, name := range jobOrder {
		if name == "reviewed-merge" {
			reviewedIdx = i
		}
		if name == "images" {
			imagesIdx = i
		}
	}
	if reviewedIdx < 0 {
		t.Fatalf("release.yml defines no reviewed-merge job (jobs: %v)", jobOrder)
	}
	if imagesIdx < 0 {
		t.Fatalf("release.yml defines no images job (jobs: %v)", jobOrder)
	}
	if reviewedIdx >= imagesIdx {
		t.Fatalf("reviewed-merge job (index %d) must precede images job (index %d): %v", reviewedIdx, imagesIdx, jobOrder)
	}

	var wf struct {
		Jobs map[string]struct {
			If          string            `yaml:"if"`
			Needs       yaml.Node         `yaml:"needs"`
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(raw), &wf); err != nil {
		t.Fatalf("unmarshal release.yml jobs: %v", err)
	}

	rmJob := wf.Jobs["reviewed-merge"]
	wantPerms := map[string]string{"contents": "read", "pull-requests": "read"}
	if !reflect.DeepEqual(rmJob.Permissions, wantPerms) {
		t.Errorf("reviewed-merge permissions = %v, want %v (read-only, no registry or signing access)", rmJob.Permissions, wantPerms)
	}
	foundRun := false
	for _, s := range rmJob.Steps {
		if strings.Contains(s.Run, "go run ./cmd/reviewed-merge") {
			foundRun = true
			for _, want := range []string{"-all", "-branches", "-mergers", "$GITHUB_STEP_SUMMARY", "reviewed-merge: refused:"} {
				if !strings.Contains(s.Run, want) {
					t.Errorf("reviewed-merge step %q missing %q", s.Name, want)
				}
			}
			for _, envKey := range []string{"GITHUB_TOKEN", "GRAIN_MERGE_LOGINS", "REQUIRE_REVIEWED_MERGE"} {
				if strings.TrimSpace(s.Env[envKey]) == "" {
					t.Errorf("reviewed-merge step %q missing env %s", s.Name, envKey)
				}
			}
		}
	}
	if !foundRun {
		t.Error("reviewed-merge job has no step running `go run ./cmd/reviewed-merge`")
	}

	imgJob := wf.Jobs["images"]
	var imgNeeds []string
	switch imgJob.Needs.Kind {
	case yaml.ScalarNode:
		imgNeeds = []string{imgJob.Needs.Value}
	case yaml.SequenceNode:
		for _, n := range imgJob.Needs.Content {
			imgNeeds = append(imgNeeds, n.Value)
		}
	}
	hasReviewedNeed := false
	for _, need := range imgNeeds {
		if need == "reviewed-merge" {
			hasReviewedNeed = true
		}
	}
	if !hasReviewedNeed {
		t.Errorf("images job needs = %v, want reviewed-merge so image builds never start after a refusal", imgNeeds)
	}
	if !strings.Contains(imgJob.If, "needs.reviewed-merge.result == 'success'") {
		t.Errorf("images job if = %q, want guard on needs.reviewed-merge.result == 'success'", imgJob.If)
	}

	// scripts/release-images.sh must keep check_reviewed_merge for attestation.
	releaseImages := read(t, "scripts", "release-images.sh")
	for _, want := range []string{"check_reviewed_merge()", "check_reviewed_merge || return 1", "attest_reviewed_merge"} {
		if !strings.Contains(releaseImages, want) {
			t.Errorf("scripts/release-images.sh missing %q", want)
		}
	}

	// Release manager template points at pipeline.unreviewed-merge rather than re-notifying.
	rmTmpl := read(t, ".grain", "templates", "release-manager.yml")
	if !strings.Contains(rmTmpl, "pipeline.unreviewed-merge:ai-on-gke/grain") {
		t.Error(".grain/templates/release-manager.yml does not reference pipeline.unreviewed-merge:ai-on-gke/grain")
	}
}
