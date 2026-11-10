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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A grant the catalogue revoked after the task was filed holds the task
// at dispatch, saying why on the task, and the wait clears once the
// catalogue admits it again.
func TestCatalogueHoldRejudgesAtDispatch(t *testing.T) {
	store, ctx := openTreeStore(t)
	deps := Deps{Store: store}
	task := model.Task{ID: "task", Title: "t", Target: &widgets,
		Grants: []model.Grant{{Capability: "self-debug", Via: model.GrantByLabel}}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if why := catalogueHold(ctx, deps)(task.ID); why != "" {
		t.Fatalf("a deployment with no catalogue holds an ordinary task: %q", why)
	}
	if why := catalogueHold(ctx, deps)("no-such-task"); why != "" {
		t.Fatalf("a deployment with no catalogue holds a task it cannot read: %q", why)
	}
	setCat := func(repos model.Patterns) {
		t.Helper()
		if err := store.UpdateConfig(ctx, func(c *model.Config) error {
			c.CapabilityPolicy.Catalogue = &model.CapabilityCatalogue{InProcess: map[string]model.CatalogueInProcess{
				"self-debug": {Restrictions: &model.CapabilityRestrictions{Repos: repos}},
			}}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	setCat(model.Patterns{"elsewhere/only"})
	why := catalogueHold(ctx, deps)(task.ID)
	if !strings.Contains(why, "self-debug") {
		t.Fatalf("hold = %q, want it to name the revoked grant", why)
	}
	wait, err := store.GrantWaitOf(ctx, task.ID)
	if err != nil || wait == nil || wait.Capability != model.CatalogueWaitCapability {
		t.Fatalf("wait = %+v (%v), want a catalogue wait", wait, err)
	}
	setCat(model.Patterns{"*"})
	if why := catalogueHold(ctx, deps)(task.ID); why != "" {
		t.Fatalf("hold = %q after the catalogue admitted the grant", why)
	}
	if wait, _ := store.GrantWaitOf(ctx, task.ID); wait != nil {
		t.Fatalf("wait %+v left behind", wait)
	}
}

// The director is held at dispatch for a grant grain's built-in Tier 3
// floor refuses it (director-capabilities.spec.md G7), with no
// settings/capabilities.yml at all as with one that tries to admit it,
// and an ordinary task holding the same grant is not.
func TestCatalogueHoldAppliesTheDirectorFloor(t *testing.T) {
	for name, cat := range map[string]*model.CapabilityCatalogue{
		"nil":   nil,
		"empty": {},
		"widening": {APIScopes: model.CatalogueAPIScopes{RawScopes: map[string]model.CapabilityRestrictions{
			"merge:force": {Kinds: model.Patterns{model.CatalogueKindDirector, model.CatalogueKindTask}},
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			store, ctx := openTreeStore(t)
			if err := store.UpdateConfig(ctx, func(c *model.Config) error {
				c.CapabilityPolicy.Catalogue = cat
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			deps := Deps{Store: store}
			director := model.Task{ID: "director-task", Title: "Director", Interactive: true,
				Origin: model.Origin{Reason: model.ReasonConsoleSession},
				Tags:   []string{model.ConsoleSessionTag(model.DirectorSessionID)},
				Grants: []model.Grant{{Capability: "self-debug", Via: model.GrantByGrain}, {Capability: "api:merge:force", Via: model.GrantByGrain}}}
			plain := model.Task{ID: "plain", Title: "t", Target: &widgets,
				Grants: []model.Grant{{Capability: "api:merge:force", Via: model.GrantByLabel}}}
			for _, task := range []model.Task{director, plain} {
				if err := store.PutTask(ctx, task); err != nil {
					t.Fatal(err)
				}
			}
			why := catalogueHold(ctx, deps)(director.ID)
			if !strings.Contains(why, "built-in director floor") || !strings.Contains(why, "merge:force") {
				t.Fatalf("hold = %q, want the built-in floor naming merge:force", why)
			}
			if wait, err := store.GrantWaitOf(ctx, director.ID); err != nil || wait == nil || wait.Capability != model.CatalogueWaitCapability {
				t.Fatalf("wait = %+v (%v), want a catalogue wait", wait, err)
			}
			if why := catalogueHold(ctx, deps)(plain.ID); why != "" {
				t.Fatalf("an ordinary task holding api:merge:force is held: %q", why)
			}
			director.Grants = director.Grants[:1]
			if err := store.PutTask(ctx, director); err != nil {
				t.Fatal(err)
			}
			if why := catalogueHold(ctx, deps)(director.ID); why != "" {
				t.Fatalf("hold = %q after the grant was removed", why)
			}
			if wait, _ := store.GrantWaitOf(ctx, director.ID); wait != nil {
				t.Fatalf("wait %+v left behind", wait)
			}
		})
	}
}

// The hold fails closed: a queued task it cannot read is held, not
// dispatched unjudged.
func TestCatalogueHoldHoldsAnUnreadableTask(t *testing.T) {
	store, ctx := openTreeStore(t)
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.CapabilityPolicy.Catalogue = &model.CapabilityCatalogue{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if why := catalogueHold(ctx, Deps{Store: store})("no-such-task"); why == "" {
		t.Fatal("a task the hold cannot read was let through")
	}
}

// A proposal filed at the end of a run is judged against the catalogue
// as the identical call filed live (ui.CreateTask): a grant the
// catalogue marks requires_person files it for a person, even when its
// proposer's own permission would have queued it.
func TestRelayedProposalHoldsARequiresPersonGrant(t *testing.T) {
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	relay := func(t *testing.T, cat *model.CapabilityCatalogue) model.Task {
		t.Helper()
		store, ctx := openTreeStore(t)
		if err := store.UpdateConfig(ctx, func(c *model.Config) error {
			c.CapabilityPolicy.Catalogue = cat
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		source := externalSource("7")
		source.Base = ""
		source.Grants = []model.Grant{{Capability: "self-debug", Via: model.GrantByLabel}}
		if err := store.PutTask(ctx, source); err != nil {
			t.Fatal(err)
		}
		result := &agent.Result{ToolCalls: []agent.ToolCall{{
			Name: "propose_task",
			Arguments: map[string]any{"title": "Follow up", "body": "And then this.",
				"capabilities": []any{"self-debug"}},
		}}}
		if err := relayProposedTasks(ctx, store, nil, nil, source, result, now); err != nil {
			t.Fatal(err)
		}
		tasks, err := store.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range tasks {
			if task.Parent() == source.ID {
				return task
			}
		}
		t.Fatal("no proposal was filed")
		return model.Task{}
	}
	if got := relay(t, nil); got.Approval == nil {
		t.Fatalf("with no catalogue the proposal was not queued on its proposer's permission; the test proves nothing: %+v", got)
	}
	got := relay(t, &model.CapabilityCatalogue{InProcess: map[string]model.CatalogueInProcess{
		"self-debug": {Restrictions: &model.CapabilityRestrictions{RequiresPerson: true}},
	}})
	if got.Approval != nil {
		t.Fatalf("a proposal holding a requires_person grant was approved by %+v", got.Approval.Actor)
	}
	if !strings.Contains(got.Body, "requires_person") {
		t.Fatalf("body %q does not say why it waits for a person", got.Body)
	}
	// A grant the run asked for that the catalogue refuses is kept, and
	// waits for a person too: dispatch re-judges only refusals, so a
	// catalogue loosened later must not let it run on its proposer's
	// permission.
	got = relay(t, &model.CapabilityCatalogue{InProcess: map[string]model.CatalogueInProcess{
		"self-debug": {Restrictions: &model.CapabilityRestrictions{Kinds: model.Patterns{}}},
	}})
	if got.Approval != nil {
		t.Fatalf("a proposal asking for a grant the catalogue refuses was approved by %+v", got.Approval.Actor)
	}
	if !slices.ContainsFunc(got.Grants, func(g model.Grant) bool { return g.Capability == model.APIRef("self-debug") }) ||
		!slices.ContainsFunc(got.Grants, func(g model.Grant) bool { return g.Capability == model.GrainSourceGrant }) {
		t.Fatalf("the refused grant the run asked for was dropped rather than kept for dispatch to hold: %+v", got.Grants)
	}
	if !strings.Contains(got.Body, "refuses") {
		t.Fatalf("body %q does not say why it waits for a person", got.Body)
	}
}

// A queued task holding a grant the catalogue marks requires_person is
// held at dispatch when no person stands behind its approval (task 3971):
// its approval is withdrawn so a person approves it, and the reason is on
// its row. A person's own approval, and an operator's schedule firing it,
// are let through.
func TestCatalogueHoldHoldsARequiresPersonGrantNobodyApproved(t *testing.T) {
	store, ctx := openTreeStore(t)
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.CapabilityPolicy.Catalogue = &model.CapabilityCatalogue{InProcess: map[string]model.CatalogueInProcess{
			"self-debug": {Restrictions: &model.CapabilityRestrictions{RequiresPerson: true}},
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	deps := Deps{Store: store}
	grants := []model.Grant{{Capability: "self-debug", Via: model.GrantByLabel}}
	automation := model.Principal{Kind: model.PrincipalAutomation, ID: "reviewer"}
	declared := model.RepoDeclaredID(model.DeclaredSchedule, widgets, "nightly")
	for _, tc := range []struct {
		name   string
		by     model.Principal
		tags   []string
		isHeld bool
	}{
		{"automation", automation, nil, true},
		{"declared schedule nobody accepted", model.SchedulePrincipal, []string{model.ScheduleFiringTag(declared)}, true},
		{"person", model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}, nil, false},
		{"operator schedule", model.SchedulePrincipal, []string{model.ScheduleFiringTag("sched-operator")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := model.Task{ID: "task-" + strings.ReplaceAll(tc.name, " ", "-"), Title: "t", Target: &widgets,
				Grants: grants, Tags: tc.tags, Approval: &model.Attribution{Actor: tc.by}}
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			why := catalogueHold(ctx, deps)(task.ID)
			if held := why != ""; held != tc.isHeld {
				t.Fatalf("held = %v (%q), want %v", held, why, tc.isHeld)
			}
			got, err := store.GetTask(ctx, task.ID)
			if err != nil || got == nil {
				t.Fatal(err)
			}
			if tc.isHeld {
				if got.Approval != nil {
					t.Errorf("a held task kept its approval by %+v", got.Approval.Actor)
				}
				if wait, err := store.GrantWaitOf(ctx, task.ID); err != nil || wait == nil || !strings.Contains(wait.Resource, "nobody approved") {
					t.Errorf("wait = %+v (%v), want one saying nobody approved it", wait, err)
				}
			} else if got.Approval == nil {
				t.Errorf("a task let through lost its approval")
			}
		})
	}
}
