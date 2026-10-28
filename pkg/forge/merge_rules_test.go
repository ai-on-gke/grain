package forge

// A pull request gated by its repository's merge rules (Policy,
// grain/task-1659): which CI system decides, which GitHub checks have to
// be there, and how long to wait.

import (
	"reflect"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
)

func TestMergeRulesGateOnTheCISystemTheyName(t *testing.T) {
	yes := true
	detail := github.PullRequestDetail{State: "open", Mergeable: &yes, HeadSHA: "abc"}
	green := []github.CheckRun{{Name: "build", Status: "completed", Conclusion: str("success")}}
	red := []github.CheckRun{{Name: "build", Status: "completed", Conclusion: str("failure")}}
	passed := GrainCI{Required: true, Passed: true}
	failed := GrainCI{Required: true, Failed: true}
	pending := GrainCI{Required: true}

	cases := []struct {
		name   string
		mode   string
		checks []github.CheckRun
		grain  GrainCI
		want   Health
	}{
		{"grain: a red GitHub check is not read", "grain", red, passed, Clean},
		{"grain: grain's red is red", "grain", green, failed, Failing},
		{"github: GitHub's red is red", "github", red, GrainCI{}, Failing},
		{"github: GitHub's green is green", "github", green, GrainCI{}, Clean},
		{"github: a granted grain red still gates", "github", green, failed, Failing},
		{"github: a granted grain run still pending", "github", green, pending, Pending},
		{"both: both green", "both", green, passed, Clean},
		{"both: GitHub red, grain green", "both", red, passed, Failing},
		{"both: GitHub green, grain red", "both", green, failed, Failing},
		{"both: GitHub green, grain not yet", "both", green, pending, Pending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := policyHealth(detail, c.checks, "", true, true, c.grain, Policy{Mode: c.mode, AllChecks: true})
			if got != c.want {
				t.Fatalf("health = %s, want %s", got, c.want)
			}
		})
	}
}

func TestARequiredGitHubCheckThatIsMissingOrFailingBlocks(t *testing.T) {
	yes := true
	detail := github.PullRequestDetail{State: "open", Mergeable: &yes, HeadSHA: "abc"}
	policy := Policy{Mode: "github", RequiredChecks: []string{"build", "cla/google"}}

	onlyBuild := []github.CheckRun{{Name: "build", Status: "completed", Conclusion: str("success")}}
	got, missing := policyHealth(detail, onlyBuild, "", true, true, GrainCI{}, policy)
	if got != Pending || !reflect.DeepEqual(missing, []string{"cla/google"}) {
		t.Fatalf("cla/google absent: health = %s, missing = %v; want PENDING naming cla/google", got, missing)
	}

	claRed := append(onlyBuild, github.CheckRun{Name: "cla/google", Status: "completed", Conclusion: str("failure")})
	if got, _ := policyHealth(detail, claRed, "", true, true, GrainCI{}, policy); got != Failing {
		t.Fatalf("cla/google red: health = %s, want FAILING", got)
	}

	// Named checks only: a red check nobody asked for does not gate.
	unasked := append(onlyBuild,
		github.CheckRun{Name: "cla/google", Status: "completed", Conclusion: str("success")},
		github.CheckRun{Name: "lint", Status: "completed", Conclusion: str("failure")})
	if got, _ := policyHealth(detail, unasked, "", true, true, GrainCI{}, policy); got != Clean {
		t.Fatalf("only an unnamed check red: health = %s, want CLEAN", got)
	}
	// ...unless something else asks for every check.
	policy.AllChecks = true
	if got, _ := policyHealth(detail, unasked, "", true, true, GrainCI{}, policy); got != Failing {
		t.Fatalf("every check gates: health = %s, want FAILING", got)
	}
}

func TestMergeRulesModeReplacesTheRepositorysForThisPullRequest(t *testing.T) {
	// The repository is on grain CI and does not read GitHub; the rules
	// say both, so GitHub's red check is read and holds the merge.
	client, transport := gateClient(openPullRequest, redChecks)
	seen, err := Observe(client, Ref{Owner: "o", Repo: "r", Number: 1}, Input{
		IgnoreGitHubCI: true, CIMode: "grain",
		PolicyFor:  func(string) *Policy { return &Policy{Mode: "both", AllChecks: true} },
		GrainCIFor: func(string) GrainCI { return GrainCI{Required: true, Passed: true} },
	}, gateTime)
	if err != nil {
		t.Fatal(err)
	}
	if !askedAboutChecks(transport) || seen.Health != Failing {
		t.Fatalf("read checks = %v, health = %s; want GitHub read and FAILING", askedAboutChecks(transport), seen.Health)
	}
}

func TestAPathsStallTimeoutShortensTheDeadline(t *testing.T) {
	defer SetCheckStallDeadline(2 * time.Hour)()
	ref := Ref{Owner: "o", Repo: "r", Number: 7}
	defer ForgetPendingChecks(ref)
	ChecksStalledWithin(ref, "abc", gateTime, 30*time.Minute)
	if stalled, d := ChecksStalledWithin(ref, "abc", gateTime.Add(29*time.Minute), 30*time.Minute); stalled || d != 30*time.Minute {
		t.Fatalf("at 29m: stalled = %v, deadline = %s; want not yet, 30m", stalled, d)
	}
	if stalled, _ := ChecksStalledWithin(ref, "abc", gateTime.Add(31*time.Minute), 30*time.Minute); !stalled {
		t.Fatal("at 31m: not stalled, want stalled under a 30m stall_timeout")
	}
	if stalled, d := ChecksStalledWithin(ref, "abc", gateTime.Add(31*time.Minute), 5*time.Hour); stalled || d != 2*time.Hour {
		t.Fatalf("a longer timeout: stalled = %v, deadline = %s; want the deployment's 2h", stalled, d)
	}
}

func str(s string) *string { return &s }
