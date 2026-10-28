package githubsim

import (
	"os/exec"
	"testing"

	"github.com/bwsalmon/grain/pkg/github"
)

// TestSimCreateCommentIsReadableThroughListComments proves the two ends
// of ProcessResult's own "relay ask_question by posting a comment" path
// agree with each other -- what CreateComment writes is what
// ListComments (and GetIssue's own comment count, if it had one) would
// read back, unlike the endpoint's earlier always-empty stand-in.
func TestSimCreateCommentIsReadableThroughListComments(t *testing.T) {
	sim, client := newSim(t, "main")
	sim.Issues[1] = &Issue{Title: "t", Body: "b", Labels: map[string]struct{}{}}

	id, err := client.CreateComment("acme", "widgets", 1, "what should I do?")
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected a non-zero comment id")
	}

	comments, err := client.ListComments("acme", "widgets", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Body != "what should I do?" || comments[0].ID != id {
		t.Fatalf("got %+v", comments)
	}
}

func TestSimCloseAndReopenIssueMutateState(t *testing.T) {
	sim, client := newSim(t, "main")
	sim.Issues[1] = &Issue{Title: "t", Body: "b", Labels: map[string]struct{}{}}

	if err := client.CloseIssue("acme", "widgets", 1); err != nil {
		t.Fatal(err)
	}
	issue, err := client.GetIssue("acme", "widgets", 1)
	if err != nil {
		t.Fatal(err)
	}
	if issue.State != "closed" {
		t.Fatalf("state = %q, want closed", issue.State)
	}

	if err := client.ReopenIssue("acme", "widgets", 1); err != nil {
		t.Fatal(err)
	}
	issue, err = client.GetIssue("acme", "widgets", 1)
	if err != nil {
		t.Fatal(err)
	}
	if issue.State != "open" {
		t.Fatalf("state = %q, want open", issue.State)
	}
}

func TestSimCreateIssueFilesAnUnlabelledIssue(t *testing.T) {
	_, client := newSim(t, "main")
	issue, err := client.CreateIssue("acme", "widgets", "follow-up", "do more", nil)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Title != "follow-up" || issue.Body != "do more" || len(issue.Labels) != 0 {
		t.Fatalf("got %+v", issue)
	}

	// Filed with no trigger label -- propose_task's own contract: a human
	// must apply it before this is ever dispatched.
	got, err := client.GetIssue("acme", "widgets", issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasLabel("grain-agent") {
		t.Fatalf("expected no labels on a freshly proposed task, got %+v", got.Labels)
	}
}

func TestSimFindOpenPullRequestForBranch(t *testing.T) {
	sim, client := newSim(t, "main")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/issue-1", "main", "t", "")
	if err != nil {
		t.Fatal(err)
	}

	found, err := client.FindOpenPullRequestForBranch("acme", "widgets", "grain/issue-1")
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.Number != pr.Number {
		t.Fatalf("got %+v", found)
	}

	none, err := client.FindOpenPullRequestForBranch("acme", "widgets", "grain/issue-2")
	if err != nil {
		t.Fatal(err)
	}
	if none != nil {
		t.Fatalf("expected no match, got %+v", none)
	}

	// A merged/closed PR no longer matches the open-only filter.
	for i := range sim.PullRequests {
		sim.PullRequests[i].State = "closed"
	}
	found, err = client.FindOpenPullRequestForBranch("acme", "widgets", "grain/issue-1")
	if err != nil {
		t.Fatal(err)
	}
	if found != nil {
		t.Fatalf("expected a closed PR not to match, got %+v", found)
	}
}

func TestSimGetPullRequestReadsStateAndMergeable(t *testing.T) {
	sim, client := newSim(t, "main")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/issue-1", "main", "t", "body")
	if err != nil {
		t.Fatal(err)
	}

	detail, err := client.GetPullRequest("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != "open" || detail.HeadRef != "grain/issue-1" || detail.BaseRef != "main" {
		t.Fatalf("got %+v", detail)
	}
	if detail.Mergeable != nil {
		t.Fatalf("expected unknown mergeability, got %+v", detail.Mergeable)
	}

	clean := true
	for i := range sim.PullRequests {
		sim.PullRequests[i].Mergeable = &clean
	}
	detail, err = client.GetPullRequest("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Mergeable == nil || !*detail.Mergeable {
		t.Fatalf("got %+v", detail.Mergeable)
	}
}

func TestSimMergePullRequestClosesIt(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "grain/issue-1")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/issue-1", "main", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.MergePullRequest("acme", "widgets", pr.Number, ""); err != nil {
		t.Fatal(err)
	}
	detail, err := client.GetPullRequest("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != "closed" {
		t.Fatalf("state = %q, want closed", detail.State)
	}

	// MergePullRequest now performs the merge for real, at the git level
	// -- the same thing GitHub's own PUT .../merge does as a side effect
	// -- not just github.PullRequestDetail's own State field.
	cmd := exec.Command("git", "--git-dir", sim.BareRepo, "merge-base", "--is-ancestor", "grain/issue-1", "main")
	if err := cmd.Run(); err != nil {
		t.Fatalf("expected grain/issue-1 to have actually landed in main: %v", err)
	}
}

