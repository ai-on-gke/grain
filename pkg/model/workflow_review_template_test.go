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

package model_test

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The three nights this prompt has to tell apart, and the one that is
// easiest to lose (grain/task-386).
//
// A firing grain concludes itself has no verdict from the run, and for
// two of those -- a run that could not be started, and one this
// deployment no longer has -- there is nothing to read and the only
// useful thing the reviewer can do is say so. The third is the opposite:
// grain gave up waiting for a run that had not finished, which leaves a
// run that exists, jobs that ended and have logs, and one job that did
// not and is still writing one. "The nightly hangs" is invisible from
// everywhere else and the evidence of where it hangs is in that log, so
// a prompt that tells the reviewer to write one sentence and stop throws
// away the most interesting night this suite can have.
//
// Asserted here rather than in pkg/orchestrator because it is this
// package's own text, and because the prompt is grain's rather than a
// deployment's (SystemTemplate's doc comment): nothing an operator
// configures can put these sentences back.
func TestTheWorkflowReviewPromptSeparatesAHungRunFromANightWithNoRun(t *testing.T) {
	tmpl := model.SystemTemplate(model.WorkflowReviewTemplateID("nightly"),
		model.RepoRef{Owner: "bwsalmon", Name: "grain-home"})
	if tmpl == nil {
		t.Fatal("this build ships no review for a scheduled workflow")
	}
	if !strings.Contains(tmpl.Body, "no run to read") {
		t.Error("the prompt does not tell the reviewer of a night with no run to report that " +
			"and stop, which is the whole of that report")
	}
	if !strings.Contains(tmpl.Body, "still being written") || !strings.Contains(tmpl.Body, "grain ci log") {
		t.Error("the prompt does not send the reviewer of a run grain stopped waiting for to " +
			"the log of the job that never ended, which is the only evidence of where it hangs")
	}
	// The prompt cannot tell which night it is -- the run happened hours
	// before this task was composed -- so it must defer to the comment
	// grain leaves, rather than guessing or naming a single behaviour.
	if !strings.Contains(tmpl.Body, "comment") {
		t.Error("the prompt does not send the reviewer to the comment that says which of the " +
			"three nights this is")
	}
}

// The reviewer is told the run was not served from grain CI's cache
// (grain/task-347), because it is the fact that decides how to read a
// short night: a scheduled run is started as a recheck
// (orchestrator.startScheduledWorkflowRun), so an unmoved commit really
// did run the suite again and the durations in front of the reviewer are
// this night's own rather than an earlier run's replayed.
func TestTheWorkflowReviewPromptSaysTheRunWasNotCached(t *testing.T) {
	tmpl := model.SystemTemplate(model.WorkflowReviewTemplateID("nightly"),
		model.RepoRef{Owner: "bwsalmon", Name: "grain-home"})
	if tmpl == nil {
		t.Fatal("this build ships no review for a scheduled workflow")
	}
	if !strings.Contains(tmpl.Body, "cache") {
		t.Error("the prompt does not say whether the run was served from the cache, which is " +
			"what tells a fast night from a replayed one")
	}
}

// And it still says nothing about which jobs the workflow has: the list
// changes week to week, and a prompt carrying one is wrong by the second
// month (workflowReviewTemplate's own reasoning).
func TestTheWorkflowReviewPromptNamesNoJob(t *testing.T) {
	tmpl := model.SystemTemplate(model.WorkflowReviewTemplateID("nightly"),
		model.RepoRef{Owner: "bwsalmon", Name: "grain-home"})
	if tmpl == nil {
		t.Fatal("this build ships no review for a scheduled workflow")
	}
	if !strings.Contains(tmpl.Body, ".grain/workflows/nightly.yml") {
		t.Error("the prompt does not send the reviewer to the workflow file in its own checkout")
	}
	for _, job := range []string{"go-long", "staterepo-soak", "guest-cache-drift"} {
		if strings.Contains(tmpl.Body, job) {
			t.Errorf("the prompt names the %q job, which is a list that goes stale", job)
		}
	}
}

// Nightly testing workflows should only report things the user needs to address
// (grain/task-349).
//
// The nightly workflows are most likely to have one of two cases:
// 1) Everything is fine, nothing needs to be fixed.
// 2) There are clear bugs, the task can file new tasks to fix them.
//
// The task should only report to the user if neither of these is true;
// there is some bug that is not easily fixable with a new task because
// either it is an architectural change or needs user intervention for
// another reason.
func TestTheWorkflowReviewPromptOnlyReportsWhenUserInterventionNeeded(t *testing.T) {
	tmpl := model.SystemTemplate(model.WorkflowReviewTemplateID("nightly"),
		model.RepoRef{Owner: "bwsalmon", Name: "grain-home"})
	if tmpl == nil {
		t.Fatal("this build ships no review for a scheduled workflow")
	}
	if !strings.Contains(tmpl.Body, "user needs to address") {
		t.Error("the prompt does not instruct the reviewer to only report things the user needs to address")
	}
	if !strings.Contains(tmpl.Body, "propose_task") {
		t.Error("the prompt does not instruct the reviewer to file clear bugs with propose_task")
	}
	if !strings.Contains(tmpl.Body, "architectural change") {
		t.Error("the prompt does not mention architectural changes as a reason to report to the user")
	}
	if !strings.Contains(tmpl.Body, "user intervention") {
		t.Error("the prompt does not mention user intervention as a reason to report to the user")
	}
}

// Scheduled workflow review prompt must explicitly distinguish capability
// unavailability, infrastructure / runner preparation errors, and external
// cloud policy constraints from repository bugs.
func TestTheWorkflowReviewPromptDistinguishesInfrastructureAndCapabilityFailures(t *testing.T) {
	tmpl := model.SystemTemplate(model.WorkflowReviewTemplateID("nightly"),
		model.RepoRef{Owner: "bwsalmon", Name: "grain-home"})
	if tmpl == nil {
		t.Fatal("this build ships no review for a scheduled workflow")
	}

	for _, want := range []string{
		"Capability or secret unavailability",
		"github-sandbox",
		"CLAUDE_CODE_OAUTH_TOKEN",
		"Infrastructure or runner preparation errors",
		"0s",
		"External cloud constraints",
		"org policies",
		"comment_on_issue",
	} {
		if !strings.Contains(tmpl.Body, want) {
			t.Errorf("prompt does not mention %q for failure categorization", want)
		}
	}

	if !strings.Contains(tmpl.Body, "Do NOT file a code modification task via `propose_task`") {
		t.Error("the prompt does not forbid filing propose_task for infrastructure or capability issues")
	}
}

// Automated triage guards: only file code repair tasks when an actual test
// assertion, build compilation, or runtime lint check failed within an executed step.
func TestTheWorkflowReviewPromptEnforcesTriageGuards(t *testing.T) {
	tmpl := model.SystemTemplate(model.WorkflowReviewTemplateID("nightly"),
		model.RepoRef{Owner: "bwsalmon", Name: "grain-home"})
	if tmpl == nil {
		t.Fatal("this build ships no review for a scheduled workflow")
	}

	if !strings.Contains(tmpl.Body, "Automated Triage Guards") {
		t.Error("the prompt does not include automated triage guards section")
	}
	if !strings.Contains(tmpl.Body, "test assertion") || !strings.Contains(tmpl.Body, "build compilation") || !strings.Contains(tmpl.Body, "lint check") {
		t.Error("the prompt does not specify the concrete code failure conditions (test assertion, compilation, lint)")
	}
}
