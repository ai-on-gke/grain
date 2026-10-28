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

// Four more of bwsalmon/agents#337's ten, all extending
// mergequeue_conflict_test.go's own real-git merge queue coverage past
// the one scenario it proves (a conflict a single automatic fix
// resolves):
//
//   - TestMergeQueueEscalatesAfterAFailedRepairAndAdvancesToTheNextQueuedTask
//     is sync.go's other exit from advanceMergeQueueHead --
//     escalateToUser -- which pkg/orchestrator/mergequeue_test.go already
//     proves against a hand-built store/sim state, but which nothing had
//     driven through a real dispatch, a real repair run that did not
//     resolve anything and a real second queued task actually taking the
//     freed-up head position before this.
//   - TestMergeQueueRepairsFailingCheckRunsNotJustConflicts is the same
//     repair-and-resolve happy path mergequeue_conflict_test.go already
//     proves, but reached through a failing CI check (healthFrom's
//     ListCheckRuns half) rather than a git conflict (healthFrom's
//     Mergeable half) -- the only e2e coverage of that half before this
//     was auto_merge_test.go's own single already-passing check.
//   - TestClosingATaskWithAnOpenPullRequestDropsItFromTheMergeQueueForGood
//     is a safety property nothing exercised before: a task a human
//     closed while its pull request was still open must never be
//     auto-merged later, however clean that pull request eventually
//     reads.
//   - TestAutoMergeLeavesAPullRequestOfUnknownMergeabilityAloneUntilIt
//     Resolves guards the third of healthFrom's outcomes, PrUnknown: a
//     pull request GitHub has not finished computing mergeability for
//     must neither merge early nor be mistaken for a conflict.
//
// All four reuse mergequeue_conflict_test.go's own worldSandboxes,
// setMergeable and realConflict, and cli_test.go's own scriptedFramework,
// rather than rebuilding any of them.
//
// A fifth has since joined the first of those, as its opposite number:
// TestMergeQueueRefreshesARepairedHeadTheQueueItselfLeftBehind is a
// repair that did resolve what it was sent to resolve and is read against
// a base the queue moved underneath it in the meantime, which is what
// passing a head over produces and what the escalation above must not be
// reached by.
package e2e

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// cleanPushScript is pushScript's own clone/commit/push, but prefixed
// with `rm -rf work` -- harness_test.go's own pushScript never cleans up
// after itself, which every existing caller gets away with because it
// only ever pushes once per slot per test. The tests in this file
// dispatch more than one task into the very same slot in a row, so a
// second dispatch's `git clone ... work` would otherwise fail outright
// against the first dispatch's own leftover "work" directory -- the same
// reason configPushScript and resolveScript (mergequeue_conflict_test.go)
// already clean up first.
func cleanPushScript(remote, branch, taskID string) []antigravity.Step {
	cmd := "rm -rf work && git clone " + remote + " work && cd work && " +
		"git checkout -b " + branch + " && " +
		"echo 'change for " + taskID + "' >> NOTES.md && " +
		"git add NOTES.md && git commit -q -m 'agent commit for " + taskID + "' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("pushed " + branch),
	}
}

// repairPushScript is a repair run's own scripted turn: continue the
// task's own branch -- the one its pull request is open from, which is
// where a repair works now -- and push a trivial, unrelated file, for
// tests where the repair's own content does not matter, only that
// something landed on the branch under review.
func repairPushScript(remote, branch string) []antigravity.Step {
	cmd := "rm -rf work && git clone " + remote + " work && cd work && " +
		"git checkout -b " + branch + " origin/" + branch + " && " +
		"echo 'fix' > FIX.md && " +
		"git add FIX.md && git commit -q -m 'repair for " + branch + "' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("pushed a repair to " + branch),
	}
}

