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

package ui_test

// A task's own model: the pin onto one configured backend rather than
// the rotation (grain/task-110), filed with the task, edited like any
// other field, offered to the pickers that file tasks through
// GET /api/config -- and deliberately still available for a model an
// operator has taken out of the rotation, which is the only way work
// reaches one of those.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// modelPool is the two-backend deployment every test here files against:
// one Codex model in the rotation, one Claude model kept out of it.
func modelPool(t *testing.T, ctx context.Context, client *ui.Client) {
	t.Helper()
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		PollInterval: ptr("30s"), MaxMergesPerRepo: ptr(1), GitHubHost: ptr("github.com"),
		AgentBackends: &[]model.AgentBackend{
			{ID: "codex-day", Name: "Day account", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.6-sol"}},
			{ID: "claude-deep", Name: "Deep account", Framework: model.AgentFrameworkClaude, Models: []string{"claude-opus-5"}, Disabled: true},
		},
	}); err != nil {
		t.Fatalf("saving the model pool: %v", err)
	}
}

func TestCreateTaskPinsAModelAndTakesItsFramework(t *testing.T) {
	client, store, ctx := testClient(t)
	modelPool(t, ctx, client)

	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "port the parser", AgentBackendID: "codex-day", Approved: true,
	})
	if err != nil {
		t.Fatalf("creating a task: %v", err)
	}
	if task.AgentBackendID != "codex-day" {
		t.Fatalf("task.agentBackendId = %q, want codex-day", task.AgentBackendID)
	}
	// Filed with the framework the pinned model runs on, without the
	// caller having to say it twice: that is what decides which pause a
	// run of this task counts against (orchestrator's frameworkFor).
	if task.AgentFramework != model.AgentFrameworkCodex {
		t.Fatalf("task.agentFramework = %q, want codex", task.AgentFramework)
	}

	// Read back through the store, not the response: what dispatch sees
	// is the stored column.
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("reading the task back: %v", err)
	}
	if stored.AgentBackendID != "codex-day" || stored.AgentFramework != model.AgentFrameworkCodex {
		t.Fatalf("stored pin = %q on %q, want codex-day on codex", stored.AgentBackendID, stored.AgentFramework)
	}
}

// The point of the pair: disabling a model takes it out of the rotation,
// not out of the deployment, so naming one by hand still files a task
// that runs on it.
func TestCreateTaskPinsADisabledModel(t *testing.T) {
	client, _, ctx := testClient(t)
	modelPool(t, ctx, client)

	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "one expensive job", AgentBackendID: "claude-deep", Approved: true,
	})
	if err != nil {
		t.Fatalf("creating a task pinned to a disabled model: %v", err)
	}
	if task.AgentBackendID != "claude-deep" || task.AgentFramework != model.AgentFrameworkClaude {
		t.Fatalf("task pinned to %q on %q, want claude-deep on claude", task.AgentBackendID, task.AgentFramework)
	}
}

func TestCreateTaskRejectsAnUnknownOrContradictedModel(t *testing.T) {
	client, _, ctx := testClient(t)
	modelPool(t, ctx, client)

	for _, tc := range []struct {
		name string
		req  ui.CreateTaskRequest
	}{
		{"unknown model", ui.CreateTaskRequest{Title: "fix it", AgentBackendID: "codex-night"}},
		{"model of another framework", ui.CreateTaskRequest{
			Title: "fix it", AgentBackendID: "codex-day", AgentFramework: model.AgentFrameworkClaude,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.CreateTask(ctx, tc.req)
			if err == nil {
				t.Fatal("the task was filed anyway")
			}
			var invalid *ui.ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
		})
	}
}

