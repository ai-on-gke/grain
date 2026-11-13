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

package repodecl_test

// This repository's own `.grain` directory, read by the package that
// reads it in production.
//
// Everywhere else in this package the trees are written by the test.
// Here the tree is the checkout, because nothing else in the repository
// looks at these files at all: a misspelt key in
// `.grain/suites/release-ui-walkthrough.yml` is otherwise discovered by
// a deployment a poll after it merges, as a line in
// `repo_declaration_sync.last_error` on a settings page nobody is
// reading, with the previous commit's declarations still live and
// nothing at all saying the new ones are not. `grain declarations
// check` exists to move that verdict earlier; this moves it earlier
// still, into a job that already runs on every branch.
//
// It asserts the shape the release ritual depends on as well as the
// parse (docs/ui-walkthrough.md): the suite runs every walkthrough
// template this repository declares, and names no branch of its own,
// because the branch is chosen when the suite is run.

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

// repoRoot is this package's own directory, two levels down.
const repoRoot = "../.."

func loadRepo(t *testing.T) model.RepoDeclarations {
	t.Helper()
	declared, err := repodecl.Load(os.DirFS(repoRoot), repo)
	if err != nil {
		t.Fatalf("this repository's own .grain declarations do not load:\n%v", err)
	}
	return declared
}

func TestThisRepositoryOwnDeclarationsAreWellFormed(t *testing.T) {
	declared := loadRepo(t)
	if declared.Empty() {
		t.Fatalf("this repository declares nothing -- %s has files in it, so Load read the wrong tree",
			model.TemplateDeclarationDir)
	}
	for _, f := range declared.Templates {
		if strings.TrimSpace(f.Template.Body) == "" {
			t.Errorf("%s: a declared template with no body files a task with no instructions", f.Path)
		}
	}
}

func TestPRReviewTemplateCarriesSharedReviewChecklist(t *testing.T) {
	declared := loadRepo(t)
	var prReview *model.Template
	for i := range declared.Templates {
		if declared.Templates[i].Path == ".grain/templates/pr-review.yml" {
			prReview = &declared.Templates[i].Template
			break
		}
	}
	if prReview == nil {
		t.Fatal(".grain/templates/pr-review.yml is not declared")
	}
	checklist := model.ReviewChecklist()
	if !strings.Contains(prReview.Body, checklist) {
		t.Errorf(".grain/templates/pr-review.yml body does not carry model.ReviewChecklist() verbatim.\nwant substring:\n%s\n\ngot body:\n%s",
			checklist, prReview.Body)
	}
}

func TestAgentdContractTriggerNamesDedicatedReviewTemplate(t *testing.T) {
	declared := loadRepo(t)
	var trigger *model.PathTrigger
	for i := range declared.Triggers {
		if declared.Triggers[i].Name == "agentd-contract" {
			trigger = &declared.Triggers[i]
			break
		}
	}
	if trigger == nil {
		t.Fatal(".grain/triggers/agentd-contract.yml is not declared")
	}
	wantID := model.RepoDeclaredID(model.DeclaredTemplate, repo, "agentd-contract-review")
	if trigger.Template != wantID {
		t.Errorf("agentd-contract trigger template = %q, want %q (not the generic security review)",
			trigger.Template, wantID)
	}
	if !trigger.Gate || trigger.Human != model.TriggerHumanRequired || !trigger.Sensitive || !trigger.AutoApprove {
		t.Errorf("agentd-contract trigger = gate %v, human %q, sensitive %v, autoApprove %v; want true, required, true, true",
			trigger.Gate, trigger.Human, trigger.Sensitive, trigger.AutoApprove)
	}
	var tmpl *model.Template
	for i := range declared.Templates {
		if declared.Templates[i].Path == ".grain/templates/agentd-contract-review.yml" {
			tmpl = &declared.Templates[i].Template
			break
		}
	}
	if tmpl == nil {
		t.Fatal(".grain/templates/agentd-contract-review.yml is not declared")
	}
	for _, want := range []string{
		"Optional fields and keys only",
		"pkg/hostd/features.go",
		"Hello.Has",
		"Deprecation window",
		"AgentdContract",
		"scripts/agentd-compat.sh",
		"Do not push, commit or open a pull request",
	} {
		if !strings.Contains(tmpl.Body, want) {
			t.Errorf("agentd-contract-review.yml body missing %q:\n%s", want, tmpl.Body)
		}
	}
}

