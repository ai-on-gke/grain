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

package postgres_test

import (
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestDrivenTaskStatesOnPostgres verifies Store.DrivenTaskStates agrees with
// Store.States and Store.StateTimes on PostgreSQL, including partial index
// predicates, boolean expressions, and correlated latest-finished-run lookup.
func TestDrivenTaskStatesOnPostgres(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	actor := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	approval := model.Attribution{Actor: actor}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	put := func(tk model.Task) {
		t.Helper()
		tk.Intent = model.IntentImplement
		tk.Title = tk.ID
		if tk.Filing.Attribution.Actor.Kind == "" {
			tk.Filing = model.Filing{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect}
		}
		tk.Binding = model.BindingDirective
		tk.Target = &repo
		tk.CreatedAt = &at
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("PutTask(%s): %v", tk.ID, err)
		}
	}

	put(model.Task{ID: "proposed"})
	put(model.Task{ID: "queued-fresh", Approval: &approval, ApprovedAt: &at})
	put(model.Task{ID: "queued-rerun", Approval: &approval, ApprovedAt: &at})
	fin1, fin2 := at.Add(time.Hour), at.Add(3*time.Hour)
	for i, fin := range []time.Time{fin1, fin2} {
		rid := "queued-rerun-" + string(rune('1'+i))
		if err := store.StartRun(ctx, model.Run{ID: rid, TaskID: "queued-rerun", Sandbox: "sb", Attempt: i + 1, StartedAt: fin.Add(-time.Minute)}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, rid, fin, "failed", ""); err != nil {
			t.Fatal(err)
		}
	}
	put(model.Task{ID: "running", Approval: &approval, ApprovedAt: &at})
	if err := store.StartRun(ctx, model.Run{ID: "running-1", TaskID: "running", Sandbox: "sb", Attempt: 1, StartedAt: at.Add(4 * time.Hour)}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	put(model.Task{
		ID: "completed-pr", Approval: &approval, ApprovedAt: &at, AutoMerge: true,
		Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#10"}},
	})
	compAt := at.Add(5 * time.Hour)
	if err := store.ObserveField(ctx, "completed-pr", compAt, func(o *model.Observation) { o.CompletedAt = &compAt }); err != nil {
		t.Fatal(err)
	}
	put(model.Task{
		ID: "awaiting-submit", Approval: &approval, ApprovedAt: &at, AutoMerge: false,
		Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#11"}},
	})
	if err := store.ObserveField(ctx, "awaiting-submit", compAt, func(o *model.Observation) { o.CompletedAt = &compAt }); err != nil {
		t.Fatal(err)
	}
	put(model.Task{ID: "deferred-wake", Approval: &approval, ApprovedAt: &at})
	wakeAt := at.Add(24 * time.Hour)
	if err := store.SetDeferred(ctx, "deferred-wake", &at, &wakeAt, at); err != nil {
		t.Fatal(err)
	}
	put(model.Task{ID: "closed", Approval: &approval, ApprovedAt: &at})
	if err := store.ObserveField(ctx, "closed", compAt, func(o *model.Observation) { o.ClosedAt = &compAt }); err != nil {
		t.Fatal(err)
	}

	states, err := store.States(ctx)
	if err != nil {
		t.Fatalf("States: %v", err)
	}
	times, err := store.StateTimes(ctx)
	if err != nil {
		t.Fatalf("StateTimes: %v", err)
	}
	driven, err := store.DrivenTaskStates(ctx)
	if err != nil {
		t.Fatalf("DrivenTaskStates: %v", err)
	}

	for id, wantState := range states {
		wantSince := times[id].Since(wantState)
		got, ok := driven[id]
		if wantSince == nil {
			if ok {
				t.Errorf("non-driven task %s (%s) appeared in DrivenTaskStates: %+v", id, wantState, got)
			}
			continue
		}
		if !ok {
			t.Errorf("driven task %s (%s) missing from DrivenTaskStates", id, wantState)
			continue
		}
		if got.State != wantState {
			t.Errorf("task %s: state = %s, want %s", id, got.State, wantState)
		}
		gotSince := got.Times.Since(got.State)
		if gotSince == nil || !gotSince.Equal(*wantSince) {
			t.Errorf("task %s: since = %v, want %v", id, gotSince, wantSince)
		}
	}
}

// TestReviewQueriesAndReportChecksOnPostgres verifies HasReviewActivity,
// OpenPullRequestLinksNeedingReview, FinishedReviewTaskIDs, ObsoleteReviews,
// OpenPathTriggerReviewsWithDependencies, and RunReportChecksWithTimes on
// PostgreSQL.
func TestReviewQueriesAndReportChecksOnPostgres(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	actor := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	approval := model.Attribution{Actor: actor}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if decl, revs, err := store.HasReviewActivity(ctx); err != nil || decl || revs {
		t.Fatalf("empty HasReviewActivity = (%v, %v, %v), want (false, false, nil)", decl, revs, err)
	}

	put := func(tk model.Task) {
		t.Helper()
		tk.Intent = model.IntentImplement
		tk.Title = tk.ID
		if tk.Filing.Attribution.Actor.Kind == "" {
			tk.Filing = model.Filing{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect}
		}
		tk.Binding = model.BindingDirective
		tk.Target = &repo
		tk.Approval = &approval
		tk.ApprovedAt = &at
		tk.CreatedAt = &at
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("PutTask(%s): %v", tk.ID, err)
		}
	}

	put(model.Task{
		ID: "pr-due", AutoMerge: true, ReviewTemplateID: "rev-tmpl",
		Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#1"}},
	})
	put(model.Task{
		ID: "pr-closed", AutoMerge: true,
		Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#2"}},
	})
	if err := store.ObserveField(ctx, "pr-due", at, func(o *model.Observation) { o.CompletedAt = &at }); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, "pr-closed", at, func(o *model.Observation) {
		o.CompletedAt = &at
		o.ClosedAt = &at
	}); err != nil {
		t.Fatal(err)
	}

	revFiling := model.Filing{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonReview}
	put(model.Task{ID: "rev-finished", Filing: revFiling})
	if err := store.ObserveField(ctx, "rev-finished", at, func(o *model.Observation) { o.CompletedAt = &at }); err != nil {
		t.Fatal(err)
	}
	put(model.Task{
		ID: "rev-obsolete", Filing: revFiling,
		Links: []model.Link{{Kind: model.LinkProposedBy, Target: "pr-closed"}},
	})
	put(model.Task{
		ID: "rev-pt-dep", Filing: revFiling,
		Links: []model.Link{{Kind: model.LinkDependsOn, Target: "rev-finished"}},
		Tags:  []string{"path-trigger-review:sec"},
	})

	if decl, revs, err := store.HasReviewActivity(ctx); err != nil || !decl || !revs {
		t.Fatalf("HasReviewActivity = (%v, %v, %v), want (true, true, nil)", decl, revs, err)
	}
	if due, err := store.OpenPullRequestLinksNeedingReview(ctx); err != nil || len(due) != 1 || due[0].TaskID != "pr-due" {
		t.Errorf("OpenPullRequestLinksNeedingReview = (%+v, %v), want [pr-due]", due, err)
	}
	if fin, err := store.FinishedReviewTaskIDs(ctx); err != nil || !slices.Equal(fin, []string{"rev-finished"}) {
		t.Errorf("FinishedReviewTaskIDs = (%v, %v), want [rev-finished]", fin, err)
	}
	if obs, err := store.ObsoleteReviews(ctx); err != nil || len(obs) != 1 || obs[0].ReviewID != "rev-obsolete" {
		t.Errorf("ObsoleteReviews = (%+v, %v), want [rev-obsolete]", obs, err)
	}
	if pt, err := store.OpenPathTriggerReviewsWithDependencies(ctx); err != nil || !slices.Equal(pt, []string{"rev-pt-dep"}) {
		t.Errorf("OpenPathTriggerReviewsWithDependencies = (%v, %v), want [rev-pt-dep]", pt, err)
	}

	if err := store.StartRun(ctx, model.Run{ID: "r1", TaskID: "pr-due", Sandbox: "sb", Attempt: 1, StartedAt: at}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	fin := at.Add(time.Minute)
	if err := store.FinishRun(ctx, "r1", fin, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRunReportCheck(ctx, model.RunReportCheck{RunID: "r1", Claims: 5, Uncited: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRunReportCheck(ctx, model.RunReportCheck{RunID: "r-missing", Claims: 3, Unread: 1}); err != nil {
		t.Fatal(err)
	}
	checks, err := store.RunReportChecksWithTimes(ctx)
	if err != nil || len(checks) != 2 {
		t.Fatalf("RunReportChecksWithTimes = (%+v, %v), want 2", checks, err)
	}
	byID := map[string]model.RunReportCheckTiming{}
	for _, c := range checks {
		byID[c.RunID] = c
	}
	if c := byID["r1"]; !c.RunFound || c.FinishedAt == nil || !c.FinishedAt.Equal(fin) {
		t.Errorf("r1 = %+v, want RunFound=true FinishedAt=%v", c, fin)
	}
	if c := byID["r-missing"]; c.RunFound || c.Claims != 3 {
		t.Errorf("r-missing = %+v, want RunFound=false Claims=3", c)
	}
}
