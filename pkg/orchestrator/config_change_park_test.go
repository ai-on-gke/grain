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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// propose_config_change records its change on the task while the run is
// live (ui.Client.ProposeConfigChange); the finish parks the task on it,
// as it does on a secret request, with a comment saying what is waiting.
func TestProcessResultParksOnAProposedConfigChange(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})
	if err := store.ObserveField(ctx, task.ID, baseTime, func(o *model.Observation) {
		o.PendingConfigChange = &model.PendingConfigChange{
			Branch: "grain/config/t1/be-kind", HeadSHA: strings.Repeat("b", 40), Summary: "Be kind",
			Files: []string{"tables/grain_config.json"}, Validation: model.ConfigChangeValidation{OK: true},
		}
	}); err != nil {
		t.Fatal(err)
	}
	result := toolResult(agent.ToolCall{Name: "propose_config_change", Arguments: map[string]any{"summary": "Be kind"}})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if st, err := store.State(ctx, task.ID); err != nil || st != model.StateAwaitingReply {
		t.Fatalf("state = %q (%v), want awaiting_reply", st, err)
	}
	got := commentBodies(t, ctx, store, task.ID)
	if len(got) != 1 || !strings.Contains(got[0], "grain/config/t1/be-kind") || !strings.Contains(got[0], "Be kind") {
		t.Fatalf("conversation = %q, want the change described", got)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil || obs.PendingConfigChange == nil {
		t.Fatalf("the pending change went with the parking: %+v %v", obs, err)
	}
}
