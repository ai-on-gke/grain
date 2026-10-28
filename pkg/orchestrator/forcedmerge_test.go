package orchestrator_test

// The other end of ui.Client.ForceMerge: what the merge queue does with
// a change an operator has said to merge anyway (grain/task-479, and the
// exception mergepredicate.go sanctions).
//
// The one thing these tests are for is the pair of facts that have to
// hold together. A forced change merges with its checks red -- otherwise
// the control does nothing an operator could not already do by waiting --
// and an unforced change beside it, red in exactly the same way, does
// not. A force merge that quietly widened the predicate for everything
// else would be indistinguishable from this one, cycle by cycle, until
// the day something landed that nobody asked to land.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/ui"
)

// forceMerge is an operator using the control -- through ui.Client
// itself rather than a hand-written store write, so what these tests put
// in front of the queue is exactly what the button does.
//
// The UI client is given the same GitHub the queue reads, so the head the
// override is stamped with is the pull request's own head at the moment
// of asking -- what an operator pressing the button is looking at.
func forceMerge(t *testing.T, ctx context.Context, store *model.Store, gh github.Client,
	taskID, reason string, now time.Time) {

	t.Helper()
	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice"), GitHubPullRequests: gh}, store)
	client.Now = func() time.Time { return now }
	if err := client.ForceMerge(ctx, taskID, reason, ""); err != nil {
		t.Fatalf("ForceMerge(%s): %v", taskID, err)
	}
}

// prFor is the sim's pull request for one branch, which is how these
// tests ask "did it actually merge?" of GitHub rather than of grain.
func prFor(t *testing.T, sim *githubsim.Sim, branch string) githubsim.PullRequest {
	t.Helper()
	for _, pr := range sim.PullRequests {
		if pr.Head == branch {
			return pr
		}
	}
	t.Fatalf("no pull request for %s", branch)
	return githubsim.PullRequest{}
}

// TestSyncPullRequestsMergesAForcedChangeAndNotTheRedOneBesideIt is the
// pair of facts, in one cycle over one repository so that neither can be
// explained by the other not having been looked at.
func TestSyncPullRequestsMergesAForcedChangeAndNotTheRedOneBesideIt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	// The unforced one is the head of the queue and the forced one is
	// behind it, so that neither "it merged because it was first" nor
	// "the other one was never looked at" can explain the result.
	_, otherBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	forced, forcedBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	setMergeable(sim, true)
	red := []github.CheckRun{{Name: "tests", Status: "completed", Conclusion: strPtr("failure")}}
	sim.CheckRuns[forcedBranch] = red
	sim.CheckRuns[otherBranch] = red

	// One ordinary cycle first, which is what an operator is looking at
	// when they reach for this: both changes red, the queue saying so on
	// their rows, and nothing merging.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests before the override: %v", err)
	}

	askedAt := baseTime.Add(time.Hour)
	forceMerge(t, ctx, store, client, forced.ID, "the release is broken and this is the fix", askedAt)

	if err := orchestrator.SyncPullRequests(ctx, store, client, askedAt.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests on the cycle after the override: %v", err)
	}

	forcedPR, otherPR := prFor(t, sim, forcedBranch), prFor(t, sim, otherBranch)
	if !forcedPR.Merged {
		t.Fatal("the forced change did not merge: the override authorised nothing")
	}
	if otherPR.Merged {
		t.Fatal("the change beside it merged too, with its own checks red: the override widened the predicate")
	}

	// grain did the merge, so grain's own state moved with it -- which is
	// the whole argument for the control existing rather than a person
	// merging the pull request underneath it.
	st, err := store.State(ctx, forced.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want the forced task closed out by the merge", st)
	}

	// The override is spent: one request, one merge.
	obs, err := store.GetObservation(ctx, forced.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.ForceMergeAt != nil || obs.ForceMergeReason != "" {
		t.Fatalf("observation = %+v, want the override cleared by the merge it authorised", obs)
	}

	// And it is on the record in all three places: countable, on the
	// task, and on the pull request.
	records, err := store.ForceMergeRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("the forced merge history holds %d row(s), want exactly one", len(records))
	}
	if records[0].TaskID != forced.ID || records[0].Number != forcedPR.Number {
		t.Fatalf("record = %+v, want it to name the task and the pull request", records[0])
	}
	if records[0].Reason != "the release is broken and this is the fix" {
		t.Fatalf("record reason = %q, want the operator's own words", records[0].Reason)
	}
	if !strings.Contains(records[0].Skipped, "tests") {
		t.Fatalf("record skipped = %q, want it to name the check that was not green", records[0].Skipped)
	}
	if records[0].By == "" {
		t.Fatalf("record = %+v, want it to name who asked", records[0])
	}
	if !mentionsTheOverride(commentBodies(t, ctx, store, forced.ID)) {
		t.Fatalf("the task's thread says nothing about having been force merged: %q",
			commentBodies(t, ctx, store, forced.ID))
	}
	var posted []string
	for _, c := range sim.Comments[forcedPR.Number] {
		posted = append(posted, c.Body)
	}
	if !mentionsTheOverride(posted) {
		t.Fatalf("the pull request says nothing about having been force merged: %q", posted)
	}
}

