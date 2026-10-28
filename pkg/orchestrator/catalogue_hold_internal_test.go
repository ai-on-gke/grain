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
	if held := catalogueHold(ctx, deps); held != nil {
		t.Fatal("a deployment with no catalogue has a catalogue hold")
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
		if err := relayProposedTasks(ctx, store, nil, source, result, now); err != nil {
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
	if !slices.ContainsFunc(got.Grants, func(g model.Grant) bool { return g.Capability == "self-debug" }) {
		t.Fatalf("the refused grant the run asked for was dropped rather than kept for dispatch to hold: %+v", got.Grants)
	}
	if !strings.Contains(got.Body, "refuses") {
		t.Fatalf("body %q does not say why it waits for a person", got.Body)
	}
}