func TestUpdateTaskSetsAndClearsTheModelPin(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	modelPool(t, ctx, client)

	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "fix it", Approved: true})
	if err != nil {
		t.Fatalf("creating a task: %v", err)
	}

	rec := do(t, srv, http.MethodPatch, "/api/tasks/"+task.ID, `{"agentBackendId":"claude-deep"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.Task](t, rec); got.AgentBackendID != "claude-deep" || got.AgentFramework != model.AgentFrameworkClaude {
		t.Fatalf("pin = %q on %q after the patch, want claude-deep on claude", got.AgentBackendID, got.AgentFramework)
	}

	// Moving the pin onto a model of another kind is one edit, not two
	// in the right order: the framework follows the model.
	rec = do(t, srv, http.MethodPatch, "/api/tasks/"+task.ID, `{"agentBackendId":"codex-day"}`)
	if got := decode[ui.Task](t, rec); rec.Code != http.StatusOK ||
		got.AgentBackendID != "codex-day" || got.AgentFramework != model.AgentFrameworkCodex {
		t.Fatalf("pin = %q on %q after moving it, want codex-day on codex (status %d)",
			got.AgentBackendID, got.AgentFramework, rec.Code)
	}

	// An empty string is a real edit here, not "leave it alone": it is
	// how the task is handed back to the rotation.
	rec = do(t, srv, http.MethodPatch, "/api/tasks/"+task.ID, `{"agentBackendId":""}`)
	if got := decode[ui.Task](t, rec); rec.Code != http.StatusOK || got.AgentBackendID != "" {
		t.Fatalf("pin = %q after clearing it, want empty (status %d)", got.AgentBackendID, rec.Code)
	}

	rec = do(t, srv, http.MethodPatch, "/api/tasks/"+task.ID, `{"agentBackendId":"nobody"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown model patch status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// A framework named on its own overtakes a pin it contradicts, rather
// than failing the edit -- so a bulk "move these onto Claude" does not
// break on whichever task in the selection happened to be pinned.
func TestUpdateTaskFrameworkDropsAContradictedModelPin(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	modelPool(t, ctx, client)

	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix it", AgentBackendID: "codex-day", Approved: true,
	})
	if err != nil {
		t.Fatalf("creating a task: %v", err)
	}

	rec := do(t, srv, http.MethodPatch, "/api/tasks/"+task.ID, `{"agentFramework":"claude"}`)
	if got := decode[ui.Task](t, rec); rec.Code != http.StatusOK ||
		got.AgentFramework != model.AgentFrameworkClaude || got.AgentBackendID != "" {
		t.Fatalf("after moving the framework: framework %q, pin %q; want claude and no pin (status %d)",
			got.AgentFramework, got.AgentBackendID, rec.Code)
	}
}

// And a framework that agrees with the pin leaves it alone -- the task
// pane saves the whole form at once, so a save that changed neither must
// not quietly unpin the task.
func TestUpdateTaskKeepsAModelPinItsFrameworkAgreesWith(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	modelPool(t, ctx, client)

	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix it", AgentBackendID: "codex-day", Approved: true,
	})
	if err != nil {
		t.Fatalf("creating a task: %v", err)
	}

	rec := do(t, srv, http.MethodPatch, "/api/tasks/"+task.ID, `{"agentFramework":"codex"}`)
	if got := decode[ui.Task](t, rec); rec.Code != http.StatusOK || got.AgentBackendID != "codex-day" {
		t.Fatalf("pin = %q after restating its framework, want codex-day (status %d)", got.AgentBackendID, rec.Code)
	}
}

func TestConfigOffersEveryModelIncludingTheDisabledOnes(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	modelPool(t, ctx, client)

	// Only the field this test is about: the rest of /api/config is
	// somebody else's contract, and spelling it out here would break this
	// test every time one of those grew a field.
	type configModels struct {
		AgentBackends []ui.AgentBackendOption `json:"agentBackends"`
	}
	got := decode[configModels](t, do(t, srv, http.MethodGet, "/api/config", ""))
	if len(got.AgentBackends) != 2 {
		t.Fatalf("config.agentBackends = %+v, want both configured models", got.AgentBackends)
	}
	// Stored order, which is the order the rotation reaches them in.
	if got.AgentBackends[0].ID != "codex-day" || got.AgentBackends[1].ID != "claude-deep" {
		t.Fatalf("config.agentBackends = %+v, want them in the configured order", got.AgentBackends)
	}
	if got.AgentBackends[0].Disabled || !got.AgentBackends[1].Disabled {
		t.Fatalf("config.agentBackends disabled flags = %t/%t, want false/true",
			got.AgentBackends[0].Disabled, got.AgentBackends[1].Disabled)
	}
	if got.AgentBackends[1].Name != "Deep account" || got.AgentBackends[1].Framework != model.AgentFrameworkClaude {
		t.Fatalf("disabled model = %+v, want it labelled for the picker", got.AgentBackends[1])
	}
}

