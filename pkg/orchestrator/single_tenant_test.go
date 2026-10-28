package orchestrator_test

// The single-tenant engine (orchestrator.Deps.SingleTenant): one backlog for the
// machine's owner, with no team scoping. A task whose default team has
// no provider connection and no GitHub credential is the one tenant's
// work and dispatches; the same task on a multi-tenant deployment is held
// for the default team's setup.

import (
	"context"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

func TestSingleTenantDispatchesATaskWhoseTeamIsNotSetUp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// connection is whether TeamHasAgentBackend says the task's team
		// has a provider connection: without one a multi-tenant
		// deployment holds the task for it, with one for its GitHub
		// credential.
		connection bool
		single     bool
		held       string
	}{
		{name: "multi, no connection", single: false, held: "Settings -> Models (/settings/models)"},
		{name: "multi, no credential", connection: true, single: false, held: "Settings -> Git (/settings/git)"},
		{name: "single, no connection", single: true},
		{name: "single, no credential", connection: true, single: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			_, client := newSim(t, "acme", "widgets", "main")
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}
			task := filedTask(t, ctx, store, "t1", repo)
			// The default team has no connection or credential here.
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatal(err)
			}

			var asked []orchestrator.AgentChoice
			var checked []string
			deps := orchestrator.Deps{
				Store:     store,
				Client:    client,
				Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
				Framework: func(_ context.Context, choice orchestrator.AgentChoice) (agent.Framework, error) {
					asked = append(asked, choice)
					return stubFramework{result: toolResult(agent.ToolCall{
						Name:      "comment_on_issue",
						Arguments: map[string]any{"comment": "done"},
					})}, nil
				},
				TeamHasAgentBackend: func(_ context.Context, team string) bool {
					checked = append(checked, team)
					return tc.connection
				},
				RequireTeamGitCredential: true,
				SingleTenant:             tc.single,
				MaxWorkers:               1,
			}
			if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
				t.Fatalf("RunCycle: %v", err)
			}

			if !tc.single {
				if len(asked) != 0 {
					t.Fatalf("multi-tenant: a task whose team is not set up was dispatched (Deps.Framework asked for %+v)", asked)
				}
				if state, err := store.State(ctx, "t1"); err != nil || state != model.StateQueued {
					t.Fatalf("multi-tenant: held task state = %q, %v; want it still queued", state, err)
				}
				w, err := store.GrantWaitOf(ctx, "t1")
				if err != nil {
					t.Fatal(err)
				}
				if w == nil || w.Capability != model.TeamSetupWaitCapability || !strings.Contains(w.Resource, tc.held) ||
					!strings.Contains(w.Resource, "the deployment") {
					t.Fatalf("multi-tenant: wait = %+v; want a team-setup wait for the deployment naming %s", w, tc.held)
				}
				return
			}

			if len(checked) != 0 {
				t.Errorf("single-tenant: TeamHasAgentBackend was asked about %v; nothing should be held for a team's setup", checked)
			}
			// Dispatched onto the one tenant's connections: the default
			// team's, whatever team the task carries.
			if len(asked) != 1 {
				t.Fatalf("single-tenant: Deps.Framework was asked for %+v; want one dispatch", asked)
			}
			if w, err := store.GrantWaitOf(ctx, "t1"); err != nil || w != nil {
				t.Fatalf("single-tenant: wait = %+v, %v; want none", w, err)
			}
		})
	}
}
