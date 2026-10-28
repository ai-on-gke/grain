package ui_test

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// A task chooses a model and, separately, may pin a connection. The model
// is stored as a model (model.Task.Model), runs on the framework it
// implies, and a pin to a connection that does not advertise it is
// refused at creation.
func TestCreateTaskChoosesAModelSeparatelyFromAConnection(t *testing.T) {
	client, store, ctx := testClient(t)
	modelPool(t, ctx, client)

	chose, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "model only", Model: "gpt-5.6-sol", Approved: true})
	if err != nil {
		t.Fatalf("choosing a model: %v", err)
	}
	stored, err := store.GetTask(ctx, chose.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Model != "gpt-5.6-sol" || stored.AgentBackendID != "" || stored.AgentFramework != model.AgentFrameworkCodex {
		t.Fatalf("stored = model %q pin %q on %q, want gpt-5.6-sol, no pin, codex",
			stored.Model, stored.AgentBackendID, stored.AgentFramework)
	}
	if chose.Model != "gpt-5.6-sol" {
		t.Fatalf("task.model on the wire = %q, want gpt-5.6-sol", chose.Model)
	}

	// The legacy spelling: a model sent as agentBackendId.
	legacy, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "legacy", AgentBackendID: "claude-opus-5", Approved: true})
	if err != nil {
		t.Fatalf("legacy model choice: %v", err)
	}
	if legacy.Model != "claude-opus-5" || legacy.AgentBackendID != "" {
		t.Fatalf("legacy = model %q pin %q, want the model moved off the pin", legacy.Model, legacy.AgentBackendID)
	}

	// A model and a connection that serves it: both kept, even though
	// that connection is disabled.
	both, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "pinned", Model: "claude-opus-5", AgentBackendID: "claude-deep", Approved: true,
	})
	if err != nil {
		t.Fatalf("pinning a disabled connection that serves the model: %v", err)
	}
	if both.Model != "claude-opus-5" || both.AgentBackendID != "claude-deep" {
		t.Fatalf("pinned = model %q pin %q, want both kept", both.Model, both.AgentBackendID)
	}

	_, err = client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "mismatch", Model: "gpt-5.6-sol", AgentBackendID: "claude-deep", Approved: true,
	})
	if err == nil || !strings.Contains(err.Error(), "does not advertise") {
		t.Fatalf("pinning a connection to a model it does not serve = %v, want a refusal", err)
	}
}

// A team's per-model settings round-trip through the teams API, one per
// model, the last stated winning.
func TestTeamModelSettingsRoundTrip(t *testing.T) {
	client, _, ctx := testClient(t)
	settings := []model.ModelSetting{
		{Model: " gpt-5.6-sol ", Effort: "low"},
		{Model: "", Effort: "high"},
		{Model: "gpt-5.6-sol", Effort: "high"},
		{Model: "gemini-3.1-pro", Effort: "medium"},
	}
	if _, err := client.UpdateTeam(ctx, model.DefaultTeamID, ui.UpdateTeamRequest{
		DefaultModel: ptr("gpt-5.6-sol"), ModelSettings: &settings,
	}); err != nil {
		t.Fatalf("updating team: %v", err)
	}
	team, err := client.GetTeam(ctx, model.DefaultTeamID)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.ModelSetting{{Model: "gpt-5.6-sol", Effort: "high"}, {Model: "gemini-3.1-pro", Effort: "medium"}}
	if team.DefaultModel != "gpt-5.6-sol" || len(team.ModelSettings) != 2 ||
		team.ModelSettings[0] != want[1] && team.ModelSettings[1] != want[1] {
		t.Fatalf("team = default %q settings %+v, want gpt-5.6-sol and %+v", team.DefaultModel, team.ModelSettings, want)
	}
	for _, s := range team.ModelSettings {
		if s.Model == "gpt-5.6-sol" && s.Effort != "high" {
			t.Fatalf("gpt-5.6-sol effort = %q, want the last stated, high", s.Effort)
		}
	}
}

func TestDeploymentMultipleDefaultModelsAndCompositionFallback(t *testing.T) {
	client, store, ctx := testClient(t)
	modelPool(t, ctx, client)

	models := []string{"claude-opus-5", "gpt-5.6-sol", "claude-opus-5"}
	updated, err := client.UpdateTeam(ctx, model.DefaultTeamID, ui.UpdateTeamRequest{
		DefaultModels: &models,
	})
	if err != nil {
		t.Fatalf("UpdateTeam with DefaultModels: %v", err)
	}
	if updated.DefaultModel != "claude-opus-5,gpt-5.6-sol" {
		t.Fatalf("updated.DefaultModel = %q, want claude-opus-5,gpt-5.6-sol", updated.DefaultModel)
	}
	if len(updated.DefaultModels) != 2 || updated.DefaultModels[0] != "claude-opus-5" || updated.DefaultModels[1] != "gpt-5.6-sol" {
		t.Fatalf("updated.DefaultModels = %+v, want [claude-opus-5 gpt-5.6-sol]", updated.DefaultModels)
	}

	// Pinning a task with no explicit model to a connection that serves the
	// second default model ("gpt-5.6-sol") succeeds because that connection
	// serves one of the deployment's default models.
	pinned, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "pinned to fallback default connection", AgentBackendID: "codex-day", Approved: true,
	})
	if err != nil {
		t.Fatalf("pinning a connection that serves a fallback default model: %v", err)
	}
	stored, err := store.GetTask(ctx, pinned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AgentBackendID != "codex-day" {
		t.Fatalf("stored.AgentBackendID = %q, want codex-day", stored.AgentBackendID)
	}
}
