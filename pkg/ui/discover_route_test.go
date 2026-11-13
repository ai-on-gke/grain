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
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// POST /api/deployment/agent-backends/{backendId}/discover asks that one
// connection's provider now -- not at RunModelDiscovery's next tick -- and
// answers with the connection as stored, its discovered list dated.
// Nothing a person configured changes, and no other connection is asked.
func TestRefreshDiscoveredModelsAsksOnlyThatConnectionNow(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	agy := &mutableModelLister{}
	agy.set(nil, "gemini-3.1-pro")
	client.Config.AgentModels = agy
	claude := &mutableModelLister{}
	claude.set(nil, "claude-opus-5-5")
	client.Config.ClaudeModels = claude
	putBackends(t, client,
		model.AgentBackend{ID: "agy", Framework: model.AgentFrameworkAntigravity, ExcludedModels: []string{"gemini-old"}},
		model.AgentBackend{ID: "claude", Framework: model.AgentFrameworkClaude},
	)
	// The provider has started serving a model since the last pass.
	agy.set(nil, "gemini-3.1-pro", "gemini-3.8-flash", "gemini-old")

	rec := do(t, srv, http.MethodPost, "/api/deployment/agent-backends/agy/discover", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	got := decode[ui.RefreshDiscoveredModelsResponse](t, rec)
	if !got.Asked || got.Error != "" {
		t.Fatalf("response = %+v, want asked with no error", got)
	}
	if got.Backend.Discovered == nil || got.Backend.Discovered.RefreshedAt.IsZero() {
		t.Fatalf("response backend discovered = %+v, want a dated list", got.Backend.Discovered)
	}
	if want := []string{"gemini-3.1-pro", "gemini-3.8-flash"}; !slices.Equal(got.Backend.SupportedModels(), want) {
		t.Errorf("response advertises %v, want %v (exclusions still apply)", got.Backend.SupportedModels(), want)
	}
	stored := storedBackend(t, client, "agy")
	if !slices.Equal(stored.ExcludedModels, []string{"gemini-old"}) || len(stored.Models) != 0 {
		t.Errorf("stored configuration changed: models %v, excluded %v", stored.Models, stored.ExcludedModels)
	}
	if !slices.Contains(stored.SupportedModels(), "gemini-3.8-flash") {
		t.Errorf("stored connection advertises %v, want the new model", stored.SupportedModels())
	}
	if claude.calls != 0 {
		t.Errorf("claude's provider was asked %d times, want only agy's", claude.calls)
	}
}

// A provider that cannot answer keeps the list it gave last, and the
// answer says why it could not be refreshed.
func TestRefreshDiscoveredModelsReportsAProviderFailure(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	agy := &mutableModelLister{}
	agy.set(nil, "gemini-3.1-pro")
	client.Config.AgentModels = agy
	putBackends(t, client, model.AgentBackend{ID: "agy", Framework: model.AgentFrameworkAntigravity})
	if rec := do(t, srv, http.MethodPost, "/api/deployment/agent-backends/agy/discover", ""); rec.Code != http.StatusOK {
		t.Fatalf("first refresh status = %d, body %s", rec.Code, rec.Body)
	}
	agy.set(errors.New("credential expired"))
	rec := do(t, srv, http.MethodPost, "/api/deployment/agent-backends/agy/discover", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	got := decode[ui.RefreshDiscoveredModelsResponse](t, rec)
	if !got.Asked || !strings.Contains(got.Error, "credential expired") {
		t.Errorf("response = %+v, want asked with the provider's error", got)
	}
	if want := []string{"gemini-3.1-pro"}; !slices.Equal(got.Backend.SupportedModels(), want) {
		t.Errorf("advertises %v after a failed refresh, want the last list %v", got.Backend.SupportedModels(), want)
	}
}

// What cannot be refreshed says so: an unknown connection is 404, a
// framework with no provider to ask is 400, and a deployment with no
// lister for the framework answers asked=false and writes nothing.
func TestRefreshDiscoveredModelsRefusals(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	putBackends(t, client,
		model.AgentBackend{ID: "agy", Framework: model.AgentFrameworkAntigravity},
		model.AgentBackend{ID: "custom", Framework: "custom-cli", Models: []string{"m"}},
	)
	if rec := do(t, srv, http.MethodPost, "/api/deployment/agent-backends/nope/discover", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown connection status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
	if rec := do(t, srv, http.MethodPost, "/api/deployment/agent-backends/custom/discover", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("non-discovering connection status = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
	rec := do(t, srv, http.MethodPost, "/api/deployment/agent-backends/agy/discover", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unwired lister status = %d, body %s", rec.Code, rec.Body)
	}
	got := decode[ui.RefreshDiscoveredModelsResponse](t, rec)
	if got.Asked || got.Error == "" || got.Backend.ID != "agy" {
		t.Errorf("unwired lister response = %+v, want asked=false with a reason", got)
	}
	if b := storedBackend(t, client, "agy"); b.Discovered != nil {
		t.Errorf("unwired lister wrote discovered %+v, want nothing", b.Discovered)
	}
}
