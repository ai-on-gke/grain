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

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The custom command connection type is gone: creating one through the
// API is refused saying so, by the per-connection route and by a whole
// settings save alike.
func TestCreatingACustomCommandConnectionIsRefused(t *testing.T) {
	srv, client := cliPathTeam(t)
	rec := doWithHeaders(t, srv, http.MethodPut, "/api/deployment/agent-backends/house",
		`{"framework":"custom","command":"run-agent","models":["house-model"]}`, map[string]string{"X-Grain-User": "bob"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "custom command connections were removed") {
		t.Fatalf("PUT a custom connection: code %d, body %s; want a 400 saying custom commands were removed", rec.Code, rec.Body.String())
	}
	backends := []model.AgentBackend{
		{ID: "infra-claude", Framework: model.AgentFrameworkClaude},
		{ID: "house", Framework: model.RemovedAgentFrameworkCustom, Command: "run-agent"},
	}
	if _, err := client.UpdateSettings(context.Background(), ui.UpdateSettingsRequest{AgentBackends: &backends}); err == nil ||
		!strings.Contains(err.Error(), "custom command connections were removed") {
		t.Fatalf("UpdateSettings with a new custom connection = %v, want it refused saying custom commands were removed", err)
	}
	if hasStoredBackend(t, client, "house") {
		t.Fatal("a refused custom connection was stored")
	}
	if _, err := client.CreateTask(context.Background(), ui.CreateTaskRequest{
		Title: "pinned", NoRepo: true, AgentFramework: model.RemovedAgentFrameworkCustom,
	}); err == nil || !strings.Contains(err.Error(), "custom command connections were removed") {
		t.Fatalf("CreateTask on the custom framework = %v, want it refused saying custom commands were removed", err)
	}
}

// A custom command connection a deployment already had (migration 103
// disabled it) survives the Models page's saves, which send every
// connection back, still disabled and saying why, until it is deleted.
func TestAStoredCustomCommandConnectionIsKeptDisabledUntilDeleted(t *testing.T) {
	srv, client := cliPathTeam(t)
	ctx := context.Background()
	cfg, err := client.Store.GetConfig(ctx)
	if err != nil || cfg == nil {
		t.Fatalf("GetConfig = %+v, %v", cfg, err)
	}
	cfg.AgentBackends = append(cfg.AgentBackends,
		model.AgentBackend{ID: "house", Name: "House agent", Framework: model.RemovedAgentFrameworkCustom, Command: "run-agent", Models: []string{"house-model"}})
	if err := client.Store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	stored := storedBackend(t, client, "house")
	if !stored.Disabled || stored.DisabledReason != model.RemovedCustomConnectionReason {
		t.Fatalf("stored custom connection = %+v, want it read disabled with the removal reason", stored)
	}

	// The page sends it back, even with the box ticked to enable it.
	resent := stored
	resent.Disabled, resent.DisabledReason, resent.Name = false, "", "Renamed"
	backends := []model.AgentBackend{storedBackend(t, client, "infra-claude"), resent}
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{AgentBackends: &backends}); err != nil {
		t.Fatalf("re-saving the Models page with the stored custom connection on it: %v", err)
	}
	if got := storedBackend(t, client, "house"); !got.Disabled || got.DisabledReason != model.RemovedCustomConnectionReason ||
		got.Name != "House agent" || got.Command != "run-agent" {
		t.Fatalf("after a save the custom connection is %+v, want it kept exactly as it was, disabled", got)
	}

	rec := doWithHeaders(t, srv, http.MethodDelete, "/api/deployment/agent-backends/house", "", map[string]string{"X-Grain-User": "bob"})
	if rec.Code/100 != 2 {
		t.Fatalf("DELETE the custom connection: code %d, body %s", rec.Code, rec.Body.String())
	}
	if hasStoredBackend(t, client, "house") {
		t.Fatal("after Delete the custom connection is still stored")
	}
}

func hasStoredBackend(t *testing.T, client *ui.Client, id string) bool {
	t.Helper()
	cfg, err := client.Store.GetConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range cfg.AgentBackends {
		if b.ID == id {
			return true
		}
	}
	return false
}
