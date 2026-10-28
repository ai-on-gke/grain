// What the merge queue does about red it cannot reach: a check that
// failed before a job of it ran a step, a check that will never pass for
// the author grain files pull requests as. The queue could already see
// the first (checkstartups.go) and said so well -- on whichever pull
// request happened to reach the front of the queue, and nowhere any
// other entry could read it. So the three behind it each spent a base
// merge, an agent run and a full CI suite arriving at the same dead end,
// one at a time (gke-labs/kontur, 18 Sep 2026; grain/task-291).
//
// These hold the four things that changed: the conclusion is reached
// once per repository and every entry carries it, a human's "try again"
// re-tests it in seconds rather than buying another repair, no base
// merge is written for a pull request that cannot merge whatever is
// written to it, and red the branch genuinely owns still earns exactly
// the repair it always did -- which is the case that matters most,
// because grain/task-219 had one of each at once and the fixable one was
// invisible among the seven that were not.
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

// mergeQueueRow is what a person watching sees on this task's row: the
// state and the sentence beside it (Observation.MergeQueueState).
func mergeQueueRow(t *testing.T, ctx context.Context, store *model.Store, taskID string) (string, string) {
	t.Helper()
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil {
		return "", ""
	}
	return obs.MergeQueueState, obs.MergeQueueNote
}

// declareUnfixableChecks is an operator naming the checks that never pass
// for a pull request grain's agents author
// (model.RepoConfig.UnfixableChecks).
func declareUnfixableChecks(t *testing.T, ctx context.Context, store *model.Store,
	repo model.RepoRef, checks ...string) {

	t.Helper()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, UnfixableChecks: checks}); err != nil {
		t.Fatal(err)
	}
}

// A repository whose Actions cannot start a job, with four changes
// queued behind each other: the conclusion is reached once, on the cycle
// that reads it, and every entry carries it -- not just the one at the
// front. Nothing is repaired, nothing is merged into anybody's branch,
// and the three entries that are not the head say on their own rows what
// they are waiting for.
//
// The old behaviour is the whole reason for the test: the head was
// escalated with a comment that said, correctly, that every pull request
// in the repository was failing this way -- and the three behind it read
// "Needs repairing", took their turn at the front one cycle at a time,
// and each bought a base merge and an agent run to be told the same
// thing.
func TestTheQueueConcludesAnUnfixableRepositoryOnceForEveryEntry(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	var tasks []model.Task
	var branches []string
	for _, id := range []string{"t1", "t2", "t3"} {
		task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, id, repo)
		tasks = append(tasks, task)
		branches = append(branches, branch)
		sim.CheckRuns[branch] = []github.CheckRun{
			{Name: "test", Status: "completed", Conclusion: strPtr("failure")},
			{Name: "check-changes", Status: "completed", Conclusion: strPtr("failure")},
		}
		// Every run in this repository died before a job of it ran a
		// step, which is what an account out of Actions minutes looks
		// like from the API.
		sim.StartupFailures[branch] = true
	}
	setMergeable(sim, true)
	// And the base has moved, so a base merge would be a real write to
	// somebody's branch rather than GitHub's 204.
	pushFileToBranch(t, sim.BareRepo, "main", "CHANGELOG.md", "released", "release")

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests against a repository whose checks never start: %v", err)
	}

	// The head: handed to a person, with the finding, and without the
	// repair being spent on it.
	obs, err := store.GetObservation(ctx, tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueBlockedAt == nil {
		t.Fatal("the queue head was not handed to a person")
	}
	if obs.MergeQueueRepairAt != nil {
		t.Error("the queue spent a repair on checks no commit reaches")
	}
	bodies := commentBodies(t, ctx, store, tasks[0].ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment on the head, got %q", bodies)
	}
	if !strings.Contains(bodies[0], "Every pull request open against this repository is failing this way") {
		t.Errorf("the head's comment does not say the finding covers the repository:\n%s", bodies[0])
	}

	// Everything behind it: same conclusion, same cycle, no agent, no
	// comment of its own yet -- and a row that says what it is waiting
	// for rather than promising a repair that is not coming.
	for _, task := range tasks[1:] {
		state, note := mergeQueueRow(t, ctx, store, task.ID)
		if state != "checks_unfixable" {
			t.Errorf("%s's row reads %q, not the finding the entry in front of it proved", task.ID, state)
		}
		if !strings.Contains(note, "every pull request open against acme/widgets") {
			t.Errorf("%s's row does not carry the finding: %q", task.ID, note)
		}
		if !strings.Contains(note, "`test`") {
			t.Errorf("%s's row does not name what is red: %q", task.ID, note)
		}
		obs, err := store.GetObservation(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if obs.MergeQueueRepairAt != nil {
			t.Errorf("%s bought its own repair for a conclusion already reached", task.ID)
		}
		if obs.CompletedAt == nil {
			t.Errorf("%s was sent back to an agent", task.ID)
		}
	}

	// And nothing was written to anybody's branch on the way: the base
	// merge is a move spent reaching a green none of these can have.
	for i, branch := range branches {
		if branchContains(t, sim.BareRepo, branch, "main") {
			t.Errorf("%s had the base merged into it on a pull request that cannot merge", tasks[i].ID)
		}
	}
}

// An operator's "try again" against checks that are still failing the
// same way: the finding is re-tested -- the checks are read afresh, which
// is the whole point of the button -- and reported. What does not happen
// is the base merge and the repair dispatch that used to follow it,
// which is hours of agent time and two full CI suites spent rediscovering
// a conclusion this cycle reached in seconds.
func TestARetryAgainstUnchangedFailingChecksReportsTheFinding(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "test", Status: "completed", Conclusion: strPtr("failure")},
	}
	sim.StartupFailures[branch] = true
	pushFileToBranch(t, sim.BareRepo, "main", "CHANGELOG.md", "released", "release")

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueBlockedAt == nil {
		t.Fatal("the queue never gave up, so there is nothing for a retry to ask again")
	}

	retryAt := baseTime.Add(2 * time.Hour)
	retryMerge(t, ctx, store, task.ID, retryAt)
	if err := orchestrator.SyncPullRequests(ctx, store, client, retryAt.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests on the cycle after the retry: %v", err)
	}

	obs, err = store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueRepairAt != nil {
		t.Error("the retry bought a repair for checks that had not changed")
	}
	if obs.CompletedAt == nil {
		t.Error("the retry sent the task back to an agent")
	}
	if obs.MergeQueueBlockedAt == nil {
		t.Error("the retry left the queue driving a pull request it had already proved cannot go green")
	}
	if branchContains(t, sim.BareRepo, branch, "main") {
		t.Error("the retry merged the base into a branch that cannot merge either way")
	}
	if obs.MergeQueueRefreshedAt != nil {
		t.Error("the retry spent this round's base merge on a pull request with no path to green")
	}
	// The queue said it again rather than saying nothing: the person who
	// pressed the button is owed the answer, and the answer is the same
	// one.
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 3 {
		t.Fatalf("expected the first escalation, the retry, and the answer to it, got %q", bodies)
	}
	if !strings.Contains(bodies[2], "before a single job of it ran a step") {
		t.Errorf("the retry's answer does not report the finding:\n%s", bodies[2])
	}
}

