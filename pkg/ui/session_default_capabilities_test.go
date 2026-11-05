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
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const sessionDefaultRef = `https://capabilities.example.com/host-script#host-script={"script":"autoscaler"}`

// grain/task-1983, grain/task-2126: a deployment whose default capabilities
// name a capability reference seeds every proposal with it, and a reference
// the proposer does not hold is an escalation (model.EscalatedCapabilities).
// A console session created with unset capabilities is seeded with the
// deployment's defaults in session.Capabilities so its proposals queue; one
// whose Capabilities do not hold the default (including after an operator
// removes it via UpdateConsoleSession) keeps it out of the session's grants
// and reports the reference on proposals as a seeded default, not asked for.
func TestSessionHoldingDeploymentDefaultsQueuesItsProposals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		removeDefault bool
		wantState     model.State
		wantInBody    string
	}{
		{name: "session created with the deployment defaults", removeDefault: false, wantState: model.StateQueued},
		{name: "session after operator removes the default", removeDefault: true, wantState: model.StateProposed,
			wantInBody: "default capability"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, store, ctx := testClient(t)
			cfg := model.DefaultConfig()
			cfg.DefaultCapabilities = []string{sessionDefaultRef}
			if err := store.PutConfig(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			sessions, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
				ID:            "ops-chat",
				Title:         "Ops Chat",
				TasksApproved: true,
			})
			if err != nil {
				t.Fatalf("CreateConsoleSession: %v", err)
			}
			var sessionTaskID string
			var createdCaps []string
			for _, s := range sessions {
				if s.ID == "ops-chat" {
					sessionTaskID = s.TaskID
					createdCaps = s.Capabilities
				}
			}
			if sessionTaskID == "" {
				t.Fatalf("created session missing taskId in %+v", sessions)
			}
			if !slices.ContainsFunc(createdCaps, model.IsCapabilityRef) {
				t.Fatalf("created session capabilities = %v, want deployment default reference seeded", createdCaps)
			}

			if tc.removeDefault {
				empty := []string{}
				if _, err := client.UpdateConsoleSession(ctx, "ops-chat", ui.UpdateConsoleSessionRequest{
					Capabilities: &empty,
				}); err != nil {
					t.Fatalf("UpdateConsoleSession: %v", err)
				}
				sessionTask := mustGetTask(t, ctx, store, sessionTaskID)
				if slices.ContainsFunc(sessionTask.Grants, func(g model.Grant) bool {
					return model.IsCapabilityRef(g.Capability)
				}) {
					t.Fatalf("updated session grants = %+v, want removed default absent", sessionTask.Grants)
				}
			}

			filed, err := client.CreateTask(ctx, ui.CreateTaskRequest{
				Title: "follow-up", Description: "d", ProposedBy: sessionTaskID,
			})
			if err != nil {
				t.Fatal(err)
			}
			if filed.State != tc.wantState {
				t.Errorf("state = %s, want %s", filed.State, tc.wantState)
			}
			proposal := mustGetTask(t, ctx, store, filed.ID)
			if !slices.ContainsFunc(proposal.Grants, func(g model.Grant) bool {
				return g.Via == model.GrantByDefault && model.IsCapabilityRef(g.Capability)
			}) {
				t.Errorf("grants = %+v, want the deployment default seeded on the proposal", proposal.Grants)
			}
			if tc.wantInBody != "" && !strings.Contains(proposal.Body, tc.wantInBody) {
				t.Errorf("body = %q, want it to say the reference is a %s", proposal.Body, tc.wantInBody)
			}
			if strings.Contains(proposal.Body, "this proposal asked for") {
				t.Errorf("body = %q: a seeded default was described as asked for", proposal.Body)
			}
		})
	}
}

// grain/task-1983: a proposal naming its own repo among its reads has it
// dropped rather than failing the propose_task call; a person's own
// filing is still refused (refuseTargetAmongReads).
func TestProposalDropsItsOwnRepoFromReads(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "parent", Repo: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	filed, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "child", Description: "d", ProposedBy: parent.ID,
		Repo: "acme/widgets", Reads: []string{"acme/widgets"},
	})
	if err != nil {
		t.Fatalf("a proposal naming its own repo among its reads was refused: %v", err)
	}
	if got := mustGetTask(t, ctx, store, filed.ID).Reads; len(got) != 0 {
		t.Errorf("reads = %v, want the task's own repo dropped", got)
	}
	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "by hand", Repo: "acme/widgets", Reads: []string{"acme/widgets"},
	}); err == nil {
		t.Error("a person's filing naming its own repo among its reads was accepted")
	}
}
