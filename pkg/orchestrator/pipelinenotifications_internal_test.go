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
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The existing escalations, raised and cleared as operator notifications
// (design/design/durable-tasks.design.md, DT-5's "Existing escalations, mapped").

func openNotification(t *testing.T, ctx context.Context, store *model.Store, subject string) *model.Notification {
	t.Helper()
	n, err := store.OpenNotification(ctx, subject)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A confirmed red raises pipeline.base-red, naming the repair; a second
// confirmation bumps the same one; the green verdict that closes the
// repair clears it.
func TestAConfirmedRedBaseRaisesBaseRedAndTheGreenVerdictClearsIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	fileTaggedTask(t, ctx, store, "repair-1", repo, BaseCIRepairBranchTag(repo, "main"))
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	row := model.BaseBranchCI{Repo: repo, Branch: "main", Commit: "c0ffee1234567890", RunID: "run-1",
		Conclusion: model.BaseBranchCIFailure, Detail: "go failed"}
	deps := Deps{Store: store}
	if err := confirmBaseBranchCI(ctx, deps, row, "re-run failed too", now); err != nil {
		t.Fatal(err)
	}
	subject := "pipeline.base-red:acme/widgets@main"
	n := openNotification(t, ctx, store, subject)
	if n == nil {
		t.Fatalf("no open %s after a confirmed red", subject)
	}
	if n.Severity != model.NotificationWarning || n.TaskID != "repair-1" ||
		!slices.Contains(n.Actions, "open-repair") || !strings.Contains(n.Body, "/tasks/repair-1") {
		t.Errorf("base-red = %+v, want a warning naming repair-1 with open-repair", n)
	}
	if n.ClearsWhen == nil || n.ClearsWhen.Kind != model.ClearsOnBaseGreen {
		t.Errorf("clears_when = %+v, want base-green", n.ClearsWhen)
	}

	row.Commit, row.RunID = "deadbeef12345678", "run-2"
	if err := confirmBaseBranchCI(ctx, deps, row, "streak", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if again := openNotification(t, ctx, store, subject); again == nil || again.ID != n.ID || again.RaiseCount != 2 {
		t.Fatalf("second confirmation = %+v, want the same notification raised twice", again)
	}

	// Still red: nothing clears.
	if err := closeBaseRepairsOnGreen(ctx, deps, row, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if openNotification(t, ctx, store, subject) == nil {
		t.Fatal("a red row cleared base-red")
	}
	green := model.BaseBranchCI{Repo: repo, Branch: "main", Commit: "feedface12345678", RunID: "run-3",
		Conclusion: "success"}
	if err := closeBaseRepairsOnGreen(ctx, deps, green, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if openNotification(t, ctx, store, subject) != nil {
		t.Fatal("base-red still open after the green verdict")
	}
	got, _ := store.GetNotification(ctx, n.ID)
	if got.ClearedBy != baseCINotifier {
		t.Errorf("cleared by %q, want %q", got.ClearedBy, baseCINotifier)
	}
}

// A repair that has stood open past the progress limit with its branch
// still confirmed red raises pipeline.repair-failing, once; the branch
// going green clears it even though a person's marked repair stays open.
func TestARepairThatKeepsFailingRaisesRepairFailing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	filed := now.Add(-time.Hour)
	fileMarkedTask(t, ctx, store, "repair-1", repo)
	if err := store.UpdateTask(ctx, "repair-1", func(task *model.Task) error {
		task.CreatedAt = &filed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	row := model.BaseBranchCI{Repo: repo, Branch: "main", Commit: "c0ffee1234567890", RunID: "run-1",
		Conclusion: model.BaseBranchCIFailure, Confirmation: model.BaseCIConfirmed, Detail: "go failed"}
	deps := Deps{Store: store}
	subject := "pipeline.repair-failing:repair-1"

	if err := reconcileRepairFailing(ctx, deps, row, now); err != nil {
		t.Fatal(err)
	}
	if openNotification(t, ctx, store, subject) != nil {
		t.Fatal("repair-failing raised for a repair an hour old with no runs")
	}
	later := filed.Add(repairFailingFor + time.Minute)
	for range 3 {
		if err := reconcileRepairFailing(ctx, deps, row, later); err != nil {
			t.Fatal(err)
		}
	}
	n := openNotification(t, ctx, store, subject)
	if n == nil {
		t.Fatalf("no %s once the repair outlived its limit", subject)
	}
	if n.RaiseCount != 1 || n.TaskID != "repair-1" || n.ClearsWhen == nil || n.ClearsWhen.Task != "repair-1" {
		t.Errorf("repair-failing = %+v, want one raise about repair-1, clearing when it closes", n)
	}

	green := model.BaseBranchCI{Repo: repo, Branch: "main", Commit: "feedface12345678", RunID: "run-2",
		Conclusion: "success"}
	if err := closeBaseRepairsOnGreen(ctx, deps, green, later); err != nil {
		t.Fatal(err)
	}
	// A marked repair is a base-green goal like grain's own (DT-4): the
	// green verdict closes it, and its notification clears.
	if st, _ := store.State(ctx, "repair-1"); st != model.StateClosed {
		t.Fatalf("a person's marked repair is %s once its branch went green, want closed", st)
	}
	if openNotification(t, ctx, store, subject) != nil {
		t.Fatal("repair-failing still open after its branch went green")
	}

	// grain's own repair is closed by the green verdict, and its
	// notification clears on that condition.
	fileTaggedTask(t, ctx, store, "repair-2", repo, BaseCIRepairBranchTag(repo, "main"))
	for i := range repairFailingRuns {
		id := fmt.Sprintf("repair-2-%d", i+1)
		if err := store.StartRun(ctx, model.Run{ID: id, TaskID: "repair-2", Attempt: i + 1, StartedAt: later},
			model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, id, later, "failed", "still red"); err != nil {
			t.Fatal(err)
		}
	}
	if err := reconcileRepairFailing(ctx, deps, row, later); err != nil {
		t.Fatal(err)
	}
	if openNotification(t, ctx, store, "pipeline.repair-failing:repair-2") == nil {
		t.Fatalf("no repair-failing after %d runs", repairFailingRuns)
	}
	if err := closeBaseRepairsOnGreen(ctx, deps, green, later); err != nil {
		t.Fatal(err)
	}
	if err := reconcileNotifications(ctx, deps, later); err != nil {
		t.Fatal(err)
	}
	if openNotification(t, ctx, store, "pipeline.repair-failing:repair-2") != nil {
		t.Fatal("repair-failing still open after its repair closed")
	}
}

// pipeline.not-publishing is the health page's own finding: raised as a
// warning, risen to critical after a day, and cleared once the branch
// publishes again.
func TestNotPublishingIsRaisedFromTheHealthFindingAndClearsWhenAReleasePublishes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	since := now.Add(-2 * time.Hour)
	failing := model.BaseBranchRelease{Repo: repo, Branch: "main", Commit: "abc", RunID: "rel-3",
		StartedAt: now.Add(-10 * time.Minute), Conclusion: "failure", Failures: 3, FailingSince: &since}
	if err := store.PutBaseBranchRelease(ctx, failing); err != nil {
		t.Fatal(err)
	}
	deps := Deps{Store: store}
	subject := "pipeline.not-publishing:acme/widgets@main"

	if err := reconcileNotifications(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	n := openNotification(t, ctx, store, subject)
	if n == nil {
		t.Fatalf("no %s for three failed releases", subject)
	}
	findings, err := store.BaseBranchPublishingFindings(ctx, now)
	if err != nil || len(findings) != 1 {
		t.Fatalf("findings = %v, %v; want the one the health page shows", findings, err)
	}
	if n.Severity != model.NotificationWarning || n.Body != findings[0].Detail {
		t.Errorf("not-publishing = %+v, want a warning carrying the health finding's detail", n)
	}

	// Every cycle that finds it again leaves it as it is.
	if err := reconcileNotifications(ctx, deps, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if again := openNotification(t, ctx, store, subject); again.RaiseCount != 1 {
		t.Errorf("raise count = %d after a second cycle, want 1", again.RaiseCount)
	}
	// A day without publishing is critical.
	if err := reconcileNotifications(ctx, deps, since.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if again := openNotification(t, ctx, store, subject); again.Severity != model.NotificationCritical {
		t.Errorf("severity = %s after a day, want critical", again.Severity)
	}

	published := failing
	published.RunID, published.Conclusion, published.Failures, published.FailingSince = "rel-4", "success", 0, nil
	if err := store.PutBaseBranchRelease(ctx, published); err != nil {
		t.Fatal(err)
	}
	if err := reconcileNotifications(ctx, deps, since.Add(26*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if openNotification(t, ctx, store, subject) != nil {
		t.Fatal("not-publishing still open after a release published")
	}
}

// request.force-merge and request.revert get their clear conditions from
// the subject alone, and reconcileNotifications clears them on those.
func TestRequestSubjectsClearOnTheirConditions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	fileTaggedTask(t, ctx, store, "12", repo)
	fileTaggedTask(t, ctx, store, "13", repo)
	steward := "agent:steward"
	force, err := store.RaiseNotification(ctx, model.Notification{Subject: "request.force-merge:12",
		Title: "Force-merge 12?", RaisedBy: steward, RaisedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if c := force.Notification.ClearsWhen; force.Notification.TaskID != "12" || c == nil ||
		c.Kind != model.ClearsOnTask || c.Task != "12" || c.Until != "closed" {
		t.Errorf("force-merge = %+v (clears %+v), want task 12, clearing when it closes",
			force.Notification, c)
	}
	revert, err := store.RaiseNotification(ctx, model.Notification{Subject: "request.revert:abc1234",
		Title: "Revert abc1234?", TaskID: "13", RaisedBy: steward, RaisedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if c := revert.Notification.ClearsWhen; c == nil || c.Task != "13" || c.Until != "merged" {
		t.Errorf("revert clears %+v, want when task 13 merges", c)
	}
	bare, err := store.RaiseNotification(ctx, model.Notification{Subject: "request.revert:9",
		Title: "Revert 9?", RaisedBy: steward, RaisedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if bare.Notification.ClearsWhen != nil {
		t.Errorf("a revert request with no revert task clears %+v, want a person to dismiss it",
			bare.Notification.ClearsWhen)
	}

	if err := closeSupersededTasks(ctx, store, []string{"12"}, baseCIRepairer, "base green", "done", now); err != nil {
		t.Fatal(err)
	}
	if events, err := store.TaskEvents(ctx, "12"); err != nil || len(events) != 1 ||
		events[0].By != baseCIRepairer || events[0].Reason != "base green" {
		t.Errorf("task 12's events = %+v (%v), want its close recorded as the merge queue's", events, err)
	}
	if err := reconcileNotifications(ctx, Deps{Store: store}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if openNotification(t, ctx, store, "request.force-merge:12") != nil {
		t.Error("force-merge request still open after its task closed")
	}
	if openNotification(t, ctx, store, "request.revert:abc1234") == nil {
		t.Error("revert request cleared before its revert merged")
	}
	if openNotification(t, ctx, store, "request.revert:9") == nil {
		t.Error("a revert request with no condition was cleared")
	}
}
