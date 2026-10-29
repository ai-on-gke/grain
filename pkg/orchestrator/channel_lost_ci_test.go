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

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/sandboxaccess"
)

// channelLostJob is a job the engine concluded errored because its
// sandbox's dial-out channel stayed down past the reattach timeout
// (docs/design/grain-on-gke.md#GKE-3).
func channelLostJob() ci.JobRun {
	return ci.JobRun{
		Workflow: "checks", WorkflowPath: ".grain/workflows/checks.yml", JobID: "test", Name: "test",
		Conclusion: ci.ConclusionErrored,
		Error:      sandboxaccess.ChannelLostFault + ": sandbox s1: sandbox access channel lost after 2m0s",
	}
}

// A job whose sandbox channel was lost is re-run once, uncached, at once --
// and when that re-run passes the verdict is green and nothing is recorded
// as a flake: the platform took the sandbox away, the test did not flake.
func TestAChannelLostJobIsReRunOnceUncachedAndGoesGreen(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	runner := newFakeCI()
	runner.answerRechecks(recheckPasses)
	first := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	pass := flakyJob()
	pass.JobID, pass.Name, pass.Conclusion, pass.FailureSummary = "lint", "lint", ci.ConclusionSuccess, nil
	runner.finish(first.RunID, ci.ConclusionErrored, channelLostJob(), pass)

	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}

	status := ciStatusOf(t, ctx, store, task.ID)
	if !status.Green() || status.RecheckOf != first.RunID {
		t.Fatalf("status = %+v, want green, rechecking %s", status, first.RunID)
	}
	if n := runner.rechecks(); n != 1 {
		t.Fatalf("%d uncached re-runs were asked for, want 1", n)
	}
	req := runner.lastRequest(t)
	if !req.Recheck || len(req.Jobs) != 1 || req.Jobs[0] != ".grain/workflows/checks.yml:test" {
		t.Fatalf("the re-run asked for %+v, want only the channel-lost job, uncached", req)
	}
	if got := runner.flakesOf(first.RunID); len(got) != 0 {
		t.Fatalf("a channel-lost job was recorded as a flake: %v", got)
	}
}

// A re-run that loses its channel again is not re-run a second time by the
// same path: the errored verdict stands, for ciReplacementFor's bounded
// whole-suite retries to own.
func TestAChannelLostReRunThatLosesItsChannelAgainIsNotReRunAgain(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	runner := newFakeCI() // the re-run ends the way the first did
	first := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(first.RunID, ci.ConclusionErrored, channelLostJob())

	for range 2 {
		if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
			t.Fatal(err)
		}
	}

	status := ciStatusOf(t, ctx, store, task.ID)
	if !status.Done() || status.Conclusion != string(ci.ConclusionErrored) {
		t.Fatalf("status = %+v, want the errored verdict to stand", status)
	}
	if n := runner.rechecks(); n != 1 {
		t.Fatalf("%d uncached re-runs were asked for, want exactly 1", n)
	}
}

// An errored run that did not lose a channel -- a sandbox that never got
// built -- is not re-run by this path; ciReplacementFor owns it.
func TestAnErroredRunWithoutALostChannelIsNotRecheckedAtOnce(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	runner := newFakeCI()
	first := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	job := channelLostJob()
	job.Error = "building a sandbox for this job: no capacity"
	runner.finish(first.RunID, ci.ConclusionErrored, job)

	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	if n := runner.rechecks(); n != 0 {
		t.Fatalf("%d uncached re-runs were asked for, want none", n)
	}
}
