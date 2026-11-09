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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestRelayProposedTasksUnifiedGrantsEquivalenceAndNormalization(t *testing.T) {
	store, ctx := openTreeStore(t)
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

	appRepo := model.RepoRef{Owner: "acme", Name: "app"}
	siteRepo := model.RepoRef{Owner: "acme", Name: "site"}
	for _, r := range []model.RepoRef{widgets, appRepo, siteRepo} {
		if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: r}); err != nil {
			t.Fatal(err)
		}
	}

	parent := model.Task{
		ID:        "10",
		Title:     "Legacy parent",
		Body:      "Parent holding legacy api scope + write target",
		Target:    &widgets,
		Base:      "main",
		Writes:    []model.WriteTarget{{Repo: appRepo, Base: "main"}},
		Grants:    []model.Grant{{Capability: "api:tasks:read@acme/app", Via: model.GrantByLabel}},
		AutoMerge: true,
		Approval:  &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "lead@example.com"}},
	}
	if err := store.PutTask(ctx, parent); err != nil {
		t.Fatal(err)
	}

	result := &agent.Result{ToolCalls: []agent.ToolCall{
		{
			Name: "propose_task",
			Arguments: map[string]any{
				"id":         "equiv-grants",
				"title":      "Equivalent via grants",
				"body":       "Covered by parent",
				"auto_merge": true,
				"grants": []any{
					map[string]any{"capability": "https://grain.dev/capabilities/api#tasks:read", "with": map[string]any{"repo": "acme/app"}},
					map[string]any{"capability": "https://grain.dev/capabilities/repo#target", "with": map[string]any{"repo": "acme/app", "base": "main"}},
				},
			},
		},
		{
			Name: "propose_task",
			Arguments: map[string]any{
				"id":         "widen-scope",
				"title":      "Widened scope via grants",
				"body":       "Not covered by parent",
				"auto_merge": true,
				"grants": []any{
					map[string]any{"capability": "https://grain.dev/capabilities/api#tasks:write", "with": map[string]any{"repo": "acme/app"}},
					map[string]any{"capability": "https://grain.dev/capabilities/repo#target", "with": map[string]any{"repo": "acme/app", "base": "main"}},
				},
			},
		},
		{
			Name: "propose_task",
			Arguments: map[string]any{
				"id":         "caps-api-ref",
				"title":      "Canonical api ref in capabilities",
				"body":       "Must store in api:<scope>@<repo> form",
				"repo":       "acme/app",
				"branch":     "main",
				"auto_merge": true,
				"capabilities": []any{
					map[string]any{"capability": "https://grain.dev/capabilities/api#tasks:read", "with": map[string]any{"repo": "acme/app"}},
				},
			},
		},
		{
			Name: "propose_task",
			Arguments: map[string]any{
				"id":           "bare-legacy-cap",
				"title":        "Bare legacy capability name",
				"body":         "Must keep bare capability label intact rather than expanding retired aliases",
				"repo":         "acme/app",
				"branch":       "main",
				"capabilities": []any{"self-repair"},
			},
		},
	}}

	if err := relayProposedTasks(ctx, store, nil, nil, parent, result, now); err != nil {
		t.Fatalf("relayProposedTasks: %v", err)
	}
	// Replay a second time to verify ProposalKeyArgs deduplicates rawP cleanly.
	if err := relayProposedTasks(ctx, store, nil, nil, parent, result, now); err != nil {
		t.Fatalf("second relayProposedTasks: %v", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]model.Task{}
	for _, tk := range tasks {
		if tk.ID != parent.ID {
			byTitle[tk.Title] = tk
		}
	}
	if len(byTitle) != 4 {
		t.Fatalf("filed %d child tasks (%v), want 4", len(byTitle), byTitle)
	}

	equiv := byTitle["Equivalent via grants"]
	if equiv.Approval == nil || !equiv.AutoMerge {
		t.Errorf("equiv proposal held unexpectedly: approval=%+v autoMerge=%v", equiv.Approval, equiv.AutoMerge)
	}
	if equiv.Target == nil || equiv.Target.String() != "acme/app" || equiv.Base != "main" {
		t.Errorf("equiv target = %v base=%q, want acme/app@main", equiv.Target, equiv.Base)
	}

	widened := byTitle["Widened scope via grants"]
	if widened.Approval != nil || widened.AutoMerge {
		t.Errorf("widened proposal auto-approved unexpectedly: approval=%+v autoMerge=%v", widened.Approval, widened.AutoMerge)
	}

	capsRef := byTitle["Canonical api ref in capabilities"]
	if capsRef.Approval == nil || !capsRef.AutoMerge {
		t.Errorf("caps-api-ref proposal held unexpectedly: approval=%+v autoMerge=%v", capsRef.Approval, capsRef.AutoMerge)
	}
	if len(capsRef.Grants) != 1 || capsRef.Grants[0].Capability != "api:tasks:read@acme/app" {
		t.Errorf("caps-api-ref Grants = %+v, want [api:tasks:read@acme/app]", capsRef.Grants)
	}

	bareLegacy := byTitle["Bare legacy capability name"]
	if bareLegacy.Approval != nil {
		t.Errorf("bare-legacy-cap proposal approved unexpectedly: %+v", bareLegacy.Approval)
	}
	if len(bareLegacy.Grants) != 1 || bareLegacy.Grants[0].Capability != "self-repair" || bareLegacy.Grants[0].Via != model.GrantByLabel {
		t.Errorf("bare-legacy-cap Grants = %+v, want [{Capability:self-repair Via:label}]", bareLegacy.Grants)
	}
}