// repairDoesNotResolveScript is a repair run that pushes something to the
// branch -- but never merges main into it, unlike resolveScript's own
// real conflict resolution -- so what it was sent to repair stays broken.
// That is the shape escalateToUser exists for: a repair that ran, and
// finished, and did not work.
func repairDoesNotResolveScript(remote, branch string) []antigravity.Step {
	cmd := "rm -rf work && git clone " + remote + " work && cd work && " +
		"git checkout -b " + branch + " origin/" + branch + " && " +
		"echo 'attempted a fix' > ATTEMPT.md && " +
		"git add ATTEMPT.md && git commit -q -m 'attempted a fix, does not touch the real conflict' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("pushed an attempted fix to " + branch),
	}
}

func TestMergeQueueEscalatesAfterAFailedRepairAndAdvancesToTheNextQueuedTask(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	const owner, repoName = "acme", "widgets"
	w.newRepo(owner, repoName)
	repo := model.RepoRef{Owner: owner, Name: repoName}

	bare := filepath.Join(w.upstreamDir, owner, repoName+".git")
	sim := githubsim.New(owner, repoName, bare, "main")
	client := github.NewClient(sim, nil)
	deps := orchestrator.Deps{Store: w.store, Client: client, Sandboxes: worldSandboxes{w}, MaxWorkers: 1}

	task1 := model.Task{
		ID: "t1", Intent: model.IntentImplement, Title: "task1",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human("alice")}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human("alice")},
		Target:   &repo, Binding: model.BindingDirective, AutoMerge: true, CreatedAt: &baseTime,
	}
	if err := w.store.PutTask(w.ctx, task1); err != nil {
		t.Fatal(err)
	}
	laterCreated := baseTime.Add(time.Second)
	task2 := model.Task{
		ID: "t2", Intent: model.IntentImplement, Title: "task2",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human("bob")}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human("bob")},
		Target:   &repo, Binding: model.BindingDirective, AutoMerge: true, CreatedAt: &laterCreated,
	}
	if err := w.store.PutTask(w.ctx, task2); err != nil {
		t.Fatal(err)
	}

	branch1, branch2 := model.BranchName("t1"), model.BranchName("t2")
	clock := baseTime

	// Step 1: both push and open pull requests -- task1 first, and it
	// sits ahead of task2 in the backlog, which is where the queue reads
	// its head from once both are members.
	deps.Framework = scriptedFramework(configPushScript(w.remote(owner, repoName), branch1, "setting: from-task1"))
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (t1 push): %v", err)
	}
	if st, err := w.store.State(w.ctx, "t1"); err != nil || st != model.StateCompleted {
		t.Fatalf("t1 state = %q (%v), want completed", st, err)
	}

	clock = clock.Add(time.Minute)
	deps.Framework = scriptedFramework(cleanPushScript(w.remote(owner, repoName), branch2, "t2"))
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (t2 push): %v", err)
	}
	if st, err := w.store.State(w.ctx, "t2"); err != nil || st != model.StateCompleted {
		t.Fatalf("t2 state = %q (%v), want completed", st, err)
	}
	if len(sim.PullRequests) != 2 {
		t.Fatalf("expected two pull requests, got %+v", sim.PullRequests)
	}

	// Step 2: an unrelated commit lands straight on main and genuinely
	// conflicts with task1's branch only -- task2's own change touches an
	// unrelated file.
	w.pushConflictingCommit(owner, repoName, "main", "CONFIG.md", "setting: from-main", "unrelated change conflicting with t1")
	if !w.realConflict(owner, repoName, "main", branch1) {
		t.Fatal("expected a genuine git conflict between main and task1's branch")
	}
	setMergeable(sim, branch1, false)
	setMergeable(sim, branch2, true)

	// Step 3: sync notices task1 -- the queue's head -- is broken and
	// sends it back for repair. task2 is clean and its change is
	// unrelated, so rather than waiting out a repair that has nothing to
	// do with it, the queue passes the broken head over and merges task2
	// on the same cycle.
	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (asks for the repair): %v", err)
	}
	obs1, err := w.store.GetObservation(w.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !obs1.RepairInFlight() {
		t.Fatalf("observation = %+v, want t1 sent back for repair", obs1)
	}
	if st, err := w.store.State(w.ctx, "t2"); err != nil || st != model.StateClosed {
		t.Fatalf("t2 state = %q (%v), want closed -- a green change does not wait out somebody else's conflict", st, err)
	}
	if !sim.PullRequests[1].Merged {
		t.Fatalf("t2's own pull request should have merged past the conflicted head, got %+v", sim.PullRequests[1])
	}
	if got := w.fileAt(owner, repoName, "main", "NOTES.md"); !strings.Contains(got, "change for t2") {
		t.Fatalf("main:NOTES.md = %q, want t2's own change merged while t1 was still being repaired", got)
	}

	// Step 4: the repair is dispatched -- as another attempt of task1
	// itself, on task1's own branch -- but what it pushes never actually
	// resolves the real conflict with main.
	clock = clock.Add(time.Minute)
	deps.Framework = scriptedFramework(repairDoesNotResolveScript(w.remote(owner, repoName), branch1))
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (dispatch the repair): %v", err)
	}
	if st, err := w.store.State(w.ctx, "t1"); err != nil || st != model.StateCompleted {
		t.Fatalf("t1 state = %q (%v), want completed once the repair ran", st, err)
	}
	if len(sim.PullRequests) != 2 {
		t.Fatalf("the repair opened a pull request of its own: %+v", sim.PullRequests)
	}
	if !w.realConflict(owner, repoName, "main", branch1) {
		t.Fatal("expected task1's branch to still genuinely conflict with main -- the repair never addressed it")
	}

	// Step 5: further cycles let the merge queue notice the repair didn't
	// take, escalate task1 to a human, and move on to merge task2.
	// Exactly which single cycle notices which is not a contract this
	// test pins down: the queue waits a cycle after a repair finishes
	// before judging it, and queueHeads' read of task1's blocked flag is
	// a snapshot taken once at the start of a cycle -- what matters is
	// that a few ticks later, both have settled correctly.
	for i := 0; i < 3; i++ {
		clock = clock.Add(time.Minute)
		if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
			t.Fatalf("RunCycle (settling, tick %d): %v", i, err)
		}
	}

	if st, err := w.store.State(w.ctx, "t1"); err != nil || st != model.StateCompleted {
		t.Fatalf("t1 state = %q (%v), want completed -- still open, waiting on a human, never merged or closed", st, err)
	}
	obs1, err = w.store.GetObservation(w.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if obs1 == nil || obs1.MergeQueueBlockedAt == nil {
		t.Fatal("expected t1 to be marked blocked on the merge queue after its one repair attempt failed")
	}
	comments1, err := w.store.Comments(w.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	foundEscalation := false
	for _, c := range comments1 {
		if strings.Contains(c.Body, "needs a person") {
			foundEscalation = true
		}
	}
	if !foundEscalation {
		t.Fatalf("expected an escalation comment on t1, got %+v", comments1)
	}

	// And t2, merged back in step 3, stayed merged: nothing about the
	// head's repair failing and being escalated disturbs a change that
	// had already gone past it.
	if st, err := w.store.State(w.ctx, "t2"); err != nil || st != model.StateClosed {
		t.Fatalf("t2 state = %q (%v), want closed -- it merged and nothing since should have reopened it", st, err)
	}
	if got := w.fileAt(owner, repoName, "main", "NOTES.md"); !strings.Contains(got, "change for t2") {
		t.Fatalf("main:NOTES.md = %q, want t2's own change actually merged", got)
	}
}

// The interaction the pass-over rule above made likely, driven end to
// end: a repair that really did resolve what it was sent to resolve, but
// which is judged against a base the queue itself moved while the repair
// was running -- because passing the head over is exactly what lets the
// changes behind it merge past it.
//
// Under the older rule that head was escalated: the repair was spent, the
// refresh was one attempt per pull request and had already been used
// before the repair was asked for, so a person was handed a branch whose
// whole problem was one merge commit. Now the repaired head gets one more
// refresh first, and lands with nobody involved. Nothing about the bound
// changes -- one repair, and one refresh per repair round.
func TestMergeQueueRefreshesARepairedHeadTheQueueItselfLeftBehind(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	const owner, repoName = "acme", "behind"
	w.newRepo(owner, repoName)
	repo := model.RepoRef{Owner: owner, Name: repoName}

	bare := filepath.Join(w.upstreamDir, owner, repoName+".git")
	sim := githubsim.New(owner, repoName, bare, "main")
	client := github.NewClient(sim, nil)
	deps := orchestrator.Deps{Store: w.store, Client: client, Sandboxes: worldSandboxes{w}, MaxWorkers: 1}

	task1 := model.Task{
		ID: "t1", Intent: model.IntentImplement, Title: "task1",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human("alice")}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human("alice")},
		Target:   &repo, Binding: model.BindingDirective, AutoMerge: true, CreatedAt: &baseTime,
	}
	if err := w.store.PutTask(w.ctx, task1); err != nil {
		t.Fatal(err)
	}
	laterCreated := baseTime.Add(time.Second)
	task2 := model.Task{
		ID: "t2", Intent: model.IntentImplement, Title: "task2",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human("bob")}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human("bob")},
		Target:   &repo, Binding: model.BindingDirective, AutoMerge: true, CreatedAt: &laterCreated,
	}
	if err := w.store.PutTask(w.ctx, task2); err != nil {
		t.Fatal(err)
	}

	branch1, branch2 := model.BranchName("t1"), model.BranchName("t2")
	clock := baseTime

	deps.Framework = scriptedFramework(configPushScript(w.remote(owner, repoName), branch1, "setting: from-task1"))
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (t1 push): %v", err)
	}
	clock = clock.Add(time.Minute)
	deps.Framework = scriptedFramework(cleanPushScript(w.remote(owner, repoName), branch2, "t2"))
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (t2 push): %v", err)
	}
	if len(sim.PullRequests) != 2 {
		t.Fatalf("expected two pull requests, got %+v", sim.PullRequests)
	}

	// A change lands on main that genuinely conflicts with t1's branch.
	w.pushConflictingCommit(owner, repoName, "main", "CONFIG.md", "setting: from-main", "unrelated change conflicting with t1")
	if !w.realConflict(owner, repoName, "main", branch1) {
		t.Fatal("expected a genuine git conflict between main and task1's branch")
	}
	setMergeable(sim, branch1, false)
	setMergeable(sim, branch2, true)

	// The head is conflicted, so the queue tries the merge itself, watches
	// git refuse it -- that is its one refresh for this round -- and asks
	// t1 to repair its own branch. t2 is green and unrelated, so it is
	// merged past the head being repaired rather than waiting it out.
	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (asks for the repair): %v", err)
	}
	obs1, err := w.store.GetObservation(w.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !obs1.RepairInFlight() {
		t.Fatalf("observation = %+v, want t1 sent back for repair", obs1)
	}
	askedAt := obs1.MergeQueueRepairAt
	if !sim.PullRequests[1].Merged {
		t.Fatalf("t2 should have merged past the conflicted head while it was being repaired, got %+v", sim.PullRequests[1])
	}

	// The repair runs and really resolves the conflict on t1's own branch.
	clock = clock.Add(time.Minute)
	deps.Framework = scriptedFramework(resolveScript(w.remote(owner, repoName), branch1))
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (dispatch the repair): %v", err)
	}
	if w.realConflict(owner, repoName, "main", branch1) {
		t.Fatal("the repair did not resolve the conflict it was sent to resolve")
	}
	setMergeable(sim, branch1, true)

	// And then the base moves again, which is the whole scenario: another
	// change merged past this head while it was being repaired, so the
	// resolution that just landed is judged against a main it has never
	// seen, and the checks that ran on it are red about a tree nobody
	// would merge.
	w.pushConflictingCommit(owner, repoName, "main", "LATER.md", "landed while t1 was repairing", "another change, merged past the head being repaired")
	failure := "failure"
	sim.CheckRuns[branch1] = []github.CheckRun{{Name: "ci", Status: "completed", Conclusion: &failure}}

	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (judges the repair): %v", err)
	}

	// No person: the queue merged main in one last time instead.
	obs1, err = w.store.GetObservation(w.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if obs1.MergeQueueBlockedAt != nil {
		t.Fatal("escalated a repaired head that had only fallen behind the base again")
	}
	if got := w.fileAt(owner, repoName, branch1, "LATER.md"); got != "landed while t1 was repairing" {
		t.Fatalf("LATER.md on %s = %q, want main's later change carried in by the second refresh", branch1, got)
	}
	comments1, err := w.store.Comments(w.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	last := comments1[len(comments1)-1].Body
	if !strings.Contains(last, "already been spent") {
		t.Fatalf("the second refresh's comment does not say the repair is spent:\n%s", last)
	}

	// CI re-runs against the tree that would actually land, and passes.
	success := "success"
	sim.CheckRuns[branch1] = []github.CheckRun{{Name: "ci", Status: "completed", Conclusion: &success}}
	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (merges the refreshed head): %v", err)
	}
	if st, err := w.store.State(w.ctx, "t1"); err != nil || st != model.StateClosed {
		t.Fatalf("t1 state = %q (%v), want closed -- the resolution was right, only behind", st, err)
	}
	if !w.branchContains(owner, repoName, "main", branch1) {
		t.Fatal("main does not contain t1's branch: it never really merged")
	}
	if got := w.fileAt(owner, repoName, "main", "CONFIG.md"); got != "setting: from-task1" {
		t.Fatalf("main:CONFIG.md = %q, want the repair's own resolution", got)
	}

	// And the bound held: one repair, never a second, however many
	// refreshes it took.
	obs1, err = w.store.GetObservation(w.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if obs1.MergeQueueRepairAt == nil || !obs1.MergeQueueRepairAt.Equal(*askedAt) {
		t.Fatalf("MergeQueueRepairAt = %v, want the one repair asked for at %v", obs1.MergeQueueRepairAt, askedAt)
	}
	if obs1.MergeQueueBlockedAt != nil {
		t.Fatal("t1 was escalated to a person despite merging")
	}
}

func TestMergeQueueRepairsFailingCheckRunsNotJustConflicts(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	const owner, repoName = "acme", "checks"
	w.newRepo(owner, repoName)
	repo := model.RepoRef{Owner: owner, Name: repoName}

	bare := filepath.Join(w.upstreamDir, owner, repoName+".git")
	sim := githubsim.New(owner, repoName, bare, "main")
	client := github.NewClient(sim, nil)
	deps := orchestrator.Deps{Store: w.store, Client: client, Sandboxes: worldSandboxes{w}, MaxWorkers: 1}

	task1 := model.Task{
		ID: "t1", Intent: model.IntentImplement, Title: "task1",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human("alice")}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human("alice")},
		Target:   &repo, Binding: model.BindingDirective, AutoMerge: true, CreatedAt: &baseTime,
	}
	if err := w.store.PutTask(w.ctx, task1); err != nil {
		t.Fatal(err)
	}

	branch1 := model.BranchName("t1")
	clock := baseTime
	deps.Framework = scriptedFramework(cleanPushScript(w.remote(owner, repoName), branch1, "t1"))
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (push): %v", err)
	}
	if st, err := w.store.State(w.ctx, "t1"); err != nil || st != model.StateCompleted {
		t.Fatalf("t1 state = %q (%v), want completed", st, err)
	}

	// Genuinely mergeable at the git level throughout -- what breaks this
	// pull request is a failing CI check, not a conflict.
	setMergeable(sim, branch1, true)
	failure := "failure"
	sim.CheckRuns[branch1] = []github.CheckRun{{Name: "ci", Status: "completed", Conclusion: &failure}}

	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (asks for a repair of failing checks): %v", err)
	}
	obs1, err := w.store.GetObservation(w.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !obs1.RepairInFlight() {
		t.Fatalf("observation = %+v, want t1 sent back for repair after a failing check", obs1)
	}
	comments1, err := w.store.Comments(w.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	foundReason := false
	for _, c := range comments1 {
		if strings.Contains(c.Body, "checks are failing") {
			foundReason = true
		}
	}
	if !foundReason {
		t.Fatalf("expected the repair comment to name failing checks, got %+v", comments1)
	}

	// The repair is dispatched and pushes to task1's own branch --
	// content does not matter here, since this test's own sim.CheckRuns
	// is what stands in for CI.
	clock = clock.Add(time.Minute)
	deps.Framework = scriptedFramework(repairPushScript(w.remote(owner, repoName), branch1))
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (dispatch the repair): %v", err)
	}
	if st, err := w.store.State(w.ctx, "t1"); err != nil || st != model.StateCompleted {
		t.Fatalf("t1 state = %q (%v), want completed once the repair ran", st, err)
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("the repair opened a second pull request: %+v", sim.PullRequests)
	}

	// CI re-runs against what the repair pushed -- on the same pull
	// request, which is the whole saving -- and passes. The next cycle
	// merges it.
	success := "success"
	sim.CheckRuns[branch1] = []github.CheckRun{{Name: "ci", Status: "completed", Conclusion: &success}}
	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (t1 merges): %v", err)
	}
	if st, err := w.store.State(w.ctx, "t1"); err != nil || st != model.StateClosed {
		t.Fatalf("t1 state = %q (%v), want closed", st, err)
	}
	obs1, err = w.store.GetObservation(w.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if obs1 != nil && obs1.MergeQueueBlockedAt != nil {
		t.Fatal("t1 should never have been escalated -- the automatic repair genuinely resolved it before any cycle ever read it as still broken")
	}
}

