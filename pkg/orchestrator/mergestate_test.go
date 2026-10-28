package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// greenChecks is one passing check, which is all it takes for a pull
// request in these tests to read clean.
func greenChecks() []github.CheckRun {
	return []github.CheckRun{{Name: "tests", Status: "completed", Conclusion: strPtr("success")}}
}

// mergeQueueStatus reads back what the queue says it is doing about a
// task and why -- the pair a person sees on the row.
func mergeQueueStatus(t *testing.T, ctx context.Context, store *model.Store, taskID string) model.Observation {
	t.Helper()
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil {
		t.Fatalf("task %s has no observation at all", taskID)
	}
	return *obs
}

// A merge GitHub refuses is on the row from the first cycle, in GitHub's
// own words, and dated, so a person can see it standing still. What
// happens after that depends on what it was refused for -- see
// mergerefusal_test.go.
func TestSyncPullRequestsSaysWhenGitHubRefusesTheMerge(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "refused", "main")
	repo := model.RepoRef{Owner: "acme", Name: "refused"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenChecks()
	sim.RefuseMerges = "Something on GitHub's side said no."

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err == nil {
		t.Fatal("expected SyncPullRequests to report the refused merge")
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueState != "merge_failed" {
		t.Fatalf("merge queue state = %q, want merge_failed", obs.MergeQueueState)
	}
	if !strings.Contains(obs.MergeQueueNote, "said no") {
		t.Fatalf("note = %q, want GitHub's own refusal in it", obs.MergeQueueNote)
	}
	if obs.MergeQueueStateAt == nil || !obs.MergeQueueStateAt.Equal(baseTime) {
		t.Fatalf("state stamped %v, want %v: the row has to say how long this has stood", obs.MergeQueueStateAt, baseTime)
	}
	// One refusal is a race, not a rule: nobody is asked for anything
	// yet, and the entry is still its repository's queue head.
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 0 {
		t.Fatalf("expected nothing said to anyone on the first refusal, got %q", bodies)
	}
	if obs.MergeQueueBlockedAt != nil {
		t.Fatal("the queue gave up on the very first refusal")
	}

	// A second refusal, after the backoff, is the same wait going on: the
	// note counts up, and the clock does not restart.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err == nil {
		t.Fatal("expected the second refused merge to be reported too")
	}
	obs = mergeQueueStatus(t, ctx, store, task.ID)
	if !strings.Contains(obs.MergeQueueNote, "2 times") {
		t.Fatalf("note = %q, want the count of refusals in it", obs.MergeQueueNote)
	}
	if obs.MergeQueueStateAt == nil || !obs.MergeQueueStateAt.Equal(baseTime) {
		t.Fatalf("state stamped %v, want it still %v: the wait did not restart", obs.MergeQueueStateAt, baseTime)
	}
}

// A change that is clean and simply not next says so. This is the wait
// the queue is *supposed* to make -- one merge per repository per cycle,
// in order -- and the only thing wrong with it was that the row said
// nothing at all while it lasted, which is indistinguishable from a queue
// that has stopped.
func TestSyncPullRequestsSaysWhoAChangeIsWaitingBehind(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "queued", "main")
	repo := model.RepoRef{Owner: "acme", Name: "queued"}
	head, headBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	setMergeable(sim, true)
	sim.CheckRuns[headBranch] = greenChecks()
	sim.CheckRuns[secondBranch] = greenChecks()

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	obs := mergeQueueStatus(t, ctx, store, second.ID)
	if obs.MergeQueueState != "waiting_queue" {
		t.Fatalf("merge queue state = %q, want waiting_queue", obs.MergeQueueState)
	}
	if !strings.Contains(obs.MergeQueueNote, head.ID) {
		t.Fatalf("note = %q, want the task it is waiting behind named", obs.MergeQueueNote)
	}
}

