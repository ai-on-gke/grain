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
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// QARootOrigins (the batch resolver loops over ListTasks use) must give
// every task the same key Store.ResolveTaskQARootOrigin (dispatch) gives
// it, or an originless child's enrollment is invisible to stranded-stage
// rescue, experiment results and metrics.
func TestQARootOriginsAgreesWithStoreForOriginlessChildren(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	put := func(id string, origin model.WorkOrigin, links ...model.Link) {
		t.Helper()
		if err := store.PutTask(ctx, model.Task{
			ID: id, Intent: model.IntentImplement, Title: "task " + id, Target: &repo,
			Filing:     model.Filing{Attribution: model.Attribution{Actor: model.TriggerPrincipal}, Reason: model.ReasonDirect},
			Binding:    model.BindingDirective,
			WorkOrigin: origin,
			Links:      links,
		}); err != nil {
			t.Fatalf("PutTask(%s): %v", id, err)
		}
	}
	proposedBy := func(id string) model.Link { return model.Link{Kind: model.LinkProposedBy, Target: id} }

	put("10", "")
	// Originless child and grandchild: stored first, link added after, as
	// pre-origin and updated-link trees are.
	put("11", "")
	put("11", "", proposedBy("10"))
	put("12", "")
	put("12", "", proposedBy("11"))
	// A GitHub-origin tree with an originless child.
	put("20", model.GitHubOrigin(repo, 7))
	put("21", "")
	put("21", "", proposedBy("20"))
	// A child whose parent is not in the store.
	put("31", "")
	put("31", "", proposedBy("999"))

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	batch := model.NewQARootOrigins(tasks)
	want := map[string]string{"10": "task:10", "11": "task:10", "12": "task:10", "21": string(model.GitHubOrigin(repo, 7)), "31": "task:999"}
	for _, tk := range tasks {
		fromStore, err := store.ResolveTaskQARootOrigin(ctx, tk)
		if err != nil {
			t.Fatalf("ResolveTaskQARootOrigin(%s): %v", tk.ID, err)
		}
		if got := batch.Of(tk); got != fromStore {
			t.Errorf("task %s: QARootOrigins.Of = %q, store = %q", tk.ID, got, fromStore)
		}
		if w, ok := want[tk.ID]; ok && fromStore != w {
			t.Errorf("task %s: root origin = %q, want %q", tk.ID, fromStore, w)
		}
	}
}
