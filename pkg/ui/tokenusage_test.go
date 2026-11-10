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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A budget on a connection the deployment does not have is refused as a
// validation error (400): usage is recorded under connection ids, so a
// mistyped one would count nothing, ever. The 2026-11-10 token-usage
// walkthrough's PUT /api/usage/settings and `grain usage -budget
// nosuch=1000/5h` both stored one. A budget already stored on a
// connection since removed stays acceptable, so changing the mode, which
// resends every budget, still works.
func TestSetUsageSettingsRefusesUnknownConnection(t *testing.T) {
	client, store, ctx := testClient(t)
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		cfg = &model.Config{}
	}
	cfg.AgentBackends = []model.AgentBackend{{ID: "claude-1", Name: "Claude", Framework: model.AgentFrameworkClaude}}
	if err := store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	_, err = client.SetUsageSettings(ctx, model.UsageSettings{Budgets: []model.TokenBudget{{Connection: "claude1", Tokens: 1000, Window: "5h"}}})
	var verr *ui.ValidationError
	if !errors.As(err, &verr) || !strings.Contains(err.Error(), `no connection "claude1"`) {
		t.Fatalf("unknown connection: err = %v, want a validation error naming it", err)
	}
	if _, err := client.SetUsageSettings(ctx, model.UsageSettings{Budgets: []model.TokenBudget{{Connection: "claude-1", Tokens: 1000, Window: "5h"}}}); err != nil {
		t.Fatalf("known connection: %v", err)
	}
	// The connection goes away; its stored budget does not block the
	// next change of mode.
	cfg.AgentBackends = nil
	if err := store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetUsageSettings(ctx, model.UsageSettings{Mode: model.UsageModeAuto, Budgets: []model.TokenBudget{{Connection: "claude-1", Tokens: 1000, Window: "5h"}}}); err != nil {
		t.Fatalf("stored budget on a removed connection: %v", err)
	}
}
