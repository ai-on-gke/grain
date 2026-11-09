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
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const seededHostScript = `https://caps.example/host-script#host-script={"script":"autoscaler"}`

// seededSessionClient is a client whose deployment has the console
// session id, queueing its own work when tasksApproved, and acme/widgets
// seeding every task with seededHostScript. It returns the session's
// task id.
func seededSessionClient(t *testing.T, id string, tasksApproved bool) (*ui.Client, *model.Store, context.Context, string) {
	t.Helper()
	client, store, ctx := testClient(t)
	cfg := model.DefaultConfig()
	cfg.ConsoleSessions = []model.ConsoleSession{{ID: id, Title: "Session", TasksApproved: tasksApproved}}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, DefaultCapabilities: []string{seededHostScript}}); err != nil {
		t.Fatal(err)
	}
	session, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Session", Interactive: true, Capabilities: &[]string{"self-debug"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := mustGetTask(t, ctx, store, session.ID)
	stored.Tags = append(stored.Tags, model.ConsoleSessionTag(id))
	stored.Origin.Reason = model.ReasonConsoleSession
	if err := store.PutTask(ctx, stored); err != nil {
		t.Fatal(err)
	}
	return client, store, ctx, session.ID
}

func holdsSeededHostScript(capabilities []string) bool {
	return slices.ContainsFunc(capabilities, func(c string) bool {
		return strings.HasPrefix(c, "https://caps.example/host-script#host-script") && strings.Contains(c, "autoscaler")
	})
}

// director-capabilities.spec.md §4.2 (G9): a director this deployment
// lets queue its own work files against a repository whose
// default_capabilities include a capability reference. The proposal
// carries that default unchanged, as any task filed there would, so it
// is queued on the director's permission -- holding the default, with a
// note naming it and the repository it came from (§4.4).
func TestDirectorProposalCarryingItsTargetsDefaultIsQueued(t *testing.T) {
	t.Parallel()
	client, store, ctx, director := seededSessionClient(t, model.DirectorSessionID, true)

	filed, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "follow-up on widgets", Description: "d",
		Repo: "acme/widgets", ProposedBy: director,
	})
	if err != nil {
		t.Fatal(err)
	}
	if filed.State != model.StateQueued {
		t.Errorf("state = %s, want queued (body: %q)", filed.State, filed.Description)
	}
	if !holdsSeededHostScript(filed.Capabilities) {
		t.Errorf("capabilities = %v, want the repository's default kept", filed.Capabilities)
	}
	if proposal := mustGetTask(t, ctx, store, filed.ID); proposal.Approval == nil || proposal.Approval.Actor != model.ConsoleSessionPrincipal {
		t.Errorf("approval = %+v, want the console session's", proposal.Approval)
	}
	for _, want := range []string{"from the default capabilities of `acme/widgets`", "the director", "§4.2"} {
		if !strings.Contains(filed.Description, want) {
			t.Errorf("body = %q, want the seeded-defaults note to say %q", filed.Description, want)
		}
	}
	if strings.Contains(filed.Description, "this proposal asked for") {
		t.Errorf("body = %q: an admitted default is not something the proposal asked for", filed.Description)
	}
}

// Every case §4.2 excludes still files the director's proposal for a
// person.
func TestDirectorProposalOutsideTheSeededRuleWaitsForAPerson(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		session       string
		tasksApproved bool
		req           func(director string) ui.CreateTaskRequest
		note          string
	}{
		// The director typed the default ref with other arguments: a
		// grant the repository's owner never decided on.
		"director typed changed arguments": {
			session: model.DirectorSessionID, tasksApproved: true,
			req: func(director string) ui.CreateTaskRequest {
				return ui.CreateTaskRequest{
					Title: "changed", Description: "d", Repo: "acme/widgets", ProposedBy: director,
					Capabilities: &[]string{`https://caps.example/host-script#host-script={"script":"elsewhere"}`},
				}
			},
			note: "this proposal asked for `https://caps.example/host-script",
		},
		// Writes beyond the target are not capabilities, and stay held.
		"extra write": {
			session: model.DirectorSessionID, tasksApproved: true,
			req: func(director string) ui.CreateTaskRequest {
				return ui.CreateTaskRequest{
					Title: "extra write", Description: "d", Repo: "acme/widgets", ProposedBy: director,
					Writes: []ui.WriteTarget{{Repo: "acme/gadgets"}},
				}
			},
			note: "acme/gadgets",
		},
		// A standing session other than the director: §4.2 is the
		// director's rule.
		"another session": {
			session: "ops", tasksApproved: true,
			req: func(session string) ui.CreateTaskRequest {
				return ui.CreateTaskRequest{Title: "ops", Description: "d", Repo: "acme/widgets", ProposedBy: session}
			},
			note: "default capability",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client, store, ctx, session := seededSessionClient(t, tc.session, tc.tasksApproved)
			filed, err := client.CreateTask(ctx, tc.req(session))
			if err != nil {
				t.Fatal(err)
			}
			if filed.State != model.StateProposed {
				t.Errorf("state = %s, want proposed (body: %q)", filed.State, filed.Description)
			}
			if proposal := mustGetTask(t, ctx, store, filed.ID); proposal.Approval != nil {
				t.Errorf("approved by %+v, want nobody", proposal.Approval)
			}
			if !strings.Contains(filed.Description, tc.note) {
				t.Errorf("body = %q, want it to say %q", filed.Description, tc.note)
			}
		})
	}
}

// An agent's proposal records no TemplateID on the stored task, so naming
// a template on propose_task must not let a director proposal carry a
// default that settings/capabilities.yml restricts to that template.
func TestDirectorProposalNamingATemplateDropsTemplateRestrictedDefaults(t *testing.T) {
	t.Parallel()
	client, store, ctx, director := seededSessionClient(t, model.DirectorSessionID, true)
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tmpl-release", Name: "release-audit", Title: "Audit", Body: "Check",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.CapabilityPolicy.Catalogue = &model.CapabilityCatalogue{
			Servers: []model.CatalogueServer{{
				ID:           "https://caps.example/host-script",
				Restrictions: &model.CapabilityRestrictions{Templates: model.Patterns{"release-audit"}},
			}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	filed, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "audit widgets", Description: "d",
		Repo: "acme/widgets", TemplateID: "tmpl-release", ProposedBy: director,
	})
	if err != nil {
		t.Fatal(err)
	}
	if holdsSeededHostScript(filed.Capabilities) {
		t.Errorf("capabilities = %v, want template-restricted default dropped for a proposal stored with no TemplateID", filed.Capabilities)
	}
	if stored := mustGetTask(t, ctx, store, filed.ID); stored.TemplateID != "" {
		t.Errorf("stored TemplateID = %q, want empty", stored.TemplateID)
	}
}