// The wait for CI names the checks it is waiting on, rather than leaving
// "Waiting for CI" to stand for hours over a check nobody can name
// without opening GitHub.
func TestSyncPullRequestsNamesTheChecksItIsWaitingFor(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "waiting", "main")
	repo := model.RepoRef{Owner: "acme", Name: "waiting"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "lint", Status: "completed", Conclusion: strPtr("success")},
		{Name: "tests", Status: "in_progress"},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueState != "waiting_ci" {
		t.Fatalf("merge queue state = %q, want waiting_ci", obs.MergeQueueState)
	}
	if !strings.Contains(obs.MergeQueueNote, "tests") {
		t.Fatalf("note = %q, want the unfinished check named", obs.MergeQueueNote)
	}
	if strings.Contains(obs.MergeQueueNote, "lint") {
		t.Fatalf("note = %q, want only the checks still outstanding", obs.MergeQueueNote)
	}
	if obs.MergeQueueStateAt == nil || !obs.MergeQueueStateAt.Equal(baseTime) {
		t.Fatalf("state stamped %v, want %v", obs.MergeQueueStateAt, baseTime)
	}
}

// grain/task-149, which is why any of the rest of this exists: grain's
// own CI on the branch was cancelled twice, and the row said nothing at
// all for a day.
//
// Each half of the silence is worth keeping in one test, because neither
// alone produced it. A cancelled run is *finished*, so nothing was
// outstanding to wait on; it carries no passing verdict, so the pull
// request never read clean, so no merge was attempted and nothing was
// ever refused. The old statusOf had an arm for each of those two and
// nothing in between, and an entry between them fell out of the bottom
// with an empty status. The row now says which run it is waiting on, that
// the run is over, and that only another one will replace it.
func TestSyncPullRequestsSaysWhenGrainCIWasCancelled(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "cancelled", "main")
	repo := model.RepoRef{Owner: "acme", Name: "cancelled"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)

	// GitHub's own checks are green and finished, which is the half that
	// made this silent rather than merely slow: nothing there is
	// outstanding, so nothing there is waited on.
	greenOnGitHub(sim, branch)
	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(status.RunID, ci.ConclusionCancelled)
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if mergedBranch(sim, branch) {
		t.Fatal("a cancelled grain CI run is not a pass: this merged on a verdict nobody gave")
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueState != "waiting_ci" {
		t.Fatalf("merge queue state = %q, want waiting_ci: a cancelled run left the row silent",
			obs.MergeQueueState)
	}
	for _, want := range []string{"grain CI", "cancelled"} {
		if !strings.Contains(obs.MergeQueueNote, want) {
			t.Errorf("note = %q, want it to say %q", obs.MergeQueueNote, want)
		}
	}
	// Nothing is filed against anybody for it, and nothing is repaired:
	// a cancelled run says nothing about the branch (forge.HealthFrom's
	// "cancelled is neither"), and the wait is bounded by the stall
	// clock, which is the escalation this earns and the only one.
	if repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a cancelled run sent an agent to repair a branch nothing has said is broken")
	}
	if obs.MergeQueueBlockedAt != nil {
		t.Fatal("the queue gave up on the first cycle rather than waiting out the stall clock")
	}
}

// The other half of what task 149 cost, and the half a person noticed
// first: every task behind it in the same repository stood still too.
//
// A queue head that is still deciding holds its position on purpose --
// it resolves within a cycle or two, and merging past it would put a
// later change ahead of an earlier one on nothing better than being
// quicker to answer (mergeWalk). A cancelled verdict is not that. It
// reads PENDING, it is finished, and no cycle changes it; what was
// waiting behind it was waiting on a re-run nobody had asked for, for
// the two hours until the stall deadline gave up on it.
func TestSyncPullRequestsDoesNotHoldTheQueueOnACancelledVerdict(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "limbo", "main")
	repo := model.RepoRef{Owner: "acme", Name: "limbo"}
	head, headBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	head = grantCI(t, ctx, store, head)
	_, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	greenOnGitHub(sim, headBranch)
	greenOnGitHub(sim, secondBranch)
	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, head, repo, headBranch)
	runner.finish(status.RunID, ci.ConclusionCancelled)
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if mergedBranch(sim, headBranch) {
		t.Fatal("the head merged on a verdict that was cancelled rather than given")
	}
	if !mergedBranch(sim, secondBranch) {
		t.Fatal("the change behind it waited out a verdict that was never coming: " +
			"one task in limbo is holding its whole repository's queue")
	}
}