// This repository declares no settings of its own, and the two tests
// that used to stand here are gone with the reach that made them
// necessary (grain/task-340).
//
// They held `.grain/settings.yml` to the values this repository's
// `repo_config` row needs -- the guest cache dispatch job, the CI
// capabilities, `release-workflows-disabled`, `ignore-github-ci` -- and
// they were right to, because until now that file *was* the row: a
// whole-row overwrite on every poll, which at 18:52 UTC on 20 Sep 2026
// emptied four of those fields at once (grain/task-460, 483).
//
// None of those settings is a file here's to write any more, so there is
// nothing in this tree left to assert about them. What a test in this
// repository can still hold is that the file does not come back saying
// them, and that is exactly what loadRepo does: a `ci-capabilities:` in
// `.grain/settings.yml` is a parse error naming the key, so
// TestThisRepositoryOwnDeclarationsAreWellFormed above fails on it. The
// values themselves are in the row, and .grain/README.md says which they
// are for whoever has to read it back.

func TestTheReleaseWalkthroughSuiteRunsEveryWalkthroughTemplate(t *testing.T) {
	declared := loadRepo(t)

	const stem = "release-ui-walkthrough"
	want := map[string]bool{}
	for _, f := range declared.Templates {
		// ui-walkthrough-review keeps the chunks current
		// (.grain/schedules/ui-walkthrough-review.yml); it walks nothing.
		if s := model.DeclarationStem(f.Path); strings.HasPrefix(s, "ui-walkthrough-") && s != "ui-walkthrough-review" {
			want[s] = false
		}
	}
	if len(want) == 0 {
		t.Fatalf("no ui-walkthrough-* template is declared, so %s/%s.yml has nothing to run",
			model.SuiteDeclarationDir, stem)
	}

	var suite *model.Suite
	for i, f := range declared.Suites {
		if model.DeclarationStem(f.Path) == stem {
			suite = &declared.Suites[i].Suite
		}
	}
	if suite == nil {
		t.Fatalf("%s/%s.yml is what a release runs (docs/ui-walkthrough.md) and it is not declared",
			model.SuiteDeclarationDir, stem)
	}
	for _, item := range suite.Items {
		for name := range want {
			if item.TemplateID == model.RepoDeclaredID(model.DeclaredTemplate, repo, name) {
				want[name] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("%s/%s.yml declares a walkthrough chunk that %s does not run -- "+
				"a chunk left out of the suite is a chunk no release walks",
				model.TemplateDeclarationDir, name, stem)
		}
	}
	// One pass, deliberately: docs/ui-walkthrough.md has the reasoning,
	// and an until-clean sweep here would re-walk five clean areas for
	// the sake of the sixth.
	if suite.Mode != model.SuiteCount || suite.Count != 1 {
		t.Errorf("%s runs %q x%d; the release walkthrough is one pass of each chunk",
			stem, suite.Mode, suite.Count)
	}
	if suite.RequireApproval || !suite.AutoMerge {
		t.Errorf("%s: require-approval %v, auto-merge %v -- the walkthrough queues itself and lands its "+
			"own fixes", stem, suite.RequireApproval, suite.AutoMerge)
	}
}

func TestTheReleaseQualificationSuiteRunsEveryQualificationTemplate(t *testing.T) {
	declared := loadRepo(t)

	const stem = "release-qualification"
	want := map[string]bool{}
	for _, f := range declared.Templates {
		if s := model.DeclarationStem(f.Path); strings.HasPrefix(s, "qualification-") {
			want[s] = false
		}
	}
	if len(want) == 0 {
		t.Fatalf("no qualification-* template is declared, so %s/%s.yml has nothing to run",
			model.SuiteDeclarationDir, stem)
	}

	var suite *model.Suite
	for i, f := range declared.Suites {
		if model.DeclarationStem(f.Path) == stem {
			suite = &declared.Suites[i].Suite
		}
	}
	if suite == nil {
		t.Fatalf("%s/%s.yml is not declared", model.SuiteDeclarationDir, stem)
	}
	for _, item := range suite.Items {
		for name := range want {
			if item.TemplateID == model.RepoDeclaredID(model.DeclaredTemplate, repo, name) {
				want[name] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("%s/%s.yml declares a qualification item that %s does not run",
				model.TemplateDeclarationDir, name, stem)
		}
	}
	// One pass that must read clean, and no auto-merge: a pull request or a
	// proposed follow-up fails Stage 2 (design/playbook/releases.playbook.md
	// §2). And no auto-approve-proposals: a follow-up on any branch but the
	// task's own scratch branch is a defect report for a person, and the
	// fixtures on that branch run unattended on the template's push of it
	// (model.ScratchFixture) -- checked below.
	if suite.Mode != model.SuiteUntilClean || suite.MaxPasses != 1 {
		t.Errorf("%s runs %q with max-passes %d; expected until_clean with max-passes 1", stem, suite.Mode, suite.MaxPasses)
	}
	if suite.RequireApproval || suite.AutoMerge || suite.AutoApproveProposals {
		t.Errorf("%s: require-approval %v, auto-merge %v, auto-approve-proposals %v; want false, false, false",
			stem, suite.RequireApproval, suite.AutoMerge, suite.AutoApproveProposals)
	}
	// Every item that files fixtures may push its own scratch branch, or
	// its fixtures wait for a person and the item can never finish.
	for _, f := range declared.Templates {
		name := model.DeclarationStem(f.Path)
		if !strings.HasPrefix(name, "qualification-") || !strings.Contains(f.Template.Body, "propose_task") {
			continue
		}
		if matched, _, del := f.Push.Allows("refs/heads/" + model.QualificationScratchBranch("123")); !matched || !del {
			t.Errorf("%s/%s.yml files scratch fixtures but does not declare a push (with delete) of "+
				"refs/heads/qualification/*", model.TemplateDeclarationDir, name)
		}
	}
	// The reconciler files the last item only after every other passes.
	smoke := model.RepoDeclaredID(model.DeclaredTemplate, repo, "qualification-system-smoke")
	if n := len(suite.Items); n == 0 || suite.Items[n-1].TemplateID != smoke {
		t.Errorf("%s: qualification-system-smoke must be its last item, got %+v", stem, suite.Items)
	}
}

func TestTheMainUISmokeSuiteRunsCoreWalkthroughTemplates(t *testing.T) {
	declared := loadRepo(t)

	const stem = "main-ui-smoke"
	expectedTemplates := []string{
		"ui-walkthrough-tasks",
		"ui-walkthrough-shell",
		"ui-walkthrough-settings",
		"ui-walkthrough-infrastructure",
		"ui-walkthrough-boards",
	}

	var suite *model.Suite
	for i, f := range declared.Suites {
		if model.DeclarationStem(f.Path) == stem {
			suite = &declared.Suites[i].Suite
		}
	}
	if suite == nil {
		t.Fatalf("%s/%s.yml is not declared", model.SuiteDeclarationDir, stem)
	}

	items := map[string]bool{}
	for _, item := range suite.Items {
		items[item.TemplateID] = true
	}
	for _, name := range expectedTemplates {
		id := model.RepoDeclaredID(model.DeclaredTemplate, repo, name)
		if !items[id] {
			t.Errorf("%s/%s.yml missing expected template %s", model.SuiteDeclarationDir, stem, name)
		}
	}
	if suite.Mode != model.SuiteCount || suite.Count != 1 {
		t.Errorf("%s runs %q x%d; expected mode count x1", stem, suite.Mode, suite.Count)
	}
	if suite.RequireApproval || !suite.AutoMerge {
		t.Errorf("%s: require-approval %v, auto-merge %v", stem, suite.RequireApproval, suite.AutoMerge)
	}
}

// A schedule with `overlap: skip` (the default) holds every future firing
// while its previous task remains open (`model.SuppressedByOpenTask`, with no
// expiry). On a task that pushes no branch, finishing with `comment_on_issue`
// (or a prose "closing note") sets CompletedAt without ClosedAt, which
// `model.AwaitsAck` derives as `awaiting_reply` -- stalling the schedule until
// a person notices and closes the task (tasks 3711, 3812, 3814).
func TestOverlapSkipScheduleTemplatesDoNotEndInAwaitingReply(t *testing.T) {
	declared := loadRepo(t)
	checked, problems := lintOverlapSkipScheduleTemplates(declared)
	if checked == 0 {
		t.Fatal("checked 0 overlap: skip template schedules; expected at least release-train and release-watchdog")
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestOverlapSkipScheduleTemplateLintCatchesAwaitingReplyPaths(t *testing.T) {
	tmplID := model.RepoDeclaredID(model.DeclaredTemplate, repo, "tick")
	makeDecl := func(overlap model.OverlapPolicy, tmpl model.Template) model.RepoDeclarations {
		tmpl.ID = tmplID
		return model.RepoDeclarations{
			Templates: []model.DeclaredTemplateFile{
				{Path: ".grain/templates/tick.yml", Template: tmpl},
			},
			Schedules: []model.DeclaredScheduleFile{
				{
					Path: ".grain/schedules/tick.yml",
					Schedule: model.Schedule{
						Overlap:    overlap,
						TemplateID: &tmplID,
					},
				},
			},
		}
	}

	clean := model.Template{
		NothingNeededAction: model.NothingNeededClose,
		Body: "Run the check.\n" +
			"- `TICK_RESULT=noop`: call `nothing_needed` with the reason (never `comment_on_issue`).\n" +
			"- `TICK_RESULT=acted`: call `nothing_needed` with what changed.\n",
	}
	if _, problems := lintOverlapSkipScheduleTemplates(makeDecl(model.OverlapSkip, clean)); len(problems) != 0 {
		t.Fatalf("clean template reported problems: %v", problems)
	}

	cases := []struct {
		name string
		decl model.RepoDeclarations
		want string
	}{
		{
			name: "nothing-needed ack",
			decl: makeDecl(model.OverlapSkip, model.Template{
				NothingNeededAction: model.NothingNeededAck,
				Body:                "If clean, call `nothing_needed`.",
			}),
			want: "nothing-needed",
		},
		{
			name: "interactive template",
			decl: makeDecl(model.OverlapSkip, model.Template{
				Interactive: true,
				Body:        "If clean, call `nothing_needed`.",
			}),
			want: "interactive",
		},
		{
			name: "missing nothing_needed",
			decl: makeDecl(model.OverlapSkip, model.Template{
				Body: "Inspect main and push any fixes.",
			}),
			want: "nothing_needed",
		},
		{
			name: "acted calls comment_on_issue",
			decl: makeDecl(model.OverlapSkip, model.Template{
				Body: "- `RELEASE_TRAIN_RESULT=noop`: call `nothing_needed`.\n" +
					"- `RELEASE_TRAIN_RESULT=acted`: call `comment_on_issue` with what it did.\n",
			}),
			want: "comment_on_issue",
		},
		{
			name: "healthy leaves closing note",
			decl: makeDecl(model.OverlapSkip, model.Template{
				Body: "If fixes apply, push a PR; if healthy, leave a closing note confirming health or call `nothing_needed`.",
			}),
			want: "closing note",
		},
		{
			name: "negation in previous sentence does not excuse comment_on_issue",
			decl: makeDecl(model.OverlapSkip, model.Template{
				Body: "Do not investigate further. Call `comment_on_issue` with the summary, or `nothing_needed`.",
			}),
			want: "comment_on_issue",
		},
		{
			name: "result bullet omits nothing_needed",
			decl: makeDecl(model.OverlapSkip, model.Template{
				Body: "- `TICK_RESULT=noop`: call `nothing_needed`.\n" +
					"- `TICK_RESULT=acted`: summarize the output.\n",
			}),
			want: "TICK_RESULT=acted",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, problems := lintOverlapSkipScheduleTemplates(tc.decl)
			if len(problems) == 0 {
				t.Fatalf("expected lint problem containing %q, got none", tc.want)
			}
			joined := strings.Join(problems, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("problems = %q, want substring %q", joined, tc.want)
			}
		})
	}

	// An `overlap: allow` schedule never holds on an open task, so a
	// report-only `comment_on_issue` template is allowed there.
	allow := makeDecl(model.OverlapAllow, model.Template{
		Body: "Post the report with `comment_on_issue`.",
	})
	if _, problems := lintOverlapSkipScheduleTemplates(allow); len(problems) != 0 {
		t.Errorf("overlap: allow schedule reported problems: %v", problems)
	}
}

var resultBulletRe = regexp.MustCompile("(?m)^[ \t]*-[ \t]+`([A-Z0-9_]+=[a-z0-9_-]+)`:(.*)$")

func lintOverlapSkipScheduleTemplates(declared model.RepoDeclarations) (int, []string) {
	byID := make(map[string]model.DeclaredTemplateFile, len(declared.Templates))
	for _, f := range declared.Templates {
		byID[f.Template.ID] = f
	}

	var checked int
	var problems []string
	for _, sf := range declared.Schedules {
		if sf.Schedule.Overlap.Allows() || sf.Schedule.TemplateID == nil {
			continue
		}
		tf, ok := byID[*sf.Schedule.TemplateID]
		if !ok {
			continue
		}
		checked++
		tmpl := tf.Template
		prefix := fmt.Sprintf("%s -> %s", sf.Path, tf.Path)

		if tmpl.NothingNeededAction.Resolved() != model.NothingNeededClose {
			problems = append(problems, fmt.Sprintf(
				"%s: nothing-needed resolves to %q, want %q so a branchless finish closes the task and frees the overlap: skip schedule",
				prefix, tmpl.NothingNeededAction.Resolved(), model.NothingNeededClose))
		}
		if tmpl.Interactive {
			problems = append(problems, fmt.Sprintf(
				"%s: interactive: true parks in awaiting_reply and holds the overlap: skip schedule", prefix))
		}
		if !strings.Contains(tmpl.Body, "nothing_needed") {
			problems = append(problems, fmt.Sprintf(
				"%s: body never names `nothing_needed`; an overlap: skip template must close branchless/no-op runs with `nothing_needed` rather than leaving them in awaiting_reply",
				prefix))
		}
		for _, phrase := range []string{"comment_on_issue", "closing note"} {
			if snippet, bad := findUnnegatedPhrase(tmpl.Body, phrase); bad {
				problems = append(problems, fmt.Sprintf(
					"%s: body instructs %q (%q), which leaves a branchless task in awaiting_reply and stalls the overlap: skip schedule; use `nothing_needed` (and `grain notify` when a person must see it)",
					prefix, phrase, snippet))
			}
		}
		// When a template dispatches on explicit `- \`VAR=outcome\`:` bullets
		// (such as RELEASE_TRAIN_RESULT=noop|acted|failed), every outcome
		// bullet must itself finish with `nothing_needed`.
		bullets := resultBulletRe.FindAllStringSubmatchIndex(tmpl.Body, -1)
		for i, loc := range bullets {
			end := len(tmpl.Body)
			if i+1 < len(bullets) {
				end = bullets[i+1][0]
			} else if para := strings.Index(tmpl.Body[loc[1]:], "\n\n"); para >= 0 {
				end = loc[1] + para
			}
			label := tmpl.Body[loc[2]:loc[3]]
			section := tmpl.Body[loc[0]:end]
			if !strings.Contains(section, "nothing_needed") {
				problems = append(problems, fmt.Sprintf(
					"%s: outcome bullet %q does not finish with `nothing_needed`", prefix, label))
			}
		}
	}
	return checked, problems
}

// findUnnegatedPhrase reports the first occurrence of phrase (case-insensitive)
// in body that is not immediately preceded in the same clause by a negation
// ("never", "not", "don't").
func findUnnegatedPhrase(body, phrase string) (string, bool) {
	norm := strings.Join(strings.Fields(body), " ")
	lower := strings.ToLower(norm)
	target := strings.ToLower(phrase)

	offset := 0
	for {
		idx := strings.Index(lower[offset:], target)
		if idx < 0 {
			return "", false
		}
		pos := offset + idx
		windowStart := pos - 48
		if windowStart < 0 {
			windowStart = 0
		}
		before := lower[windowStart:pos]
		// Stop at sentence or clause boundaries so a negation in an earlier
		// sentence ("Do not investigate further. Call comment_on_issue...")
		// never excuses a positive instruction in the next one.
		if cut := strings.LastIndexAny(before, ".;:!?"); cut >= 0 {
			before = before[cut+1:]
		}
		negated := strings.Contains(before, "never ") ||
			strings.Contains(before, "not ") ||
			strings.Contains(before, "don't ")
		if !negated {
			snipEnd := pos + len(target) + 32
			if snipEnd > len(norm) {
				snipEnd = len(norm)
			}
			return strings.TrimSpace(norm[windowStart:snipEnd]), true
		}
		offset = pos + len(target)
	}
}
