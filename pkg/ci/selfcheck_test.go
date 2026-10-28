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
	workflows, err := LoadNativeDir(os.DirFS(selfRepoRoot))
	if err != nil {
		t.Fatalf("this repository's own %s does not load: %v", WorkflowDir, err)
	}
	if len(workflows) == 0 {
		t.Fatalf("no workflow read out of %s, so this test planned nothing", WorkflowDir)
	}
	plan, err := PlanRun(Request{
		Repo: selfRepo, Ref: selfBranch, Event: event,
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

// TestTheBaseBranchRunOfThisRepositoryRunsItsTests is the property
// grain/task-355 was filed about, stated against the event that judges a
// base branch today: the run that judges `main` has to contain the jobs
// that judge a commit.
//
// It reads weaker than it did -- the judging event is `push` and
// tests.yml answers `push`, so what is left to go wrong is a `branches:`
// filter that stops matching `main`, a job that narrows its own trigger,
// or somebody moving the suite into a workflow that answers something
// else. Those are exactly the quiet narrowings this file exists to catch,
// and a base-branch run planning none of this repository's tests is the
// state that cost it 33 hours of unjudged merges (grain/task-458).
//
// It is asserted against every job of tests.yml rather than against a
// list written here, so a job added to that file is covered the day it
// lands.
func TestTheBaseBranchRunOfThisRepositoryRunsItsTests(t *testing.T) {
	judging := planIDs(planSelf(t, baseBranchJudgingEvent))

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
		t.Errorf("a base-branch run of %s is planned under %s, and these jobs of %s are not in it: %s.\n"+
			"That run is the merge queue's whole answer to not requiring a branch to be up to date "+
			"before it merges (docs/merge-queue-behind.md): jobs that are not in it never judge the "+
			"tree merges land on. Check that file's `on:` block and the job's own triggers.",
			selfBranch, baseBranchJudgingEvent, selfTestsWorkflow, strings.Join(missing, ", "))
	}
}

// TestTheBaseBranchRunOfThisRepositoryDoesNotPublish is the other half of
// the same rule, and since grain/task-461 it is the sharper half: the
// judging run and the publishing run are two runs, and the whole point of
// that is that a publisher which fails cannot hold the merge queue. If
// the publish job ever appeared in the judging plan, its failure would be
// a red base branch again -- and it would be one carrying this
// repository's release credentials into the run every merge waits on.
func TestTheBaseBranchRunOfThisRepositoryDoesNotPublish(t *testing.T) {
	if planIDs(planSelf(t, baseBranchJudgingEvent))[selfPublishJob] {
		t.Errorf("%s is in the %s plan of %s, which is the run the merge queue gates on: a failed "+
			"publish would stop every merge into %s again, which is what grain/task-461 took apart",
			selfPublishJob, baseBranchJudgingEvent, selfBranch, selfBranch)
	}
	// And the release plan still has it, because a merge is still what
	// publishes this deployment's images -- what changed is which run is
	// the verdict, not whether there is a release
	// (orchestrator.reconcileBaseBranchRelease).
	if !planIDs(planSelf(t, EventRelease))[selfPublishJob] {
		t.Errorf("no %s in a %s plan of %s: a merge into %s would publish nothing, and this "+
			"deployment's images are what a release run is for",
			selfPublishJob, EventRelease, selfBranch, selfBranch)
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
