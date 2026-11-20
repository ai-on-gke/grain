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
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestDrivenTaskStatesMatchesStatesAndStateTimes pins Store.DrivenTaskStates
// against Store.States + Store.StateTimes across every task state and
// precedence edge: every task whose StateTimes.Since(state) is non-nil must
// appear with the exact same state and since instant, and non-driven states
// (closed, failed, proposed, awaiting_reply, awaiting_submit, undated
// deferred) must be omitted.
func TestDrivenTaskStatesMatchesStatesAndStateTimes(t *testing.T) {
	t.Parallel()
	store, ctx := open(t)
	VerifyDrivenTaskStates(t, ctx, store)
}

// VerifyDrivenTaskStates is shared by the SQLite and PostgreSQL test suites.
func VerifyDrivenTaskStates(t *testing.T, ctx context.Context, store *model.Store) {
	t.Helper()
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	actor := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	approval := model.Attribution{Actor: actor}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	put := func(tk model.Task) {
		t.Helper()
		if tk.Intent == "" {
			tk.Intent = model.IntentImplement
		}
		if tk.Title == "" {
			tk.Title = tk.ID
		}
		if tk.Filing.Attribution.Actor.Kind == "" {
			tk.Filing = model.Filing{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect}
		}
		if tk.Binding == "" {
			tk.Binding = model.BindingDirective
		}
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("PutTask(%s): %v", tk.ID, err)
		}
	}
	run := func(taskID string, attempt int, started time.Time, finished *time.Time, outcome string) {
		t.Helper()
		id := taskID + "-r" + string(rune('0'+attempt))
		if err := store.StartRun(ctx, model.Run{
			ID: id, TaskID: taskID, Sandbox: "sb-" + id, Attempt: attempt, StartedAt: started,
		}, model.Limits{}); err != nil {
			t.Fatalf("StartRun(%s): %v", id, err)
		}
		if finished != nil {
			if err := store.FinishRun(ctx, id, *finished, outcome, ""); err != nil {
				t.Fatalf("FinishRun(%s): %v", id, err)
			}
		}
	}

	// 1. Proposed (not driven).
	put(model.Task{ID: "proposed", Target: &repo, CreatedAt: &at})

	// 2. Queued, never run (driven: ApprovedAt).
	put(model.Task{ID: "queued-fresh", Target: &repo, Approval: &approval, ApprovedAt: &at, CreatedAt: &at})

	// 3. Queued, never run, nil ApprovedAt (still StateQueued; Since is nil).
	put(model.Task{ID: "queued-no-ts", Target: &repo, Approval: &approval, CreatedAt: &at})

	// 4. Queued after finished attempts (driven: LastRunFinishedAt).
	put(model.Task{ID: "queued-rerun", Target: &repo, Approval: &approval, ApprovedAt: &at, CreatedAt: &at})
	fin1, fin2 := at.Add(time.Hour), at.Add(3*time.Hour)
	run("queued-rerun", 1, at.Add(30*time.Minute), &fin1, "failed")
	run("queued-rerun", 2, at.Add(2*time.Hour), &fin2, "failed")

	// 5. Queued after retry / run-now / undeferral (driven: RequeuedAt).
	put(model.Task{ID: "queued-retried", Target: &repo, Approval: &approval, ApprovedAt: &at, CreatedAt: &at})
	run("queued-retried", 1, at.Add(30*time.Minute), &fin1, "failed")
	retried := at.Add(5 * time.Hour)
	if err := store.ObserveField(ctx, "queued-retried", retried, func(o *model.Observation) {
		o.RetryRequestedAt = &retried
	}); err != nil {
		t.Fatal(err)
	}

	put(model.Task{ID: "queued-undeferred", Target: &repo, Approval: &approval, ApprovedAt: &at, CreatedAt: &at})
	defAt := at.Add(time.Hour)
	undefAt := at.Add(6 * time.Hour)
	if err := store.SetDeferred(ctx, "queued-undeferred", &defAt, nil, defAt); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDeferred(ctx, "queued-undeferred", nil, nil, undefAt); err != nil {
		t.Fatal(err)
	}

	// 6. Running (driven: LiveRunStartedAt).
	put(model.Task{ID: "running", Target: &repo, Approval: &approval, ApprovedAt: &at, CreatedAt: &at})
	run("running", 1, at.Add(4*time.Hour), nil, "")

	// 7. Running with pending question: pending_question_comment_id outranks running (not driven).
	put(model.Task{ID: "running-question", Target: &repo, Approval: &approval, ApprovedAt: &at, CreatedAt: &at})
	run("running-question", 1, at.Add(4*time.Hour), nil, "")
	qid := int64(42)
	if err := store.ObserveField(ctx, "running-question", at.Add(4*time.Hour), func(o *model.Observation) {
		o.PendingQuestionCommentID = &qid
	}); err != nil {
		t.Fatal(err)
	}

	// 8. Completed auto-merge PR (driven: CompletedAt).
	put(model.Task{
		ID: "completed-pr", Target: &repo, Approval: &approval, ApprovedAt: &at, AutoMerge: true, CreatedAt: &at,
		Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#10"}},
	})
	compAt := at.Add(7 * time.Hour)
	if err := store.ObserveField(ctx, "completed-pr", compAt, func(o *model.Observation) {
		o.CompletedAt = &compAt
	}); err != nil {
		t.Fatal(err)
	}

	// 9. Completed review without PR (driven: CompletedAt).
	put(model.Task{
		ID: "completed-review", Target: &repo, Approval: &approval, ApprovedAt: &at, CreatedAt: &at,
		Filing: model.Filing{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonReview},
	})
	if err := store.ObserveField(ctx, "completed-review", compAt, func(o *model.Observation) {
		o.CompletedAt = &compAt
	}); err != nil {
		t.Fatal(err)
	}

	// 10. Awaiting submit (completed + auto_merge=false + fixes link -> not driven).
	put(model.Task{
		ID: "awaiting-submit", Target: &repo, Approval: &approval, ApprovedAt: &at, AutoMerge: false, CreatedAt: &at,
		Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#11"}},
	})
	if err := store.ObserveField(ctx, "awaiting-submit", compAt, func(o *model.Observation) {
		o.CompletedAt = &compAt
	}); err != nil {
		t.Fatal(err)
	}

	// 11. Completed non-review without change link -> awaiting_reply (not driven).
	put(model.Task{ID: "awaiting-ack", Target: &repo, Approval: &approval, ApprovedAt: &at, CreatedAt: &at})
	if err := store.ObserveField(ctx, "awaiting-ack", compAt, func(o *model.Observation) {
		o.CompletedAt = &compAt
	}); err != nil {
		t.Fatal(err)
	}

	// 12. Deferred without wake time (not driven).
	put(model.Task{ID: "deferred-indef", Target: &repo, Approval: &approval, ApprovedAt: &at, CreatedAt: &at})
	if err := store.SetDeferred(ctx, "deferred-indef", &defAt, nil, defAt); err != nil {
		t.Fatal(err)
	}

	// 13. Deferred with wake time (driven: DeferredUntil).
	put(model.Task{ID: "deferred-wake", Target: &repo, Approval: &approval, ApprovedAt: &at, CreatedAt: &at})
	wakeAt := at.Add(24 * time.Hour)
	if err := store.SetDeferred(ctx, "deferred-wake", &defAt, &wakeAt, defAt); err != nil {
		t.Fatal(err)
	}

	// 14. Failed (streak >= MaxConsecutiveFailures) + deferred with wake time: failed outranks deferred (not driven).
	put(model.Task{ID: "failed-deferred", Target: &repo, Approval: &approval, ApprovedAt: &at, CreatedAt: &at})
	for a := 1; a <= model.MaxConsecutiveFailures; a++ {
		f := at.Add(time.Duration(a) * time.Minute)
		run("failed-deferred", a, f.Add(-30*time.Second), &f, "failed")
	}
	if err := store.SetDeferred(ctx, "failed-deferred", &defAt, &wakeAt, defAt); err != nil {
		t.Fatal(err)
	}

	// 15. Closed (even with live run / deferred_until / completed_at -> not driven).
	put(model.Task{ID: "closed", Target: &repo, Approval: &approval, ApprovedAt: &at, CreatedAt: &at})
	if err := store.SetDeferred(ctx, "closed", &defAt, &wakeAt, defAt); err != nil {
		t.Fatal(err)
	}
	closedAt := at.Add(8 * time.Hour)
	if err := store.ObserveField(ctx, "closed", closedAt, func(o *model.Observation) {
		o.CompletedAt = &compAt
		o.ClosedAt = &closedAt
	}); err != nil {
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

	// Every task whose States + StateTimes yields a non-nil Since must match
	// DrivenTaskStates on both state and since timestamp.
	for id, wantState := range states {
		wantSince := times[id].Since(wantState)
		got, inDriven := driven[id]
		if wantSince != nil {
			if !inDriven {
				t.Errorf("task %s (%s, since=%v) missing from DrivenTaskStates", id, wantState, *wantSince)
				continue
			}
			if got.State != wantState {
				t.Errorf("task %s: DrivenTaskStates.State = %s, want %s", id, got.State, wantState)
			}
			gotSince := got.Times.Since(got.State)
			if gotSince == nil || !gotSince.Equal(*wantSince) {
				t.Errorf("task %s (%s): DrivenTaskStates.Since = %v, want %v", id, wantState, gotSince, wantSince)
			}
		} else if inDriven {
			// A queued task with nil ApprovedAt/RequeuedAt/LastRunFinishedAt
			// is still StateQueued with Since == nil; any other state must
			// never appear in DrivenTaskStates.
			if got.State != wantState || got.Times.Since(got.State) != nil {
				t.Errorf("task %s: unexpected DrivenTaskStates entry %+v for non-driven state %s", id, got, wantState)
			}
		}
	}
	// Verify every non-driven state was excluded.
	for _, excluded := range []string{"proposed", "running-question", "awaiting-submit", "awaiting-ack", "deferred-indef", "failed-deferred", "closed"} {
		if got, ok := driven[excluded]; ok {
			t.Errorf("non-driven task %s appeared in DrivenTaskStates: %+v", excluded, got)
		}
	}
}

