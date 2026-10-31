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
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// teamScopedFixture is a deployment with a system admin ("root") and a
// non-admin team "payments" whose admin is carol and whose member is bob,
// plus a second non-admin team "search".
func teamScopedFixture(t *testing.T) (*ui.Client, *model.Store) {
	t.Helper()
	c, store, ctx := testClient(t)
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		cfg = &model.Config{}
	}
	if err := store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	return c, store
}

// grain/task-2555: a non-admin team's admin may grant self-debug to the
// team's own tasks, with no deployment admin.
// Since the single-tenant reset (stage 4) what every such grant reaches
// is the whole deployment, which the task's record says
// (TaskDetail.DeploymentWide) for `grain mcpserver` to read.
func TestANonAdminTeamAdminGrantsTheTeamScopedCapabilities(t *testing.T) {
	t.Parallel()
	c, _ := teamScopedFixture(t)
	carol := ui.WithPrincipal(t.Context(), model.Principal{Kind: model.PrincipalHuman, ID: "carol"})

	task, err := c.CreateTask(carol, ui.CreateTaskRequest{
		Title: "debug our queue", Approved: true, Repo: "acme/widgets", Creator: "carol",
		Capabilities: &[]string{"self-debug"},
	})
	if err != nil {
		t.Fatalf("a payments admin granting self-debug: %v", err)
	}
	if !slices.Contains(task.Capabilities, "self-debug") {
		t.Fatalf("task capabilities = %v, want all team-scoped grants", task.Capabilities)
	}
	detail, err := c.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"self-debug"}; !slices.Equal(detail.DeploymentWide, want) {
		t.Errorf("payments' task DeploymentWide = %v, want %v: every grant is deployment-wide", detail.DeploymentWide, want)
	}

	// The default team's task has the same deployment-wide reach.
	admin, err := c.CreateTask(t.Context(), ui.CreateTaskRequest{
		Title: "debug everything", Approved: true, Repo: "acme/widgets",
		Capabilities: &[]string{"self-debug"},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err = c.GetTask(t.Context(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(detail.DeploymentWide, "self-debug") {
		t.Errorf("default team's task DeploymentWide = %v, want self-debug deployment-wide", detail.DeploymentWide)
	}
}