// pendingChecks is one check still running, which is how these tests hold
// a pull request in a state that has something to say and does not merge
// -- so that a read failing over it can be shown to change nothing.
func pendingChecks() []github.CheckRun {
	return []github.CheckRun{{Name: "tests", Status: "in_progress"}}
}

// flaky is GitHub having a bad minute, in the shape this deployment's own
// daemon log carried it every few hours through 16-17 September 2026.
func flaky() *githubsim.ReadFailure {
	return &githubsim.ReadFailure{Status: 502, Message: "Server Error"}
}

// One failed read says nothing to anybody, and this is the whole of the
// complaint that asked for it: every "grain could not read this pull
// request" an operator saw was a GitHub 5xx that the next cycle, ten
// seconds later, read perfectly well. The changes all merged. What was
// wrong was the sentence.
//
// So a cycle whose read failed leaves the row exactly as it found it --
// the state, the words and the age -- and still reports the failure to
// this process's log the way it always did.
func TestSyncPullRequestsSaysNothingAboutOneFailedRead(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "flake", "main")
	repo := model.RepoRef{Owner: "acme", Name: "flake"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = pendingChecks()
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	before := mergeQueueStatus(t, ctx, store, task.ID)
	if before.MergeQueueState != "waiting_ci" {
		t.Fatalf("merge queue state = %q, want waiting_ci to start from", before.MergeQueueState)
	}

	sim.FailReads = flaky()
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(10*time.Second)); err == nil {
		t.Fatal("expected the failed read to be reported to the cycle")
	}

	after := mergeQueueStatus(t, ctx, store, task.ID)
	if after.MergeQueueState != before.MergeQueueState || after.MergeQueueNote != before.MergeQueueNote {
		t.Fatalf("one 502 rewrote the row: %q/%q, was %q/%q",
			after.MergeQueueState, after.MergeQueueNote, before.MergeQueueState, before.MergeQueueNote)
	}
	if after.MergeQueueStateAt == nil || !after.MergeQueueStateAt.Equal(*before.MergeQueueStateAt) {
		t.Fatalf("one 502 restarted the age on the wait: %v, was %v",
			after.MergeQueueStateAt, before.MergeQueueStateAt)
	}
}

// Reads that go on failing are a different thing, and the state exists
// for it: a run that outlasts the deadline is on the row, with what
// GitHub said, how many reads it has been and for how long. The words are
// the transient ones -- grain is still asking, and will merge the moment
// it gets an answer -- because a 5xx is not something the person reading
// the row can do anything about.
//
// And the moment a read answers, the sentence goes: the row says what the
// pull request is actually waiting for again.
func TestSyncPullRequestsReportsReadsThatKeepFailingAndTakesItBack(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "outage", "main")
	repo := model.RepoRef{Owner: "acme", Name: "outage"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = pendingChecks()
	sim.FailReads = flaky()
	for _, at := range []time.Time{baseTime, baseTime.Add(10 * time.Second)} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, at); err == nil {
			t.Fatalf("expected the failed read at %s to be reported", at)
		}
	}
	// Two failed reads, ten seconds apart: still inside the window, so
	// still nothing said.
	if obs := mergeQueueStatus(t, ctx, store, task.ID); obs.MergeQueueState == "unreadable" {
		t.Fatalf("twenty seconds of 502s was reported as a fault: %q", obs.MergeQueueNote)
	}

	late := baseTime.Add(3 * time.Minute)
	if err := orchestrator.SyncPullRequests(ctx, store, client, late); err == nil {
		t.Fatal("expected the failed read to be reported")
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueState != "unreadable" {
		t.Fatalf("merge queue state = %q, want unreadable after three minutes of failed reads", obs.MergeQueueState)
	}
	if !strings.Contains(obs.MergeQueueNote, "Server Error") {
		t.Fatalf("note = %q, want GitHub's own words in it", obs.MergeQueueNote)
	}
	if !strings.Contains(obs.MergeQueueNote, "3 failed reads") {
		t.Fatalf("note = %q, want the count of failed reads in it", obs.MergeQueueNote)
	}
	if !strings.Contains(obs.MergeQueueNote, "still asking") {
		t.Fatalf("note = %q, want a 5xx described as grain retrying rather than as a fault to act on", obs.MergeQueueNote)
	}

	sim.FailReads = nil
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(4*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests once GitHub answered again: %v", err)
	}
	obs = mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueState != "waiting_ci" {
		t.Fatalf("merge queue state = %q, want waiting_ci: one read that answered clears this", obs.MergeQueueState)
	}
}