func TestClosingATaskWithAnOpenPullRequestDropsItFromTheMergeQueueForGood(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	const owner, repoName = "acme", "declined"
	w.newRepo(owner, repoName)
	repo := model.RepoRef{Owner: owner, Name: repoName}

	bare := filepath.Join(w.upstreamDir, owner, repoName+".git")
	sim := githubsim.New(owner, repoName, bare, "main")
	client := github.NewClient(sim, nil)

	task := model.Task{
		ID: "t-closed-early", Intent: model.IntentImplement, Title: "closed before merge",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human("gina")}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human("gina")},
		Target:   &repo, Binding: model.BindingDirective, AutoMerge: true, CreatedAt: &baseTime,
	}
	if err := w.store.PutTask(w.ctx, task); err != nil {
		t.Fatal(err)
	}

	branch := model.BranchName("t-closed-early")
	deps := orchestrator.Deps{
		Store: w.store, Client: client, Sandboxes: worldSandboxes{w}, MaxWorkers: 1,
		Framework: scriptedFramework(pushScript(w.remote(owner, repoName), branch, "t-closed-early")),
	}
	clock := baseTime
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (push): %v", err)
	}
	assertState(w, "t-closed-early", model.StateCompleted, false)
	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected one pull request, got %+v", sim.PullRequests)
	}

	// A human closes the task before anyone merges its pull request --
	// the same effect ui.Client.Close (grain close) has on Observation.
	// ClosedAt, applied directly here since this test is about
	// SyncPullRequests' own reaction, not the CLI verb itself
	// (cli_verbs_test.go covers that).
	clock = clock.Add(time.Minute)
	if err := w.store.ObserveField(w.ctx, "t-closed-early", clock, func(o *model.Observation) { o.ClosedAt = &clock }); err != nil {
		t.Fatal(err)
	}
	assertState(w, "t-closed-early", model.StateClosed, false)

	// The pull request would merge cleanly if grain ever looked at it
	// again -- which is exactly the point: it must not.
	setMergeable(sim, branch, true)

	for i := 0; i < 3; i++ {
		clock = clock.Add(time.Minute)
		if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
			t.Fatalf("RunCycle (tick %d after close): %v", i, err)
		}
		assertState(w, "t-closed-early", model.StateClosed, false)
		if sim.PullRequests[0].State != "open" {
			t.Fatalf("tick %d: pull request state = %q, want still open -- a closed task must never auto-merge", i, sim.PullRequests[0].State)
		}
	}
	if w.branchContains(owner, repoName, "main", branch) {
		t.Fatal("main must never have absorbed a task that was closed before its pull request merged")
	}
}