func TestCreateTaskWithModelAndFramework(t *testing.T) {
	client, store, ctx := testClient(t)
	modelPool(t, ctx, client)

	// Specifying Model sets AgentBackendID and resolves AgentFramework.
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "model only", Model: "codex-day", Approved: true,
	})
	if err != nil {
		t.Fatalf("creating task with Model: %v", err)
	}
	if task.AgentBackendID != "codex-day" || task.AgentFramework != model.AgentFrameworkCodex {
		t.Fatalf("task = %q on %q, want codex-day on codex", task.AgentBackendID, task.AgentFramework)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("reading stored task: %v", err)
	}
	if stored.AgentBackendID != "codex-day" || stored.AgentFramework != model.AgentFrameworkCodex {
		t.Fatalf("stored pin = %q on %q, want codex-day on codex", stored.AgentBackendID, stored.AgentFramework)
	}

	// Specifying both Model and matching AgentFramework succeeds.
	taskWithBoth, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "model and framework", Model: "claude-deep", AgentFramework: "claude", Approved: true,
	})
	if err != nil {
		t.Fatalf("creating task with matching Model and AgentFramework: %v", err)
	}
	if taskWithBoth.AgentBackendID != "claude-deep" || taskWithBoth.AgentFramework != model.AgentFrameworkClaude {
		t.Fatalf("task = %q on %q, want claude-deep on claude", taskWithBoth.AgentBackendID, taskWithBoth.AgentFramework)
	}

	// Specifying AgentFramework alone sets AgentFramework and leaves AgentBackendID empty.
	frameworkOnly, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "framework only", AgentFramework: "claude", Approved: true,
	})
	if err != nil {
		t.Fatalf("creating task with AgentFramework only: %v", err)
	}
	if frameworkOnly.AgentBackendID != "" || frameworkOnly.AgentFramework != model.AgentFrameworkClaude {
		t.Fatalf("task = %q on %q, want empty backend on claude", frameworkOnly.AgentBackendID, frameworkOnly.AgentFramework)
	}
}

func TestProposedTaskWithModelAndFramework(t *testing.T) {
	client, store, ctx := testClient(t)
	modelPool(t, ctx, client)

	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "parent task", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Proposal with Model and matching AgentFramework.
	proposal, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "child task", ProposedBy: parent.ID, Model: "claude-deep", AgentFramework: "claude",
	})
	if err != nil {
		t.Fatalf("creating proposal with Model and AgentFramework: %v", err)
	}
	if proposal.AgentBackendID != "claude-deep" || proposal.AgentFramework != model.AgentFrameworkClaude {
		t.Fatalf("proposal = %q on %q, want claude-deep on claude", proposal.AgentBackendID, proposal.AgentFramework)
	}
	stored, err := store.GetTask(ctx, proposal.ID)
	if err != nil {
		t.Fatalf("reading stored proposal: %v", err)
	}
	if stored.AgentBackendID != "claude-deep" || stored.AgentFramework != model.AgentFrameworkClaude {
		t.Fatalf("stored proposal = %q on %q, want claude-deep on claude", stored.AgentBackendID, stored.AgentFramework)
	}

	// Proposal with unknown model fails with validation error.
	_, err = client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "bad model proposal", ProposedBy: parent.ID, Model: "nonexistent-model",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent model in proposal, got nil")
	}
	var invalid *ui.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want ValidationError", err)
	}

	// Proposal with contradicted framework fails with validation error.
	_, err = client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "contradicted proposal", ProposedBy: parent.ID, Model: "codex-day", AgentFramework: "claude",
	})
	if err == nil {
		t.Fatal("expected error for contradicted framework in proposal, got nil")
	}
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want ValidationError", err)
	}
}
