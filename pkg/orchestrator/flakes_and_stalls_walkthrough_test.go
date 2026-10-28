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

// docs/test-plans/2026-10-05.md §3, driven through the real grain CI
// service rather than fakeCI: a ci.Service planning from a workflow file
// on disk, a ci.Engine running its one job through a sandbox that is a
// bash shell on this machine, a DirCache in front of it, and the merge
// queue's own reconcilers reading the verdicts. Only GitHub is simulated
// (githubsim). The job's script counts its executions in a file the
// sandbox names (FLAKE_COUNTER) and fails while the count is at most
// FLAKE_FAILS, so "fails once, then passes" is the job's own behaviour and
// nothing in the fingerprinted tree changes between the two executions.
// The report is docs/test-runs/2026-10-05-merge-queue-flakes-and-stalls.md.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

const flakyWorkflow = `name: tests
on: [push, pull_request]
jobs:
  go:
    name: go
    cache: true
    steps:
      - name: go test
        run: |
          n=$(cat "$FLAKE_COUNTER" 2>/dev/null || echo 0)
          n=$((n+1))
          echo "$n" > "$FLAKE_COUNTER"
          echo "=== RUN   TestSometimesTimesOut"
          if [ "$n" -le "$FLAKE_FAILS" ]; then
            echo "--- FAIL: TestSometimesTimesOut (0.01s)"
            echo "    widgets_test.go:12: got 1 widget, want 2"
            echo "FAIL"
            exit 1
          fi
          echo "--- PASS: TestSometimesTimesOut (0.01s)"
          echo "PASS"
`

// shellSandboxes is ci.Sandboxes as one bash shell on this machine. It
// runs the workflow's own step, and adds the two variables the step reads
// its counter from -- which are the sandbox's, not the tree's, so they are
// in no job fingerprint. With hold set, the step waits for it to close
// (or for the run to be cancelled) before running: a run that has got a
// machine and is making no progress, for the stall walkthrough.
type shellSandboxes struct {
	counter string
	fails   atomic.Int32
	execs   atomic.Int32
	mu      sync.Mutex
	hold    chan struct{}
}

func (s *shellSandboxes) Acquire(context.Context, ci.SandboxRequest) (ci.Sandbox, error) {
	return s, nil
}
func (s *shellSandboxes) Name() string                  { return "shell" }
func (s *shellSandboxes) Release(context.Context) error { return nil }

func (s *shellSandboxes) Exec(ctx context.Context, cmd ci.Command) (ci.CommandResult, error) {
	if !strings.Contains(cmd.Script, "FLAKE_COUNTER") {
		// The checkout and anything else grain does around a step:
		// there is no repository to fetch, and nothing reads one.
		return ci.CommandResult{ExitCode: 0}, nil
	}
	s.mu.Lock()
	hold := s.hold
	s.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ci.CommandResult{ExitCode: 1}, ctx.Err()
		}
	}
	s.execs.Add(1)
	sh := exec.CommandContext(ctx, "bash", "-ec", cmd.Script)
	sh.Env = append(os.Environ(), "FLAKE_COUNTER="+s.counter, "FLAKE_FAILS="+strconv.Itoa(int(s.fails.Load())))
	for k, v := range cmd.Env {
		sh.Env = append(sh.Env, k+"="+v)
	}
	out, err := sh.CombinedOutput()
	code := 0
	if err != nil {
		code = 1
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		}
	}
	return ci.CommandResult{Output: string(out), ExitCode: code}, nil
}

// reset starts the counter again, with the job failing its first fails
// executions.
func (s *shellSandboxes) reset(t *testing.T, fails int) {
	t.Helper()
	if err := os.Remove(s.counter); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	s.fails.Store(int32(fails))
	s.execs.Store(0)
}