func TestAutoMergeLeavesAPullRequestOfUnknownMergeabilityAloneUntilItResolves(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	const owner, repoName = "acme", "pending"
	w.newRepo(owner, repoName)
	repo := model.RepoRef{Owner: owner, Name: repoName}

	bare := filepath.Join(w.upstreamDir, owner, repoName+".git")
	sim := githubsim.New(owner, repoName, bare, "main")
	client := github.NewClient(sim, nil)

	task := model.Task{
		ID: "t-pending", Intent: model.IntentImplement, Title: "still computing",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human("hank")}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human("hank")},
		Target:   &repo, Binding: model.BindingDirective, AutoMerge: true, CreatedAt: &baseTime,
	}
	if err := w.store.PutTask(w.ctx, task); err != nil {
		t.Fatal(err)
	}

	branch := model.BranchName("t-pending")
	deps := orchestrator.Deps{
		Store: w.store, Client: client, Sandboxes: worldSandboxes{w}, MaxWorkers: 1,
		Framework: scriptedFramework(pushScript(w.remote(owner, repoName), branch, "t-pending")),
	}
	clock := baseTime
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (push): %v", err)
	}
	assertState(w, "t-pending", model.StateCompleted, false)
	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected one pull request, got %+v", sim.PullRequests)
	}
	if sim.PullRequests[0].Mergeable != nil {
		t.Fatalf("expected a freshly opened pull request's mergeability to read unknown, got %+v", sim.PullRequests[0].Mergeable)
	}

	// Two more ticks with mergeability still unresolved: neither merges
	// prematurely nor files a spurious fix, unlike a real conflict or a
	// real failing check would.
	for i := 0; i < 2; i++ {
		clock = clock.Add(time.Minute)
		if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
			t.Fatalf("RunCycle (tick %d, still unknown): %v", i, err)
		}
		assertState(w, "t-pending", model.StateCompleted, false)
		if sim.PullRequests[0].State != "open" {
			t.Fatalf("tick %d: pull request state = %q, want still open", i, sim.PullRequests[0].State)
		}
	}
	obs, err := w.store.GetObservation(w.ctx, "t-pending")
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil && obs.MergeQueueRepairAt != nil {
		t.Fatal("asked for a repair while mergeability was merely unknown")
	}
	comments, err := w.store.Comments(w.ctx, "t-pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 0 {
		t.Fatalf("expected no merge-queue commentary while mergeability was merely unknown, got %+v", comments)
	}

	// GitHub finishes computing it: clean. Now it merges.
	setMergeable(sim, branch, true)
	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (resolves clean): %v", err)
	}
	assertState(w, "t-pending", model.StateClosed, false)
	if sim.PullRequests[0].State != "closed" {
		t.Fatalf("pull request state = %q, want closed (merged)", sim.PullRequests[0].State)
	}
}
