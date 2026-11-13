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
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestLiveAndRelayedProposalsStoreIdenticalGrants files the same propose_task
// payloads through ui.Client.CreateTask (the live path) and through
// orchestrator.ProcessResult / relayProposedTasks (the run-end fallback) on
// identically seeded stores, verifying both paths record the exact same grants,
// queue state, auto-merge decision, and escalation note for one-release bare
// capability aliases, canonical api#* references, and unmigrated parent rows.
func TestLiveAndRelayedProposalsStoreIdenticalGrants(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	cases := []struct {
		name            string
		defaultCaps     []string
		parentGrants    []model.Grant
		proposalPayload map[string]any
		wantGrants      []model.Grant
		wantState       model.State
		wantAutoMerge   bool
	}{
		{
			name: "self-debug covered by canonical parent",
			parentGrants: []model.Grant{
				{Capability: "api:self-debug", Via: model.GrantByLabel},
				{Capability: model.GrainSourceGrant, Via: model.GrantByLabel},
			},
			proposalPayload: map[string]any{
				"title":        "Follow up with self-debug",
				"body":         "Inspect daemon state",
				"repo":         "acme/widgets",
				"branch":       "main",
				"auto_merge":   true,
				"capabilities": []any{"api:self-debug", model.GrainSourceGrant},
			},
			wantGrants: []model.Grant{
				{Capability: model.APIRef("self-debug"), Via: model.GrantByLabel},
				{Capability: model.GrainSourceGrant, Via: model.GrantByLabel},
			},
			wantState:     model.StateQueued,
			wantAutoMerge: true,
		},
		{
			name: "self-debug covered by unmigrated bare self-debug parent",
			parentGrants: []model.Grant{
				{Capability: "self-debug", Via: model.GrantByLabel},
			},
			proposalPayload: map[string]any{
				"title":        "Follow up from bare parent",
				"body":         "Inspect daemon state",
				"repo":         "acme/widgets",
				"branch":       "main",
				"auto_merge":   true,
				"capabilities": []any{"api:self-debug", model.GrainSourceGrant},
			},
			wantGrants: []model.Grant{
				{Capability: model.APIRef("self-debug"), Via: model.GrantByLabel},
				{Capability: model.GrainSourceGrant, Via: model.GrantByLabel},
			},
			wantState:     model.StateQueued,
			wantAutoMerge: true,
		},
		{
			name:         "self-debug escalated when parent lacks it",
			parentGrants: nil,
			proposalPayload: map[string]any{
				"title":        "Escalated self-debug",
				"body":         "Needs source and debug API",
				"repo":         "acme/widgets",
				"branch":       "main",
				"auto_merge":   true,
				"capabilities": []any{"api:self-debug", model.GrainSourceGrant},
			},
			wantGrants: []model.Grant{
				{Capability: model.APIRef("self-debug"), Via: model.GrantByLabel},
				{Capability: model.GrainSourceGrant, Via: model.GrantByLabel},
			},
			wantState:     model.StateProposed,
			wantAutoMerge: false,
		},
		{
			name: "self-repair person-only alias expands and holds for person",
			parentGrants: []model.Grant{
				{Capability: model.GrainHostExecGrant, Via: model.GrantByLabel},
			},
			proposalPayload: map[string]any{
				"title":        "Host repair follow-up",
				"body":         "Restart wedged service",
				"repo":         "acme/widgets",
				"branch":       "main",
				"auto_merge":   true,
				"capabilities": []any{model.GrainHostExecGrant},
			},
			wantGrants: []model.Grant{
				{Capability: model.GrainHostExecGrant, Via: model.GrantByLabel},
			},
			wantState:     model.StateProposed,
			wantAutoMerge: false,
		},
		{
			name: "self-upgrade non-ref person-only alias expands and holds for person",
			parentGrants: []model.Grant{
				{Capability: "api:self-upgrade", Via: model.GrantByLabel},
			},
			proposalPayload: map[string]any{
				"title":        "Upgrade follow-up",
				"body":         "Schedule daemon upgrade",
				"repo":         "acme/widgets",
				"branch":       "main",
				"auto_merge":   true,
				"capabilities": []any{"api:self-upgrade"},
			},
			wantGrants: []model.Grant{
				{Capability: model.APIRef("self-upgrade"), Via: model.GrantByLabel},
			},
			wantState:     model.StateProposed,
			wantAutoMerge: false,
		},
		{
			name: "defaults and unified grants with api# reference and bare alias",
			defaultCaps: []string{
				"api:self-debug", model.GrainSourceGrant,
			},
			parentGrants: []model.Grant{
				{Capability: "api:self-debug", Via: model.GrantByDefault},
				{Capability: model.GrainSourceGrant, Via: model.GrantByDefault},
				{Capability: "api:tasks:read@acme/widgets", Via: model.GrantByLabel},
				{Capability: "api:memory", Via: model.GrantByLabel},
			},
			proposalPayload: map[string]any{
				"title":      "Unified grants proposal",
				"body":       "Read tasks and write memory",
				"auto_merge": true,
				"grants": []any{
					map[string]any{"capability": "https://grain.dev/capabilities/repo#target", "with": map[string]any{"repo": "acme/widgets", "base": "main"}},
					map[string]any{"capability": "https://grain.dev/capabilities/api#tasks:read", "with": map[string]any{"repo": "acme/widgets"}},
					"api:memory",
				},
			},
			wantGrants: []model.Grant{
				{Capability: model.APIRef("memory"), Via: model.GrantByLabel},
				{Capability: model.APIRef("self-debug"), Via: model.GrantByDefault},
				{Capability: model.APIGrantString("tasks:read", "acme/widgets"), Via: model.GrantByLabel},
				{Capability: model.GrainSourceGrant, Via: model.GrantByDefault},
			},
			wantState:     model.StateQueued,
			wantAutoMerge: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seedOne := func(t *testing.T) (*model.Store, context.Context, model.Task) {
				t.Helper()
				store, ctx := openStore(t)
				if err := store.PutConfig(ctx, model.Config{
					DefaultCapabilities: tc.defaultCaps,
				}); err != nil {
					t.Fatal(err)
				}
				if err := store.PutRepoConfig(ctx, model.RepoConfig{
					Repo:   repo,
					CIMode: model.CIModeGitHub,
				}); err != nil {
					t.Fatal(err)
				}
				parent := filedTask(t, ctx, store, "t1", repo)
				parent.Base = "main"
				parent.Grants = slices.Clone(tc.parentGrants)
				parent.AutoMerge = true
				parent.CreatedAt = &baseTime
				if err := store.PutTask(ctx, parent); err != nil {
					t.Fatal(err)
				}
				return store, ctx, parent
			}

			// Path 1: Live filing through ui.Client.CreateTask (ProposedBy).
			liveStore, liveCtx, liveParent := seedOne(t)
			uiClient := ui.NewClient(ui.Config{
				Actor:        ui.DefaultActor("operator@example.com"),
				Capabilities: ui.OfferedCapabilities(),
			}, liveStore)
			uiClient.Now = func() time.Time { return baseTime }

			reqMap := map[string]any{
				"title":       tc.proposalPayload["title"],
				"description": tc.proposalPayload["body"],
				"proposedBy":  liveParent.ID,
			}
			if r, ok := tc.proposalPayload["repo"]; ok {
				reqMap["repo"] = r
			}
			if b, ok := tc.proposalPayload["branch"]; ok {
				reqMap["base"] = b
			}
			if am, ok := tc.proposalPayload["auto_merge"]; ok {
				reqMap["autoMerge"] = am
			}
			if caps, ok := tc.proposalPayload["capabilities"]; ok {
				reqMap["capabilities"] = caps
			}
			if gr, ok := tc.proposalPayload["grants"]; ok {
				reqMap["grants"] = gr
			}
			rawReq, err := json.Marshal(reqMap)
			if err != nil {
				t.Fatal(err)
			}
			var createReq ui.CreateTaskRequest
			if err := json.Unmarshal(rawReq, &createReq); err != nil {
				t.Fatalf("unmarshaling CreateTaskRequest: %v", err)
			}
			liveCreated, err := uiClient.CreateTask(liveCtx, createReq)
			if err != nil {
				t.Fatalf("live CreateTask: %v", err)
			}
			liveStored, err := liveStore.GetTask(liveCtx, liveCreated.ID)
			if err != nil || liveStored == nil {
				t.Fatalf("reading live task: %v", err)
			}
			liveState, err := liveStore.State(liveCtx, liveStored.ID)
			if err != nil {
				t.Fatal(err)
			}

			// Path 2: Run-end replay through orchestrator.ProcessResult / relayProposedTasks.
			relayStore, relayCtx, relayParent := seedOne(t)
			_, simClient := newSim(t, "acme", "widgets", "main")
			result := toolResult(agent.ToolCall{
				Name:      "propose_task",
				Arguments: tc.proposalPayload,
			})
			if err := orchestrator.ProcessResult(relayCtx, relayStore, simClient, relayParent, result, "t1-1", baseTime); err != nil {
				t.Fatalf("ProcessResult: %v", err)
			}
			title, _ := tc.proposalPayload["title"].(string)
			relayStored, ok := proposalsByTitle(t, relayCtx, relayStore, relayParent.ID)[title]
			if !ok {
				t.Fatalf("relayed proposal %q was not filed", title)
			}
			relayState, err := relayStore.State(relayCtx, relayStored.ID)
			if err != nil {
				t.Fatal(err)
			}

			// Compare stored grants between live and relay, and against wantGrants.
			if !slices.EqualFunc(liveStored.Grants, relayStored.Grants, model.Grant.Equal) {
				t.Errorf("stored grants diverged:\n  live:  %+v\n  relay: %+v", liveStored.Grants, relayStored.Grants)
			}
			// Both paths give a proposal on acme/widgets (GitHub CI) the
			// default api:ci-read@acme/widgets (model.DefaultCIReadGrant);
			// what this pins is everything else.
			if !slices.EqualFunc(model.GrantsBeyondCIReadDefault(*liveStored), tc.wantGrants, model.Grant.Equal) {
				t.Errorf("stored grants = %+v, want %+v", liveStored.Grants, tc.wantGrants)
			}
			if liveState != relayState || liveState != tc.wantState {
				t.Errorf("state: live=%s relay=%s, want %s\n  live body:  %q\n  relay body: %q",
					liveState, relayState, tc.wantState, liveStored.Body, relayStored.Body)
			}
			if liveStored.AutoMerge != relayStored.AutoMerge || liveStored.AutoMerge != tc.wantAutoMerge {
				t.Errorf("autoMerge: live=%v relay=%v, want %v", liveStored.AutoMerge, relayStored.AutoMerge, tc.wantAutoMerge)
			}
			if liveStored.Body != relayStored.Body {
				t.Errorf("body diverged:\n  live:  %q\n  relay: %q", liveStored.Body, relayStored.Body)
			}
		})
	}
}
