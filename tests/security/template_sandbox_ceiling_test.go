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

package security

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestPath1TemplateSandboxShapeCeilings holds a template's own sandbox
// shape (model.Template.Sandbox) to the ceilings a task's own shape is
// held to (TestPath1SandboxShapeCeilings): a limited role can neither
// file from a template larger than its ceiling nor write one, an agent
// can neither file from nor write a template larger than its own
// sandbox, and a schedule firing a template above the deployment's
// maximum files nothing.
func TestPath1TemplateSandboxShapeCeilings(t *testing.T) {
	rig := newEscalationRig(t)
	ctx := context.Background()
	cfg, err := rig.store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SandboxShapeLimits = model.SandboxShapeLimits{
		Max:   model.SandboxShape{CPUs: 16},
		Roles: map[string]model.SandboxShape{"grain/contributor": {CPUs: 4}},
	}
	if err := rig.store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Now().UTC()
	for _, tmpl := range []model.Template{
		{ID: "template-big", Name: "big", Title: "Big", Body: "b", Target: &widgets, Sandbox: model.SandboxShape{CPUs: 12}, CreatedAt: now},
		{ID: "template-huge", Name: "huge", Title: "Huge", Body: "h", Target: &widgets, Sandbox: model.SandboxShape{CPUs: 32}, CreatedAt: now},
	} {
		if err := rig.store.PutTemplate(ctx, tmpl); err != nil {
			t.Fatal(err)
		}
	}

	// A limited role, filing from a template above its ceiling.
	if c, b := rig.callAs("contrib@example.com", http.MethodPost, "/api/tasks",
		`{"templateId":"template-big","repo":"acme/widgets"}`); c != http.StatusForbidden || !strings.Contains(b, "ceiling") {
		t.Fatalf("contributor filing from a 12-CPU template over its 4-CPU ceiling = %d %s, want 403 naming the ceiling", c, b)
	}
	// ... or writing one.
	if c, b := rig.callAs("contrib@example.com", http.MethodPost, "/api/templates",
		`{"name":"mine","title":"Mine","repo":"acme/widgets","sandbox":{"cpus":8}}`); c != http.StatusForbidden {
		t.Fatalf("contributor creating an 8-CPU template over its 4-CPU ceiling = %d %s, want 403", c, b)
	}

	// An agent, from a task running at 2 CPUs.
	origin := model.Origin{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "ops@example.com", Source: model.SourceIAP}}, Reason: model.ReasonDirect}
	if err := rig.store.PutTask(ctx, model.Task{
		ID: "940", Intent: model.IntentImplement, Title: "parent", Target: &widgets,
		Writes: []model.WriteTarget{{Repo: widgets}}, Binding: model.BindingDirective, SandboxCPUs: 2,
		Origin: origin, Approval: &origin.Attribution,
		Grants: []model.Grant{
			{Capability: "api:tasks:read@acme/widgets", Via: model.GrantByLabel},
			{Capability: "api:tasks:write@acme/widgets", Via: model.GrantByLabel},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := rig.store.StartRun(ctx, model.Run{ID: "run-940-1", TaskID: "940", Sandbox: "sb-940", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if c, b := rig.callAgent("940", http.MethodPost, "/api/tasks",
		`{"templateId":"template-big","repo":"acme/widgets"}`); c != http.StatusForbidden || !strings.Contains(b, "larger sandbox") {
		t.Fatalf("agent filing from a 12-CPU template out of a 2-CPU task = %d %s, want 403 naming the larger sandbox", c, b)
	}
	if c, b := rig.callAgent("940", http.MethodPost, "/api/templates",
		`{"name":"agent-made","title":"Agent made","repo":"acme/widgets","sandbox":{"cpus":8}}`); c != http.StatusForbidden {
		t.Fatalf("agent creating an 8-CPU template out of a 2-CPU task = %d %s, want 403", c, b)
	}

	// A schedule files its tasks approved straight through the store, so
	// the deployment's maximum is asked there.
	templateID := "template-huge"
	sched := model.Schedule{
		ID: "huge-nightly", TemplateID: &templateID, Target: widgets, Base: "main",
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 60},
		Enabled:    true, NextRunAt: now, CreatedAt: now.Add(-time.Hour),
	}
	if err := rig.store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}
	firing, err := rig.store.FireSchedule(ctx, sched, now, time.UTC, model.RepoRef{})
	if err == nil && firing.TaskID != "" {
		task, _ := rig.store.GetTask(ctx, firing.TaskID)
		if task != nil && task.SandboxCPUs > 16 {
			t.Fatalf("schedule firing a 32-CPU template filed task %s at %d CPUs over the 16-CPU maximum", task.ID, task.SandboxCPUs)
		}
	}
}