// realCI is a ci.Service as a daemon builds one, less the parts that need
// a forge: workflows from a directory, a cache on disk, and the shell
// above as every job's machine.
func realCI(t *testing.T) (*ci.Service, *shellSandboxes) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	root := t.TempDir()
	tree := filepath.Join(root, "tree")
	if err := os.MkdirAll(filepath.Join(tree, ".grain", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, ".grain", "workflows", "tests.yml"), []byte(flakyWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	cache, err := ci.OpenDirCache(filepath.Join(root, "cache"), ci.DirCacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sandboxes := &shellSandboxes{counter: filepath.Join(root, "counter")}
	n := 0
	var mu sync.Mutex
	svc := &ci.Service{
		Source: ci.FSSource{Root: tree},
		Engine: &ci.Engine{Sandboxes: sandboxes, Cache: cache, Concurrency: 1, Log: t.Logf},
		NewID: func() string {
			mu.Lock()
			defer mu.Unlock()
			n++
			return fmt.Sprintf("run-%d", n)
		},
		Log: t.Logf,
	}
	return svc, sandboxes
}

// awaitRuns waits for every run svc holds to conclude.
func awaitRuns(t *testing.T, svc *ci.Service) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		pending := ""
		for _, run := range svc.List() {
			if !run.Conclusion.Done() {
				pending = run.ID
			}
		}
		if pending == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("grain CI run %s did not conclude", pending)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// settleCI is the CI reconciler run until grain CI has nothing left in
// flight: it collects a verdict, starts a recheck, and collects that.
func settleCI(t *testing.T, ctx context.Context, store *model.Store, client github.Client, svc *ci.Service, at time.Time) {
	t.Helper()
	for i := 0; i < 4; i++ {
		awaitRuns(t, svc)
		if err := runCIReconcilerAt(t, ctx, store, client, svc, at); err != nil {
			t.Fatal(err)
		}
	}
	awaitRuns(t, svc)
}

// startRealCI is startRequiredCI against the real service.
func startRealCI(t *testing.T, ctx context.Context, store *model.Store, client github.Client,
	svc *ci.Service, task model.Task, repo model.RepoRef, branch string) *model.CIStatus {

	t.Helper()
	sha := headSHA(t, client, repo, branch)
	run, err := svc.Start(ctx, ci.Request{
		Repo: repo.String(), Ref: branch, Commit: sha, Event: ci.EventPush, RequestedBy: task.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := baseTime
	status := &model.CIStatus{RunID: run.ID, Commit: sha, StartedAt: &started}
	recordCIStatus(t, ctx, store, task.ID, status)
	return status
}

// historyRunner is the slice of mcp.GrainCIRunner query_test_history
// reads, answered from the service's own run store.
type historyRunner struct {
	mcp.GrainCIRunner
	svc *ci.Service
}

func (h historyRunner) FindGrainCIRuns(_ context.Context, f ci.RunFilter, limit int) (ci.RunPage, error) {
	return h.svc.Find(f, limit), nil
}

func queryTestHistory(t *testing.T, svc *ci.Service, test string) string {
	t.Helper()
	for _, tool := range mcp.NewGrainCIWaitTools(historyRunner{svc: svc}, mcp.CIRepoScope{Repo: "acme/widgets"}) {
		if tool.Name == "query_test_history" {
			res := tool.Handler(context.Background(), map[string]any{"test_name": test})
			if res.IsError {
				t.Fatalf("query_test_history: %s", res.Text)
			}
			return res.Text
		}
	}
	t.Fatal("no query_test_history tool")
	return ""
}

// §3 step 1: a job that fails once and then passes, on one pull request.
func TestWalkthroughAFlakeThroughRealGrainCIMergesAndIsCounted(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	svc, sandboxes := realCI(t)
	sandboxes.reset(t, 1)

	first := startRealCI(t, ctx, store, client, svc, task, repo, branch)
	settleCI(t, ctx, store, client, svc, baseTime)

	if n := sandboxes.execs.Load(); n != 2 {
		t.Fatalf("the job ran %d times, want 2: the run and one uncached recheck (a cached red would be 1)", n)
	}
	status := ciStatusOf(t, ctx, store, task.ID)
	if !status.Green() || status.RecheckOf != first.RunID {
		t.Fatalf("status = %+v, want green, rechecking %s", status, first.RunID)
	}
	recheck, ok := svc.Get(status.RunID)
	if !ok || recheck.ID == first.RunID || len(recheck.Jobs) != 1 || recheck.Jobs[0].Cached {
		t.Fatalf("the recheck is not an uncached run of the job: %+v", recheck)
	}
	run, _ := svc.Get(first.RunID)
	if len(run.Jobs) != 1 || !run.Jobs[0].Flake {
		t.Fatalf("the first run's job is not marked JobRun.Flake: %+v", run.Jobs)
	}
	comments := strings.Join(commentBodies(t, ctx, store, task.ID), "\n")
	for _, want := range []string{"flake", "tests/go", "TestSometimesTimesOut", "No automatic repair"} {
		if !strings.Contains(comments, want) {
			t.Errorf("the queue comment does not say %q:\n%s", want, comments)
		}
	}
	history := queryTestHistory(t, svc, "TestSometimesTimesOut")
	if !strings.Contains(history, "Flaked:  1") {
		t.Errorf("query_test_history does not count the flake:\n%s", history)
	}

	greenOnGitHub(sim, branch)
	syncWithCI(t, ctx, store, client, svc, baseTime.Add(time.Minute))
	if !mergedBranch(sim, branch) {
		obs := mergeQueueStatus(t, ctx, store, task.ID)
		t.Fatalf("the pull request did not merge after its flake (state %q, note %q)", obs.MergeQueueState, obs.MergeQueueNote)
	}
	if n := repairObservationOf(t, ctx, store, task.ID).RepairsSpent(); n != 0 {
		t.Fatalf("%d repair(s) spent on a flake", n)
	}
}

// §3 step 1, second half: the job fails both times, so it is the change's
// failure and repair 1 is filed.
func TestWalkthroughAJobFailingTwiceThroughRealGrainCISpendsRepairOne(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	svc, sandboxes := realCI(t)
	sandboxes.reset(t, 2)

	first := startRealCI(t, ctx, store, client, svc, task, repo, branch)
	settleCI(t, ctx, store, client, svc, baseTime)

	if n := sandboxes.execs.Load(); n != 2 {
		t.Fatalf("the job ran %d times, want 2", n)
	}
	status := ciStatusOf(t, ctx, store, task.ID)
	if status.Green() || !status.Done() || status.Detail != "failure: tests/go" {
		t.Fatalf("status = %+v, want the job that failed twice as the verdict", status)
	}
	if run, _ := svc.Get(first.RunID); run.Jobs[0].Flake {
		t.Fatal("a job that failed twice was marked a flake")
	}
	greenOnGitHub(sim, branch)
	syncWithCI(t, ctx, store, client, svc, baseTime.Add(time.Minute))
	if mergedBranch(sim, branch) {
		t.Fatal("a pull request whose job failed twice merged")
	}
	obs := repairObservationOf(t, ctx, store, task.ID)
	if !obs.RepairInFlight() || obs.RepairsSpent() != 1 || obs.MergeQueueRepairFailures != "tests/go" {
		t.Fatalf("repair 1 not filed: in flight %v, spent %d, failures %q", obs.RepairInFlight(), obs.RepairsSpent(), obs.MergeQueueRepairFailures)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if last := bodies[len(bodies)-1]; !strings.Contains(last, "automatic repair 1 of 2") {
		t.Errorf("the repair does not say which attempt it is:\n%s", last)
	}
	if history := queryTestHistory(t, svc, "TestSometimesTimesOut"); !strings.Contains(history, "Flaked:  0") {
		t.Errorf("query_test_history counts a real failure as a flake:\n%s", history)
	}
}
