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
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// isDefaultCIRead reports whether a recorded capability string is the
// api:ci-read a task on a GitHub-CI repository is filed holding by default
// (model.DefaultCIReadGrant), on any repository.
func isDefaultCIRead(c string) bool {
	return strings.HasPrefix(c, model.APIRef("ci-read")+"=")
}

// withoutDefaultCIRead is a task's capabilities minus the default CI read,
// for the tests here that pin which other grants a filing carries: every
// task testClient files targets acme/widgets, a repository with no CI mode
// set (so GitHub CI), and is given api:ci-read@acme/widgets beside them.
func withoutDefaultCIRead(caps []string) []string {
	return slices.DeleteFunc(slices.Clone(caps), isDefaultCIRead)
}

// withoutDefaultCIReadGrants is withoutDefaultCIRead for stored grants.
func withoutDefaultCIReadGrants(grants []model.Grant) []model.Grant {
	return slices.DeleteFunc(slices.Clone(grants), func(g model.Grant) bool {
		return g.Via == model.GrantByDefault && isDefaultCIRead(model.CanonicalGrantKey(g.Capability))
	})
}

// A task filed against a repository GitHub Actions gates is given read
// access to that repository's CI and nothing more: grain#ci is not
// attached there (it would change merge gating), and before task 4364 that
// left the run with no CI read at all, so its `grain ci log` answered
// "forbidden" and it asked a person for grain#ci.
func TestCreateTaskOnAGitHubCIRepoReadsItsOwnCIByDefault(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "github ci", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := model.DefaultCIReadGrant(model.RepoRef{Owner: "acme", Name: "widgets"})
	if len(stored.Grants) != 1 || !stored.Grants[0].Equal(want) {
		t.Fatalf("grants = %+v, want only %+v", stored.Grants, want)
	}
	names := []string{stored.Grants[0].Capability}
	set := model.APIGrantsOf(names)
	if !set.Allows("ci:read", "acme/widgets") {
		t.Errorf("the default grant does not open ci:read on acme/widgets")
	}
	if set.Allows("ci:read", "acme/other") || set.Allows("ci:read", "") {
		t.Errorf("the default grant reaches CI beyond acme/widgets")
	}
	for _, scope := range []string{"ci:write", "tasks:read", "merge:write", "memory:read"} {
		if set.Has(model.APIScope(scope)) {
			t.Errorf("the default grant opens %s, want ci:read alone", scope)
		}
	}
	if model.HasGrainCI(names) {
		t.Errorf("a GitHub-CI repository's task holds grain#ci")
	}
}

// A grain-CI repository's task keeps grain#ci, which already reads its CI,
// and is not given api:ci-read beside it.
func TestCreateTaskOnAGrainCIRepoHoldsGrainCINotCIRead(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "grain ci", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Grants) != 1 || !grainci.IsGrant(stored.Grants[0].Capability) {
		t.Fatalf("grants = %+v, want only grain#ci", stored.Grants)
	}
}

// A capability policy deny rule on api#ci-read is the opt-out: for every
// repository, or for the ones its repos name.
func TestCapabilityPolicyDenyTurnsTheDefaultCIReadOff(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		repos []string
		want  bool
	}{
		{name: "deployment-wide"},
		{name: "this repository", repos: []string{"acme/widgets"}},
		{name: "another repository", repos: []string{"acme/other"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, store, ctx := testClient(t)
			if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
				if cfg.PollInterval == 0 {
					cfg.PollInterval = model.DefaultPollInterval
				}
				cfg.CapabilityPolicy.Rules = []model.CapabilityPolicyRule{
					{Capability: model.APIRef("ci-read"), Repos: tc.repos, Deny: true},
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "opted out", Approved: true})
			if err != nil {
				t.Fatal(err)
			}
			got := slices.ContainsFunc(task.Capabilities, isDefaultCIRead)
			if got != tc.want {
				t.Errorf("default ci-read held = %v (capabilities %v), want %v", got, task.Capabilities, tc.want)
			}
			if len(task.DroppedDefaults) != 0 {
				t.Errorf("DroppedDefaults = %+v: an opted-out default is not offered and dropped, it is not offered", task.DroppedDefaults)
			}
		})
	}
}

// The agent API gate (authorizeAgentCall) opens to the default grant
// exactly the CI reads the prompt lists for a GitHub-CI run -- `grain ci
// list`, `grain ci show/log` of a GitHub Actions run (gha:<owner>:<name>:<run>)
// -- on the task's own repository, and nothing beyond it: another
// repository's runs, and every other scope, are refused.
func TestDefaultCIReadOpensOnlyItsOwnRepositorysCIOverTheAgentAPI(t *testing.T) {
	t.Parallel()
	now := time.Now()
	forge := newFakeActions(now)
	srv, client, store := actionsServer(t, forge, now)
	filed, err := client.CreateTask(context.Background(), ui.CreateTaskRequest{Title: "github ci", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetTask(context.Background(), filed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Grants) != 1 || !model.IsDefaultCIReadGrant(stored.Grants[0], model.RepoRef{Owner: "acme", Name: "widgets"}) {
		t.Fatalf("grants = %+v, want only the default ci-read on acme/widgets", stored.Grants)
	}
	id := filed.ID
	for _, path := range []string{
		"/api/ci/runs",
		"/api/ci/runs?repo=acme/widgets",
		"/api/ci/runs/gha:acme:widgets:42",
		"/api/ci/runs/gha:acme:widgets:42/log",
	} {
		if code, body := asAgent(t, srv, id, http.MethodGet, path, "", ""); code != http.StatusOK {
			t.Errorf("the default ci-read was refused %s: %d %s", path, code, body)
		}
	}
	for _, path := range []string{
		"/api/ci/runs?repo=acme/other",
		"/api/ci/runs/gha:acme:other:42",
		"/api/ci/runs/gha:acme:other:42/log",
		"/api/tasks",
		"/api/memory",
	} {
		if code, body := asAgent(t, srv, id, http.MethodGet, path, "", ""); code != http.StatusForbidden {
			t.Errorf("the default ci-read reached %s: %d %s", path, code, body)
		}
	}
	if code, body := asAgent(t, srv, id, http.MethodPost, "/api/ci/runs", `{"repo":"acme/widgets","ref":"main"}`, "rerun"); code != http.StatusForbidden {
		t.Errorf("the default ci-read started a CI run: %d %s", code, body)
	}
}
