package orchestrator

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/ci"
)

func TestInfraVerdictTellsTheDeploymentsRedFromTheTrees(t *testing.T) {
	t.Parallel()
	quota := ci.JobRun{JobID: "smoke", Conclusion: ci.ConclusionFailure,
		Steps: []ci.StepRun{{Conclusion: ci.ConclusionFailure, Output: "pull: toomanyrequests: rate limited"}}}
	grant := ci.JobRun{JobID: "live", Conclusion: ci.ConclusionFailure, Error: "capability github-sandbox not granted"}
	test := ci.JobRun{JobID: "go", Conclusion: ci.ConclusionFailure,
		Steps: []ci.StepRun{{Conclusion: ci.ConclusionFailure, Output: "--- FAIL: TestX: got 503 Service Unavailable"}}}
	for _, tc := range []struct {
		name  string
		run   ci.Run
		infra bool
	}{
		{"cancelled", ci.Run{Conclusion: ci.ConclusionCancelled}, true},
		{"errored", ci.Run{Conclusion: ci.ConclusionErrored}, true},
		{"interrupted", ci.Run{Conclusion: ci.ConclusionCancelled, Interrupted: true}, true},
		{"quota", ci.Run{Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{quota}}, true},
		{"capability", ci.Run{Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{grant,
			{JobID: "ok", Conclusion: ci.ConclusionSuccess}}}, true},
		{"test failure", ci.Run{Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{test}}, false},
		{"test failure beside a quota", ci.Run{Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{quota, test}}, false},
		{"failure with no job", ci.Run{Conclusion: ci.ConclusionFailure}, false},
		{"success", ci.Run{Conclusion: ci.ConclusionSuccess}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			why, infra := infraVerdict(tc.run)
			if infra != tc.infra {
				t.Fatalf("infraVerdict = %v (%q), want %v", infra, why, tc.infra)
			}
			if infra && why == "" {
				t.Fatal("an infra verdict with no reason given")
			}
		})
	}
}

// Only the end of a step's output is read: a signature early in a long
// log is the tool recovering, not the reason it gave up.
func TestInfraSignatureReadsTheEndOfAStepsOutput(t *testing.T) {
	t.Parallel()
	job := ci.JobRun{Conclusion: ci.ConclusionFailure, Steps: []ci.StepRun{{
		Conclusion: ci.ConclusionFailure,
		Output:     "429 Too Many Requests, retrying\n" + strings.Repeat("x", 2*infraStepTail) + "\nFAIL",
	}}}
	if sig := infraSignature(job); sig != "" {
		t.Fatalf("infraSignature = %q, want nothing for a signature outside the tail", sig)
	}
}

func TestInfraRetriesAreCountedOnTheRequester(t *testing.T) {
	t.Parallel()
	base := "workflow-schedule:nightly"
	if got := infraRetryRequester(base, 0); got != base {
		t.Fatalf("an original run asks as %q, want %q", got, base)
	}
	for n := 0; n <= maxInfraRetries; n++ {
		if got := infraRetriesOf(infraRetryRequester(base, n)); got != n {
			t.Fatalf("re-run %d reads as %d", n, got)
		}
	}
	if !strings.HasPrefix(infraRetryRequester(base, 2), base) {
		t.Fatal("a re-run's requester lost its base as a prefix")
	}
	if got := infraRetriesOf("workflow-schedule:x#infra-retry-nope"); got != 0 {
		t.Fatalf("a malformed count reads as %d, want 0", got)
	}
}