// TestReviewQueriesAndReportChecks verifies the bounded review queries,
// OpenTasksWithLink/Tag queries, and RunReportChecksWithTimes.
func TestReviewQueriesAndReportChecks(t *testing.T) {
	t.Parallel()
	store, ctx := open(t)
	VerifyReviewQueriesAndReportChecks(t, ctx, store)
}

// VerifyReviewQueriesAndReportChecks is shared by the SQLite and PostgreSQL test suites.
func VerifyReviewQueriesAndReportChecks(t *testing.T, ctx context.Context, store *model.Store) {
	t.Helper()
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	actor := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	approval := model.Attribution{Actor: actor}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// Empty store: HasReviewActivity is (false, false).
	decl, revs, err := store.HasReviewActivity(ctx)
	if err != nil || decl || revs {
		t.Fatalf("empty HasReviewActivity = (%v, %v, %v), want (false, false, nil)", decl, revs, err)
	}
	checks, err := store.RunReportChecksWithTimes(ctx)
	if err != nil || len(checks) != 0 {
		t.Fatalf("empty RunReportChecksWithTimes = (%v, %v), want empty", checks, err)
	}

	// Seed tasks with open PRs: one needing review, one already reviewed,
	// one with no review template, one in merge-queue repair needing review,
	// and one closed.
	put := func(tk model.Task) {
		t.Helper()
		tk.Intent = model.IntentImplement
		if tk.Title == "" {
			tk.Title = tk.ID
		}
		if tk.Filing.Attribution.Actor.Kind == "" {
			tk.Filing = model.Filing{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect}
		}
		tk.Binding = model.BindingDirective
		tk.Approval = &approval
		tk.ApprovedAt = &at
		tk.CreatedAt = &at
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("PutTask(%s): %v", tk.ID, err)
		}
	}

	put(model.Task{
		ID: "pr-needs-rev", Target: &repo, AutoMerge: true, ReviewTemplateID: "rev-tmpl",
		Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#1"}},
	})
	put(model.Task{
		ID: "pr-already-rev", Target: &repo, AutoMerge: true, ReviewTemplateID: "rev-tmpl",
		Links: []model.Link{
			{Kind: model.LinkFixes, Target: "acme/widgets#2"},
			{Kind: model.LinkReviewTask, Target: "rev-existing"},
		},
	})
	put(model.Task{
		ID: "pr-no-rev", Target: &repo, AutoMerge: true,
		Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#3"}},
	})
	put(model.Task{
		ID: "pr-repair-needs-rev", Target: &repo, AutoMerge: true, ReviewTemplateID: "rev-tmpl",
		Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#4"}},
	})
	put(model.Task{
		ID: "pr-closed", Target: &repo, AutoMerge: true, ReviewTemplateID: "rev-tmpl",
		Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#5"}},
		Tags:  []string{"tag-a"},
	})
	for _, id := range []string{"pr-needs-rev", "pr-already-rev", "pr-no-rev"} {
		if err := store.ObserveField(ctx, id, at, func(o *model.Observation) { o.CompletedAt = &at }); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ObserveField(ctx, "pr-repair-needs-rev", at, func(o *model.Observation) { o.MergeQueueRepairAt = &at }); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, "pr-closed", at, func(o *model.Observation) {
		o.CompletedAt = &at
		o.ClosedAt = &at
	}); err != nil {
		t.Fatal(err)
	}

	decl, revs, err = store.HasReviewActivity(ctx)
	if err != nil || !decl || revs {
		t.Fatalf("after decls HasReviewActivity = (%v, %v, %v), want (true, false, nil)", decl, revs, err)
	}

	allOpen, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		t.Fatalf("OpenPullRequestLinks: %v", err)
	}
	var allOpenIDs []string
	for _, l := range allOpen {
		allOpenIDs = append(allOpenIDs, l.TaskID)
	}
	wantAllOpen := []string{"pr-already-rev", "pr-needs-rev", "pr-no-rev", "pr-repair-needs-rev"}
	if !slices.Equal(allOpenIDs, wantAllOpen) {
		t.Fatalf("OpenPullRequestLinks = %v, want %v", allOpenIDs, wantAllOpen)
	}

	needRev, err := store.OpenPullRequestLinksNeedingReview(ctx)
	if err != nil {
		t.Fatalf("OpenPullRequestLinksNeedingReview: %v", err)
	}
	var needRevIDs []string
	for _, l := range needRev {
		needRevIDs = append(needRevIDs, l.TaskID)
	}
	wantNeedRev := []string{"pr-needs-rev", "pr-repair-needs-rev"}
	if !slices.Equal(needRevIDs, wantNeedRev) {
		t.Fatalf("OpenPullRequestLinksNeedingReview = %v, want %v", needRevIDs, wantNeedRev)
	}

	// Seed review tasks covering finished, obsolete, running, deferred, failed,
	// and path-trigger dependency shapes.
	reviewFiling := model.Filing{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonReview}

	// Finished review without fixes link -> in FinishedReviewTaskIDs.
	put(model.Task{ID: "rev-done-clean", Target: &repo, Filing: reviewFiling})
	if err := store.ObserveField(ctx, "rev-done-clean", at, func(o *model.Observation) { o.CompletedAt = &at }); err != nil {
		t.Fatal(err)
	}
	// Finished review WITH fixes link -> NOT in FinishedReviewTaskIDs.
	put(model.Task{
		ID: "rev-done-pr", Target: &repo, AutoMerge: true, Filing: reviewFiling,
		Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#99"}},
	})
	if err := store.ObserveField(ctx, "rev-done-pr", at, func(o *model.Observation) { o.CompletedAt = &at }); err != nil {
		t.Fatal(err)
	}
	// Completed review with CL link and auto_merge=false (awaiting_submit) -> NOT in FinishedReviewTaskIDs, YES in ObsoleteReviews when target closed.
	put(model.Task{
		ID: "rev-cl-unsubmitted", Target: &repo, AutoMerge: false, Filing: reviewFiling,
		Links: []model.Link{
			{Kind: model.LinkProposedBy, Target: "pr-closed"},
			{Kind: model.LinkChangelist, Target: "cl/123"},
		},
	})
	if err := store.ObserveField(ctx, "rev-cl-unsubmitted", at, func(o *model.Observation) { o.CompletedAt = &at }); err != nil {
		t.Fatal(err)
	}
	// Queued review of closed task -> in ObsoleteReviews.
	put(model.Task{
		ID: "rev-obsolete-queued", Target: &repo, Filing: reviewFiling,
		Links: []model.Link{{Kind: model.LinkProposedBy, Target: "pr-closed"}},
	})
	// Running review of closed task -> NOT in ObsoleteReviews.
	put(model.Task{
		ID: "rev-obsolete-running", Target: &repo, Filing: reviewFiling,
		Links: []model.Link{{Kind: model.LinkProposedBy, Target: "pr-closed"}},
	})
	if err := store.StartRun(ctx, model.Run{
		ID: "rev-obsolete-running-1", TaskID: "rev-obsolete-running", Sandbox: "sb", Attempt: 1, StartedAt: at,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	// Deferred review of closed task -> NOT in ObsoleteReviews.
	put(model.Task{
		ID: "rev-obsolete-deferred", Target: &repo, Filing: reviewFiling,
		Links: []model.Link{{Kind: model.LinkProposedBy, Target: "pr-closed"}},
	})
	if err := store.SetDeferred(ctx, "rev-obsolete-deferred", &at, nil, at); err != nil {
		t.Fatal(err)
	}
	// Failed+deferred review of closed task -> failed outranks deferred -> YES in ObsoleteReviews.
	put(model.Task{
		ID: "rev-obsolete-failed-def", Target: &repo, Filing: reviewFiling,
		Links: []model.Link{{Kind: model.LinkProposedBy, Target: "pr-closed"}},
	})
	for a := 1; a <= model.MaxConsecutiveFailures; a++ {
		end := at.Add(time.Duration(a) * time.Minute)
		rid := "rev-obsolete-failed-def-" + string(rune('0'+a))
		if err := store.StartRun(ctx, model.Run{ID: rid, TaskID: "rev-obsolete-failed-def", Sandbox: "sb", Attempt: a, StartedAt: at}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, rid, end, "failed", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetDeferred(ctx, "rev-obsolete-failed-def", &at, nil, at); err != nil {
		t.Fatal(err)
	}
	// Open path-trigger review with depends-on + ordinary task with depends-on + closed path-trigger review.
	put(model.Task{
		ID: "rev-pt-dep-open", Target: &repo, Filing: reviewFiling,
		Links: []model.Link{{Kind: model.LinkDependsOn, Target: "rev-done-clean"}},
		Tags:  []string{"path-trigger-review:sec"},
	})
	put(model.Task{
		ID: "rev-pt-dep-closed", Target: &repo, Filing: reviewFiling,
		Links: []model.Link{{Kind: model.LinkDependsOn, Target: "rev-done-clean"}},
		Tags:  []string{"path-trigger-review:sec"},
	})
	if err := store.ObserveField(ctx, "rev-pt-dep-closed", at, func(o *model.Observation) { o.ClosedAt = &at }); err != nil {
		t.Fatal(err)
	}
	put(model.Task{
		ID: "ordinary-dep-open", Target: &repo,
		Links: []model.Link{{Kind: model.LinkDependsOn, Target: "rev-done-clean"}},
		Tags:  []string{"tag-a", "prefix-one"},
	})

	decl, revs, err = store.HasReviewActivity(ctx)
	if err != nil || !decl || !revs {
		t.Fatalf("after reviews HasReviewActivity = (%v, %v, %v), want (true, true, nil)", decl, revs, err)
	}

	finished, err := store.FinishedReviewTaskIDs(ctx)
	if err != nil {
		t.Fatalf("FinishedReviewTaskIDs: %v", err)
	}
	if !slices.Equal(finished, []string{"rev-done-clean"}) {
		t.Errorf("FinishedReviewTaskIDs = %v, want [rev-done-clean]", finished)
	}

	obsolete, err := store.ObsoleteReviews(ctx)
	if err != nil {
		t.Fatalf("ObsoleteReviews: %v", err)
	}
	var obsIDs []string
	for _, r := range obsolete {
		obsIDs = append(obsIDs, r.ReviewID)
	}
	wantObs := []string{"rev-cl-unsubmitted", "rev-obsolete-failed-def", "rev-obsolete-queued"}
	if !slices.Equal(obsIDs, wantObs) {
		t.Errorf("ObsoleteReviews = %v, want %v", obsIDs, wantObs)
	}

	ptDeps, err := store.OpenPathTriggerReviewsWithDependencies(ctx)
	if err != nil {
		t.Fatalf("OpenPathTriggerReviewsWithDependencies: %v", err)
	}
	if !slices.Equal(ptDeps, []string{"rev-pt-dep-open"}) {
		t.Errorf("OpenPathTriggerReviewsWithDependencies = %v, want [rev-pt-dep-open]", ptDeps)
	}

	// OpenTasksWithTag / OpenTasksWithTagPrefix / OpenTasksWithLink skip closed tasks.
	if has, err := store.HasOpenTaskWithTag(ctx, "tag-a"); err != nil || !has {
		t.Errorf("HasOpenTaskWithTag(tag-a) = (%v, %v), want (true, nil)", has, err)
	}
	if openTagged, err := store.OpenTasksWithTag(ctx, "tag-a"); err != nil || !slices.Equal(openTagged, []string{"ordinary-dep-open"}) {
		t.Errorf("OpenTasksWithTag(tag-a) = (%v, %v), want [ordinary-dep-open]", openTagged, err)
	}
	if openPrefix, err := store.OpenTasksWithTagPrefix(ctx, "prefix-"); err != nil || !slices.Equal(openPrefix, []string{"ordinary-dep-open"}) {
		t.Errorf("OpenTasksWithTagPrefix(prefix-) = (%v, %v), want [ordinary-dep-open]", openPrefix, err)
	}
	if openLinked, err := store.OpenTasksWithLink(ctx, model.LinkDependsOn); err != nil || !slices.Equal(openLinked, []string{"ordinary-dep-open", "rev-pt-dep-open"}) {
		t.Errorf("OpenTasksWithLink(depends-on) = (%v, %v), want [ordinary-dep-open rev-pt-dep-open]", openLinked, err)
	}

	// RunReportChecksWithTimes: finished run, live run (FinishedAt nil), and orphan run_id.
	if err := store.RecordRunReportCheck(ctx, model.RunReportCheck{RunID: "rev-obsolete-running-1", Claims: 4, Uncited: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRunReportCheck(ctx, model.RunReportCheck{RunID: "rev-obsolete-failed-def-1", Claims: 6, Mismatched: 2}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRunReportCheck(ctx, model.RunReportCheck{RunID: "orphan-run", Claims: 2, Unread: 1}); err != nil {
		t.Fatal(err)
	}
	gotChecks, err := store.RunReportChecksWithTimes(ctx)
	if err != nil {
		t.Fatalf("RunReportChecksWithTimes: %v", err)
	}
	if len(gotChecks) != 3 {
		t.Fatalf("RunReportChecksWithTimes len = %d, want 3", len(gotChecks))
	}
	byID := map[string]model.RunReportCheckTiming{}
	for _, c := range gotChecks {
		byID[c.RunID] = c
	}
	if c := byID["orphan-run"]; c.RunFound || c.Claims != 2 || c.Unread != 1 {
		t.Errorf("orphan-run check = %+v, want RunFound=false Claims=2 Unread=1", c)
	}
	if c := byID["rev-obsolete-running-1"]; !c.RunFound || c.FinishedAt != nil || !c.StartedAt.Equal(at) {
		t.Errorf("running check = %+v, want RunFound=true FinishedAt=nil StartedAt=%v", c, at)
	}
	wantFin := at.Add(time.Minute)
	if c := byID["rev-obsolete-failed-def-1"]; !c.RunFound || c.FinishedAt == nil || !c.FinishedAt.Equal(wantFin) {
		t.Errorf("finished check = %+v, want RunFound=true FinishedAt=%v", c, wantFin)
	}
}
