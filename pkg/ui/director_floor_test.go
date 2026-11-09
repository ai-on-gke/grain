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

// directorTier3Grants are the grants design/spec/director/director-capabilities.spec.md
// §3.3 (G7) keeps off the director whatever settings/capabilities.yml says.
var directorTier3Grants = []string{
	"api:merge:force", "api:settings:write", "api:secrets:write", "api:connections:write", "api:repos:write",
}

// Attaching a Tier 3 grant to the director's task is refused with no
// settings/capabilities.yml at all, with an empty one, and with one that
// names the director kind on every one of those scopes -- while an
// ordinary task may still be given them.
func TestDirectorFloorRefusesAttachingTier3Grants(t *testing.T) {
	t.Parallel()
	widening := &model.CapabilityCatalogue{APIScopes: model.CatalogueAPIScopes{RawScopes: map[string]model.CapabilityRestrictions{}}}
	for _, g := range directorTier3Grants {
		widening.APIScopes.RawScopes[strings.TrimPrefix(g, "api:")] = model.CapabilityRestrictions{
			Kinds: model.Patterns{model.CatalogueKindDirector, model.CatalogueKindTask},
		}
	}
	for name, cat := range map[string]*model.CapabilityCatalogue{"nil": nil, "empty": {}, "widening": widening} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client, store, ctx := testClient(t)
			setCatalogue(t, ctx, store, cat)
			director := sessionTask("director-task")
			if err := store.PutTask(ctx, director); err != nil {
				t.Fatal(err)
			}
			plain, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "ordinary", Approved: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, g := range directorTier3Grants {
				err := client.SetCapability(ctx, director.ID, g, true)
				var verr *ui.ValidationError
				if !errors.As(err, &verr) || !strings.Contains(err.Error(), "director") {
					t.Errorf("SetCapability(director, %s) = %v, want a validation error naming the director floor", g, err)
				}
				if err := client.SetCapability(ctx, plain.ID, g, true); err != nil {
					t.Errorf("SetCapability(ordinary task, %s) = %v, want it attached", g, err)
				}
			}
			got, err := store.GetTask(ctx, director.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, g := range got.Grants {
				if r := model.DirectorFloorRefusal(g.Capability, model.CatalogueKindDirector); r != nil {
					t.Errorf("the director's task holds %s after the refusals", g.Capability)
				}
			}
			// An ordinary grant still attaches to the director.
			if err := client.SetCapability(ctx, director.ID, "api:tasks:read", true); err != nil {
				t.Errorf("SetCapability(director, api:tasks:read) = %v", err)
			}
		})
	}
}

// The director's session row may not be saved naming a Tier 3 grant: the
// settings pane, the API and a config import all go through
// model.ValidateConsoleSessions.
func TestDirectorFloorRefusesSavingTier3OnTheSession(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	for _, g := range directorTier3Grants {
		caps := []string{g}
		_, err := client.UpdateConsoleSession(ctx, model.DirectorSessionID, ui.UpdateConsoleSessionRequest{Capabilities: &caps})
		if err == nil || !strings.Contains(err.Error(), strings.TrimPrefix(g, "api:")) {
			t.Errorf("UpdateConsoleSession(director, %s) = %v, want a refusal naming it", g, err)
		}
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		for _, s := range cfg.ConsoleSessions {
			for _, c := range s.Capabilities {
				if model.DirectorFloorRefusal(c, model.CatalogueKindDirector) != nil {
					t.Errorf("session %s was saved holding %s", s.ID, c)
				}
			}
		}
	}
}
