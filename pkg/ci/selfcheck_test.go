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

package ci

// This repository's own `.grain/workflows`, planned by the package that
// plans them in production.
//
// Everywhere else in this package the workflows are written by the test.
// Here they are the checkout's, because which jobs judge this
// deployment's own `main` is decided by those files and is answered
// nowhere else: a base-branch run is asked for under `push`
// (orchestrator.baseBranchCIWanted), which jobs a run contains is decided
// by the event, and for a native grain workflow no Actions translation
// widens that (workflowEvent).
//
// The property this file pins is the general one and it has outlived two
// different answers to it: **a judging run's event has to plan the jobs
// that judge a commit.** For weeks the merge queue's compensating control
// on this repository ran .grain/workflows/release.yml's `images` -- a job
// that builds and publishes an image -- and no test at all, because the
// base-branch run was asked for under `release` and `release` matched
// that file alone (grain/task-355). That was first answered by making
// tests.yml answer `release` too, and then, when it turned out the
// publisher's conclusion was still the thing holding every merge in the
// repository, by moving the judging run to `push` and giving the
// publishing a run of its own (grain/task-461). The assertions below
// followed the second answer; the claim did not change.
//
// Nothing else in the tree would notice either answer going wrong. The
// workflows parse, the plan is a legal plan, the run is green or red for
// real reasons; the only thing wrong with it is which question it asks,
// and that is a property of the two files together rather than of either
// one. This is where that property is written down.

import (
	"os"
	"strings"
	"testing"
)

// selfRepo and selfBranch are what a base-branch run of this repository
// is asked for: the branch names matter because a workflow's `on:` filters
// by branch, and a filter that excluded `main` would be exactly the kind
// of quiet narrowing this file exists to catch.
const (
	selfRepo   = "bwsalmon/grain"
	selfBranch = "main"
	// selfRepoRoot is the checkout itself, two levels up from this
	// package. The workflows are read out of it by the same loader a
	// running daemon reads a repository's tree with (LoadNativeDir), so
	// what this test plans is what a deployment would plan -- including
	// which of the two parsers each file goes through.
	selfRepoRoot = "../.."
	// selfPublishJob is the plan-wide id of the job that publishes this
	// deployment's images -- the one a release run is *for*.
	selfPublishJob = WorkflowDir + "/release.yml:images"
	// selfTestsWorkflow is the file holding the jobs that judge a commit.
	selfTestsWorkflow = WorkflowDir + "/tests.yml"
	// baseBranchJudgingEvent is the event grain asks a base-branch run
	// for, and therefore the plan this file's claims are about. It is a
	// copy of orchestrator.baseBranchCIEvent -- pkg/ci cannot import
	// pkg/orchestrator, which imports it -- so a change to that constant
	// that is not made here shows up as this file asserting the property
	// about the wrong plan.
	baseBranchJudgingEvent = EventPush
)

// planSelf plans one run of this repository's own workflows, as grain
// would at the tip of selfBranch. TrustedRelease rides with the release
// event because Request.Validate refuses a release nobody trusted, and a
// plan is all this asks for -- no sandbox, no credential, no run.
func planSelf(t *testing.T, event Event) Plan {
	t.Helper()
	return planSelfWorkflow(t, event, "")
}

func planSelfWorkflow(t *testing.T, event Event, workflow string) Plan {
	t.Helper()
	workflows, err := LoadNativeDir(os.DirFS(selfRepoRoot))
	if err != nil {
		t.Fatalf("this repository's own %s does not load: %v", WorkflowDir, err)
	}
	if len(workflows) == 0 {
		t.Fatalf("no workflow read out of %s, so this test planned nothing", WorkflowDir)
	}
	plan, err := PlanRun(Request{
		Repo: selfRepo, Ref: selfBranch, Event: event,
		Workflow:       workflow,
		TrustedRelease: event == EventRelease,
	}, workflows)
	if err != nil {
		t.Fatalf("planning a %s run of this repository at %s: %v", event, selfBranch, err)
	}
	return plan
}

// planIDs is a plan's jobs as the ids ci.Request.Jobs selects by.
func planIDs(plan Plan) map[string]bool {
	out := map[string]bool{}
	for _, job := range plan.Jobs {
		out[job.ID] = true
	}
	return out
}

