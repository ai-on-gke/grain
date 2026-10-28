package model_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// Every store resolves every task's git credential through the
// deployment's git connections, held on the default team's row -- the
// connection grain init points at the daemon's own credential --
// whatever the tenancy
// (docs/single-tenant-reset.md, stage 1).
func TestSingleTenantTaskGitCredentialIsTheDefaultTeams(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTestStore(t)
	if err := st.UpdateConfig(ctx, func(team *model.Config) error {
		team.GitConnections = []model.TeamGitConnection{{Credential: "owner-token"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := st.PutTask(ctx, model.Task{
		ID: "t1", Intent: model.IntentImplement, Title: "fix the thing",
		Origin: model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Target: &repo, Binding: model.BindingDirective,
	}); err != nil {
		t.Fatal(err)
	}

	if name, err := st.TaskGitCredentialOn(ctx, "t1", "", "acme", "widgets"); err != nil || name != "owner-token" {
		t.Fatalf("multi-tenant: TaskGitCredentialOn = %q, %v; want the deployment's owner-token", name, err)
	}
	if _, err := st.TaskGitCredentialOn(ctx, "t1", "gitlab.example.com", "acme", "widgets"); !errors.Is(err, model.ErrNoTeamGitCredential) {
		t.Fatalf("another host: TaskGitCredentialOn = %v; want no connection there", err)
	}
	st.SingleTenant = true
	if name, err := st.TaskGitCredentialOn(ctx, "t1", "", "acme", "widgets"); err != nil || name != "owner-token" {
		t.Fatalf("single-tenant: TaskGitCredentialOn = %q, %v; want the default team's owner-token", name, err)
	}
}
