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

package model_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
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
