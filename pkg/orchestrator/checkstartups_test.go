package orchestrator_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// The sentence a deployment with a broken repository could not get from
// anywhere: this is the repository, not your branch.
//
// The escalation next door already blocks each such pull request and
// lists the four causes, one pull request at a time. What it could not
// say -- holding one pull request -- is that nothing in this repository
// has run for anybody, which is the difference between a person reading
// six "this pull request is failing" comments and a person changing one
// setting. grain asks GitHub for it at the moment it escalates, rather
// than keeping a tally of its own escalations: see checkstartups.go on
// why nothing is remembered.
func TestSyncPullRequestsSaysWhenNothingInTheRepositoryStartsAJob(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "tests", Status: "completed", Conclusion: strPtr("failure")},
	}
	// The only CI this repository has: a run that died before a job of it
	// started. Which is what a repository with Actions switched off, or
	// an account at its spending limit, looks like from the outside.
	sim.StartupFailures[branch] = true

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests once CI failed at startup: %v", err)
	}

	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment escalating to a person, got %q", bodies)
	}
	for _, want := range []string{
		// The verdict, as a fact about the repository, with the evidence
		// it was read from rather than an inference from grain's own
		// escalation history.
		"not one got a job as far as running a step",
		"acme/widgets",
		// And what follows from it, which is the whole reason to say it:
		// nobody should go looking at any of these branches, and one
		// change clears all of them at once.
		"none of them can be repaired from a branch",
		"clears all of them at once",
		// The run page carrying GitHub's own reason, which is the only
		// place the actual cause is written.
		"/actions/runs/",
	} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the escalation does not say %q:\n%s", want, bodies[0])
		}
	}
}

// The other direction, and the reason the read is worth making at all: a
// repository whose runs do start jobs has two of the four causes ruled
// out for free. Actions is on, the account has minutes -- so the reader
// is looking at a workflow file or a runner label, and not at a billing
// page.
//
// Same failure on the pull request either way: its own run was concluded
// without a job of it starting. Only the repository around it differs.
func TestSyncPullRequestsRulesOutTheAccountWhenOtherRunsStart(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "tests", Status: "completed", Conclusion: strPtr("failure")},
	}
	sim.WorkflowJobs[branch] = []githubsim.WorkflowJob{
		{Name: "tests", Conclusion: "failure", NeverStarted: true},
	}
	// And a run elsewhere in the same repository that did get a job
	// started -- the base branch's own, which is the ordinary state of a
	// repository whose Actions work.
	sim.WorkflowJobs["main"] = []githubsim.WorkflowJob{{Name: "tests"}}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests once CI failed without starting a job: %v", err)
	}

	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment escalating to a person, got %q", bodies)
	}
	for _, want := range []string{
		"Two more of those causes are ruled out",
		"did get a job started",
		"Actions is not switched off for this repository",
	} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the escalation does not say %q:\n%s", want, bodies[0])
		}
	}
	// And it does not make the larger claim, which is false here.
	if strings.Contains(bodies[0], "not one got a job as far as running a step") {
		t.Errorf("a repository whose runs do start jobs was called dead:\n%s", bodies[0])
	}
}

// unreadableRuns is a client whose Actions history cannot be read: a
// token without the scope, a repository whose CI is not Actions, a
// GitHub that is briefly unreachable. Everything else about it is the
// real thing.
type unreadableRuns struct {
	github.Client
}

func (unreadableRuns) RecentRunStarts(owner, repo string, limit int) (github.RunStarts, error) {
	return github.RunStarts{}, errors.New("Resource not accessible by personal access token")
}

// A read that fails changes nothing: the escalation is written exactly as
// it was before any of this existed, and the queue gives up on the head
// either way.
//
// This is the property worth having a test for. The repository sentence
// is an extra, and the failure it is an extra on is one where somebody
// is already being woken: a comment that went missing, or a merge queue
// that stopped advancing, because grain could not reach one more API
// would be a worse deployment than one that says less.
func TestSyncPullRequestsStillEscalatesWhenTheRepositoryCannotBeRead(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "tests", Status: "completed", Conclusion: strPtr("failure")},
	}
	sim.StartupFailures[branch] = true

	if err := orchestrator.SyncPullRequests(ctx, store, unreadableRuns{client}, baseTime); err != nil {
		t.Fatalf("SyncPullRequests with an unreadable Actions history: %v", err)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueBlockedAt == nil {
		t.Fatal("the queue head was not given up on")
	}
	if obs.MergeQueueRepairAt != nil {
		t.Error("the queue spent this pull request's one repair on a run that never started")
	}

	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment escalating to a person, got %q", bodies)
	}
	if !strings.Contains(bodies[0], "one automatic repair") {
		t.Errorf("the escalation lost its own subject:\n%s", bodies[0])
	}
	// And it claims nothing about the repository, in either direction,
	// on the strength of a read that did not happen.
	for _, unwanted := range []string{
		"not one got a job as far as running a step",
		"Two more of those causes are ruled out",
	} {
		if strings.Contains(bodies[0], unwanted) {
			t.Errorf("the escalation says %q having read nothing:\n%s", unwanted, bodies[0])
		}
	}
}
