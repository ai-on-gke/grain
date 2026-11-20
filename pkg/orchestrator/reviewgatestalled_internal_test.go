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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// stalledGateFixture is review r2 of acme/widgets#1, queued behind task
// blocker, filed at filed.
func stalledGateFixture(t *testing.T, filed time.Time) (context.Context, *model.Store) {
	t.Helper()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	put := func(task model.Task) {
		t.Helper()
		task.Intent, task.Body, task.Target, task.Binding = model.IntentImplement, "please", &repo, model.BindingDirective
		task.Approval = &model.Attribution{Actor: human}
		task.CreatedAt = &filed
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	put(model.Task{ID: "blocker", Title: "older review",
		Origin: model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonReview}})
	put(model.Task{ID: "r2", Title: "Code review: fix the thing (acme/widgets#1)",
		Origin: model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonReview},
		Links: []model.Link{
			{Kind: model.LinkDependsOn, Target: "blocker"},
			{Kind: model.LinkReviewsPullRequest, Target: "acme/widgets#1"},
		}})
	return ctx, store
}

// A review queued behind an open blocker past ReviewGateStalledAfter raises
// one pipeline.review-gate-stalled naming the blocker; before then, and
// once the blocker closes, there is none (task 6236: seven re-reviews sat
// queued for days with their gates reading "running" and nobody told).
func TestAReviewQueuedBehindABlockerRaisesOneNotificationNamingIt(t *testing.T) {
	t.Parallel()
	filed := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	ctx, store := stalledGateFixture(t, filed)
	subject := model.NotificationSubject(FamilyReviewGateStalled, "r2")

	if err := reconcileReviewGateStalled(ctx, store, filed.Add(ReviewGateStalledAfter-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, err := store.OpenNotification(ctx, subject); err != nil || n != nil {
		t.Fatalf("raised %+v (%v) before the review had waited its patience", n, err)
	}

	for _, at := range []time.Duration{ReviewGateStalledAfter + time.Minute, ReviewGateStalledAfter + time.Hour} {
		if err := reconcileReviewGateStalled(ctx, store, filed.Add(at)); err != nil {
			t.Fatal(err)
		}
	}
	n, err := store.OpenNotification(ctx, subject)
	if err != nil || n == nil {
		t.Fatalf("no %s raised for a review queued behind a blocker (%v)", subject, err)
	}
	if !strings.Contains(n.Title, "task blocker (queued)") || !strings.Contains(n.Title, "acme/widgets#1") {
		t.Errorf("title %q does not name the blocker and the pull request", n.Title)
	}
	if !strings.Contains(n.Body, "[task blocker](/tasks/blocker)") {
		t.Errorf("body does not link the blocker:\n%s", n.Body)
	}
	all, err := store.ListNotifications(ctx, model.NotificationFilter{Family: FamilyReviewGateStalled, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("raised %d notifications over two cycles, want one", len(all))
	}

	done := filed.Add(ReviewGateStalledAfter + 2*time.Hour)
	if err := store.ObserveField(ctx, "blocker", done, func(o *model.Observation) { o.ClosedAt = &done }); err != nil {
		t.Fatal(err)
	}
	if err := reconcileReviewGateStalled(ctx, store, done); err != nil {
		t.Fatal(err)
	}
	if n, err := store.OpenNotification(ctx, subject); err != nil || n != nil {
		t.Errorf("%s still open (%+v, %v) once the blocker closed", subject, n, err)
	}
}
