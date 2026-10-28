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

// The merge queue's cheapest repair: merging a queue head's own base
// branch back into it before deciding that anything is wrong with it.
// docs/merge-queue-staleness.md is the design; what these tests hold to is
// its central claim -- that a head whose checks last ran against a base
// that has since moved is fixed by a merge the queue can make itself, and
// that the one case a fix task is genuinely for (a real conflict) still
// files one, now naming the conflict the queue watched GitHub refuse.
//
// Everything here runs against githubsim over a real bare repository, so
// "behind", "up to date" and "conflicts" are answered by real git rather
// than by a flag a test set: the whole point of asking GitHub to make the
// merge is that its answer is authoritative, and a double that made that
// answer up would prove nothing.
package orchestrator_test

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// pushFileToBranch lands one commit writing path on an existing branch of
// bare -- how these tests move `main` out from under an already-open pull
// request, and how they give two branches something to genuinely conflict
// over (pushBranch's own empty commits never can).
func pushFileToBranch(t *testing.T, bare, branch, path, content, message string) {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "clone", "-q", "--branch", branch, bare, "work")
	wd := filepath.Join(dir, "work")
	run(t, wd, "git", "config", "user.email", "human@example.com")
	run(t, wd, "git", "config", "user.name", "human")
	full := filepath.Join(wd, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wd, "git", "add", path)
	run(t, wd, "git", "commit", "-q", "-m", message)
	run(t, wd, "git", "push", "-q", "origin", branch)
}

// branchContains reports whether branch's history really holds other's
// tip, read straight off the bare repo -- the test's own way of checking
// that a refresh moved the branch, independent of anything the store or
// the sim believes happened.
func branchContains(t *testing.T, bare, branch, other string) bool {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", bare, "merge-base", "--is-ancestor",
		"refs/heads/"+other, "refs/heads/"+branch)
	return cmd.Run() == nil
}

// mergeWrites is how many branch-into-branch merges the queue has asked
// GitHub for -- POST /repos/{owner}/{repo}/merges, the one write
// refreshStaleHead makes. Counted off Sim's own call log rather than a
// wrapper client, so it counts what actually went over the seam.
func mergeWrites(sim *githubsim.Sim, repo model.RepoRef) int {
	n := 0
	for _, c := range sim.Calls {
		if c.Method == "POST" && c.Path == "/repos/"+repo.Owner+"/"+repo.Name+"/merges" {
			n++
		}
	}
	return n
}

