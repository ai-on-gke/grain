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

package orchestrator_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// Every kind of task a pull-request trigger files -- grain's security
// review for a sensitive trigger, a gating review that posts findings, one
// that pushes fixes, an untitled template, and an ungated trigger's
// ordinary task -- is titled with no `{{name}}` left in it. Task 6235 was
// filed "Code review: {{task_title}} (ai-on-gke/grain#1542)": the ungated
// path filled {{pr}} and never {{task_title}}. And a template naming a
// placeholder nothing fills is titled grain's own way rather than filed
// with the hole in it.
func TestEveryTriggerKindLeavesNoPlaceholderInItsTitle(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	const codeTitle = "Code review: {{task_title}} ({{pr}})"
	sensitive := gateTrigger("release-machinery", model.TriggerHumanOnFindings, "release")
	sensitive.Template, sensitive.Sensitive = model.SecurityReviewTemplateID, true
	findings := gateTrigger("every-pr", model.TriggerHumanOnFindings, "src")
	findings.Template = templateID("code")
	fixes := gateTrigger("fixer", model.TriggerHumanOnFindings, "lib")
	fixes.Template, fixes.Fixes = templateID("fixes"), true
	untitled := gateTrigger("docs", model.TriggerHumanOnFindings, "docs")
	untitled.Template = templateID("untitled")
	broken := gateTrigger("broken", model.TriggerHumanOnFindings, "cfg")
	broken.Template = templateID("broken")
	ungated := model.PathTrigger{Name: "ungated", On: model.TriggerOnPullRequest, Paths: []string{"web"},
		Template: templateID("ungated"), AutoApprove: true}
	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"), map[string]string{
		"code": codeTitle, "fixes": codeTitle, "ungated": codeTitle, "untitled": "",
		"broken": "Code review: {{task_title}} {{not_a_field}} ({{pr}})",
	}, sensitive, findings, fixes, untitled, broken, ungated)
	task := finishTouching(t, ctx, store, sim, "t1", "release/build.sh", true)
	for _, p := range []string{"src/a.go", "lib/b.go", "docs/c.md", "cfg/d.yml", "web/e.ts"} {
		pushFile(t, sim.BareRepo, model.BranchName(task.ID), p, "changed\n")
	}
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	got := reloaded(t, ctx, store, task.ID)
	ref := linkOf(got, model.LinkFixes)
	filed := triggered(t, ctx, store)
	titles := map[string]bool{}
	for _, f := range filed {
		if holes := model.UnfilledPlaceholders(f.Title); len(holes) > 0 {
			t.Errorf("task %s is titled %q, leaving %v unfilled", f.ID, f.Title, holes)
		}
		titles[f.Title] = true
	}
	for _, want := range []string{
		"Targeted review (release-machinery): " + got.Title,
		"Code review: " + got.Title + " (" + ref + ")",
		"Review: " + got.Title,
	} {
		if !titles[want] {
			t.Errorf("no task titled %q among %v", want, titles)
		}
	}
	if len(filed) != 6 {
		t.Errorf("filed %d tasks, want one per trigger (6): %v", len(filed), titles)
	}
}

// A merge trigger has no task under review, so a template naming
// {{task_title}} cannot be filled there: the task is titled grain's own
// way ("Trigger <name>: <group>") rather than filed with the hole in it.
func TestAMergeTriggerNeverFilesAnUnfilledTitle(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"),
		map[string]string{"planner": "Plan {{group}} for {{task_title}}"}, designTrigger())
	reconcileTriggers(t, ctx, store, client, baseTime)
	pushFile(t, sim.BareRepo, "main", "docs/design/foo/README.md", "# foo\n")
	reconcileTriggers(t, ctx, store, client, baseTime.Add(2*time.Minute))

	got := triggered(t, ctx, store)
	if len(got) != 1 {
		t.Fatalf("filed %d tasks, want one", len(got))
	}
	if want := "Trigger design: docs/design/foo"; got[0].Title != want {
		t.Errorf("title = %q, want %q", got[0].Title, want)
	}
}