// A 404 is not a bad minute at GitHub. A credential that has lost its
// access to a repository meets one on every call it makes there, and a
// pull request somebody deleted never comes back -- so the row says what
// the operator has to go and check, rather than telling them grain is
// retrying something that cannot work.
//
// The deadline is the same one: a fault that is real crosses it inside a
// couple of minutes anyway, and a 404 from a replica that has not caught
// up deserves the same benefit of the doubt as a 502.
func TestSyncPullRequestsSaysWhatToDoAboutAPullRequestItCannotRead(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "revoked", "main")
	repo := model.RepoRef{Owner: "acme", Name: "revoked"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = pendingChecks()
	sim.FailReads = &githubsim.ReadFailure{Status: 404, Message: "Not Found"}
	for _, at := range []time.Time{baseTime, baseTime.Add(3 * time.Minute)} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, at); err == nil {
			t.Fatalf("expected the failed read at %s to be reported", at)
		}
	}

	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueState != "unreadable" {
		t.Fatalf("merge queue state = %q, want unreadable", obs.MergeQueueState)
	}
	if strings.Contains(obs.MergeQueueNote, "still asking") {
		t.Fatalf("note = %q: a 404 was described as GitHub having trouble", obs.MergeQueueNote)
	}
	for _, want := range []string{"Not Found", "cannot read this pull request", "acme/revoked"} {
		if !strings.Contains(obs.MergeQueueNote, want) {
			t.Fatalf("note = %q, want %q in it: this one is somebody's to go and fix", obs.MergeQueueNote, want)
		}
	}
}

// The state is the one here that is about *this cycle's* attempt rather
// than about the change, so it cannot be allowed to outlive the cycle
// covering the task. A task that stops being watched with a failed read
// as the last thing said about it used to keep "grain cannot read this
// pull request" on its row for good -- beside, in the worst case, a badge
// saying an agent was running on it.
func TestSyncPullRequestsTakesTheUnreadableNoteOffATaskItStopsWatching(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "unwatched", "main")
	repo := model.RepoRef{Owner: "acme", Name: "unwatched"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = pendingChecks()
	sim.FailReads = &githubsim.ReadFailure{Status: 404, Message: "Not Found"}
	for _, at := range []time.Time{baseTime, baseTime.Add(3 * time.Minute)} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, at); err == nil {
			t.Fatalf("expected the failed read at %s to be reported", at)
		}
	}
	if obs := mergeQueueStatus(t, ctx, store, task.ID); obs.MergeQueueState != "unreadable" {
		t.Fatalf("merge queue state = %q, want unreadable to start from", obs.MergeQueueState)
	}

	// Somebody puts an agent back on the task: its completion is cleared,
	// so it is out of the queue's watched set entirely and no cycle will
	// ever read its pull request again to correct what the row says.
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	obs.CompletedAt = nil
	if err := store.Observe(ctx, obs); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(4*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests with nothing left to watch: %v", err)
	}
	after := mergeQueueStatus(t, ctx, store, task.ID)
	if after.MergeQueueState != "" {
		t.Fatalf("merge queue state = %q (%q), want it cleared: the queue is not reading this pull request",
			after.MergeQueueState, after.MergeQueueNote)
	}
	if after.MergeQueueStateAt != nil {
		t.Fatalf("state stamped %v, want no stamp on a state that is not there", after.MergeQueueStateAt)
	}
}

