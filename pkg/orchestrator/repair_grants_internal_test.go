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
	"strings"
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

// A repair already open when the next red run arrives -- one filed before
// repairs were granted CI, as task 5426 was -- is brought up to the grant
// a new repair gets when that run is appended to it, rather than staying
// forbidden from `grain ci log` for as long as it is open.
func TestAnOpenRepairWithoutTheCIGrantGainsItOnTheNextRedRun(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filers := []struct {
		name string
		file func(context.Context, *model.Store, ConcludedGitHubWorkflowRun, []model.GitHubCIJob, time.Time) (string, error)
	}{
		{name: "stage1", file: fileOrUpdateStage1Repair},
		{name: "github-base", file: fileOrUpdateGitHubBaseRepair},
	}
	modes := []struct {
		name string
		mode model.CIMode
		want []model.Grant
	}{
		{name: "github", mode: model.CIModeGitHub, want: []model.Grant{model.DefaultCIReadGrant(repo)}},
		{name: "grain", mode: model.CIModeGrain, want: []model.Grant{{Capability: model.GrainCIGrant, Via: model.GrantByDefault}}},
	}
	for _, f := range filers {
		for _, m := range modes {
			t.Run(f.name+"/"+m.name, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				store := openInternalStore(t)
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
				// What a repair filed before the grant looks like.
				if err := store.UpdateTask(ctx, id, func(t *model.Task) error {
					t.Grants = nil
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				later := now.Add(time.Hour)
				run, jobs = stage1Run(t, ctx, store, repo, 72, "main", "push", "failure", later,
					map[string]string{"go": "failure"})
				again, err := f.file(ctx, store, run, jobs, later)
				if err != nil {
					t.Fatal(err)
				}
				if again != id {
					t.Fatalf("second red run filed %s, want it appended to the open repair %s", again, id)
				}
				repair, err := store.GetTask(ctx, id)
				if err != nil || repair == nil {
					t.Fatalf("reading repair %s: %v", id, err)
				}
				if !slices.EqualFunc(repair.Grants, m.want, model.Grant.Equal) {
					t.Errorf("repair grants after the next red run = %+v, want %+v", repair.Grants, m.want)
				}
			})
		}
	}
}

// A repair the capability policy denies the default api:ci-read is told
// so, in its body and in each red run appended to it: its `grain ci log`
// will answer forbidden, and asking for the grant is pointless. One that
// holds a CI grant is told nothing of the kind.
func TestARepairDeniedCIReadSaysWhy(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	denyCIRead := model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{
		{Capability: model.APIRef("ci-read"), Deny: true},
	}}
	for _, c := range []struct {
		name   string
		mode   model.CIMode
		policy model.CapabilityPolicy
		denied bool
	}{
		{name: "denied", mode: model.CIModeGitHub, policy: denyCIRead, denied: true},
		{name: "permitted", mode: model.CIModeGitHub},
		{name: "grain", mode: model.CIModeGrain, policy: denyCIRead},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := openInternalStore(t)
			if err := store.PutConfig(ctx, model.Config{CapabilityPolicy: c.policy}); err != nil {
				t.Fatal(err)
			}
			if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: c.mode}); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
			run, jobs := stage1Run(t, ctx, store, repo, 71, "main", "push", "failure", now,
				map[string]string{"go": "failure"})
			id, err := fileOrUpdateGitHubBaseRepair(ctx, store, run, jobs, now)
			if err != nil {
				t.Fatal(err)
			}
			run, jobs = stage1Run(t, ctx, store, repo, 72, "main", "push", "failure", now.Add(time.Hour),
				map[string]string{"go": "failure"})
			if _, err := fileOrUpdateGitHubBaseRepair(ctx, store, run, jobs, now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			repair, err := store.GetTask(ctx, id)
			if err != nil || repair == nil {
				t.Fatalf("reading repair %s: %v", id, err)
			}
			comments, err := store.Comments(ctx, id)
			if err != nil || len(comments) == 0 {
				t.Fatalf("comments of %s: %d, %v", id, len(comments), err)
			}
			const says = "capability policy denies https://grain.dev/capabilities/api#ci-read on acme/widgets"
			for what, text := range map[string]string{"body": repair.Body, "appended run": comments[len(comments)-1].Body} {
				if got := strings.Contains(text, says); got != c.denied {
					t.Errorf("%s names the policy deny = %v, want %v:\n%s", what, got, c.denied, text)
				}
			}
		})
	}
}

// The deny note follows what the repair holds, not the repository's
// defaults alone: an open repair that still holds an api:ci-read -- the
// default it was filed with before a deny rule was added, or one a person
// granted it -- can still run `grain ci log`, so a red run appended to it
// must not tell it otherwise (review task 6183). AdjustRepoCIGrants never
// strips that grant, and neither does the append.
func TestAnAppendedRunDoesNotDenyACIReadTheRepairStillHolds(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	denyCIRead := model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{
		{Capability: model.APIRef("ci-read"), Deny: true},
	}}
	personGrant := model.Grant{Capability: model.APIGrantString("ci-read", repo.String()), Via: model.GrantByLabel}
	for _, c := range []struct {
		name string
		// filedUnder is the policy the repair is filed under; the deny
		// rule is in force from then on.
		filedUnder model.CapabilityPolicy
		// granted replaces the repair's grants after filing.
		granted []model.Grant
		want    []model.Grant
	}{
		{name: "filed-before-the-deny", want: []model.Grant{model.DefaultCIReadGrant(repo)}},
		{name: "granted-by-a-person", filedUnder: denyCIRead, granted: []model.Grant{personGrant}, want: []model.Grant{personGrant}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := openInternalStore(t)
			if err := store.PutConfig(ctx, model.Config{CapabilityPolicy: c.filedUnder}); err != nil {
				t.Fatal(err)
			}
			if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
			run, jobs := stage1Run(t, ctx, store, repo, 71, "main", "push", "failure", now,
				map[string]string{"go": "failure"})
			id, err := fileOrUpdateGitHubBaseRepair(ctx, store, run, jobs, now)
			if err != nil {
				t.Fatal(err)
			}
			if c.granted != nil {
				if err := store.UpdateTask(ctx, id, func(t *model.Task) error {
					t.Grants = c.granted
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.PutConfig(ctx, model.Config{CapabilityPolicy: denyCIRead}); err != nil {
				t.Fatal(err)
			}
			later := now.Add(time.Hour)
			run, jobs = stage1Run(t, ctx, store, repo, 72, "main", "push", "failure", later,
				map[string]string{"go": "failure"})
			if _, err := fileOrUpdateGitHubBaseRepair(ctx, store, run, jobs, later); err != nil {
				t.Fatal(err)
			}
			repair, err := store.GetTask(ctx, id)
			if err != nil || repair == nil {
				t.Fatalf("reading repair %s: %v", id, err)
			}
			if !slices.EqualFunc(repair.Grants, c.want, model.Grant.Equal) {
				t.Errorf("repair grants = %+v, want %+v", repair.Grants, c.want)
			}
			comments, err := store.Comments(ctx, id)
			if err != nil || len(comments) == 0 {
				t.Fatalf("comments of %s: %d, %v", id, len(comments), err)
			}
			if body := comments[len(comments)-1].Body; strings.Contains(body, "holds no CI read") {
				t.Errorf("appended run tells a repair holding %+v it has no CI read:\n%s", repair.Grants, body)
			}
		})
	}
}
