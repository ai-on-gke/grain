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
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// loginLapsedPullRequestClient refuses to open a pull request the way a
// forge reached through the git proxy does while the grain host's own
// login for it has lapsed -- with the proxy's LoginLapsedMarker -- for
// its first refusals calls, and then opens it.
type loginLapsedPullRequestClient struct {
	github.Client
	refusals int32
	calls    *atomic.Int32
}

const lapsedSaid = "depot bridge needs `bridge-helper login` on host build-1: run `bridge-helper login` as grain on build-1, then retry"

func (c loginLapsedPullRequestClient) CreatePullRequest(owner, repo, head, base, title, body string) (github.PullRequest, error) {
	if c.calls.Add(1) <= c.refusals {
		return github.PullRequest{}, errors.New("POST /repos/" + owner + "/" + repo + "/pulls: " + gitproxy.LoginLapsedMarker + lapsedSaid)
	}
	return c.Client.CreatePullRequest(owner, repo, head, base, title, body)
}

func runLoginLapsedFinish(t *testing.T, refusals int32) (*model.Store, context.Context, model.Task, int32, error) {
	t.Helper()
	t.Cleanup(orchestrator.SetFinishLoginRetries(time.Millisecond, time.Millisecond))
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))
	pushed := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{ToolCalls: []agent.ToolCall{{Name: "run_command", Text: "pushed"}}}, nil
	})
	calls := &atomic.Int32{}
	deps := orchestrator.Deps{
		Store:      store,
		Client:     loginLapsedPullRequestClient{Client: client, refusals: refusals, calls: calls},
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(pushed),
		MaxWorkers: 1,
	}
	err := orchestrator.RunCycle(ctx, deps, baseTime)
	return store, ctx, task, calls.Load(), err
}

// A finish whose pull request call meets the host's lapsed login once,
// the way the bridge's login lapses on and off, is tried again and goes
// through: the run keeps the outcome it earned.
func TestFinishRefusedOnceOnALapsedHostLoginIsTriedAgain(t *testing.T) {
	store, ctx, task, calls, err := runLoginLapsedFinish(t, 1)
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if calls != 2 {
		t.Fatalf("CreatePullRequest called %d times; want 2 (the refusal and the retry)", calls)
	}
	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Outcome != "succeeded" {
		t.Fatalf("runs = %+v; want one succeeded", runs)
	}
	if st, err := store.State(ctx, task.ID); err != nil || st != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, %v; want awaiting_submit", st, err)
	}
}

// One that meets it on every try is parked on the login, as a setup clone
// refused on it is: model.EnvironmentOutcome, never failed or
// finish-failed, nothing on the failure streak, and the branch left
// pushed for the next dispatch.
func TestFinishStillRefusedOnALapsedHostLoginParksTheTask(t *testing.T) {
	store, ctx, task, calls, err := runLoginLapsedFinish(t, 1000)
	if err != nil {
		t.Fatalf("RunCycle: %v; want the park to be the run's ending", err)
	}
	if calls != 3 {
		t.Fatalf("CreatePullRequest called %d times; want 3 (the try and two retries)", calls)
	}
	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v; want one", runs)
	}
	if runs[0].Outcome != model.EnvironmentOutcome {
		t.Errorf("outcome = %q; want %q -- the host's login, not the task", runs[0].Outcome, model.EnvironmentOutcome)
	}
	if !strings.Contains(runs[0].Detail, "bridge-helper login") || !strings.Contains(runs[0].Detail, "left pushed") {
		t.Errorf("detail = %q; want what renews the login and that the branch is kept", runs[0].Detail)
	}
	if st, err := store.State(ctx, task.ID); err != nil || st != model.StateAwaitingReply {
		t.Fatalf("state = %q, %v; want awaiting_reply, parked on the login", st, err)
	}
	streak, err := store.FailureStreak(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if streak != nil && streak.Count != 0 {
		t.Errorf("failure streak = %+v; want none", streak)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range comments {
		if strings.Contains(c.Body, "This task's run finished, but") && strings.Contains(c.Body, "bridge-helper login") &&
			strings.Contains(c.Body, "Reply here") {
			found = true
		}
	}
	if !found {
		t.Fatalf("comments = %+v; want the park saying what to run", comments)
	}
}
