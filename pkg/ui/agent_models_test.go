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

// grain/task-365: the Gemini model setting is a name only agy's own
// catalog defines, so the pane asks the binary rather than making an
// operator remember one. These are the three answers the field renders
// from: a listing, a deployment with no lister at all, and a lister that
// could not answer -- the last two both leaving the field typable.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

type fakeModelLister struct {
	catalog ui.AgentModelCatalog
	err     error
}

func (f fakeModelLister) ListModels(context.Context, string) (ui.AgentModelCatalog, error) {
	return f.catalog, f.err
}

func TestListAgentModelsReportsTheCatalog(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.AgentModels = fakeModelLister{catalog: ui.AgentModelCatalog{
		Models: []ui.AgentModel{
			{ID: "gemini-3.1-pro-high", Label: "Gemini 3.1 Pro (High)", Effort: "high", Family: "gemini-3.1-pro"},
			{ID: "gemini-3.1-pro-low", Label: "Gemini 3.1 Pro (Low)", Effort: "low", Family: "gemini-3.1-pro"},
		},
		Efforts: []string{"low", "medium", "high"},
	}}

	rec := do(t, srv, http.MethodGet, "/api/agent-models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[struct {
		Available bool            `json:"available"`
		Models    []ui.AgentModel `json:"models"`
		Efforts   []string        `json:"efforts"`
		Error     string          `json:"error"`
	}](t, rec)
	if !got.Available {
		t.Fatalf("available = false, want true with a lister configured")
	}
	if len(got.Models) != 2 || got.Models[0].ID != "gemini-3.1-pro-high" {
		t.Fatalf("models = %+v, want agy's own listing in its own order", got.Models)
	}
	if got.Models[0].Effort != "high" || got.Models[0].Family != "gemini-3.1-pro" {
		t.Errorf("models[0] = %+v, want the effort and family split out", got.Models[0])
	}
	if len(got.Efforts) != 3 {
		t.Errorf("efforts = %v, want the framework's own vocabulary", got.Efforts)
	}
	if got.Error != "" {
		t.Errorf("error = %q, want none", got.Error)
	}
}

func TestCodexModelCatalogIsSeparateAndCarriesEfforts(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.CodexModels = fakeModelLister{catalog: ui.AgentModelCatalog{Models: []ui.AgentModel{{ID: "earlier-model", SupportedEfforts: []string{"low", "high"}, DefaultEffort: "high"}}}}
	rec := do(t, srv, http.MethodGet, "/api/agent-models?framework=codex", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"supportedEfforts":["low","high"]`) || !strings.Contains(rec.Body.String(), `"defaultEffort":"high"`) {
		t.Fatalf("response %d: %s", rec.Code, rec.Body)
	}
	legacy := do(t, srv, http.MethodGet, "/api/agent-models", "")
	if !strings.Contains(legacy.Body.String(), `"available":false`) {
		t.Fatalf("Codex leaked into Gemini catalog: %s", legacy.Body)
	}
	unknown := do(t, srv, http.MethodGet, "/api/agent-models?framework=unknown", "")
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown framework status: %d", unknown.Code)
	}
}

// Claude's listing is a third catalog behind the same route: the names
// come from Anthropic rather than a CLI, they carry no effort vocabulary
// (claude takes no effort flag), and asking for one framework must never
// answer with another's.
func TestClaudeModelCatalogIsItsOwn(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.ClaudeModels = fakeModelLister{catalog: ui.AgentModelCatalog{
		Models: []ui.AgentModel{{ID: "claude-opus-5", Label: "Claude Opus 5"}},
	}}

	rec := do(t, srv, http.MethodGet, "/api/agent-models?framework=claude", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"claude-opus-5"`) ||
		!strings.Contains(rec.Body.String(), `"label":"Claude Opus 5"`) {
		t.Fatalf("response %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"efforts"`) {
		t.Errorf("body = %s, want no effort vocabulary: claude takes no effort flag", rec.Body)
	}
	legacy := do(t, srv, http.MethodGet, "/api/agent-models", "")
	if !strings.Contains(legacy.Body.String(), `"available":false`) {
		t.Fatalf("Claude leaked into the Gemini catalog: %s", legacy.Body)
	}
}

// A deployment with no Claude lister leaves the field free text, the
// same answer the two frameworks beside it give.
func TestClaudeModelCatalogReportsUnavailableWithNoLister(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/agent-models?framework=claude", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("response %d: %s", rec.Code, rec.Body)
	}
}

func TestListAgentModelsReportsUnavailableWithNoLister(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/agent-models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[map[string]any](t, rec)
	if got["available"] != false {
		t.Fatalf("available = %v, want false", got["available"])
	}
	if _, ok := got["error"]; ok {
		t.Errorf("body = %v, want no error: nothing was asked", got)
	}
}

// A wired lister that fails answers 200 carrying why, rather than a 5xx:
// the pane falls back to a text field either way, and the reason is the
// only thing that tells an operator whether to install agy, set a key, or
// simply type the model name.
func TestListAgentModelsReportsWhyItCouldNotAsk(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.AgentModels = fakeModelLister{err: errors.New("the Antigravity CLI (agy) is not installed")}

	rec := do(t, srv, http.MethodGet, "/api/agent-models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[struct {
		Available bool            `json:"available"`
		Models    []ui.AgentModel `json:"models"`
		Error     string          `json:"error"`
	}](t, rec)
	if !got.Available {
		t.Errorf("available = false, want true: a lister was configured, it just failed")
	}
	if len(got.Models) != 0 {
		t.Errorf("models = %+v, want none", got.Models)
	}
	if !strings.Contains(got.Error, "not installed") {
		t.Errorf("error = %q, want the lister's own reason", got.Error)
	}
}
