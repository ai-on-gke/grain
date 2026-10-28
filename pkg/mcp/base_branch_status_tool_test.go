package mcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/github"
)

// callBase runs base_branch_status the way a client would, off the same
// roster the other CI tools come from.
func callBase(t *testing.T, client PullRequestReader, scope PullRequestScope) Result {
	t.Helper()
	return namedTool(t, NewPullRequestTools(client, nil, scope), "base_branch_status").
		Handler(context.Background(), map[string]any{})
}

// The whole point of the tool: a check failing on the base is failing
// without this run's change in it, and the answer has to say so in words
// rather than leaving a run to infer it from two lists of check names.
func TestBaseBranchStatusNamesTheBasesOwnFailures(t *testing.T) {
	client := &fakePullRequests{
		heads: map[string]*github.BranchHead{
			"main": {SHA: "aaaaaaaabbbbbbbb", Message: "merge #820"},
		},
		checksByRef: map[string][]github.CheckRun{
			"aaaaaaaabbbbbbbb": {
				{Name: "go", Status: "completed", Conclusion: conclusion("failure")},
				{Name: "lint", Status: "completed", Conclusion: conclusion("success")},
			},
		},
		jobLogs: []github.JobLog{{
			Name: "go", URL: "https://example.test/job/1", Log: "panic: already broken on main",
		}},
		defaultBranch: "main",
	}

	res := callBase(t, client, testScope)
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	for _, want := range []string{
		"main", "aaaaaaa", "FAILING", "go (failure)", "1 failing",
		// The conclusion, which is the part a run cannot work out for
		// itself, and the base's own log so it can match the failure.
		"without your change in them", "already failing on main",
		"panic: already broken on main",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("answer does not carry %q:\n%s", want, res.Text)
		}
	}
	// A run must not read this as an instruction to go and fix main.
	if strings.Contains(res.Text, "Reproduce those failures in your checkout") {
		t.Errorf("answer tells the run to fix the base's failures:\n%s", res.Text)
	}
}