// A conflict is still a refusal. An override is a person overruling
// grain's own judgement, and nothing about that reaches GitHub's: a merge
// GitHub will not perform is not one an override can authorise, so the
// request stays outstanding and lands when the branch merges cleanly.
func TestSyncPullRequestsRefusesAForcedMergeThatConflicts(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, false)

	askedAt := baseTime.Add(time.Hour)
	forceMerge(t, ctx, store, client, task.ID, "main is broken and this fixes it", askedAt)
	if err := orchestrator.SyncPullRequests(ctx, store, client, askedAt.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests with a conflicted forced change: %v", err)
	}
	if prFor(t, sim, branch).Merged {
		t.Fatal("a conflicted pull request merged on an override: GitHub's own refusal is not overridable")
	}
	// Outstanding rather than spent: what the operator asked for happens
	// once the conflict is resolved on the branch.
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.ForceMergeAt == nil {
		t.Fatal("the override was spent by a merge that never happened")
	}
	if records, err := store.ForceMergeRecords(ctx); err != nil {
		t.Fatal(err)
	} else if len(records) != 0 {
		t.Fatalf("the history holds %d row(s), want none: nothing was forced", len(records))
	}

	// The conflict resolved -- by whoever the queue asked, or by hand --
	// and the merge the operator asked for happens on the next cycle.
	setMergeable(sim, true)
	if err := orchestrator.SyncPullRequests(ctx, store, client, askedAt.Add(2*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests once the conflict was resolved: %v", err)
	}
	if !prFor(t, sim, branch).Merged {
		t.Fatal("the outstanding override did nothing once the branch merged cleanly")
	}
}

// TestSyncPullRequestsDoesNotForceACommitPushedAfterTheOverride is
// bwsalmon/grain#2399: an operator forces the merge of what they
// reviewed, something is pushed to the branch before the next cycle, and
// the override must not carry the new commit in with it. The branch is
// red, so the only thing that could merge it is the override -- and the
// override is withdrawn, said so on the task, and gone for good rather
// than waiting to be read again.
func TestSyncPullRequestsDoesNotForceACommitPushedAfterTheOverride(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "tests", Status: "completed", Conclusion: strPtr("failure")}}

	askedAt := baseTime.Add(time.Hour)
	forceMerge(t, ctx, store, client, task.ID, "the release is broken and this is the fix", askedAt)
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	reviewed := obs.ForceMergeHeadSHA
	if reviewed == "" {
		t.Fatal("the override names no head: nothing ties it to what was reviewed")
	}

	// The push nobody reviewed, landing between the button and the cycle.
	pushCommitSaying(t, sim.BareRepo, branch, "something nobody looked at")

	if err := orchestrator.SyncPullRequests(ctx, store, client, askedAt.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests after a push behind the override: %v", err)
	}
	if prFor(t, sim, branch).Merged {
		t.Fatal("a commit pushed after the override merged on the strength of it")
	}
	obs, err = store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.ForceMergeAt != nil || obs.ForceMergeHeadSHA != "" || obs.ForceMergeReason != "" {
		t.Fatalf("observation = %+v, want the override withdrawn once its branch moved", obs)
	}
	if records, err := store.ForceMergeRecords(ctx); err != nil {
		t.Fatal(err)
	} else if len(records) != 0 {
		t.Fatalf("the history holds %d row(s), want none: nothing was forced", len(records))
	}
	var said bool
	for _, body := range commentBodies(t, ctx, store, task.ID) {
		if strings.Contains(body, "branch has moved since the force merge") &&
			strings.Contains(body, reviewed[:12]) {
			said = true
		}
	}
	if !said {
		t.Fatalf("the task's thread does not say the override was withdrawn and why: %q",
			commentBodies(t, ctx, store, task.ID))
	}

	// And it stays withdrawn: the next cycle is an ordinary one, and a red
	// change does not merge on it.
	if err := orchestrator.SyncPullRequests(ctx, store, client, askedAt.Add(2*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests on the cycle after: %v", err)
	}
	if prFor(t, sim, branch).Merged {
		t.Fatal("the withdrawn override merged the change a cycle later")
	}
}