// The merge endpoint's own `sha` parameter -- "SHA that pull request head
// must match to allow merge". A caller that merged on one commit's CI
// sends it, and a branch that has moved since must be refused rather than
// merged, or nothing on this side can tell the two apart and a test about
// racing pushes would pass whatever the code did.
func TestSimMergePullRequestRefusesAMovedHead(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "grain/issue-1")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/issue-1", "main", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	judged := sim.branchSHA("grain/issue-1")

	// The push that lands after whoever is merging read the branch.
	pushAnotherCommit(t, sim.BareRepo, "grain/issue-1")

	err = client.MergePullRequest("acme", "widgets", pr.Number, judged)
	if err == nil {
		t.Fatal("expected the merge to be refused, the head having moved")
	}
	ghErr, ok := err.(*github.Error)
	if !ok || ghErr.Status != 409 {
		t.Fatalf("got %v, want a 409", err)
	}
	detail, err := client.GetPullRequest("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != "open" || detail.Merged {
		t.Fatalf("a refused merge left the PR %+v", detail)
	}

	// Pinning the commit that is actually there now merges, so the
	// refusal above is about the sha and not about pinning at all.
	if err := client.MergePullRequest("acme", "widgets", pr.Number, sim.branchSHA("grain/issue-1")); err != nil {
		t.Fatalf("merging the branch's own tip: %v", err)
	}
	if detail, err = client.GetPullRequest("acme", "widgets", pr.Number); err != nil {
		t.Fatal(err)
	}
	if !detail.Merged {
		t.Fatalf("got %+v, want merged", detail)
	}
}

// Check runs are seeded under whichever ref a test finds readable and
// read back under whichever ref the caller asks by -- a branch name here,
// a commit sha in orchestrator.checkRunsFor -- because BareRepo resolves
// the two to one commit the way GitHub does.
func TestSimListCheckRunsResolvesBetweenBranchAndSHA(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "grain/issue-1")
	sim.CheckRuns["grain/issue-1"] = []github.CheckRun{{Name: "build", Status: "completed"}}

	runs, err := client.ListCheckRuns("acme", "widgets", sim.branchSHA("grain/issue-1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Name != "build" {
		t.Fatalf("a sha-scoped read of branch-seeded checks got %+v", runs)
	}

	// And the other way round, which is what lets a test seed one
	// commit's checks and not another's.
	sim.CheckRuns = map[string][]github.CheckRun{
		sim.branchSHA("grain/issue-1"): {{Name: "tests", Status: "queued"}},
	}
	if runs, err = client.ListCheckRuns("acme", "widgets", "grain/issue-1"); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Name != "tests" {
		t.Fatalf("a branch-scoped read of sha-seeded checks got %+v", runs)
	}

	// A commit nothing was seeded for has no checks, however many the
	// branch it is on has: that distinction is the whole point.
	pushAnotherCommit(t, sim.BareRepo, "grain/issue-1")
	if runs, err = client.ListCheckRuns("acme", "widgets", sim.branchSHA("grain/issue-1")); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("the new commit answered with the old one's checks: %+v", runs)
	}
}

// GitHub's own GET .../pulls/{number} carries head.sha, and grain reads
// it for two things that both go wrong quietly without it: scoping the
// Actions fallback to the commit the pull request actually points at, and
// telling one push's empty check list from the next one's
// (orchestrator's emptyChecksSettled). A sim that always answered with an
// empty sha would exercise neither.
func TestSimGetPullRequestReadsTheHeadBranchesTip(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "grain/issue-1")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/issue-1", "main", "t", "")
	if err != nil {
		t.Fatal(err)
	}

	detail, err := client.GetPullRequest("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	want := sim.branchSHA("grain/issue-1")
	if want == "" {
		t.Fatal("the branch just pushed has no tip")
	}
	if detail.HeadSHA != want {
		t.Fatalf("HeadSHA = %q, want the branch's own tip %q", detail.HeadSHA, want)
	}
}

func TestSimListCheckRunsReadsSeededRuns(t *testing.T) {
	sim, client := newSim(t, "main")

	runs, err := client.ListCheckRuns("acme", "widgets", "grain/issue-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected no check runs before seeding, got %+v", runs)
	}

	failure := "failure"
	sim.CheckRuns["grain/issue-1"] = []github.CheckRun{
		{Name: "build", Status: "completed", Conclusion: &failure},
	}
	runs, err = client.ListCheckRuns("acme", "widgets", "grain/issue-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Name != "build" || runs[0].Conclusion == nil || *runs[0].Conclusion != "failure" {
		t.Fatalf("got %+v", runs)
	}
}
