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
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// forkHeadServer is a server whose GitHub is a simulator of acme/widgets
// holding pull request #139, opened from andrzejfudro's fork with head
// chore/bump-agy-1.4.9 into main, and #140 from a branch of acme/widgets
// itself.
func forkHeadServer(t *testing.T) (*ui.Server, *ui.Client) {
	t.Helper()
	client, _, _ := testClient(t)
	sim := githubsim.New("acme", "widgets", "", "main")
	sim.PullRequests = []githubsim.PullRequest{
		{Number: 139, Title: "chore: bump agy to 1.4.9", Head: "chore/bump-agy-1.4.9", Base: "main", State: "open",
			HeadRepo: "andrzejfudro/widgets", Author: "andrzejfudro"},
		{Number: 140, Title: "in-repo change", Head: "feature/x", Base: "main", State: "open"},
	}
	client.Config.GitHubForkPullRequests = github.NewClient(sim, nil)
	return ui.NewServerWithClient(client), client
}

// A task filed with a GitHub fork head label as its branch -- what a pull
// request's page shows as its head -- is resolved to the open pull request
// that label heads: based on that pull request's base, linked to it, and
// told how to start from its head, since grain cannot push a fork.
func TestTaskFiledAgainstAForkHeadResolvesToItsPullRequest(t *testing.T) {
	t.Parallel()
	srv, client := forkHeadServer(t)

	rec := do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"t","description":"d","repo":"acme/widgets","base":"andrzejfudro:chore/bump-agy-1.4.9"}`)
	wantStatus(t, rec, http.StatusCreated)
	task, err := client.Store.GetTask(t.Context(), decode[ui.Task](t, rec).ID)
	if err != nil || task == nil {
		t.Fatalf("GetTask = %v, %v", task, err)
	}
	if task.Base != "main" {
		t.Errorf("Base = %q, want the pull request's base, main", task.Base)
	}
	if !hasExternalPR(*task, "acme/widgets#139") {
		t.Errorf("Links = %+v, want an external-pr link to acme/widgets#139", task.Links)
	}
	for _, want := range []string{"andrzejfudro:chore/bump-agy-1.4.9", "refs/pull/139/head", "cannot push", "@andrzejfudro"} {
		if !strings.Contains(task.Body, want) {
			t.Errorf("Body does not say %q:\n%s", want, task.Body)
		}
	}

	t.Run("an edit to a fork head links the task the same way", func(t *testing.T) {
		rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"t","description":"d","repo":"acme/widgets"}`)
		wantStatus(t, rec, http.StatusCreated)
		id := decode[ui.Task](t, rec).ID
		wantStatus(t, do(t, srv, http.MethodPatch, "/api/tasks/"+id,
			`{"base":"andrzejfudro:chore/bump-agy-1.4.9"}`), http.StatusOK)
		edited, _ := client.Store.GetTask(t.Context(), id)
		if edited.Base != "main" || !hasExternalPR(*edited, "acme/widgets#139") ||
			!strings.Contains(edited.Body, "refs/pull/139/head") {
			t.Fatalf("edited task = base %q, links %+v, body %q", edited.Base, edited.Links, edited.Body)
		}
	})

	t.Run("a head in the repository itself is based on that branch", func(t *testing.T) {
		rec := do(t, srv, http.MethodPost, "/api/tasks",
			`{"title":"t","description":"d","repo":"acme/widgets","base":"acme:feature/x"}`)
		wantStatus(t, rec, http.StatusCreated)
		got, _ := client.Store.GetTask(t.Context(), decode[ui.Task](t, rec).ID)
		if got.Base != "feature/x" || !hasExternalPR(*got, "acme/widgets#140") {
			t.Fatalf("task = base %q, links %+v", got.Base, got.Links)
		}
	})
}

// A label no open pull request heads is refused naming the label, not
// with the generic "not a usable branch name"; a malformed one is refused
// saying what is wrong with it, and a plain invalid ref is still refused.
func TestTaskFiledAgainstAForkHeadIsRefusedClearly(t *testing.T) {
	t.Parallel()
	srv, _ := forkHeadServer(t)
	for name, tc := range map[string]struct{ body, want string }{
		"no open pull request": {
			`{"title":"t","repo":"acme/widgets","base":"someone:chore/bump-agy-1.4.9"}`,
			`no open pull request on acme/widgets has it as its head`,
		},
		"too many colons": {`{"title":"t","repo":"acme/widgets","base":"a:b:c"}`, `more than one ':'`},
		"empty owner":     {`{"title":"t","repo":"acme/widgets","base":":main"}`, `needs both an owner and a branch`},
		"empty branch":    {`{"title":"t","repo":"acme/widgets","base":"andrzejfudro:"}`, `needs both an owner and a branch`},
		"new branch": {
			`{"title":"t","repo":"acme/widgets","base":"andrzejfudro:chore/bump-agy-1.4.9","newBranch":true}`,
			`cannot be created`,
		},
		"new branch from a fork": {
			`{"title":"t","repo":"acme/widgets","base":"mine","newBranch":true,"newBranchFrom":"andrzejfudro:chore/bump-agy-1.4.9"}`,
			`grain cannot cut a branch from a fork`,
		},
		"plain invalid ref": {`{"title":"t","repo":"acme/widgets","base":"bad..ref"}`, `is not a usable branch name`},
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(t, srv, http.MethodPost, "/api/tasks", tc.body)
			wantStatus(t, rec, http.StatusBadRequest)
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body = %s, want it to say %q", rec.Body, tc.want)
			}
		})
	}
}

// With no GitHub client to ask, a label is refused saying why rather than
// filed against a branch that cannot exist.
func TestTaskFiledAgainstAForkHeadWithoutGitHubIsRefused(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"t","repo":"acme/widgets","base":"andrzejfudro:chore/bump-agy-1.4.9"}`)
	wantStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "cannot read acme/widgets's pull requests") {
		t.Fatalf("body = %s", rec.Body)
	}
}

func hasExternalPR(task model.Task, ref string) bool {
	for _, l := range task.Links {
		if l.Kind == model.LinkExternalPR && l.Target == ref {
			return true
		}
	}
	return false
}
