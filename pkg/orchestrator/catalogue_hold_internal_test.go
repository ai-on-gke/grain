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
	"strings"
	"testing"

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
