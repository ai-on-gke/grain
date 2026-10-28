package ui_test

import (
	"slices"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
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

// grain/task-2555: a non-admin team's admin may grant self-debug and
// pipeline-control to the team's own tasks, with no deployment admin.
// Since the single-tenant reset (stage 4) what every such grant reaches
// is the whole deployment, which the task's record says
// (TaskDetail.DeploymentWide) for `grain mcpserver` to read.
func TestANonAdminTeamAdminGrantsTheTeamScopedCapabilities(t *testing.T) {
	t.Parallel()
	c, _ := teamScopedFixture(t)
	carol := ui.WithPrincipal(t.Context(), model.Principal{Kind: model.PrincipalHuman, ID: "carol"})

	task, err := c.CreateTask(carol, ui.CreateTaskRequest{
		Title: "debug our queue", Approved: true, Repo: "acme/widgets", Creator: "carol",
		Capabilities: &[]string{"self-debug", "pipeline-control", "memory"},
	})
	if err != nil {
		t.Fatalf("a payments admin granting self-debug, pipeline-control, and memory: %v", err)
	}
	if !slices.Contains(task.Capabilities, "self-debug") || !slices.Contains(task.Capabilities, "pipeline-control") || !slices.Contains(task.Capabilities, "memory") {
		t.Fatalf("task capabilities = %v, want all team-scoped grants", task.Capabilities)
	}
	detail, err := c.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"memory", "pipeline-control", "self-debug"}; !slices.Equal(detail.DeploymentWide, want) {
		t.Errorf("payments' task DeploymentWide = %v, want %v: every grant is deployment-wide", detail.DeploymentWide, want)
	}

	// The default team's task has the same deployment-wide reach.
	admin, err := c.CreateTask(t.Context(), ui.CreateTaskRequest{
		Title: "debug everything", Approved: true, Repo: "acme/widgets",
		Capabilities: &[]string{"self-debug", "pipeline-control"},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err = c.GetTask(t.Context(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(detail.DeploymentWide, "self-debug") || !slices.Contains(detail.DeploymentWide, "pipeline-control") {
		t.Errorf("default team's task DeploymentWide = %v, want both grants deployment-wide", detail.DeploymentWide)
	}
}
