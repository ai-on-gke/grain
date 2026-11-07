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
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestAdmissionTaskCreateUpdateAndTransitions(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"})

	task, err := c.CreateTask(humanCtx, ui.CreateTaskRequest{
		Title:       "Urgent fix",
		Description: "Fix production wedge",
		Repo:        "acme/widgets",
		Author:      "jane@example.com",
		Admission:   "bypass",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.Admission != "bypass" {
		t.Fatalf("task.Admission = %q, want %q", task.Admission, "bypass")
	}

	adm := "durable"
	updated, err := c.UpdateTask(humanCtx, task.ID, ui.UpdateTaskRequest{
		Admission: &adm,
	})
	if err != nil {
		t.Fatalf("UpdateTask(durable): %v", err)
	}
	if updated.Admission != "durable" {
		t.Fatalf("updated.Admission = %q, want %q", updated.Admission, "durable")
	}

	got, err := c.GetTask(humanCtx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	var found bool
	for _, tr := range got.Transitions {
		if tr.State == model.TransitionAdmissionChanged && tr.OldAdmission == "bypass" && tr.NewAdmission == "durable" && tr.Actor == "alice@example.com" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Transitions = %+v, want admission_changed bypass -> durable by alice@example.com", got.Transitions)
	}

	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range stored.Grants {
		if model.IsRetiredAdmissionGrant(g.Capability) {
			t.Fatalf("stored task has retired admission grant %+v", g)
		}
	}
}

func TestAdmissionDeprecatedGrantInputsFoldToAdmissionWithoutStoringGrants(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	caps := []string{"priority-dispatch", "durable"}
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:        "Legacy grants",
		Description:  "Maps both retired grants to durable",
		Repo:         "acme/widgets",
		Capabilities: &caps,
	})
	if err != nil {
		t.Fatalf("CreateTask with deprecated capabilities: %v", err)
	}
	if task.Admission != "durable" {
		t.Fatalf("task.Admission = %q, want %q", task.Admission, "durable")
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Grants) != 0 {
		t.Fatalf("stored.Grants = %+v, want empty", stored.Grants)
	}

	// SetCapability detach clears matching admission; attach raises it.
	if err := c.SetCapability(ctx, task.ID, "durable", false); err != nil {
		t.Fatalf("SetCapability detach durable: %v", err)
	}
	afterDetach, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterDetach.Admission != "standard" {
		t.Fatalf("after detach Admission = %q, want standard", afterDetach.Admission)
	}

	pdTrue := true
	afterPD, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{PriorityDispatch: &pdTrue})
	if err != nil {
		t.Fatalf("UpdateTask PriorityDispatch=true: %v", err)
	}
	if afterPD.Admission != "bypass" {
		t.Fatalf("after PriorityDispatch=true Admission = %q, want bypass", afterPD.Admission)
	}
}

func TestAdmissionEscalationWaitsForAPerson(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	// Enable external agent API + OwnerTrust for jane@example.com on acme/widgets.
	cfg := model.DefaultConfig()
	cfg.ExternalAgentAPI = true
	cfg.OwnerTrust = []model.OwnerTrustRule{{Owner: "jane@example.com", Repo: "acme/widgets"}}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	extCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: "external:ci"})
	filed, err := c.CreateTask(extCtx, ui.CreateTaskRequest{
		Title:       "Agent filing asking for durable",
		Description: "Must wait for a person despite OwnerTrust",
		Repo:        "acme/widgets",
		Author:      "jane@example.com",
		Admission:   "durable",
	})
	if err != nil {
		t.Fatalf("CreateTask by external agent: %v", err)
	}
	if filed.Approved {
		t.Fatalf("external agent task with admission=durable was auto-approved under OwnerTrust; want held for a person")
	}

}
