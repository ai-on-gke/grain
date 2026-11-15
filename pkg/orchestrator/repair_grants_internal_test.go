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
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A Stage 1 or default-branch repair is filed holding the CI grant every
// other filing path gives a task on its repository: the default
// api:ci-read@<repo> on a GitHub-CI repository unless the capability
// policy denies it, and grain#ci on a grain-CI one. Without it the repair
// cannot read the full logs of the run its body names (task 5426 parked
// asking a person for api:ci-read).
func TestRepairsHoldTheirRepositorysDefaultCIGrant(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	denyCIRead := model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{
		{Capability: model.APIRef("ci-read"), Deny: true},
	}}
	filers := []struct {
		name string
		file func(context.Context, *model.Store, ConcludedGitHubWorkflowRun, []model.GitHubCIJob, time.Time) (string, error)
	}{
		{name: "stage1", file: fileOrUpdateStage1Repair},
		{name: "github-base", file: fileOrUpdateGitHubBaseRepair},
	}
	modes := []struct {
		name   string
		mode   model.CIMode
		policy model.CapabilityPolicy
		want   []model.Grant
	}{
		{name: "github", mode: model.CIModeGitHub, want: []model.Grant{model.DefaultCIReadGrant(repo)}},
		{name: "github-denied", mode: model.CIModeGitHub, policy: denyCIRead},
		{name: "grain", mode: model.CIModeGrain, want: []model.Grant{{Capability: model.GrainCIGrant, Via: model.GrantByDefault}}},
	}
	for _, f := range filers {
		for _, m := range modes {
			t.Run(f.name+"/"+m.name, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				store := openInternalStore(t)
				if err := store.PutConfig(ctx, model.Config{CapabilityPolicy: m.policy}); err != nil {
					t.Fatal(err)
				}
				if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: m.mode}); err != nil {
					t.Fatal(err)
				}
				now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
				run, jobs := stage1Run(t, ctx, store, repo, 71, "main", "push", "failure", now,
					map[string]string{"go": "failure"})
				id, err := f.file(ctx, store, run, jobs, now)
				if err != nil {
					t.Fatal(err)
				}
				repair, err := store.GetTask(ctx, id)
				if err != nil || repair == nil {
					t.Fatalf("reading repair %s: %v", id, err)
				}
				if !slices.EqualFunc(repair.Grants, m.want, model.Grant.Equal) {
					t.Errorf("repair grants = %+v, want %+v", repair.Grants, m.want)
				}
			})
		}
	}
}
