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

package orchestrator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// aptDNSTail is how every hosted-runner job ended from 13:56 UTC on
// 2026-11-11: the runner's resolver stopped answering, and apt said so.
const aptDNSTail = "##[group]Run actions/checkout@v4\n##[endgroup]\nChecked out.\n" +
	"##[group]Run sudo apt-get update -qq && sudo apt-get install -y expect socat\n" +
	"W: Failed to fetch mirror+file:/etc/apt/apt-mirrors.txt/dists/resolute/InRelease  " +
	"Temporary failure resolving 'security.ubuntu.com'\n" +
	"E: Failed to fetch https://security.ubuntu.com/ubuntu/pool/universe/e/expect/expect_5.45.4-4build1_amd64v3.deb  " +
	"Temporary failure resolving 'security.ubuntu.com'\n" +
	"E: Unable to fetch some archives, maybe run apt update or try with --fix-missing?\n" +
	"##[error]Process completed with exit code 100.\n" +
	"Post job cleanup.\n"

// An outage on the runners is waited out with re-runs of the failed jobs,
// not repaired: no commit clears it, and every repair spent on it is one
// the pull request does not have for a red it really caused. Once the
// re-runs are spent it is an ordinary red again, and gets the repair.
func TestARunnerDNSOutageIsReRunNotRepaired(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "go unit", Status: "completed", Conclusion: strPtr("failure")},
		{Name: "go e2e", Status: "completed", Conclusion: strPtr("failure")},
	}
	sim.WorkflowJobs[branch] = []githubsim.WorkflowJob{
		{Name: "go unit", Conclusion: "failure", Log: aptDNSTail},
		{Name: "go e2e", Conclusion: "failure", Log: aptDNSTail},
	}

	now := baseTime
	for n := 1; n <= 2; n++ {
		if err := orchestrator.SyncPullRequests(ctx, store, client, now); err != nil {
			t.Fatalf("SyncPullRequests (cycle %d): %v", n, err)
		}
		if got := len(sim.RerunFailedJobs); got != n {
			t.Fatalf("cycle %d: %d re-run requests, want %d", n, got, n)
		}
		obs := repairObservationOf(t, ctx, store, task.ID)
		if obs.RepairInFlight() || obs.RepairsSpent() != 0 {
			t.Fatalf("cycle %d: a repair was spent on a runner outage (in flight %v, spent %d)",
				n, obs.RepairInFlight(), obs.RepairsSpent())
		}
		if !strings.HasPrefix(obs.MergeQueueNote, "waiting out an infrastructure failure") {
			t.Errorf("cycle %d: the row does not say it is waiting out an infrastructure failure: %q",
				n, obs.MergeQueueNote)
		}
		if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 0 {
			t.Fatalf("cycle %d: the queue commented on a runner outage: %q", n, bodies)
		}
		now = now.Add(10 * time.Minute)
	}

	// The re-runs are spent (the sim's run is at attempt 3 and still
	// failing the same way): an ordinary red now, and it gets its repair.
	if err := orchestrator.SyncPullRequests(ctx, store, client, now); err != nil {
		t.Fatalf("SyncPullRequests after the re-runs: %v", err)
	}
	if got := len(sim.RerunFailedJobs); got != 2 {
		t.Fatalf("%d re-run requests, want the cap of 2", got)
	}
	obs := repairObservationOf(t, ctx, store, task.ID)
	if !obs.RepairInFlight() || obs.RepairsSpent() != 1 {
		t.Fatalf("after the re-runs: in flight %v, spent %d -- want the ordinary repair",
			obs.RepairInFlight(), obs.RepairsSpent())
	}
}

// A build with one job lost to the runner and one genuinely red is the
// tree's: the red job is the part worth sending an agent at.
func TestAMixedInfraAndTestFailureStillGetsARepair(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "go unit", Status: "completed", Conclusion: strPtr("failure")},
		{Name: "go", Status: "completed", Conclusion: strPtr("failure")},
	}
	sim.WorkflowJobs[branch] = []githubsim.WorkflowJob{
		{Name: "go unit", Conclusion: "failure", Log: aptDNSTail},
		{Name: "go", Conclusion: "failure", Log: "--- FAIL: TestX (0.01s)\n    x_test.go:12: got 1, want 2\nFAIL\n" +
			"##[error]Process completed with exit code 1.\n"},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if got := len(sim.RerunFailedJobs); got != 0 {
		t.Fatalf("a mixed build was re-run %d times instead of repaired", got)
	}
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a mixed build with a real test failure was not sent back for repair")
	}
}

// An apt warning in a step that went on to pass is not why the job
// failed: only the failing step's tail is evidence.
func TestAResolverWarningBeforeARealFailureIsTheTrees(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "go", Status: "completed", Conclusion: strPtr("failure")},
	}
	sim.WorkflowJobs[branch] = []githubsim.WorkflowJob{
		{Name: "go", Conclusion: "failure", Log: "##[group]Run sudo apt-get install -y expect\n##[endgroup]\n" +
			"W: Temporary failure resolving 'security.ubuntu.com'\n" +
			"Setting up expect ...\n##[group]Run go test ./...\n##[endgroup]\n" + strings.Repeat("ok  \tgithub.com/acme/widgets/pkg\t0.1s\n", 10) +
			"--- FAIL: TestX (0.01s)\nFAIL\n##[error]Process completed with exit code 1.\n"},
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if len(sim.RerunFailedJobs) != 0 || !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatalf("re-runs %d, repair in flight %v: want the repair a real failure earns",
			len(sim.RerunFailedJobs), repairInFlightNow(t, ctx, store, task.ID))
	}
}
