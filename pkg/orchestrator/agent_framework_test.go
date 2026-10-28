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

// Which framework a dispatch asks for, and what happens when it cannot
// be built. Deps.Framework used to be a bare factory taking nothing: one
// framework, chosen at daemon startup, for every run. It takes the
// task's own choice now -- its model.Task.AgentFramework and the model
// it was pinned to, as one orchestrator.AgentChoice -- and may refuse,
// since the credential it needs is set from the UI and may simply not be
// there yet.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestDispatchAsksForTheTaskOwnAgentFramework(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	task := filedTask(t, ctx, store, "t1", repo)
	task.AgentFramework = model.AgentFrameworkClaude
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	var asked []orchestrator.AgentChoice
	deps := orchestrator.Deps{
		Store:     store,
		Client:    client,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: func(_ context.Context, choice orchestrator.AgentChoice) (agent.Framework, error) {
			asked = append(asked, choice)
			return stubFramework{result: toolResult(agent.ToolCall{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": "done"},
			})}, nil
		},
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	if len(asked) != 1 || !reflect.DeepEqual(asked[0], orchestrator.AgentChoice{Framework: model.AgentFrameworkClaude}) {
		t.Fatalf("Deps.Framework was asked for %v, want one call for %q (team default)", asked, model.AgentFrameworkClaude)
	}
}

// The other half of the choice: a task pinned to one model is dispatched
// naming it, so the factory can build that model rather than the next
// one in the rotation (grain/task-110). Resolving the id is the
// factory's job -- this package only has to carry it.
func TestDispatchAsksForTheModelTheTaskWasPinnedTo(t *testing.T) {
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	task := filedTask(t, ctx, store, "t1", repo)
	task.AgentFramework = model.AgentFrameworkClaude
	task.AgentBackendID = "claude-deep"
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	var asked []orchestrator.AgentChoice
	deps := orchestrator.Deps{
		Store:     store,
		Client:    client,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: func(_ context.Context, choice orchestrator.AgentChoice) (agent.Framework, error) {
			asked = append(asked, choice)
			return stubFramework{result: toolResult(agent.ToolCall{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": "done"},
			})}, nil
		},
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	want := orchestrator.AgentChoice{Framework: model.AgentFrameworkClaude, BackendID: "claude-deep"}
	if len(asked) != 1 || !reflect.DeepEqual(asked[0], want) {
		t.Fatalf("Deps.Framework was asked for %v, want one call for %+v", asked, want)
	}
}

func TestDispatchAsksForTheDeploymentDefaultWhenATaskNamesNoFramework(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)

	var asked []orchestrator.AgentChoice
	deps := orchestrator.Deps{
		Store:     store,
		Client:    client,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: func(_ context.Context, choice orchestrator.AgentChoice) (agent.Framework, error) {
			asked = append(asked, choice)
			return stubFramework{result: toolResult(agent.ToolCall{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": "done"},
			})}, nil
		},
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	// "" is what a task with no override says, and resolving it into a
	// real framework is deliberately the factory's job (cmd/grain's own
	// defaultAgentFramework), not this package's: only the caller knows
	// what this deployment's default currently is.
	if len(asked) != 1 || !reflect.DeepEqual(asked[0], orchestrator.AgentChoice{}) {
		t.Fatalf("Deps.Framework was asked for %v, want one call for the empty (default) framework", asked)
	}
}

func TestDispatchCarriesTheTaskOwnTeam(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	task := filedTask(t, ctx, store, "t1", repo)
	task.AgentFramework = model.AgentFrameworkClaude
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	var asked []orchestrator.AgentChoice
	deps := orchestrator.Deps{
		Store:     store,
		Client:    client,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: func(_ context.Context, choice orchestrator.AgentChoice) (agent.Framework, error) {
			asked = append(asked, choice)
			return stubFramework{result: toolResult(agent.ToolCall{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": "done"},
			})}, nil
		},
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	want := orchestrator.AgentChoice{Framework: model.AgentFrameworkClaude}
	if len(asked) != 1 || !reflect.DeepEqual(asked[0], want) {
		t.Fatalf("Deps.Framework was asked for %v, want one call for %+v", asked, want)
	}
}

func TestDispatchFinishesTheRunAsSetupFailedWhenTheFrameworkCannotBeBuilt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	missingKey := errors.New("no Claude Code OAuth token is configured: set one in the UI")
	deps := orchestrator.Deps{
		Store:     store,
		Client:    client,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			return nil, missingKey
		},
		MaxWorkers: 1,
	}
	err := orchestrator.RunCycle(ctx, deps, baseTime)
	if !errors.Is(err, missingKey) {
		t.Fatalf("RunCycle error = %v, want it to carry the framework failure", err)
	}

	// The run must be finished, not left open: the point of building the
	// framework inside runOne's setup guard is that a deployment missing
	// a key ends up with a run an operator can read the reason off,
	// rather than a task that silently never moves.
	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	if runs[0].FinishedAt == nil {
		t.Fatal("the run was left open after its framework could not be built")
	}
	if runs[0].Outcome != "setup-failed" {
		t.Fatalf("outcome = %q, want setup-failed", runs[0].Outcome)
	}
	if !strings.Contains(runs[0].Detail, "set one in the UI") {
		t.Fatalf("detail = %q, want it to carry the reason the framework could not be built", runs[0].Detail)
	}
}

