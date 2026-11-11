// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
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

// git's own words for a forge 5xx are infra only out of the step that
// checks the tree out -- the first step, or one named for a checkout --
// and never out of a step that tests it.
func TestInfraSignatureReadsGitFiveHundredsOnlyFromACheckout(t *testing.T) {
	t.Parallel()
	step := func(name, out string) ci.StepRun {
		return ci.StepRun{Name: name, Conclusion: ci.ConclusionFailure, Output: out}
	}
	passed := ci.StepRun{Name: "Set up", Conclusion: ci.ConclusionSuccess}
	for _, tc := range []struct {
		name  string
		steps []ci.StepRun
		want  string
	}{
		{"502 in the first step", []ci.StepRun{step("prepare",
			"fatal: unable to access 'https://forge.example/acme/widgets/': The requested URL returned error: 502")},
			"the requested url returned error: 502"},
		{"503 in a checkout step", []ci.StepRun{passed, step("Checkout",
			"error: The requested URL returned error: 503\nfatal: expected flush after ref listing")},
			"the requested url returned error: 503"},
		{"504 in a git fetch", []ci.StepRun{passed, step("git fetch --tags origin",
			"The requested URL returned error: 504")}, "the requested url returned error: 504"},
		{"RPC failed", []ci.StepRun{passed, step("actions/checkout",
			"error: RPC failed; HTTP 502 curl 22 The requested URL returned error: 502")}, "the requested url returned error: 502"},
		{"RPC failed alone", []ci.StepRun{step("clone", "error: RPC failed; HTTP 504 curl 22")}, "rpc failed; http 504"},
		{"502 printed by a test", []ci.StepRun{passed, step("go test",
			"--- FAIL: TestProxy: The requested URL returned error: 502")}, ""},
		{"500 is not one", []ci.StepRun{step("Checkout", "The requested URL returned error: 500")}, ""},
		{"a runner that went away", []ci.StepRun{passed, step("Build the images",
			"The hosted runner lost communication with the server.")}, "lost communication with the server"},
		{"a sandbox that died", []ci.StepRun{passed, {Name: "Publish", Conclusion: ci.ConclusionFailure,
			Error: "infra: sandbox lost: running a step in ci-7: EOF"}}, "infra: sandbox lost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := ci.JobRun{JobID: "images", Conclusion: ci.ConclusionFailure, Steps: tc.steps}
			if got := infraSignature(job); got != tc.want {
				t.Fatalf("infraSignature = %q, want %q", got, tc.want)
			}
		})
	}
}

// A job whose sandbox died concludes errored with the sandbox-lost fault
// (pkg/grit/engine), and a run of nothing but those is an infra verdict.
func TestASandboxThatDiedIsAnInfraVerdict(t *testing.T) {
	t.Parallel()
	run := ci.Run{Conclusion: ci.ConclusionErrored, Jobs: []ci.JobRun{{JobID: "images",
		Conclusion: ci.ConclusionErrored, Error: "infra: sandbox lost: running a step in ci-7: connection reset by peer"}}}
	if why, infra := infraVerdict(run); !infra || why == "" {
		t.Fatalf("infraVerdict = %v (%q), want an infra verdict", infra, why)
	}
	if sig := infraSignature(run.Jobs[0]); sig != "infra: sandbox lost" {
		t.Fatalf("infraSignature = %q, want the sandbox-lost fault", sig)
	}
}

func TestGitHubInfraVerdictReadsTheLatestAttemptsFailingSteps(t *testing.T) {
	t.Parallel()
	checkout := model.GitHubCIJob{JobName: "release images", Conclusion: "failure", RunAttempt: 1,
		FailingStep: "Checkout", FailureTail: "remote: Internal error\nfatal: unable to access: The requested URL returned error: 502\n"}
	inTest := model.GitHubCIJob{JobName: "release images", Conclusion: "failure", RunAttempt: 1,
		FailingStep: "Run the container e2e suite", FailureTail: "The requested URL returned error: 502\n--- FAIL"}
	runner := model.GitHubCIJob{JobName: "release images", Conclusion: "failure", RunAttempt: 1,
		FailingStep: "Build the images", Output: "The hosted runner lost communication with the server."}
	passed := model.GitHubCIJob{JobName: "lint", Conclusion: "success", RunAttempt: 1}
	for _, tc := range []struct {
		name    string
		jobs    []model.GitHubCIJob
		attempt int
		infra   bool
	}{
		{"checkout 502", []model.GitHubCIJob{checkout, passed}, 1, true},
		{"502 in a test step", []model.GitHubCIJob{inTest}, 1, false},
		{"runner lost", []model.GitHubCIJob{runner}, 1, true},
		{"checkout beside a test failure", []model.GitHubCIJob{checkout, inTest}, 1, false},
		{"an earlier attempt's test failure does not count",
			[]model.GitHubCIJob{inTest, func() model.GitHubCIJob { j := checkout; j.RunAttempt = 2; return j }()}, 2, true},
		{"nothing failed", []model.GitHubCIJob{passed}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			why, infra := gitHubInfraVerdict(tc.jobs, tc.attempt)
			if infra != tc.infra {
				t.Fatalf("gitHubInfraVerdict = %v (%q), want %v", infra, why, tc.infra)
			}
		})
	}
}

// The 2026-11-11 outage: a trunk run whose jobs died on the runner's
// resolver inside `apt-get install` is the deployment's, and is re-run
// rather than filed against anyone -- on grain CI and on Actions alike.
func TestInfraVerdictKnowsARunnerDNSOutage(t *testing.T) {
	t.Parallel()
	const tail = "E: Failed to fetch https://security.ubuntu.com/ubuntu/pool/universe/e/expect/expect.deb  " +
		"Temporary failure resolving 'security.ubuntu.com'\n" +
		"E: Unable to fetch some archives, maybe run apt update or try with --fix-missing?\n"
	dns := ci.JobRun{JobID: "go unit", Conclusion: ci.ConclusionFailure, Steps: []ci.StepRun{
		{Name: "Checkout", Conclusion: ci.ConclusionSuccess},
		{Name: "Install system dependencies", Conclusion: ci.ConclusionFailure, Output: tail},
	}}
	why, infra := infraVerdict(ci.Run{Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{dns}})
	if !infra || !strings.Contains(why, "temporary failure resolving") {
		t.Fatalf("infraVerdict = %v (%q), want infra on the resolver", infra, why)
	}
	// A package really gone from the mirror is a 404 the workflow file in
	// the tree can fix, and is not the runner's.
	gone := ci.JobRun{JobID: "go unit", Conclusion: ci.ConclusionFailure, Steps: []ci.StepRun{
		{Name: "Checkout", Conclusion: ci.ConclusionSuccess},
		{Name: "Install system dependencies", Conclusion: ci.ConclusionFailure,
			Output: "E: Failed to fetch https://archive.example/pool/e/expect.deb  404  Not Found\n" +
				"E: Unable to fetch some archives, maybe run apt update or try with --fix-missing?\n"},
	}}
	if why, infra := infraVerdict(ci.Run{Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{gone}}); infra {
		t.Fatalf("a 404 from the mirror read as infra (%q)", why)
	}
	jobs := []model.GitHubCIJob{{JobName: "go unit", Conclusion: "failure", RunAttempt: 1,
		FailingStep: "Install system dependencies", FailureTail: tail}}
	if why, infra := gitHubInfraVerdict(jobs, 1); !infra {
		t.Fatalf("gitHubInfraVerdict = %v (%q), want infra on the resolver", infra, why)
	}
}