// A failing head that is merely behind is the majority case this whole
// design exists for: its checks ran against a base that has moved, so the
// failure they report may be about a tree nobody would ever merge. The
// queue merges the base in, says so, and leaves the verdict to the next
// cycle -- no fix task, no agent run, one API call.
//
// And then it stops. The refresh gets exactly one attempt per pull
// request, so a branch that is still red once it is up to date gets the
// fix task it always would have, this time describing a failure measured
// against the base it will actually merge into.
func TestMergeQueueMergesTheBaseIntoAStaleHeadBeforeFilingAnyFix(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	// main moves on after the pull request was opened, and CI is red for
	// the commit that was judged against the older main.
	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "go", Status: "completed", Conclusion: strPtr("failure")},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests over a stale failing head: %v", err)
	}

	if n := mergeWrites(sim, repo); n != 1 {
		t.Fatalf("the queue made %d branch merges, want exactly one", n)
	}
	if !branchContains(t, sim.BareRepo, branch, "main") {
		t.Fatal("the head branch does not contain main: the refresh never actually merged anything")
	}
	if repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("asked for a repair of a head that was only behind its base")
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueRefreshedAt == nil {
		t.Fatal("the refresh was not recorded, so the next cycle would make it again")
	}
	// And counted, for the merges report: the rebase, and the suite it
	// sent the change back through -- and the moment the queue took the
	// change on, which its time in the queue is measured from.
	if got, want := contentionCounts(t, ctx, store), map[string]int{
		model.MergeQueueRebase + "/" + model.MergeQueueStaleHead:   1,
		model.MergeQueueRetest + "/" + model.MergeQueueAfterRebase: 1,
	}; !maps.Equal(got, want) {
		t.Fatalf("contention recorded = %v, want %v", got, want)
	}
	if obs.MergeQueueEnteredAt == nil || !obs.MergeQueueEnteredAt.Equal(baseTime) {
		t.Fatalf("MergeQueueEnteredAt = %v, want the cycle that first drove it (%v)", obs.MergeQueueEnteredAt, baseTime)
	}
	// Not optional: a human working in this branch who did not want a
	// merge commit has one now, and this is the only thing that says who
	// made it.
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment saying the queue merged the base in, got %q", bodies)
	}
	if !strings.Contains(bodies[0], "main") {
		t.Errorf("the comment does not name the branch that was merged in:\n%s", bodies[0])
	}

	// Still red once it is up to date: the failure is real, so the repair
	// is asked for now -- against the current base -- and no second merge
	// is attempted.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests after the refresh: %v", err)
	}
	if n := mergeWrites(sim, repo); n != 1 {
		t.Fatalf("the queue made %d branch merges, want still exactly one", n)
	}
	asked := repairAskedAt(t, ctx, store, task.ID)
	bodies = commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 2 {
		t.Fatalf("expected the merge comment plus the repair comment, got %q", bodies)
	}
	if !strings.Contains(bodies[1], "its checks are failing (`go`)") {
		t.Errorf("the repair comment does not name the failure it was asked for:\n%s", bodies[1])
	}

	// A third cycle changes nothing: one refresh, one repair, then it is
	// a person's problem.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests a cycle later: %v", err)
	}
	if n := mergeWrites(sim, repo); n != 1 {
		t.Fatalf("the queue made %d branch merges, want still exactly one", n)
	}
	if again := repairAskedAt(t, ctx, store, task.ID); !again.Equal(asked) {
		t.Fatalf("MergeQueueRepairAt moved from %v to %v: a second repair was asked for", asked, again)
	}
	if got := contentionCounts(t, ctx, store)[model.MergeQueueRebase+"/"+model.MergeQueueStaleHead]; got != 1 {
		t.Fatalf("%d rebase(s) recorded after one refresh, want 1", got)
	}
	// The entry stamp is the first cycle's, kept through the repair.
	if obs, err := store.GetObservation(ctx, task.ID); err != nil || obs.MergeQueueEnteredAt == nil ||
		!obs.MergeQueueEnteredAt.Equal(baseTime) {
		t.Fatalf("MergeQueueEnteredAt moved to %v (err %v), want it kept at %v", obs.MergeQueueEnteredAt, err, baseTime)
	}
}

// contentionCounts is the merge queue's contention history as
// "kind/reason" -> how many.
func contentionCounts(t *testing.T, ctx context.Context, store *model.Store) map[string]int {
	t.Helper()
	events, err := store.MergeQueueContentionEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, e := range events {
		out[e.Kind+"/"+e.Reason]++
	}
	return out
}

// GitHub's 204 is the whole answer to "is this failure genuine": if the
// head branch already contains its base then whatever CI reported, it
// reported about the tree that would actually merge. So the repair is
// asked for in the very same cycle, exactly as it was before any of this
// existed -- and nothing is recorded as refreshed, because nothing was.
func TestMergeQueueAsksForTheRepairAtOnceWhenTheHeadIsAlreadyUpToDate(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	// main has not moved: the branch was cut from it and still contains
	// it, so there is nothing for a merge to bring in.
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "go", Status: "completed", Conclusion: strPtr("failure")},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests over an up-to-date failing head: %v", err)
	}

	if n := mergeWrites(sim, repo); n != 1 {
		t.Fatalf("the queue made %d branch merges, want exactly one (the ask that answered 204)", n)
	}
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("no repair was asked for a genuine failure on an up-to-date head")
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueRefreshedAt != nil {
		t.Fatal("recorded a refresh for a branch nothing was merged into")
	}
	// The one comment is the repair one -- there was no merge to
	// announce.
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 1 {
		t.Fatalf("expected exactly the repair comment, got %q", bodies)
	}

	// And the queue does not come back: the repair already in flight is
	// what stops it, so no second merge is asked for.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests a cycle later: %v", err)
	}
	if n := mergeWrites(sim, repo); n != 1 {
		t.Fatalf("the queue made %d branch merges, want still exactly one", n)
	}
}