// grain/task-256: and the same for the rest of the vocabulary, which is
// where the bug people actually meet lives. Every one of these states is
// written by a cycle and taken back by the next cycle that covers the
// task -- so a task that leaves the watched set between two of them
// keeps whatever was last said about it, and the row goes on claiming
// the queue is landing a change it has stopped looking at.
//
// The route in is the ordinary one: somebody puts an agent back on the
// task themselves (ui.Client.RunNow), which clears its completion and
// drops it out of Store.OpenPullRequestLinks. "Waiting for CI" beside a
// live run is the same silence grain/task-170 closed, pointed the other
// way -- not a row with nothing on it, but a row with something stale on
// it that outranks the truth.
func TestSyncPullRequestsTakesTheQueuesAccountOffATaskItStopsWatching(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "unwatched-wait", "main")
	repo := model.RepoRef{Owner: "acme", Name: "unwatched-wait"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = pendingChecks()
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if obs := mergeQueueStatus(t, ctx, store, task.ID); obs.MergeQueueState != "waiting_ci" {
		t.Fatalf("merge queue state = %q, want waiting_ci to start from", obs.MergeQueueState)
	}

	obs := mergeQueueStatus(t, ctx, store, task.ID)
	obs.CompletedAt = nil
	if err := store.Observe(ctx, obs); err != nil {
		t.Fatal(err)
	}

	// The checks pass while nothing is watching, which is exactly the
	// point: the cycle that would have noticed is not running for this
	// task any more, and the row must not go on saying it is waiting.
	sim.CheckRuns[branch] = greenChecks()
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests with nothing left to watch: %v", err)
	}
	after := mergeQueueStatus(t, ctx, store, task.ID)
	if after.MergeQueueState != "" {
		t.Fatalf("merge queue state = %q (%q), want it cleared: the queue is not driving this change",
			after.MergeQueueState, after.MergeQueueNote)
	}
	if after.MergeQueueNote != "" || after.MergeQueueStateAt != nil {
		t.Fatalf("note %q stamped %v, want both gone with the state",
			after.MergeQueueNote, after.MergeQueueStateAt)
	}
}

// The exception, and the reason the sweep is not simply "clear
// everything": a merge that failed is a verdict a person owns, reached
// once and cleared by a person pressing Retry merge
// (ui.Client.RetryMerge). It is true of a change nobody is driving in a
// way "Merging" is not -- and the "Merge blocked" chip beside it reads
// off MergeQueueBlockedAt, which nothing here may touch either, since
// clearing that would quietly put the task back on the queue.
func TestSyncPullRequestsKeepsAFailedMergeOnATaskItStopsWatching(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "unwatched-failed", "main")
	repo := model.RepoRef{Owner: "acme", Name: "unwatched-failed"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenChecks()
	sim.RefuseMerges = "Repository rule violations found\n\nCommits must have verified signatures."
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err == nil {
		t.Fatal("expected the refused merge to be reported")
	}
	before := mergeQueueStatus(t, ctx, store, task.ID)
	if before.MergeQueueState != "merge_failed" || before.MergeQueueBlockedAt == nil {
		t.Fatalf("state = %q blocked at %v, want a merge the queue has given up on",
			before.MergeQueueState, before.MergeQueueBlockedAt)
	}

	// Somebody puts an agent on it -- the likeliest thing to happen to a
	// task the queue has handed back -- so it leaves the watched set.
	obs := before
	obs.CompletedAt = nil
	if err := store.Observe(ctx, obs); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(32*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests with nothing left to watch: %v", err)
	}

	after := mergeQueueStatus(t, ctx, store, task.ID)
	if after.MergeQueueState != before.MergeQueueState || after.MergeQueueNote != before.MergeQueueNote {
		t.Fatalf("state %q (%q), want the verdict kept word for word: %q (%q)",
			after.MergeQueueState, after.MergeQueueNote, before.MergeQueueState, before.MergeQueueNote)
	}
	if after.MergeQueueStateAt == nil || !after.MergeQueueStateAt.Equal(*before.MergeQueueStateAt) {
		t.Fatalf("state stamped %v, want it still %v: nothing happened to restart this clock",
			after.MergeQueueStateAt, before.MergeQueueStateAt)
	}
	if after.MergeQueueBlockedAt == nil || !after.MergeQueueBlockedAt.Equal(*before.MergeQueueBlockedAt) {
		t.Fatalf("blocked at %v, want it still %v: a sweep put a task the queue gave up on back on it",
			after.MergeQueueBlockedAt, before.MergeQueueBlockedAt)
	}
}