// The same failing base, read by the one run that must not leave it
// alone: the merge queue's repair of the branch the queue is stuck on
// (PullRequestScope.Repair). Inherited breakage still stops that branch
// merging, so the answer has to send the run at it rather than giving it
// the "report, do not fix" conclusion every other run gets.
func TestBaseBranchStatusTellsARepairToFixInheritedBreakage(t *testing.T) {
	client := &fakePullRequests{
		heads: map[string]*github.BranchHead{
			"main": {SHA: "aaaaaaaabbbbbbbb", Message: "merge #820"},
		},
		checksByRef: map[string][]github.CheckRun{
			"aaaaaaaabbbbbbbb": {
				{Name: "go", Status: "completed", Conclusion: conclusion("failure")},
			},
		},
		defaultBranch: "main",
	}
	scope := testScope
	scope.Repair = true

	res := callBase(t, client, scope)
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	for _, want := range []string{
		// Still the same diagnosis -- the run is told where the failure
		// came from -- and then the opposite conclusion.
		"without your change in them", "Fix it here anyway",
		"already failing on main",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("answer does not carry %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "leave it alone") {
		t.Errorf("a repair run is still told to leave the base's breakage alone:\n%s", res.Text)
	}
	// The tool's own description has to agree with its answer: a repair
	// deciding whether to call this at all reads that first.
	desc := namedTool(t, NewPullRequestTools(client, nil, scope), "base_branch_status").Description
	if !strings.Contains(desc, "fix it here anyway") {
		t.Errorf("the tool's description still advertises reporting rather than fixing:\n%s", desc)
	}
	// And an ordinary run's answer is untouched by any of this.
	ordinary := callBase(t, client, testScope)
	if !strings.Contains(ordinary.Text, "leave it alone") {
		t.Errorf("an ordinary run no longer inherits the leave-it-alone conclusion:\n%s", ordinary.Text)
	}
}

// The other half of the same question, and the answer that costs a run
// its excuse: a green base means a red branch is the run's own doing.
func TestBaseBranchStatusSaysAGreenBaseLeavesTheFailureYours(t *testing.T) {
	client := &fakePullRequests{
		head:          &github.BranchHead{SHA: "ccccccccdddddddd", Message: "green as grass"},
		checks:        []github.CheckRun{{Name: "go", Status: "completed", Conclusion: conclusion("success")}},
		defaultBranch: "main",
	}

	res := callBase(t, client, testScope)
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	if !strings.Contains(res.Text, "your own change introduced") {
		t.Errorf("a green base does not put the failure back on the run:\n%s", res.Text)
	}
	// Nothing failed on the base, so nothing is read: the logs cost three
	// GitHub calls and a green commit has none of them to find.
	if client.logReads != 0 {
		t.Errorf("read the failing jobs' logs %d time(s) for a green base, want 0", client.logReads)
	}
}

// A cancellation on the base is a different fact from a cancellation on
// the run's own branch, and gets a different sentence. Nobody is merging
// the base, so there is no merge to hold and nothing here for the run to
// push -- what a cancelled check costs is the comparison itself, since a
// check the base never ran can neither exonerate the run's change nor
// convict it. The merge-queue paragraph pull_request_status and
// wait_for_checks carry (cancelledHold) must not turn up here telling a
// run to push at somebody else's branch.
func TestBaseBranchStatusSaysACancelledBaseCheckSettlesNothing(t *testing.T) {
	client := &fakePullRequests{
		head: &github.BranchHead{SHA: "ccccccccdddddddd", Message: "merge #900"},
		checks: []github.CheckRun{
			{Name: "go", Status: "completed", Conclusion: conclusion("cancelled")},
			{Name: "lint", Status: "completed", Conclusion: conclusion("success")},
		},
		defaultBranch: "main",
	}

	res := callBase(t, client, testScope)
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	for _, want := range []string{
		"0 failing, 0 not finished, 1 cancelled, 1 otherwise done",
		"cancelled go (cancelled)",
		"has no verdict from them at all",
		"treat it",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("answer does not carry %q:\n%s", want, res.Text)
		}
	}
	// The green conclusion, which would hand the run a base verdict the
	// base never gave.
	if strings.Contains(res.Text, "your own change introduced") {
		t.Errorf("a base with a cancelled check still reads as fully green:\n%s", res.Text)
	}
	if strings.Contains(res.Text, "merge queue") || strings.Contains(res.Text, "git push origin") {
		t.Errorf("the branch's own merge-queue advice leaked into the base report:\n%s", res.Text)
	}
}

// An empty scope.Base is the ordinary task, not a missing value: it means
// the repo's default branch, and the tool resolves it rather than
// reporting that it has no base.
func TestBaseBranchStatusResolvesAnEmptyBaseToTheDefaultBranch(t *testing.T) {
	client := &fakePullRequests{
		head:          &github.BranchHead{SHA: "eeeeeeeeffffffff", Message: "trunk"},
		defaultBranch: "trunk-main",
	}

	res := callBase(t, client, testScope)
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	if !strings.Contains(res.Text, "trunk-main") || !strings.Contains(res.Text, "default branch") {
		t.Errorf("answer does not name the resolved default branch:\n%s", res.Text)
	}
	// And it is the branch that was actually read, rather than a name
	// printed over a read of something else.
	if !slices.Contains(client.refs, "trunk-main") {
		t.Errorf("refs = %v, want a read of trunk-main", client.refs)
	}
}

// A task with a base of its own is stacked on somebody else's branch, and
// comparing it against the repo's trunk would answer a question nobody
// asked -- so the task's base wins and the default branch is never read.
func TestBaseBranchStatusPrefersTheTasksOwnBase(t *testing.T) {
	client := &fakePullRequests{
		head:             &github.BranchHead{SHA: "1111222233334444", Message: "stacked"},
		defaultBranchErr: errors.New("base_branch_status must not resolve the default branch here"),
	}
	scope := testScope
	scope.Base = "grain/task-7"

	res := callBase(t, client, scope)
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	if !strings.Contains(res.Text, "grain/task-7") {
		t.Errorf("answer does not name the task's own base:\n%s", res.Text)
	}
	if strings.Contains(res.Text, "default branch") {
		t.Errorf("answer calls the task's own base a default branch:\n%s", res.Text)
	}
}