// The case that matters most, and grain/task-219's own state: a
// repository whose GitHub checks cannot start a job, and a branch whose
// own grain CI run is red. The second is real work an agent should do,
// and it still earns exactly the repair it earned before -- the seven
// checks nobody can fix must not swallow the one that can be.
func TestAHeadWithItsOwnRedStillEarnsItsRepairInAnUnfixableRepository(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	recordCIStatus(t, ctx, store, task.ID, &model.CIStatus{
		RunID:      "run1",
		Commit:     headSHA(t, client, repo, branch),
		Conclusion: string(ci.ConclusionFailure),
		Detail:     "failure: go",
	})
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "test", Status: "completed", Conclusion: strPtr("failure")},
	}
	sim.StartupFailures[branch] = true

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a branch whose own grain CI run is red did not get its repair")
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueBlockedAt != nil {
		t.Error("the repository's own breakage cost this branch the repair for its own failure")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment asking for the repair, got %q", bodies)
	}
	if !strings.Contains(bodies[0], "grain's own CI") {
		t.Errorf("the repair does not name what is actually red:\n%s", bodies[0])
	}
}

// The same split arrived at the other way, and the one that outlives an
// Actions budget: a CLA check that is working exactly as intended and is
// never going to pass for an agent-authored pull request, beside a test
// that genuinely broke on this branch. The repair is still asked for --
// and it is told which half is its, rather than being sent at all of it
// and spending the run proving the other half cannot be fixed here.
func TestARepairIsToldWhichHalfOfTheRedIsIts(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	declareUnfixableChecks(t, ctx, store, repo, "cla/google")
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "test", Status: "completed", Conclusion: strPtr("failure")},
		{Name: "cla/google", Status: "completed", Conclusion: strPtr("failure")},
	}
	sim.WorkflowJobs[branch] = []githubsim.WorkflowJob{
		{Name: "test", Conclusion: "failure", Log: "--- FAIL: TestThing (0.01s)\n"},
	}
	pushFileToBranch(t, sim.BareRepo, "main", "CHANGELOG.md", "released", "release")

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("the check this branch really did break did not earn a repair")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment asking for the repair, got %q", bodies)
	}
	for _, want := range []string{
		"What is in scope is `test`",
		"`cla/google` is red for a reason nothing pushed to this branch reaches",
	} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the repair comment does not say %q:\n%s", want, bodies[0])
		}
	}
	// The instruction it must not carry: every other repair tells its run
	// that a check already red on the base is still its to fix, and here
	// that would be a run sent at a CLA bot.
	if strings.Contains(bodies[0], "Everything failing on it is in scope") {
		t.Errorf("the repair told its run to fix a check that cannot pass here:\n%s", bodies[0])
	}
	// And no base merge was written for it: this pull request does not
	// merge whatever lands on its branch, so the write buys nothing and
	// the conflict it can create is somebody's afternoon.
	if branchContains(t, sim.BareRepo, branch, "main") {
		t.Error("the queue merged the base into a branch with no path to green")
	}
}

// And the CLA check alone, which is where a repository lands once
// somebody pays the Actions bill: nothing else is red, the pull request
// still cannot merge, and there is no repair to spend on it. A person is
// told once, in seconds.
func TestACheckThatNeverPassesHereIsNotRepaired(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	declareUnfixableChecks(t, ctx, store, repo, "cla/google")
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "test", Status: "completed", Conclusion: strPtr("success")},
		{Name: "cla/google", Status: "completed", Conclusion: strPtr("failure")},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueRepairAt != nil {
		t.Error("the queue spent a repair on a check that will never pass for this author")
	}
	if obs.MergeQueueBlockedAt == nil {
		t.Fatal("the queue went on driving a merge nothing it can do will land")
	}
	if mergedBranch(sim, branch) {
		t.Fatal("the declaration let a pull request merge with a red check -- it is not an exemption")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment handing this to a person, got %q", bodies)
	}
	for _, want := range []string{
		"`cla/google`",
		"cannot go green from this branch",
		"merges as soon as it reads clean",
	} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the escalation does not say %q:\n%s", want, bodies[0])
		}
	}
}