// TestTheBaseBranchRunOfThisRepositoryRunsItsTests checks that once
// ai-on-gke/grain switched to ci_mode: "github", no native .grain/workflows
// job runs automatically on push or pull_request, while a manual dispatch of
// .grain/workflows/tests.yml still plans every job in that file.
func TestTheBaseBranchRunOfThisRepositoryRunsItsTests(t *testing.T) {
	for _, event := range []Event{EventPush, EventPullRequest} {
		if got := planSelf(t, event); len(got.Jobs) != 0 {
			t.Errorf("an automatic %s run of %s planned %v; expected no native .grain/workflows jobs now that CI runs on GitHub Actions",
				event, selfBranch, planIDs(got))
		}
	}

	judging := planIDs(planSelfWorkflow(t, EventManual, selfTestsWorkflow))

	declared, err := LoadNativeDir(os.DirFS(selfRepoRoot))
	if err != nil {
		t.Fatalf("this repository's own %s does not load: %v", WorkflowDir, err)
	}
	var missing []string
	tested := 0
	for _, wf := range declared {
		if wf.Path != selfTestsWorkflow {
			continue
		}
		for _, job := range wf.Jobs {
			id := wf.Path + ":" + job.ID
			tested++
			if !judging[id] {
				missing = append(missing, id)
			}
		}
	}
	if tested == 0 {
		t.Fatalf("%s declares no job at all, so this test asserted nothing about the jobs that "+
			"judge a commit", selfTestsWorkflow)
	}
	if len(missing) > 0 {
		t.Errorf("a manual run of %s on %s is missing these jobs: %s.\n"+
			"Check that file's `on:` block and the job's own triggers.",
			selfTestsWorkflow, selfBranch, strings.Join(missing, ", "))
	}
}

// TestTheBaseBranchRunOfThisRepositoryDoesNotPublish checks that neither a
// base-branch push nor an automatic release event triggers native
// .grain/workflows publishing jobs, while manual dispatch of
// .grain/workflows/release.yml still plans the image publish job and is kept
// separate from tests.yml.
func TestTheBaseBranchRunOfThisRepositoryDoesNotPublish(t *testing.T) {
	if planIDs(planSelf(t, baseBranchJudgingEvent))[selfPublishJob] {
		t.Errorf("%s is in the %s plan of %s, which is the run the merge queue gates on: a failed "+
			"publish would stop every merge into %s again, which is what grain/task-461 took apart",
			selfPublishJob, baseBranchJudgingEvent, selfBranch, selfBranch)
	}
	if planIDs(planSelf(t, EventRelease))[selfPublishJob] {
		t.Errorf("%s is still in the automatic %s plan of %s; release publishing moved to .github/workflows/release.yml",
			selfPublishJob, EventRelease, selfBranch)
	}
	if planIDs(planSelfWorkflow(t, EventManual, selfTestsWorkflow))[selfPublishJob] {
		t.Errorf("%s is in the manual plan of %s", selfPublishJob, selfTestsWorkflow)
	}
	if !planIDs(planSelfWorkflow(t, EventManual, WorkflowDir+"/release.yml"))[selfPublishJob] {
		t.Errorf("no %s in a manual dispatch of %s/release.yml on %s",
			selfPublishJob, WorkflowDir, selfBranch)
	}
}

// TestThisRepositoryDoesNotRunItsTestsTwicePerMerge is the cost half of
// grain/task-461. A merge starts two runs at the same tip -- the judging
// push and the trusted release -- so a job that answers both events is
// planned into both and this repository pays for its whole suite twice,
// the second time inside a run holding release secrets. That is what
// tests.yml answering `release` did (grain/task-355) and what removing
// that trigger undid.
func TestThisRepositoryDoesNotRunItsTestsTwicePerMerge(t *testing.T) {
	release := planIDs(planSelf(t, EventRelease))
	var both []string
	for _, job := range planSelf(t, baseBranchJudgingEvent).Jobs {
		if job.WorkflowPath == selfTestsWorkflow && release[job.ID] {
			both = append(both, job.ID)
		}
	}
	if len(both) > 0 {
		t.Errorf("these jobs are in both the %s plan and the %s plan of %s, so every merge runs "+
			"them twice -- once as the gate and once inside the trusted release run: %s",
			baseBranchJudgingEvent, EventRelease, selfBranch, strings.Join(both, ", "))
	}
}
