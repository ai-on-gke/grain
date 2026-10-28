package orchestrator

// The merge queue's one repository-specific rule: a pull request against
// the deployment's own state repository is not clean without the check
// that deployment posts on it itself (pkg/staterepo/postcheck.go).
//
// Every case here is a shape that is CLEAN without the rule and must not
// be with it. That asymmetry is the point: a self-posted check is the
// only kind whose *absence* carries information -- a deployment that was
// down for the life of a settings pull request leaves exactly the same
// empty check list a repository with no CI does -- and reading that as
// clean is a gate that fails open on the one repository where what is
// merged decides what grain runs.

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestARequiredCheckThatIsMissingBlocksAnOtherwiseCleanPullRequest(t *testing.T) {
	t.Parallel()
	yes := true
	success := "success"
	detail := github.PullRequestDetail{State: "open", Mergeable: &yes}
	// Somebody else's CI, green, and settled. Without the rule this is
	// the shape that merges.
	checks := []github.CheckRun{{Name: "go", Status: "completed", Conclusion: &success}}
	if got := healthFrom(detail, checks, "", true, true); got != model.PrClean {
		t.Fatalf("without a required check this reads %q, so the test below proves nothing", got)
	}
	if got := healthFrom(detail, checks, "grain state check", true, true); got != model.PrPending {
		t.Fatalf("got %q, want pending: the check this deployment posts itself is not there", got)
	}
}

func TestARequiredCheckThatIsMissingBlocksASettledEmptyCheckList(t *testing.T) {
	t.Parallel()
	yes := true
	detail := github.PullRequestDetail{State: "open", Mergeable: &yes}
	// The state a deployment that never saw the pull request leaves
	// behind: nothing reported, and reported nothing for long enough that
	// the queue would otherwise believe there is nothing to report.
	if got := healthFrom(detail, nil, "", true, true); got != model.PrClean {
		t.Fatalf("without a required check an empty settled list reads %q", got)
	}
	if got := healthFrom(detail, nil, "grain state check", true, true); got != model.PrPending {
		t.Fatalf("got %q, want pending: an empty list is not a checked pull request here", got)
	}
}

func TestARequiredCheckThatIsThereDecidesNothingByItself(t *testing.T) {
	t.Parallel()
	yes := true
	success, failure := "success", "failure"
	detail := github.PullRequestDetail{State: "open", Mergeable: &yes}
	green := []github.CheckRun{{Name: "grain state check", Status: "completed", Conclusion: &success}}
	if got := healthFrom(detail, green, "grain state check", true, true); got != model.PrClean {
		t.Fatalf("got %q, want clean", got)
	}
	// Present and red is failing, exactly as any other check would be:
	// the rule is about absence, and adds nothing to the verdict on a
	// check that actually ran.
	red := []github.CheckRun{{Name: "grain state check", Status: "completed", Conclusion: &failure}}
	if got := healthFrom(detail, red, "grain state check", true, true); got != model.PrFailing {
		t.Fatalf("got %q, want failing", got)
	}
	// Present and unfinished is pending, which is what grain leaves
	// behind between creating the check run and concluding it.
	running := []github.CheckRun{{Name: "grain state check", Status: "in_progress"}}
	if got := healthFrom(detail, running, "grain state check", true, true); got != model.PrPending {
		t.Fatalf("got %q, want pending", got)
	}
}

func TestTheGateOnlyAppliesToTheStateRepository(t *testing.T) {
	t.Parallel()
	state := model.RepoRef{Owner: "bwsalmon", Name: "grain-home"}
	other := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	gate := StateRepoGate{Repo: state, Check: "grain state check"}
	if got := gate.requiredCheckFor(model.PullRequestRef{Repo: state, Number: 1}); got != "grain state check" {
		t.Fatalf("the state repository's own pull request requires %q", got)
	}
	if got := gate.requiredCheckFor(model.PullRequestRef{Repo: other, Number: 1}); got != "" {
		t.Fatalf("a pull request somewhere else requires %q, and should require nothing", got)
	}
	// A deployment with a state repository that posts no check of its own
	// -- which is almost all of them -- changes nothing about how its own
	// pull requests are judged.
	quiet := StateRepoGate{Repo: state}
	if got := quiet.requiredCheckFor(model.PullRequestRef{Repo: state, Number: 1}); got != "" {
		t.Fatalf("a deployment that posts no check required %q", got)
	}
}

func TestOnlyAStateRepoMergeIsAnnounced(t *testing.T) {
	t.Parallel()
	state := model.RepoRef{Owner: "bwsalmon", Name: "grain-home"}
	told := 0
	gate := StateRepoGate{Repo: state, Merged: func() { told++ }}
	gate.merged(model.PullRequestRef{Repo: model.RepoRef{Owner: "bwsalmon", Name: "grain"}, Number: 1})
	if told != 0 {
		t.Fatal("a merge somewhere else told the state manager to sync")
	}
	gate.merged(model.PullRequestRef{Repo: state, Number: 1})
	if told != 1 {
		t.Fatalf("the state repository's own merge told the state manager %d times, want 1", told)
	}
	// And a gate nobody wired is a gate nobody calls, rather than a nil
	// dereference on the reconciler's goroutine.
	StateRepoGate{Repo: state}.merged(model.PullRequestRef{Repo: state, Number: 1})
}
