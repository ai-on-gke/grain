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

package ui

// GET /api/usage and PUT /api/usage/settings: token usage and budgets
// per connection and model, and the concurrency they sustain
// (pkg/usage, docs/connections-and-models.md "Token usage and budgets").

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/usage"
)

// TaskUsage is a task's attempts' usage summed (TaskDetail.Usage).
type TaskUsage struct {
	Runs             int     `json:"runs"`
	InputTokens      int64   `json:"inputTokens"`
	OutputTokens     int64   `json:"outputTokens"`
	CacheReadTokens  int64   `json:"cacheReadTokens"`
	CacheWriteTokens int64   `json:"cacheWriteTokens"`
	CostUSD          float64 `json:"costUsd,omitempty"`
	Billable         int64   `json:"billable"`
}

func taskUsageOf(usages []model.RunUsage) *TaskUsage {
	if len(usages) == 0 {
		return nil
	}
	var t TaskUsage
	for _, u := range usages {
		t.Runs++
		t.InputTokens += u.InputTokens
		t.OutputTokens += u.OutputTokens
		t.CacheReadTokens += u.CacheReadTokens
		t.CacheWriteTokens += u.CacheWriteTokens
		t.CostUSD += u.CostUSD
		t.Billable += u.Billable()
	}
	return &t
}

// UsageResponse is GET /api/usage's body: the budget report and the
// settings it was computed under.
type UsageResponse struct {
	usage.Report
	Settings model.UsageSettings `json:"settings"`
}

// Usage computes the usage report as of now.
func (c *Client) Usage(ctx context.Context) (UsageResponse, error) {
	rep, err := usage.Load(ctx, c.Store, c.now(), -1)
	if err != nil {
		return UsageResponse{}, err
	}
	settings, err := c.Store.UsageSettings(ctx)
	if err != nil {
		return UsageResponse{}, err
	}
	return UsageResponse{Report: rep, Settings: settings}, nil
}

// SetUsageSettings replaces the usage settings and reports the usage
// they produce.
func (c *Client) SetUsageSettings(ctx context.Context, settings model.UsageSettings) (UsageResponse, error) {
	// usage_setting is a settings table (staterepo.SettingsTables), set
	// from Settings -> Models: writable here in internal mode and
	// exported to grain/config, refused in favour of a pull request when
	// an external config repository owns it.
	if err := c.checkBootstrapWritable(ctx, "usage_setting"); err != nil {
		return UsageResponse{}, err
	}
	if err := settings.Validate(); err != nil {
		return UsageResponse{}, validationErrorf("%v", err)
	}
	if err := c.checkBudgetConnections(ctx, settings); err != nil {
		return UsageResponse{}, err
	}
	if err := c.Store.PutUsageSettings(ctx, settings, c.now()); err != nil {
		return UsageResponse{}, err
	}
	by := RequestActor(ctx)
	if _, err := c.Store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		By: by.Actor, OnBehalfOf: by.OnBehalfOf, Surface: "usage",
		Summary: fmt.Sprintf("usage settings: mode %s, min workers %d, retention %d days, %d budget(s)",
			settings.EffectiveMode(), settings.MinWorkers, settings.RetentionDays, len(settings.Budgets)),
	}); err != nil {
		log.Printf("ui: recording the usage settings change: %v", err)
	}
	return c.Usage(ctx)
}

// checkBudgetConnections refuses a budget on a connection this
// deployment does not have: usage is recorded under connection ids, so a
// budget on a mistyped one ("claude1" for "claude-1") would never count
// anything and would read as a limit nothing ever approaches. A budget
// already stored on a connection since removed stays acceptable, so the
// next change of mode or floor -- which resends every budget -- is not
// refused for it.
func (c *Client) checkBudgetConnections(ctx context.Context, settings model.UsageSettings) error {
	if len(settings.Budgets) == 0 {
		return nil
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	if cfg != nil {
		for _, b := range cfg.AgentBackends {
			known[b.ID] = true
		}
	}
	if stored, err := c.Store.UsageSettings(ctx); err == nil {
		for _, b := range stored.Budgets {
			known[strings.TrimSpace(b.Connection)] = true
		}
	}
	for i, b := range settings.Budgets {
		if conn := strings.TrimSpace(b.Connection); !known[conn] {
			return validationErrorf("budget %d: no connection %q on this deployment (Settings -> Models lists their ids)", i+1, conn)
		}
	}
	return nil
}

func (s *Server) handleGetUsage(w http.ResponseWriter, r *http.Request) {
	resp, err := s.tasks.Usage(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePutUsageSettings(w http.ResponseWriter, r *http.Request) {
	var settings model.UsageSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&settings); err != nil {
		writeError(w, http.StatusBadRequest, validationErrorf("reading usage settings: %v", err))
		return
	}
	resp, err := s.tasks.SetUsageSettings(r.Context(), settings)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Usage is Client.Usage over the wire (GET /api/usage).
func (c *HTTPClient) Usage(ctx context.Context) (UsageResponse, error) {
	var resp UsageResponse
	if err := c.do(ctx, http.MethodGet, "/api/usage", nil, &resp); err != nil {
		return UsageResponse{}, err
	}
	return resp, nil
}

// SetUsageSettings is Client.SetUsageSettings over the wire (PUT
// /api/usage/settings).
func (c *HTTPClient) SetUsageSettings(ctx context.Context, settings model.UsageSettings) (UsageResponse, error) {
	var resp UsageResponse
	if err := c.do(ctx, http.MethodPut, "/api/usage/settings", settings, &resp); err != nil {
		return UsageResponse{}, err
	}
	return resp, nil
}
