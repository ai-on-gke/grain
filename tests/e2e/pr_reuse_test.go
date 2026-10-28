// TestSecondAttemptPushReusesTheFirstAttemptsOpenPullRequest is
// bwsalmon/agents#325's own scenario. pkg/orchestrator/finish_test.go's
// TestProcessResultReusesAnAlreadyOpenPullRequest already proves
// EnsurePullRequest's reuse logic in isolation, against a hand-built store
// state with a PR pre-seeded by a direct CreatePullRequest call. This test
// proves the same thing reached through two real dispatches and two real
// pushes onto the same branch.
//
// That needs a real github.Client backed by a real githubsim.Sim, since PR
// identity (is PR #1 the one still open, does a second one ever get
// opened) can only be observed for real that way -- this package's own
// harness (world/newWorld, harness_test.go) builds no github.Client at
// all, so "the PR opened" is simulated there with a bare store.Observe
// rather than a real CreatePullRequest. cli_test.go is the one file in
// this package that does build a real client, but its syncedSim/
// httptest.Server rig exists to serialize a subprocess CLI and a
// simulated "user" hitting the sim from separate goroutines at once; this
// test drives everything sequentially from its own goroutine (one
// scripted agent, one orchestrator.RunCycle at a time), so it uses the
// lighter rig pkg/orchestrator/live_test.go already established for
// exactly that case instead -- a Sim wired straight into a github.Client
// in-process, and a local bare repo path as the git remote.
package e2e

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// newPRReuseSim is pkg/orchestrator/orchestrator_test.go's own newSim,
// duplicated here for the same reason every other package in this
// codebase duplicates small test helpers rather than exporting them
// (harness_test.go's own comment on why): a real bare git repo behind a
// Sim, wired into a real github.Client with no HTTP round trip at all.
func newPRReuseSim(t *testing.T, owner, repo, branch string) (*githubsim.Sim, *github.RESTClient) {
	t.Helper()
	dir := t.TempDir()
	bare := initBareRepo(t, filepath.Join(dir, "repo.git"), branch)

	seed := filepath.Join(dir, "seed")
	run(t, dir, "git", "clone", "-q", bare, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	run(t, seed, "git", "commit", "-q", "--allow-empty", "-m", "seed")
	run(t, seed, "git", "push", "-q", "origin", branch)

	sim := githubsim.New(owner, repo, bare, branch)
	return sim, github.NewClient(sim, nil)
}

// pushMoreScript is pushScript's second-attempt counterpart: it checks out
// branch as it already exists on the remote (no -b) rather than creating
// it fresh, and pushes one more commit onto its tip. Proving PR reuse
// needs a second attempt that lands on the *same* branch a first attempt
// already pushed -- pushScript alone can't do that, since it always
// creates a fresh branch off the clone's default branch.
//
// It also removes any leftover "work" directory before cloning. That is
// belt-and-braces now rather than load-bearing: each attempt gets a
// sandbox of its own, built for that run and destroyed with it, so there
// is no previous attempt's clone left to trip over. It was load-bearing
// while a sandbox was a slot's and outlived every attempt dispatched onto
// it.
func pushMoreScript(remote, branch, taskID string) []antigravity.Step {
	cmd := "rm -rf work && git clone " + remote + " work && cd work && " +
		"git checkout " + branch + " && " +
		"echo 'second change for " + taskID + "' >> NOTES.md && " +
		"git add NOTES.md && git commit -q -m 'second agent commit for " + taskID + "' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("pushed more commits to " + branch),
	}
}

// countLinkFixes reports how many of links are a LinkFixes pointing at
// target -- more than one would mean finishWithPullRequest's own
// idempotency check (finish.go: "Re-checking the link inside the closure
// is also what makes it idempotent across a retry") failed to hold.
func countLinkFixes(links []model.Link, target string) int {
	n := 0
	for _, l := range links {
		if l.Kind == model.LinkFixes && l.Target == target {
			n++
		}
	}
	return n
}

