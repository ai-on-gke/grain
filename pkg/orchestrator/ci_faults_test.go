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

// grain/task-299: a red verdict says whether the red is this branch's
// own. The pane learned to (grain/task-296); the two places a verdict is
// read by whoever has to act on it -- the pull request, and the task's
// own conversation, which is replayed into the next agent's prompt --
// had not.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
)

// mirror405 is the failure the whole feature was written for: a
// deployment-wide cause printing byte-identically on every branch that
// reaches it.
const mirror405 = `+ debootstrap --arch=amd64 trixie /build/root
reading scripts/granule/guest-inputs.sh
E: Failed getting release file from the package cache
E: proxy CONNECT returned 405 Method Not Allowed
debootstrap exited 1`

// otherBranchRun is somebody else's run, finished and red, as this
// deployment still remembers it.
func otherBranchRun(id, ref string, at time.Time, output string) ci.Run {
	finished := at
	return ci.Run{
		ID: id, Repo: "acme/widgets", Ref: ref, Commit: "c" + id,
		Conclusion: ci.ConclusionFailure, FinishedAt: &finished,
		Jobs: []ci.JobRun{{
			JobID: "image", Name: "image", Conclusion: ci.ConclusionFailure, FinishedAt: &finished,
			Steps: []ci.StepRun{{Name: "build the image", Conclusion: ci.ConclusionFailure,
				ExitCode: 1, Output: output}},
		}},
	}
}

// failingImageJob is this branch's own share of it.
func failingImageJob(output string) ci.JobRun {
	return ci.JobRun{
		Workflow: "checks", JobID: "image", Name: "image", Conclusion: ci.ConclusionFailure,
		Steps: []ci.StepRun{{Name: "build the image", Conclusion: ci.ConclusionFailure,
			ExitCode: 1, Output: output}},
	}
}

func TestGrainCIVerdictSaysWhenTheRedIsSharedWithOtherBranches(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()
	// Two other branches got there first, inside the window.
	runner.remember(otherBranchRun("ci-246", "grain/task-246", baseTime.Add(-20*time.Minute), mirror405))
	runner.remember(otherBranchRun("ci-251", "grain/task-251", baseTime.Add(-70*time.Minute), mirror405))

	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(status.RunID, ci.ConclusionFailure, failingImageJob(mirror405))
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatalf("collecting the verdict: %v", err)
	}

	// The conversation the next agent on this task reads back.
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("comments = %q, want one verdict", bodies)
	}
	if !strings.HasPrefix(bodies[0], "grain CI failure at `") {
		t.Errorf("the verdict itself changed shape: %q", bodies[0])
	}
	for _, want := range []string{
		"image is failing the same way on 2 other branches right now",
		"Also failing on: grain/task-246, grain/task-251",
	} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("task comment lacks %q: %q", want, bodies[0])
		}
	}
	// Still terse: the fault is one sentence, not the job list and
	// output this comment has always kept out.
	if strings.Contains(bodies[0], "proxy CONNECT") {
		t.Errorf("the fault sentence dragged step output into the conversation: %q", bodies[0])
	}

	// And the pull request, where a reviewer is.
	body := onlyPRComment(t, client, repo, sim.PullRequests[0].Number)
	for _, want := range []string{
		"**This red is shared.**",
		"image is failing the same way on 2 other branches right now",
		"- Also failing on: grain/task-246, grain/task-251",
		"scripts/granule/guest-inputs.sh",
		"will not make it green",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pull request comment lacks %q:\n%s", want, body)
		}
	}
}

// grain/task-362: and it names the change that already fixes it. This
// is the sentence a reviewer reads first, and the one that stops an
// approval of a repair nobody needed -- the pane and wait_for_checks
// have said it since grain/task-296; the pull request comment could only
// point at the pane until the lookup moved to pkg/cifault.
func TestGrainCIVerdictNamesTheOpenPullRequestThatAlreadyFixesIt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)

	// The change that actually fixes it: open, finished, and touching
	// the file every branch in the fault printed.
	_, fixBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t7", repo)
	pushFile(t, sim.BareRepo, fixBranch, "scripts/granule/guest-inputs.sh", "# the fix\n")
	if err := store.UpdateTask(ctx, "t7", func(tk *model.Task) error {
		tk.Title = "Stop the guest build reading the apt mirror"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fixPR := pullRequestNumberFor(t, sim, fixBranch)

	runner := newFakeCI()
	runner.remember(otherBranchRun("ci-246", "grain/task-246", baseTime.Add(-20*time.Minute), mirror405))
	runner.remember(otherBranchRun("ci-251", "grain/task-251", baseTime.Add(-70*time.Minute), mirror405))

	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(status.RunID, ci.ConclusionFailure, failingImageJob(mirror405))
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatalf("collecting the verdict: %v", err)
	}

	body := onlyPRComment(t, client, repo, sim.PullRequests[0].Number)
	for _, want := range []string{
		"**This red is shared.**",
		fmt.Sprintf("Open already, touching scripts/granule/guest-inputs.sh: acme/widgets#%d", fixPR),
		"Stop the guest build reading the apt mirror",
		"from task t7",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pull request comment lacks %q:\n%s", want, body)
		}
	}
	// It says it here rather than sending the reviewer somewhere else.
	if strings.Contains(body, "CI pane names") {
		t.Errorf("the comment still defers the fix to the pane:\n%s", body)
	}
	// The task's own conversation stays one sentence: it is replayed
	// into the next agent's prompt, and the fault itself is the part
	// that decides what that agent does.
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 || strings.Contains(bodies[0], "Open already") {
		t.Errorf("the fix list reached the task conversation: %q", bodies)
	}
}

// pullRequestNumberFor is the sim's pull request open from one branch.
func pullRequestNumberFor(t *testing.T, sim *githubsim.Sim, branch string) int {
	t.Helper()
	for _, pr := range sim.PullRequests {
		if pr.Head == branch {
			return pr.Number
		}
	}
	t.Fatalf("no pull request open from %s", branch)
	return 0
}

// The rule underneath all of it: one branch failing is one branch with a
// problem. A verdict that called that a shared fault would teach every
// reader to ignore the line.
func TestGrainCIVerdictSaysNothingSharedAboutALoneRedBuild(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()
	// Another branch is red, but of its own test failure -- a different
	// failure, and so a different fault.
	runner.remember(otherBranchRun("ci-246", "grain/task-246", baseTime.Add(-20*time.Minute),
		"--- FAIL: TestThing\n    thing_test.go:12: wanted 3, got 4\nFAIL\nexit status 1"))

	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(status.RunID, ci.ConclusionFailure, failingImageJob(mirror405))
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatalf("collecting the verdict: %v", err)
	}

	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 || strings.Contains(bodies[0], "failing the same way") {
		t.Fatalf("a lone red build was reported as shared: %q", bodies)
	}
	if body := onlyPRComment(t, client, repo, sim.PullRequests[0].Number); strings.Contains(body, "This red is shared") {
		t.Fatalf("a lone red build was reported as shared on the pull request:\n%s", body)
	}
}

func onlyPRComment(t *testing.T, client github.Client, repo model.RepoRef, number int) string {
	t.Helper()
	comments, err := client.ListComments(repo.Owner, repo.Name, number)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("pull request comments = %d, want one naming the failure", len(comments))
	}
	return comments[0].Body
}