// A repo whose CI only runs on pull requests has no checks on its base at
// all, which is not the same as a healthy base -- and a run told "0
// failing" would read it as exactly that.
func TestBaseBranchStatusDoesNotReadNoChecksAsHealthy(t *testing.T) {
	res := callBase(t, &fakePullRequests{
		head:          &github.BranchHead{SHA: "5555666677778888", Message: "quiet"},
		defaultBranch: "main",
	}, testScope)
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	for _, want := range []string{"no checks at all", "nobody looked"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("answer does not carry %q:\n%s", want, res.Text)
		}
	}
}

// A base branch that is gone is a fact about the task rather than about
// the run's change, and the finish path already retargets it
// (orchestrator.pullRequestBase). Reporting it beats an empty check list,
// which reads as "the base is fine".
func TestBaseBranchStatusReportsAVanishedBase(t *testing.T) {
	scope := testScope
	scope.Base = "release-1"
	res := callBase(t, &fakePullRequests{}, scope)
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	if !strings.Contains(res.Text, "release-1 does not exist") {
		t.Errorf("answer does not say the base is gone:\n%s", res.Text)
	}
}

// A credential that cannot read the repo's default branch is a real
// error, not an "unknown": telling a run there is no base would send it
// off to treat every failure as its own.
func TestBaseBranchStatusErrorsWhenTheDefaultBranchIsUnreadable(t *testing.T) {
	res := callBase(t, &fakePullRequests{
		defaultBranchErr: errors.New("connection refused"),
	}, testScope)
	if !res.IsError {
		t.Fatalf("IsError = false, want the unreadable-base error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "connection refused") {
		t.Errorf("answer loses GitHub's own error:\n%s", res.Text)
	}
}

// The scope is fixed at construction here exactly as it is for
// pull_request_status: the second branch this tool adds is the one the
// run's work merges into, and no argument can make it a third.
func TestBaseBranchStatusOnlyEverReadsItsOwnScope(t *testing.T) {
	client := &fakePullRequests{
		head:          &github.BranchHead{SHA: "99998888aaaabbbb", Message: "x"},
		defaultBranch: "main",
	}
	res := namedTool(t, NewPullRequestTools(client, nil, testScope), "base_branch_status").
		Handler(context.Background(), map[string]any{
			"owner": "attacker", "repo": "secrets", "branch": "wherever",
		})
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	for _, repo := range client.repos {
		if repo != "acme/widgets" {
			t.Errorf("read %q, want only acme/widgets", repo)
		}
	}
	for _, ref := range client.refs {
		if ref != "main" && ref != "99998888aaaabbbb" {
			t.Errorf("a read named %q, want only the base branch and its tip", ref)
		}
	}
	if len(client.repos) == 0 {
		t.Error("the tool read nothing at all, so this proves nothing")
	}
}

// A run with no repo attached gets the tool anyway, answering why there
// is nothing to look at -- the nil-client contract NewPullRequestTools
// documents for the whole roster.
func TestBaseBranchStatusWithoutARepoSaysSo(t *testing.T) {
	res := callBase(t, nil, PullRequestScope{})
	if !res.IsError || !strings.Contains(res.Text, "no GitHub repository configured") {
		t.Errorf("answer = %q (isError=%v), want the nothing-configured explanation",
			res.Text, res.IsError)
	}
}

// Knowing the tool exists is the hard part, and the moment it matters is
// the moment a report names a failing check -- so both of the tools that
// name one point at it there rather than trusting the roster.
func TestFailingReportsPointAtTheBaseComparison(t *testing.T) {
	status := call(t, &fakePullRequests{
		head:   &github.BranchHead{SHA: "0123456789abcdef", Message: "wip"},
		checks: []github.CheckRun{{Name: "go", Status: "completed", Conclusion: conclusion("failure")}},
	}, testScope)
	if !strings.Contains(status.Text, "base_branch_status") {
		t.Errorf("pull_request_status does not point at the base comparison:\n%s", status.Text)
	}

	waited, err := waiterFor(&scriptedChecks{
		head:   pushed(),
		rounds: [][]github.CheckRun{{done("go", "failure")}},
	}, &fakeClock{}).wait(context.Background(), DefaultWaitForChecksTimeout)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !strings.Contains(waited, "base_branch_status") {
		t.Errorf("wait_for_checks does not point at the base comparison:\n%s", waited)
	}
}
