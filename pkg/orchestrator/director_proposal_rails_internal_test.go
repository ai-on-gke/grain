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
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// relayDirector stores a TasksApproved director holding grain/config in
// its Writes (the shipped director's propose_config_change grant) and
// files its task.
func relayDirector(t *testing.T, store *model.Store, ctx context.Context, now time.Time) model.Task {
	t.Helper()
	cfg := model.DefaultConfig()
	cfg.ConsoleSessions = []model.ConsoleSession{{
		ID: model.DirectorSessionID, Title: "Director", TasksApproved: true,
		Writes: []model.WriteTarget{{Repo: model.ConfigRepo()}},
	}}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	director := model.Task{
		ID: "director", Intent: model.IntentAnalyze, Title: "Director",
		Origin:    model.Origin{Attribution: model.Attribution{Actor: model.ConsoleSessionPrincipal}, Reason: model.ReasonConsoleSession},
		Binding:   model.BindingDirective,
		Tags:      []string{model.ConsoleSessionTag(model.DirectorSessionID)},
		Writes:    []model.WriteTarget{{Repo: model.ConfigRepo()}},
		CreatedAt: &now,
		Approval:  &model.Attribution{Actor: model.ConsoleSessionPrincipal},
	}
	if err := store.PutTask(ctx, director); err != nil {
		t.Fatal(err)
	}
	return director
}

func relayedProposals(t *testing.T, store *model.Store, ctx context.Context, parent string) map[string]model.Task {
	t.Helper()
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]model.Task{}
	for _, task := range tasks {
		if task.Parent() == parent {
			out[task.Title] = task
		}
	}
	return out
}

// The end-of-run relay holds a proposal naming grain/config for a person
// even from a TasksApproved session that holds grain/config itself --
// ui.CreateTask's rule for the identical call filed live (gap G5).
func TestRelayHoldsConfigRepoProposalsFromAQueueingSession(t *testing.T) {
	now := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	store, ctx := openTreeStore(t)
	director := relayDirector(t, store, ctx, now)
	result := &agent.Result{ToolCalls: []agent.ToolCall{
		{Name: "propose_task", Arguments: map[string]any{"title": "plain", "body": "b"}},
		{Name: "propose_task", Arguments: map[string]any{"title": "config", "body": "b",
			"writes": []any{map[string]any{"repo": model.ConfigRepo().String()}}}},
	}}
	if err := relayProposedTasks(ctx, store, nil, nil, director, result, now); err != nil {
		t.Fatal(err)
	}
	filed := relayedProposals(t, store, ctx, director.ID)
	plain, ok := filed["plain"]
	if !ok || plain.Approval == nil {
		t.Fatalf("plain proposal = %+v, want it queued on the session's permission", plain)
	}
	config, ok := filed["config"]
	if !ok {
		t.Fatal("the grain/config proposal was never filed")
	}
	if config.Approval != nil {
		t.Errorf("grain/config proposal approved by %+v, want nobody", config.Approval)
	}
	if !strings.Contains(config.Body, "it writes grain/config") || !strings.Contains(config.Body, "always waits for a person") {
		t.Errorf("body = %q, want a note saying it writes grain/config and waits for a person", config.Body)
	}
	if model.HeldForApproval(config) {
		t.Error("tagged held-for-approval: grain held it, the run did not ask")
	}
}

// The relay bounds a session's own permission per turn too (gap G12):
// past the turn's fan-out, proposals are filed for a person with the
// limit note.
func TestRelayBoundsSessionQueuedProposalsPerTurn(t *testing.T) {
	now := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	store, ctx := openTreeStore(t)
	director := relayDirector(t, store, ctx, now.Add(-time.Hour))
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: director.ID, Body: "split it up",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		CreatedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	var calls []agent.ToolCall
	n := model.DefaultConsoleSessionTurnFanOut + 2
	for i := range n {
		calls = append(calls, agent.ToolCall{Name: "propose_task",
			Arguments: map[string]any{"title": fmt.Sprintf("piece %02d", i), "body": "b"}})
	}
	if err := relayProposedTasks(ctx, store, nil, nil, director, &agent.Result{ToolCalls: calls}, now); err != nil {
		t.Fatal(err)
	}
	filed := relayedProposals(t, store, ctx, director.ID)
	if len(filed) != n {
		t.Fatalf("filed %d proposals, want %d", len(filed), n)
	}
	queued, held := 0, 0
	for _, p := range filed {
		if p.Approval != nil {
			queued++
			continue
		}
		held++
		if !strings.Contains(p.Body, "since the last message a person sent it") {
			t.Errorf("held proposal %q body = %q, want the turn limit note", p.Title, p.Body)
		}
	}
	if queued != model.DefaultConsoleSessionTurnFanOut || held != 2 {
		t.Errorf("queued %d and held %d, want %d and 2", queued, held, model.DefaultConsoleSessionTurnFanOut)
	}
}
