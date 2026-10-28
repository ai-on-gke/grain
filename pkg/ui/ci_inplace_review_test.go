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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// putReviewedAndReview stores task 7 and a review of it, 8. inPlace makes
// 8 a review working on 7's own branch (model.LinkPushesReviewedBranch),
// the way the security review is filed; otherwise 8 is a review stacked
// on 7's branch with a branch of its own, the way reviews were filed
// before that and declared reviews still are.
func putReviewedAndReview(t *testing.T, store *model.Store, inPlace bool) {
	t.Helper()
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	grants := []model.Grant{{Capability: "grain-ci"}}
	if err := store.PutTask(ctx, model.Task{
		ID: "7", Intent: model.IntentImplement, Title: "change it", Target: &repo, Grants: grants,
	}); err != nil {
		t.Fatal(err)
	}
	review := model.Task{
		ID: "8", Intent: model.IntentImplement, Title: "review it", Target: &repo, Grants: grants,
		Origin: model.Origin{Reason: model.ReasonReview}, Base: "grain/task-7",
	}
	if inPlace {
		review.Branch = "grain/task-7"
		review.Links = []model.Link{{Kind: model.LinkPushesReviewedBranch, Target: "7"}}
	}
	if err := store.PutTask(ctx, review); err != nil {
		t.Fatal(err)
	}
}

// A review working on the reviewed change's own branch owns no CI: its
// wait_for_checks asks as the reviewed task, on that task's branch, and
// the verdict is recorded on -- and read back from -- the reviewed task,
// so one push to the shared branch is one run rather than one per task.
func TestInPlaceReviewsCIRunIsTheReviewedTasks(t *testing.T) {
	t.Parallel()
	started := time.Unix(10, 0).UTC()
	runner := &fakeCIRunner{run: ci.Run{ID: "run-branch", Repo: "acme/widgets", Ref: "grain/task-7",
		Commit: "abcdef", StartedAt: &started}}
	client, store, _ := testClient(t)
	client.Config.CI = runner
	srv := ui.NewServerWithClient(client)
	putReviewedAndReview(t, store, true)

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"8"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	if len(runner.started) != 1 {
		t.Fatalf("started %d runs, want 1", len(runner.started))
	}
	if req := runner.started[0]; req.Ref != "grain/task-7" || req.RequestedBy != "7" {
		t.Fatalf("the review's run was asked as %q on %q, want as the reviewed task 7 on its branch", req.RequestedBy, req.Ref)
	}
	ctx := context.Background()
	if obs, err := store.GetObservation(ctx, "7"); err != nil || obs == nil || obs.CI == nil || obs.CI.RunID != "run-branch" {
		t.Fatalf("the reviewed task's recorded CI = %+v (%v), want run-branch", obs, err)
	}
	if obs, err := store.GetObservation(ctx, "8"); err != nil || (obs != nil && obs.CI != nil) {
		t.Fatalf("the review recorded a CI status of its own: %+v (%v)", obs, err)
	}

	// What the review's pull_request_status and wait_for_checks read back
	// (ui.TaskDetail.CI), and its task page shows, is the reviewed task's.
	detail, err := client.GetTask(ctx, "8")
	if err != nil {
		t.Fatal(err)
	}
	if detail.CI == nil || detail.CI.RunID != "run-branch" || detail.CI.Commit != "abcdef" {
		t.Fatalf("the review's CI = %+v, want the reviewed task's run-branch", detail.CI)
	}

	// retrigger_ci and run_ci_job ask as the reviewed task too.
	rec = do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"8","jobs":["go"],"recheck":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("recheck status = %d, want 202: %s", rec.Code, rec.Body)
	}
	if req := runner.started[len(runner.started)-1]; req.RequestedBy != "7" || req.Ref != "grain/task-7" {
		t.Fatalf("the review's recheck was asked as %q on %q, want as task 7", req.RequestedBy, req.Ref)
	}
}

// A review stacked on the reviewed branch with a branch of its own --
// filed before in-place reviewing, or a declared review -- keeps its own
// CI, on its own branch.
func TestStackedReviewKeepsItsOwnCIRun(t *testing.T) {
	t.Parallel()
	started := time.Unix(10, 0).UTC()
	runner := &fakeCIRunner{run: ci.Run{ID: "run-review", Repo: "acme/widgets", Ref: "grain/task-8",
		Commit: "abcdef", StartedAt: &started}}
	srv, store := ciServer(t, runner)
	putReviewedAndReview(t, store, false)

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"8"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	if req := runner.started[0]; req.Ref != "grain/task-8" || req.RequestedBy != "8" {
		t.Fatalf("the stacked review's run was asked as %q on %q, want its own", req.RequestedBy, req.Ref)
	}
	if obs, err := store.GetObservation(context.Background(), "8"); err != nil || obs == nil || obs.CI == nil ||
		obs.CI.RunID != "run-review" {
		t.Fatalf("the stacked review's recorded CI = %+v (%v), want run-review", obs, err)
	}
}

// A review whose link names a task on some other branch is not let
// through to that task's CI: what it may ask for is its own branch's.
func TestInPlaceReviewNamingAnotherBranchIsRefusedCI(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{run: ci.Run{ID: "run-x"}}
	srv, store := ciServer(t, runner)
	putReviewedAndReview(t, store, true)
	ctx := context.Background()
	if err := store.UpdateTask(ctx, "8", func(task *model.Task) error {
		task.Branch = "grain/task-8"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"8"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if len(runner.started) != 0 {
		t.Fatalf("started %+v for a review naming another branch's task", runner.started)
	}
}