// The case a repair is genuinely for. The queue tries the merge, GitHub
// refuses it with a real conflict, and the task is sent back straight
// away -- with the conflict named as something the queue watched happen
// rather than inferred from a Mergeable flag, so the agent sent to repair
// it starts from "resolve this" rather than "merge main in", which is
// what the queue has just proved will not work.
func TestMergeQueueAsksForARepairNamingTheConflictItsOwnMergeHit(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	// Both sides touch the same file: a merge of one into the other
	// really does conflict, and the sim really does run the merge.
	pushFileToBranch(t, sim.BareRepo, branch, "CONFIG.md", "setting: from-the-task", "the task's own change")
	pushFileToBranch(t, sim.BareRepo, "main", "CONFIG.md", "setting: from-main", "an unrelated change on main")
	setMergeable(sim, false)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests over a genuinely conflicted head: %v", err)
	}

	if n := mergeWrites(sim, repo); n != 1 {
		t.Fatalf("the queue made %d branch merges, want exactly one", n)
	}
	if branchContains(t, sim.BareRepo, branch, "main") {
		t.Fatal("a conflicting merge landed anyway")
	}
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("no repair was asked for a head the queue could not merge")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment asking for the repair, got %q", bodies)
	}
	if !strings.Contains(bodies[0], "conflicted") || !strings.Contains(bodies[0], "real resolution") {
		t.Errorf("the repair comment does not say the queue's own merge conflicted:\n%s", bodies[0])
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueRefreshedAt == nil {
		t.Fatal("the attempt was not recorded: a conflict would be re-attempted every cycle")
	}

	// One attempt, however many cycles pass.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests a cycle later: %v", err)
	}
	if n := mergeWrites(sim, repo); n != 1 {
		t.Fatalf("the queue made %d branch merges, want still exactly one", n)
	}
}

