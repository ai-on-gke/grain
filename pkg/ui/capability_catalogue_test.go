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
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func setCatalogue(t *testing.T, ctx context.Context, store *model.Store, cat *model.CapabilityCatalogue) {
	t.Helper()
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.CapabilityPolicy.Catalogue = cat
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A proposal holding a capability settings/capabilities.yml marks
// requires_person is filed for a person, even from a proposer whose
// permission would have queued it.
func TestCatalogueRequiresPersonHoldsAProposal(t *testing.T) {
	client, store, ctx := testClient(t)
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "the whole job", Approved: true, AutoMerge: true, Capabilities: &[]string{"gemini-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	setCatalogue(t, ctx, store, &model.CapabilityCatalogue{InProcess: map[string]model.CatalogueInProcess{
		"gemini-key": {Restrictions: &model.CapabilityRestrictions{RequiresPerson: true}},
	}})
	plain, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "no grant", Description: "d", ProposedBy: parent.ID, AutoMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plain.State != model.StateQueued {
		t.Fatalf("a proposal with no dangerous grant is %s, want queued", plain.State)
	}
	held, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "a piece of it", Description: "d", ProposedBy: parent.ID, AutoMerge: true,
		Capabilities: &[]string{"gemini-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if held.State != model.StateProposed {
		t.Fatalf("state = %s, want proposed", held.State)
	}
	if !strings.Contains(held.Description, "requires_person") {
		t.Errorf("body = %q, want it to name the catalogue's reason", held.Description)
	}

	// An agent filing directly (its own bearer token) is not approved
	// by asking to be.
	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: parent.ID})
	direct, err := client.CreateTask(agentCtx, ui.CreateTaskRequest{
		Title: "direct", Approved: true, Capabilities: &[]string{"gemini-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if direct.State != model.StateProposed {
		t.Fatalf("agent's own filing is %s, want proposed", direct.State)
	}
}

// The task an agent's filing leaves proposed for a person is not
// approved by that agent either: neither Approve nor Run now from an
// agent queues a task holding a requires_person grant, so a run never
// holds one without a person deciding. A person still may.
func TestCatalogueRequiresPersonRefusesAnAgentsApproval(t *testing.T) {
	client, store, ctx := testClient(t)
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "the whole job", Approved: true,
		Capabilities: &[]string{"gemini-key", "api:tasks:admin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	setCatalogue(t, ctx, store, &model.CapabilityCatalogue{InProcess: map[string]model.CatalogueInProcess{
		"gemini-key": {Restrictions: &model.CapabilityRestrictions{RequiresPerson: true}},
	}})
	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: parent.ID})
	held, err := client.CreateTask(agentCtx, ui.CreateTaskRequest{
		Title: "direct", Approved: true, Capabilities: &[]string{"gemini-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if held.State != model.StateProposed {
		t.Fatalf("agent's own filing is %s, want proposed", held.State)
	}
	for name, approve := range map[string]func(context.Context, string) error{
		"Approve": client.Approve,
		"RunNow":  client.RunNow,
	} {
		err := approve(agentCtx, held.ID)
		var ferr *ui.ForbiddenError
		if !errors.As(err, &ferr) || !strings.Contains(err.Error(), "requires_person") ||
			!strings.Contains(err.Error(), "gemini-key") {
			t.Errorf("agent %s = %v, want a 403 naming gemini-key and requires_person", name, err)
		}
		if state, err := store.State(ctx, held.ID); err != nil || state != model.StateProposed {
			t.Errorf("after agent %s: state = %s (%v), want proposed", name, state, err)
		}
	}

	if err := client.Approve(ctx, held.ID); err != nil {
		t.Fatalf("a person approving it: %v", err)
	}
	if state, err := store.State(ctx, held.ID); err != nil || state != model.StateQueued {
		t.Fatalf("after a person's approval: state = %s (%v), want queued", state, err)
	}
}

// A grant the catalogue refuses is refused with the task, and attaching
// one later is refused the same way; an agent may not attach a
// requires_person capability at all.
func TestCatalogueRefusesFilingAndEdit(t *testing.T) {
	client, store, ctx := testClient(t)
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "t", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	setCatalogue(t, ctx, store, &model.CapabilityCatalogue{InProcess: map[string]model.CatalogueInProcess{
		"gemini-key": {Restrictions: &model.CapabilityRestrictions{CIOnly: true}},
	}})
	_, err = client.CreateTask(ctx, ui.CreateTaskRequest{Title: "t", Capabilities: &[]string{"gemini-key"}})
	var verr *ui.ValidationError
	if !errors.As(err, &verr) || !strings.Contains(err.Error(), "CI-only") {
		t.Fatalf("CreateTask = %v, want a validation error saying CI-only", err)
	}
	if err := client.SetCapability(ctx, task.ID, "gemini-key", true); !errors.As(err, &verr) {
		t.Fatalf("SetCapability = %v, want a validation error", err)
	}

	setCatalogue(t, ctx, store, &model.CapabilityCatalogue{InProcess: map[string]model.CatalogueInProcess{
		"gemini-key": {Restrictions: &model.CapabilityRestrictions{RequiresPerson: true}},
	}})
	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: task.ID})
	err = client.SetCapability(agentCtx, task.ID, "gemini-key", true)
	if err == nil || !strings.Contains(err.Error(), "requires human approval") {
		t.Fatalf("agent SetCapability = %v, want a refusal", err)
	}
	if err := client.SetCapability(ctx, task.ID, "gemini-key", true); err != nil {
		t.Fatalf("a person attaching it: %v", err)
	}
}