func TestSecondAttemptPushReusesTheFirstAttemptsOpenPullRequest(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	const owner, repoName = "acme", "widgets"
	repo := model.RepoRef{Owner: owner, Name: repoName}

	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	sim, client := newPRReuseSim(t, owner, repoName, "main")

	sandboxes := credentialed(t, "http://placeholder.example/x/y.git")

	actor := human("dana")
	task := model.Task{
		ID: "iss-325", Intent: model.IntentImplement, Title: "issue iss-325",
		Origin:  model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
		Binding: model.BindingDirective,
		Target:  &repo,
	}
	if model.LandsQueued(task.Origin) {
		task.Approval = &model.Attribution{Actor: actor}
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing iss-325: %v", err)
	}

	clock := baseTime
	branch := model.BranchName(task.ID)

	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: sandboxes, MaxWorkers: 1,
		Framework: scriptedFramework(pushScript(sim.BareRepo, branch, task.ID)),
	}

	// Attempt 1: dispatch, push, and let finishWithPullRequest open PR #1
	// and link it onto the task -- the way TestIssueCompletesEndToEnd
	// reaches "completed," but through a real client instead of a
	// simulated store.Observe.
	if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (attempt 1): %v", err)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingSubmit {
		t.Fatalf("state after attempt 1 = %q, want awaiting_submit: the pull request is open and unsubmitted", st)
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected one pull request after attempt 1, got %+v", sim.PullRequests)
	}
	firstPR := sim.PullRequests[0].Number

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantLink := model.PullRequestRef{Repo: repo, Number: firstPR}.String()
	if n := countLinkFixes(got.Links, wantLink); n != 1 {
		t.Fatalf("LinkFixes to %s after attempt 1 = %d, want exactly 1 (links: %+v)", wantLink, n, got.Links)
	}

	// Get the task back into queued for a second attempt. Observe REPLACEs
	// the whole row (model/simulate_test.go's
	// TestGitHubSyncObservationsReplaceTheWholeRowNotJustTheChangedField
	// proves this), so an Observe naming nothing but the task id clears
	// CompletedAt and hands the task back to the queue -- the same trick
	// e2e_test.go's TestAgentQuestionParksTaskThenReplyResumesAndItCompletes
	// uses to resume from a parked question.
	clock = clock.Add(time.Minute)
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, ObservedAt: &clock}); err != nil {
		t.Fatal(err)
	}
	st, err = store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("state after requeuing = %q, want queued", st)
	}

	// Attempt 2: the scripted agent pushes more commits onto the same
	// branch, rather than a fresh one.
	deps.Framework = scriptedFramework(pushMoreScript(sim.BareRepo, branch, task.ID))
	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (attempt 2): %v", err)
	}

	st, err = store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingSubmit {
		t.Fatalf("state after attempt 2 = %q, want awaiting_submit: the pull request is open and unsubmitted", st)
	}
	if n, err := store.Attempts(ctx, task.ID); err != nil || n != 2 {
		t.Fatalf("Attempts(%s) = %d (%v), want 2", task.ID, n, err)
	}

	// The whole point: finishWithPullRequest must find and reuse PR #1,
	// not open a second one.
	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected no second PR to be opened, got %+v", sim.PullRequests)
	}
	if sim.PullRequests[0].Number != firstPR {
		t.Fatalf("got PR %+v, want the same PR #%d reused", sim.PullRequests[0], firstPR)
	}

	got, err = store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n := countLinkFixes(got.Links, wantLink); n != 1 {
		t.Fatalf("LinkFixes to %s after attempt 2 = %d, want still exactly 1 (no second link), links: %+v", wantLink, n, got.Links)
	}

	// And the branch itself really did get a second commit -- proving
	// attempt 2 pushed more work onto it rather than being a no-op.
	log := runOutput(t, t.TempDir(), "git", "--git-dir", sim.BareRepo, "log", "--oneline", branch)
	if !strings.Contains(log, "second agent commit for "+task.ID) {
		t.Fatalf("branch %s log = %q, want it to contain attempt 2's commit", branch, log)
	}
}