func mentionsTheOverride(bodies []string) bool {
	for _, b := range bodies {
		if strings.Contains(b, "Force merged by") && strings.Contains(b, "Reason given") {
			return true
		}
	}
	return false
}

// orchestratorPullRequests is ui.Config.PullRequests over the
// orchestrator's own opener, the way the daemon wires it.
type orchestratorPullRequests struct {
	store  *model.Store
	client github.Client
}

func (p orchestratorPullRequests) OpenForTask(ctx context.Context, taskID string) (ui.PullRequestStatus, error) {
	task, err := p.store.GetTask(ctx, taskID)
	if err != nil {
		return ui.PullRequestStatus{}, err
	}
	_, err = orchestrator.OpenPullRequestForTask(ctx, p.store, p.client, *task)
	return ui.PullRequestStatus{}, err
}

// TestSyncPullRequestsMergesAChangeForcedWhileItsRunWasHungOnCI is the
// case the control is most wanted for: the agent pushed its branch and
// sat in wait_for_checks on CI that never finished, so the task is
// running -- off the merge queue's walk -- and, in the first case, has
// not even opened its pull request. Forcing the merge stops the run,
// finishes the task and opens the pull request, and the very next cycle
// merges the commit the operator saw. The second case is the same task
// after somebody pressed Stop first, which parks it in awaiting_reply.
func TestSyncPullRequestsMergesAChangeForcedWhileItsRunWasHungOnCI(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		stopped  bool
		noPRYet  bool
		wantFrom model.State
	}{
		{name: "running with no pull request yet", noPRYet: true, wantFrom: model.StateRunning},
		{name: "running with its pull request open", wantFrom: model.StateRunning},
		{name: "stopped first", stopped: true, wantFrom: model.StateAwaitingReply},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			sim, client := newSim(t, "acme", "widgets", "main")
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}
			task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
			setMergeable(sim, true)
			sim.CheckRuns[branch] = []github.CheckRun{{Name: "tests", Status: "in_progress"}}
			if tc.noPRYet {
				// The run pushed and went straight to wait_for_checks:
				// grain has no pull request on record, and GitHub none
				// open.
				task.Links = nil
				if err := store.PutTask(ctx, task); err != nil {
					t.Fatal(err)
				}
				sim.PullRequests = nil
			}
			if err := store.ObserveField(ctx, task.ID, baseTime, func(o *model.Observation) {
				o.CompletedAt = nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := store.StartRun(ctx, model.Run{
				ID: "r1", TaskID: task.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
			}, model.Limits{}); err != nil {
				t.Fatal(err)
			}

			operator := ui.NewClient(ui.Config{
				Actor:              ui.DefaultActor("alice"),
				GitHubPullRequests: client,
				PullRequests:       orchestratorPullRequests{store: store, client: client},
			}, store)
			askedAt := baseTime.Add(time.Hour)
			operator.Now = func() time.Time { return askedAt }
			if tc.stopped {
				if err := operator.Stop(ctx, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			if st, err := store.State(ctx, task.ID); err != nil || st != tc.wantFrom {
				t.Fatalf("state before the override = %q (%v), want %q", st, err, tc.wantFrom)
			}
			if err := operator.ForceMerge(ctx, task.ID, "CI is hung and this is the fix", ""); err != nil {
				t.Fatalf("ForceMerge: %v", err)
			}

			if err := orchestrator.SyncPullRequests(ctx, store, client, askedAt.Add(time.Minute)); err != nil {
				t.Fatalf("SyncPullRequests on the cycle after the override: %v", err)
			}
			if pr := prFor(t, sim, branch); !pr.Merged {
				t.Fatal("the forced change did not merge on the next cycle")
			}
			st, err := store.State(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if st != model.StateClosed {
				t.Fatalf("state = %q, want the forced task closed out by the merge", st)
			}
			records, err := store.ForceMergeRecords(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || records[0].TaskID != task.ID {
				t.Fatalf("records = %+v, want the one forced merge recorded", records)
			}
		})
	}
}
