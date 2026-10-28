package forge

// What a repository that has declared GitHub's check runs are not a
// source of verdicts for it actually gets (Input.IgnoreGitHubCI,
// model.RepoConfig.IgnoreGitHubCI). Each of these has a twin with the
// declaration off, because "everything behaves exactly as now when it is
// off" is half of what the setting has to be true of.

import (
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
)

var gateTime = time.Date(2026, 9, 18, 15, 0, 0, 0, time.UTC)

// openPullRequest is the detail read every test below starts with: open,
// mergeable, and sitting on one commit.
const openPullRequest = `{"state":"open","mergeable":true,` +
	`"head":{"ref":"topic","sha":"deadbeef"},"base":{"ref":"main"}}`

// redChecks is one completed, failing check run -- the shape that has
// been on every commit in this repository since GitHub Actions was
// blocked at the account level.
const redChecks = `{"check_runs":[{"name":"go","status":"completed","conclusion":"failure"}]}`

// queuedChecks is a check that has registered and not finished: PENDING,
// and the state the check-stall clock times.
const queuedChecks = `{"check_runs":[{"name":"go","status":"queued"}]}`

func gateClient(bodies ...string) (github.Client, *github.FakeTransport) {
	responses := make([]github.ApiResponse, 0, len(bodies))
	for _, body := range bodies {
		responses = append(responses, github.ApiResponse{Status: 200, Body: []byte(body)})
	}
	transport := github.NewFakeTransport(responses...)
	return github.NewClient(transport, nil), transport
}

// askedAboutChecks reports whether anything went to the Checks or
// Actions API at all. "Not read" rather than "read and disregarded" is
// the whole of the setting: an answer nobody uses is still a read that
// can fail, and a failed read used to make the pull request unreadable.
func askedAboutChecks(transport *github.FakeTransport) bool {
	for _, call := range transport.Calls {
		if strings.Contains(call.Path, "check-runs") || strings.Contains(call.Path, "/actions/") {
			return true
		}
	}
	return false
}

// The headline: a red GitHub check does not make the pull request
// unhealthy where GitHub is not a verdict source, and grain CI's pass is
// the whole answer.
func TestARedGitHubCheckDoesNotMakeThePullRequestUnhealthy(t *testing.T) {
	client, transport := gateClient(openPullRequest, redChecks)

	seen, err := Observe(client, testRef(), Input{
		IgnoreGitHubCI: true,
		GrainCIFor:     func(string) GrainCI { return GrainCI{Required: true, Passed: true} },
	}, gateTime)
	if err != nil {
		t.Fatal(err)
	}
	if seen.Health != Clean {
		t.Errorf("Health = %q, want Clean: a check nobody reads cannot make a pull request red", seen.Health)
	}
	if len(seen.Checks) != 0 {
		t.Errorf("Observation.Checks = %v, want none: there is nothing downstream should be able to list", seen.Checks)
	}
	if askedAboutChecks(transport) {
		t.Errorf("GitHub's checks were read anyway: %v", transport.Calls)
	}
}

// The twin with the declaration off, which is the control for all of
// this: the same red check, read and counted, exactly as before.
func TestWithoutTheDeclarationARedGitHubCheckStillFails(t *testing.T) {
	client, transport := gateClient(openPullRequest, redChecks)

	seen, err := Observe(client, testRef(), Input{
		GrainCIFor: func(string) GrainCI { return GrainCI{Required: true, Passed: true} },
	}, gateTime)
	if err != nil {
		t.Fatal(err)
	}
	if !askedAboutChecks(transport) {
		t.Fatalf("GitHub's checks were not read for an ordinary repository: %v", transport.Calls)
	}
	if len(seen.Checks) != 1 {
		t.Errorf("Observation.Checks = %v, want the one GitHub reported", seen.Checks)
	}
}