// A team runs only on its own provider connections, with no deployment
// connection behind them (grain/task-1601). A task whose team has none is
// held queued -- never handed to Deps.Framework to fail setup -- and is
// dispatched by the first cycle after the team has one.
func TestDispatchHoldsATaskWhoseTeamHasNoConnection(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)

	hasConnection := false
	var asked []string
	var checked []string
	deps := orchestrator.Deps{
		Store:     store,
		Client:    client,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: func(_ context.Context, choice orchestrator.AgentChoice) (agent.Framework, error) {
			asked = append(asked, model.DefaultTeamID)
			return stubFramework{result: toolResult(agent.ToolCall{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": "done"},
			})}, nil
		},
		TeamHasAgentBackend: func(_ context.Context, team string) bool {
			checked = append(checked, team)
			return hasConnection
		},
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if len(asked) != 0 {
		t.Fatalf("a task whose team has no connection was dispatched (Deps.Framework asked for team %v)", asked)
	}
	if len(checked) == 0 {
		t.Fatal("TeamHasAgentBackend was never asked about the task's team")
	}
	if state, err := store.State(ctx, "t1"); err != nil || state != model.StateQueued {
		t.Fatalf("held task state = %q, %v; want it still queued", state, err)
	}
	// The task pane says why (docs/test-runs/2026-10-04-connections-and-models.md, F3).
	waits, err := store.GrantWaits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if w := waits["t1"]; w.Capability != model.TeamSetupWaitCapability || !strings.Contains(w.Resource, "Settings -> Models (/settings/models)") {
		t.Fatalf("held task's wait = %+v, want a team-setup wait naming Settings -> Models", w)
	}

	hasConnection = true
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if len(asked) != 1 {
		t.Fatalf("once the team has a connection, Deps.Framework was asked %d times, want 1", len(asked))
	}
	if waits, err := store.GrantWaits(ctx); err != nil || waits["t1"].Resource != "" {
		t.Fatalf("after dispatch the wait is %+v, %v; want it cleared", waits["t1"], err)
	}
}

// A task held for want of a connection and then, once its team has one,
// for want of a GitHub credential says each in turn, naming where in Team
// settings it is set, and says neither once its team has both: the
// admitter that would replace the sentence never runs while the task is
// held (docs/test-runs/2026-10-04-connections-and-models.md, F7).
func TestDispatchSaysWhichTeamSettingATaskWaitsFor(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)

	hasConnection := false
	var asked int
	deps := orchestrator.Deps{
		Store:     store,
		Client:    client,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			asked++
			return stubFramework{result: toolResult(agent.ToolCall{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": "done"},
			})}, nil
		},
		TeamHasAgentBackend:      func(context.Context, string) bool { return hasConnection },
		RequireTeamGitCredential: true,
		MaxWorkers:               1,
	}
	waitAfterCycle := func(at time.Time) *model.GrantWait {
		t.Helper()
		if err := orchestrator.RunCycle(ctx, deps, at); err != nil {
			t.Fatalf("RunCycle: %v", err)
		}
		w, err := store.GrantWaitOf(ctx, "t1")
		if err != nil {
			t.Fatal(err)
		}
		return w
	}

	if w := waitAfterCycle(baseTime); w == nil || w.Capability != model.TeamSetupWaitCapability ||
		!strings.Contains(w.Resource, "Settings -> Models (/settings/models)") {
		t.Fatalf("wait with no connection = %+v; want a team-setup wait naming Settings -> Models", w)
	}

	hasConnection = true
	w := waitAfterCycle(baseTime.Add(time.Minute))
	if asked != 0 {
		t.Fatalf("a task whose team has no GitHub credential was dispatched")
	}
	if w == nil || w.Capability != model.TeamSetupWaitCapability {
		t.Fatalf("wait with no GitHub credential = %+v; want a team-setup wait", w)
	}
	for _, want := range []string{"the deployment", "GitHub credential", "acme/widgets", "Settings -> Git (/settings/git)"} {
		if !strings.Contains(w.Resource, want) {
			t.Errorf("wait with no GitHub credential = %q, want it to say %q", w.Resource, want)
		}
	}

	if err := store.UpdateConfig(ctx, func(team *model.Config) error {
		team.GitCredential = "acme-token"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := waitAfterCycle(baseTime.Add(2 * time.Minute)); w != nil {
		t.Fatalf("wait once the team has a connection and a credential = %+v; want none", w)
	}
	if asked != 1 {
		t.Fatalf("once the team is set up, Deps.Framework was asked %d times, want 1", asked)
	}
}

// A task on a plain git host is held only for want of a connection on
// that host: in the 10-15 walkthrough the default team's one connection
// was on its plain git host, and the task waited forever for "no GitHub
// credential" (docs/test-runs/2026-10-15-ambient-git-credentials.md).
func TestDispatchHoldsATaskForAConnectionOnItsOwnHost(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "team", "notes", "main")
	repo := model.RepoRef{Host: "git.example:8080", Owner: "team", Name: "notes"}
	filedTask(t, ctx, store, "t1", repo)
	if err := store.UpdateConfig(ctx, func(team *model.Config) error {
		team.GitConnections = []model.TeamGitConnection{{Credential: "plain", Host: "git.example:8080"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	deps := orchestrator.Deps{
		Store:     store,
		Client:    client,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			return stubFramework{result: toolResult(agent.ToolCall{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": "done"},
			})}, nil
		},
		TeamHasAgentBackend:      func(context.Context, string) bool { return true },
		RequireTeamGitCredential: true,
		MaxWorkers:               1,
	}
	// Whatever the dispatch itself makes of a repository the sim does
	// not serve, the question here is only whether it was held.
	_ = orchestrator.RunCycle(ctx, deps, baseTime)
	if w, err := store.GrantWaitOf(ctx, "t1"); err != nil || (w != nil && strings.Contains(w.Resource, "GitHub credential")) {
		t.Fatalf("wait = %+v, %v; a team with a connection on the task's own host was held for a GitHub credential", w, err)
	}
}