// The refresh is the queue head's alone, and only while the queue is
// still driving it. Everything else the sync loop passes over -- a head
// whose repair is already in flight, a task waiting its turn behind one,
// a legacy fix task's own stacked pull request, a task the queue has
// given up on -- is a branch this write has no business landing on: it
// would be a merge commit nobody asked for on a branch the queue is not
// steering, and for a fix task's own branch it would be a merge into a
// base that is itself moving.
func TestMergeQueueRefreshesNothingButTheHeadItIsStillDriving(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	head, headBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	behind, behindBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	blocked, blockedBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t3", repo)
	fix, fixBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t4", repo)

	// The head already has its automatic repair in flight, and t4 stands
	// in for the queue-ineligible fix task an older database may still
	// carry.
	if err := store.ObserveField(ctx, head.ID, baseTime, func(o *model.Observation) {
		o.MergeQueueRepairAt = &baseTime
		o.CompletedAt = nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTask(ctx, fix.ID, func(tk *model.Task) error {
		tk.Origin.Reason = model.ReasonFix
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// And t3 is one the queue has already given up on.
	if err := store.Observe(ctx, model.Observation{
		TaskID: blocked.ID, CompletedAt: &baseTime, MergeQueueBlockedAt: &baseTime,
	}); err != nil {
		t.Fatal(err)
	}

	// Every one of them is behind main and failing, so the only thing
	// keeping the queue off them is what it is allowed to drive.
	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	setMergeable(sim, true)
	for _, branch := range []string{headBranch, behindBranch, blockedBranch, fixBranch} {
		sim.CheckRuns[branch] = []github.CheckRun{
			{Name: "go", Status: "completed", Conclusion: strPtr("failure")},
		}
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if n := mergeWrites(sim, repo); n != 0 {
		t.Fatalf("the queue made %d branch merges, want none at all", n)
	}
	for _, id := range []string{head.ID, behind.ID, blocked.ID, fix.ID} {
		obs, err := store.GetObservation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if obs != nil && obs.MergeQueueRefreshedAt != nil {
			t.Errorf("task %s was refreshed", id)
		}
	}
}

// repairRan is the store side of an agent run that has finished repairing
// its own branch: CompletedAt set again, later than the repair was asked
// for, which is what RepairInFlight reads as "the repair is done, judge
// it". The push itself is a separate pushFileToBranch, since these tests
// dispatch nobody -- tests/e2e drives the real run.
func repairRan(t *testing.T, ctx context.Context, store *model.Store, taskID string, at time.Time) {
	t.Helper()
	if err := store.ObserveField(ctx, taskID, at, func(o *model.Observation) {
		o.CompletedAt = &at
	}); err != nil {
		t.Fatal(err)
	}
}

// blockedOnAPerson is Observation.MergeQueueBlockedAt read fresh: the
// queue has given up and asked a human.
func blockedOnAPerson(t *testing.T, ctx context.Context, store *model.Store, taskID string) bool {
	t.Helper()
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	return obs != nil && obs.MergeQueueBlockedAt != nil
}

// The interaction the pass-over rule made likely: while a head is being
// repaired the queue keeps merging the changes behind it, so the base a
// repair is judged against has moved -- by the queue's own doing -- since
// the repair was asked for. A resolution that was right when it was
// pushed can be merely behind by the time it is read, and escalating it
// hands a person a branch whose whole problem is one merge commit.
//
// So the repaired head gets one more refresh before escalateToUser, on
// exactly the terms the first one had: the queue asks GitHub to merge the
// base in and reads the answer off what GitHub does. Here it lands, CI
// re-runs against a tree somebody would actually merge, and the change
// goes in with no person involved.
func TestMergeQueueRefreshesARepairedHeadOnceMoreBeforeAskingAPerson(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	// Round one, unchanged: main has moved, the head is red against the
	// older main, the queue merges main in and waits a cycle.
	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "go", Status: "completed", Conclusion: strPtr("failure")},
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests (refresh): %v", err)
	}
	// Still red once it is up to date, so the failure was genuine and the
	// one repair this pull request gets is spent on it.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests (asks for the repair): %v", err)
	}
	asked := repairAskedAt(t, ctx, store, task.ID)
	if n := mergeWrites(sim, repo); n != 1 {
		t.Fatalf("the queue made %d branch merges before the repair, want exactly one", n)
	}

	// The repair runs and pushes a real fix to the branch under review --
	// and while it was running, the queue merged somebody else's change
	// past this head, which is what passing over a head being repaired
	// does. So the fix is judged against a main it has never seen.
	pushFileToBranch(t, sim.BareRepo, branch, "FIX.md", "the repair's own work", "repair for t1")
	pushFileToBranch(t, sim.BareRepo, "main", "OTHER.md", "a change that merged past this head",
		"another queued change, merged while t1 was being repaired")
	repairRan(t, ctx, store, task.ID, baseTime.Add(2*time.Minute))

	// The cycle that judges the repair. It does not escalate: it merges
	// the base in one last time and leaves the verdict to the next cycle.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(3*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests (judges the repair): %v", err)
	}
	if blockedOnAPerson(t, ctx, store, task.ID) {
		t.Fatal("escalated a repaired head that had only fallen behind its base again")
	}
	if n := mergeWrites(sim, repo); n != 2 {
		t.Fatalf("the queue made %d branch merges, want two: one per repair round", n)
	}
	if !branchContains(t, sim.BareRepo, branch, "main") {
		t.Fatal("the second refresh never actually merged anything")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 3 {
		t.Fatalf("expected the first merge, the repair, and the second merge, got %q", bodies)
	}
	// And it says what the first one could not: there is no second repair
	// behind this, so a red verdict next cycle is a person's.
	if !strings.Contains(bodies[2], "needs a person") {
		t.Errorf("the second merge comment does not say what happens if it is still red:\n%s", bodies[2])
	}

	// CI re-runs against the tree that would actually land, and passes:
	// the resolution was right all along, and it merges.
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "go", Status: "completed", Conclusion: strPtr("success")},
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(4*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests (merges the refreshed repair): %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatal("the repaired head never merged")
	}
	if blockedOnAPerson(t, ctx, store, task.ID) {
		t.Fatal("a head that merged was escalated to a person anyway")
	}
	if again := repairAskedAt(t, ctx, store, task.ID); !again.Equal(asked) {
		t.Fatalf("MergeQueueRepairAt moved from %v to %v: a second repair was asked for", asked, again)
	}
}

// The other half of the same rule: the extra refresh is one attempt, not
// an extra chance every cycle. A repaired head that is still red once it
// is up to date has answered the question -- the failure is not
// staleness. Here the base moved while the first repair ran, which is the
// one thing that buys a second automatic repair (secondRepairReason); a
// head still red after that is handed to a person, and no third repair
// and no further merge is attempted however long it sits there.
//
// This is what holds the whole sequence to a bound: at most two agent
// runs per pull request, whatever the base does in between.
func TestMergeQueueEscalatesAfterTheSecondRefreshInsteadOfRepairingAgain(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "go", Status: "completed", Conclusion: strPtr("failure")},
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests (refresh): %v", err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests (asks for the repair): %v", err)
	}
	asked := repairAskedAt(t, ctx, store, task.ID)

	pushFileToBranch(t, sim.BareRepo, branch, "FIX.md", "an attempt", "attempted repair for t1")
	pushFileToBranch(t, sim.BareRepo, "main", "OTHER.md", "a change that merged past this head",
		"another queued change, merged while t1 was being repaired")
	repairRan(t, ctx, store, task.ID, baseTime.Add(2*time.Minute))

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(3*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests (judges the repair): %v", err)
	}
	if n := mergeWrites(sim, repo); n != 2 {
		t.Fatalf("the queue made %d branch merges, want two", n)
	}

	// Up to date and still red: the failure is real, and the repair that
	// was meant to fix it has already run.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(4*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests (escalates): %v", err)
	}
	// main moved while the first repair ran, so a second is allowed --
	// and says it is the last.
	second := repairAskedAt(t, ctx, store, task.ID)
	if second.Equal(asked) {
		t.Fatal("main moved since the first repair, but no second repair was asked for")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if last := bodies[len(bodies)-1]; !strings.Contains(last, "automatic repair 2 of 2") ||
		!strings.Contains(last, "new commits since the first repair") {
		t.Errorf("the second repair does not say which attempt it is, or why:\n%s", last)
	}
	repairRan(t, ctx, store, task.ID, baseTime.Add(5*time.Minute))
	for i := 6; i < 8; i++ {
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("SyncPullRequests (judges the second repair, tick %d): %v", i, err)
		}
	}
	if !blockedOnAPerson(t, ctx, store, task.ID) {
		t.Fatal("a head that has had both its repairs was not handed to a person")
	}
	if again := repairAskedAt(t, ctx, store, task.ID); !again.Equal(second) {
		t.Fatalf("MergeQueueRepairAt moved from %v to %v: a third repair was asked for", second, again)
	}
	bodies = commentBodies(t, ctx, store, task.ID)
	if !strings.Contains(bodies[len(bodies)-1], "needs a person") {
		t.Errorf("the last comment is not the escalation:\n%s", bodies[len(bodies)-1])
	}

	// And nothing further is attempted: the queue is off this head, and
	// the refresh it might otherwise re-ask for every cycle is spent.
	for i := 8; i < 11; i++ {
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("SyncPullRequests (settling, tick %d): %v", i, err)
		}
	}
	// One refresh per repair round (refreshSpent): the first, the one
	// after repair 1, and the one after repair 2 -- and no more.
	if n := mergeWrites(sim, repo); n != 3 {
		t.Fatalf("the queue made %d branch merges in the end, want exactly three", n)
	}
}