// The clock. A check that has registered and not finished is the state
// ChecksStalled times, and two hours of it is what hands a pull request
// to a person -- which is what happened to grain/task-149, waiting on a
// provider that was never going to report. Where GitHub is not a verdict
// source that state is not reachable: there is no unfinished check,
// because there is no check.
func TestTheStallClockDoesNotRunOnChecksNobodyReads(t *testing.T) {
	ForgetPendingChecks(testRef())
	client, _ := gateClient(openPullRequest, queuedChecks)

	seen, err := Observe(client, testRef(), Input{
		IgnoreGitHubCI: true,
		GrainCIFor:     func(string) GrainCI { return GrainCI{Required: true, Passed: true} },
	}, gateTime)
	if err != nil {
		t.Fatal(err)
	}
	if seen.Health != Clean {
		t.Fatalf("Health = %q, want Clean", seen.Health)
	}
	// Past any deadline the queue has: if the first call had started a
	// clock on that queued check, this one would be reading hours off it.
	later, _ := gateClient(openPullRequest, queuedChecks)
	again, err := Observe(later, testRef(), Input{
		IgnoreGitHubCI: true,
		GrainCIFor:     func(string) GrainCI { return GrainCI{Required: true, Passed: true} },
	}, gateTime.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if again.Stalled {
		t.Errorf("a day later the queue says it has been stalled on a check it never read")
	}
}

// The same unfinished check on an ordinary repository, which does start
// the clock and does reach the deadline -- the behaviour this setting
// must not quietly take away from everybody else.
func TestWithoutTheDeclarationAnUnfinishedCheckStillStalls(t *testing.T) {
	ForgetPendingChecks(testRef())
	client, _ := gateClient(openPullRequest, queuedChecks)
	if _, err := Observe(client, testRef(), Input{}, gateTime); err != nil {
		t.Fatal(err)
	}
	later, _ := gateClient(openPullRequest, queuedChecks)
	seen, err := Observe(later, testRef(), Input{}, gateTime.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if seen.Health != Pending {
		t.Fatalf("Health = %q, want Pending on an unfinished check", seen.Health)
	}
	if !seen.Stalled {
		t.Errorf("Stalled = false a day into an unfinished check, want the ordinary deadline")
	}
	ForgetPendingChecks(testRef())
}

// A Checks API that will not answer stops being fatal, because the
// verdict never depended on it. Before this, a repository whose gate was
// grain CI could still be made unreadable by a credential that had lost
// its Checks scope.
func TestAChecksAPIThatRefusesDoesNotMatterWhereNobodyReadsIt(t *testing.T) {
	transport := github.NewFakeTransport(
		github.ApiResponse{Status: 200, Body: []byte(openPullRequest)},
		github.ApiResponse{Status: 403, Body: []byte(`{"message":"Resource not accessible by integration"}`)},
	)
	client := github.NewClient(transport, nil)

	seen, err := Observe(client, testRef(), Input{
		IgnoreGitHubCI: true,
		GrainCIFor:     func(string) GrainCI { return GrainCI{Required: true, Passed: true} },
	}, gateTime)
	if err != nil {
		t.Fatalf("Observe() = %v, want the refusal to be beside the point", err)
	}
	if seen.Health != Clean {
		t.Errorf("Health = %q, want Clean", seen.Health)
	}
}

// The decision the setting is not allowed to get wrong: a repository
// that declares grain CI its gate and has no grain CI merges *nothing*,
// not everything. With GitHub's checks unread, an empty settled list
// would otherwise read Clean -- so the declaration requires grain CI by
// itself, and a commit with no verdict sits Pending until the ordinary
// stall deadline hands it to a person.
func TestDeclaringTheGateWithNoGrainCIMergesNothingRatherThanEverything(t *testing.T) {
	ForgetPendingChecks(testRef())
	client, _ := gateClient(openPullRequest, `{"check_runs":[]}`)

	seen, err := Observe(client, testRef(), Input{IgnoreGitHubCI: true}, gateTime)
	if err != nil {
		t.Fatal(err)
	}
	if seen.Health != Pending {
		t.Fatalf("Health = %q, want Pending: nobody has judged this commit at all", seen.Health)
	}
	if !seen.GrainCI.Required {
		t.Errorf("GrainCI.Required = false: the declaration swaps the gate, it does not remove it")
	}
	// And it does not sit there silently: the wait is the same bounded
	// one every other PENDING is under, so a person is told.
	later, _ := gateClient(openPullRequest, `{"check_runs":[]}`)
	again, err := Observe(later, testRef(), Input{IgnoreGitHubCI: true}, gateTime.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Stalled {
		t.Errorf("Stalled = false a day in, want the deadline that hands this to a person")
	}
	ForgetPendingChecks(testRef())
}
