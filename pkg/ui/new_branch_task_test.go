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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A task filed with "create a new branch" (grain/task-2317) targets the
// branch it names and records the request to cut it from the branch it
// was based on -- the request the branches reconciler carries out and
// dispatch waits for.
func TestTaskCreatingItsBranchRecordsTheRequest(t *testing.T) {
	t.Parallel()
	srv, client := existenceServer(t, &branchLister{names: []string{"main", "release/1.2"}})

	rec := do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"t","description":"d","repo":"acme/widgets","base":" hotfix/x ","newBranch":true,"newBranchFrom":"release/1.2"}`)
	wantStatus(t, rec, http.StatusCreated)
	task := decode[ui.Task](t, rec)
	if task.Base != "hotfix/x" {
		t.Fatalf("task base = %q, want the new branch hotfix/x", task.Base)
	}
	got, err := client.Store.ListBranches(context.Background(), model.RepoRef{Owner: "acme", Name: "widgets"})
	if err != nil || len(got) != 1 {
		t.Fatalf("list branches: (%+v, %v)", got, err)
	}
	if got[0].Name != "hotfix/x" || got[0].From != "release/1.2" || got[0].Status != model.BranchPending {
		t.Fatalf("got %+v, want a pending hotfix/x cut from release/1.2", got[0])
	}

	// Cut from the default branch when no base is picked.
	wantStatus(t, do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"t","description":"d","repo":"acme/widgets","base":"spike","newBranch":true}`), http.StatusCreated)
}

func TestTaskCreatingItsBranchIsRefusedWhenItCannotBeCut(t *testing.T) {
	t.Parallel()
	srv, client := existenceServer(t, &branchLister{names: []string{"main"}})

	for name, tc := range map[string]struct{ body, want string }{
		"no name": {`{"title":"t","repo":"acme/widgets","newBranch":true}`, "needs a name"},
		"missing from": {`{"title":"t","repo":"acme/widgets","base":"x","newBranch":true,"newBranchFrom":"no-such-branch"}`,
			"does not exist on acme/widgets"},
		"bad from": {`{"title":"t","repo":"acme/widgets","base":"x","newBranch":true,"newBranchFrom":"a..b"}`,
			"not a usable branch name"},
		"from itself": {`{"title":"t","repo":"acme/widgets","base":"main","newBranch":true,"newBranchFrom":"main"}`,
			"cut from itself"},
		"from without new": {`{"title":"t","repo":"acme/widgets","base":"main","newBranchFrom":"main"}`,
			"only for a task that creates"},
		"no repo": {`{"title":"t","noRepo":true,"base":"x","newBranch":true}`, "needs a repo"},
	} {
		rec := do(t, srv, http.MethodPost, "/api/tasks", tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: got %d %s, want 400 saying %q", name, rec.Code, rec.Body, tc.want)
		}
	}
	got, err := client.Store.ListBranches(context.Background(), model.RepoRef{Owner: "acme", Name: "widgets"})
	if err != nil || len(got) != 0 {
		t.Fatalf("branches = (%+v, %v), want none requested by a refused task", got, err)
	}
}

// grain/task-2387: two tasks filed onto the same new branch, the second
// naming a different "Create from" while the first request is still
// pending. The branch is cut once, from the first request's base, so the
// second is refused with a sentence saying so rather than filed onto a
// branch made from something its form did not ask for. The same base
// files.
func TestTaskCreatingABranchAlreadyPendingFromAnotherBaseIsRefused(t *testing.T) {
	t.Parallel()
	srv, client := existenceServer(t, &branchLister{names: []string{"main", "release/1.2"}})

	wantStatus(t, do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"first","description":"d","repo":"acme/widgets","base":"feature/x","newBranch":true,"newBranchFrom":"release/1.2"}`),
		http.StatusCreated)

	rec := do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"second","description":"d","repo":"acme/widgets","base":"feature/x","newBranch":true,"newBranchFrom":"main"}`)
	if rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), `already waiting to be created on acme/widgets from \"release/1.2\"`) {
		t.Fatalf("got %d %s, want 400 naming the pending request's own base", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"third","description":"d","repo":"acme/widgets","base":"feature/x","newBranch":true}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "release/1.2") {
		t.Fatalf("default base: got %d %s, want 400", rec.Code, rec.Body)
	}

	wantStatus(t, do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"same base","description":"d","repo":"acme/widgets","base":"feature/x","newBranch":true,"newBranchFrom":"release/1.2"}`),
		http.StatusCreated)
	got, err := client.Store.ListBranches(context.Background(), model.RepoRef{Owner: "acme", Name: "widgets"})
	if err != nil || len(got) != 1 || got[0].From != "release/1.2" {
		t.Fatalf("branches = (%+v, %v), want the one request from release/1.2", got, err)
	}
	tasks, err := client.Store.ListTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.Title == "second" || task.Title == "third" {
			t.Fatalf("refused task %q was written", task.Title)
		}
	}
}
