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

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

func tasksWithTag(t *testing.T, ctx context.Context, store *model.Store, tag string) []model.Task {
	t.Helper()
	all, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Task
	for _, tk := range all {
		for _, got := range tk.Tags {
			if got == tag {
				out = append(out, tk)
				break
			}
		}
	}
	return out
}

func seedPublishingHold(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef, now time.Time) string {
	t.Helper()
	since := now.Add(-6 * time.Hour)
	finished := now.Add(-time.Hour)
	if err := store.PutBaseBranchRelease(ctx, model.BaseBranchRelease{
		Repo: repo, Branch: "main", Commit: loopTipSHA, RunID: "rel-99",
		StartedAt: now.Add(-2 * time.Hour), FinishedAt: &finished, Conclusion: "failure",
		Failures: 4, FailingSince: &since,
	}); err != nil {
		t.Fatal(err)
	}
	triageID := "4256"
	if err := store.PutTask(ctx, model.Task{
		ID: triageID, Title: "Triage main's failing releases", Target: &repo, Base: "main",
		Tags: []string{model.BaseReleaseTriageBranchTag(repo, "main")},
	}); err != nil {
		t.Fatal(err)
	}
	return triageID
}

// A schedule declaring `skip-when: publishing-held` still fires while main's
// publishing is held whenever a release candidate is currently in
// qualification (so the train can judge, promote or drop that rc while
// skipping cut), and returns to holding once the rc finishes qualification.
// While held, the schedule is never escalated as starved.
func TestPublishingHeldScheduleFiresWhileRCQualifiesAndHoldsWhenIdle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	triageID := seedPublishingHold(t, ctx, store, repo, now)

	sched := model.Schedule{
		ID: "release-train", Title: "Release train", Body: "Run one tick.",
		Target: repo, Base: "main",
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 20},
		Overlap:    model.OverlapSkip, OverlapGroup: "release-train",
		SkipWhen: model.SkipWhenPublishingHeld,
		Enabled:  true, NextRunAt: now, CreatedAt: now.Add(-48 * time.Hour),
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}

	forge := newFakeReleaseLoopForge()
	forge.setTags(
		github.Tag{Name: "v3.7.0", SHA: loopRelSHA},
		github.Tag{Name: "v3.8.0-rc.1", SHA: loopRC1SHA},
	)
	deps := Deps{Store: store, Client: forge}

	// 1. RC in qualification + publishing held -> fires a tick to judge/release/drop it.
	if err := reconcileSchedule(ctx, deps, now); err != nil {
		t.Fatalf("reconcileSchedule (rc qualifying): %v", err)
	}
	tasks := tasksWithTag(t, ctx, store, model.ScheduleFiringTag(sched.ID))
	if len(tasks) != 1 {
		t.Fatalf("tasks after qualifying tick = %+v; want 1 tick filed while v3.8.0-rc.1 qualifies", tasks)
	}
	tick1 := tasks[0].ID
	closedAt := now.Add(5 * time.Minute)
	if err := store.ObserveField(ctx, tick1, closedAt, func(o *model.Observation) { o.ClosedAt = &closedAt }); err != nil {
		t.Fatal(err)
	}

	// 2. RC promoted (v3.8.0 tagged) -> no RC in qualification -> schedule is held,
	//    no second tick is filed, and after 2 hours no pipeline.schedule-starved
	//    notification is raised and triage task priority is untouched.
	forge.setTags(
		github.Tag{Name: "v3.7.0", SHA: loopRelSHA},
		github.Tag{Name: "v3.8.0-rc.1", SHA: loopRC1SHA},
		github.Tag{Name: "v3.8.0", SHA: loopRC1SHA},
	)
	if err := reconcileSchedule(ctx, deps, now.Add(20*time.Minute)); err != nil {
		t.Fatalf("reconcileSchedule (after promotion): %v", err)
	}
	if err := reconcileSchedule(ctx, deps, now.Add(140*time.Minute)); err != nil {
		t.Fatalf("reconcileSchedule (2h into hold): %v", err)
	}
	tasks = tasksWithTag(t, ctx, store, model.ScheduleFiringTag(sched.ID))
	if len(tasks) != 1 {
		t.Fatalf("tasks after promotion under hold = %d; want still 1 (held)", len(tasks))
	}
	skips, err := store.ScheduleSkips(ctx, sched.ID)
	if err != nil || len(skips) != 1 || skips[0].Reason != model.SuppressedByPublishingHeld || skips[0].TaskID != triageID {
		t.Fatalf("skips = %+v, %v; want one SuppressedByPublishingHeld skip naming triage task %s", skips, err, triageID)
	}
	if n, err := store.OpenNotification(ctx, model.NotificationSubject(model.FamilyScheduleStarved, sched.ID)); err != nil || n != nil {
		t.Fatalf("pipeline.schedule-starved = %+v, %v; want none while deliberately held", n, err)
	}
	stale, err := store.StaleSchedules(ctx, now.Add(140*time.Minute))
	if err != nil || len(stale) != 1 || !stale[0].Held || stale[0].HeldByTaskID != triageID {
		t.Fatalf("StaleSchedules = %+v, %v; want held schedule naming triage task %s", stale, err, triageID)
	}

	// 3. Triage task closes -> held occurrence fires once.
	triageClosed := now.Add(145 * time.Minute)
	if err := store.ObserveField(ctx, triageID, triageClosed, func(o *model.Observation) { o.ClosedAt = &triageClosed }); err != nil {
		t.Fatal(err)
	}
	if err := reconcileSchedule(ctx, deps, now.Add(146*time.Minute)); err != nil {
		t.Fatalf("reconcileSchedule (after triage closed): %v", err)
	}
	tasks = tasksWithTag(t, ctx, store, model.ScheduleFiringTag(sched.ID))
	if len(tasks) != 2 {
		t.Fatalf("tasks after hold cleared = %d; want 2 (held occurrence fired once)", len(tasks))
	}
}

func TestWorkflowRunPublishingHeldFiresWhenRCQualifiesAndDropsWhenIdle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	now := time.Date(2026, 11, 15, 11, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	triageID := seedPublishingHold(t, ctx, store, repo, now)

	sched := model.Schedule{
		ID: "stage1-train", Title: "Stage 1 train", Body: "Run one tick.",
		Target: repo, Base: "main",
		Recurrence: model.Recurrence{
			Kind: model.RecurrenceGitHubWorkflowRun,
			GitHubWorkflowRun: &model.GitHubWorkflowRunTrigger{
				Workflow:    "nightly.yml",
				Branches:    []string{"v*-rc.*"},
				Conclusions: []string{"success", "failure", "cancelled", "timed_out"},
			},
		},
		Overlap: model.OverlapSkip, OverlapGroup: "release-train",
		SkipWhen: model.SkipWhenPublishingHeld,
		Enabled:  true, CreatedAt: now.Add(-24 * time.Hour),
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}

	forge := newFakeReleaseLoopForge()
	deps := Deps{Store: store, Client: forge}
	started := now.Add(-time.Hour)
	run := ConcludedGitHubWorkflowRun{
		Repo: repo, RunID: 7001, Attempt: 1,
		WorkflowName: "nightly", WorkflowPath: ".github/workflows/nightly.yml",
		Ref: "v3.8.0-rc.1", HeadSHA: loopRC1SHA, HeadRepo: repo.String(),
		Event: "push", Conclusion: "success",
		StartedAt: &started, CompletedAt: &now,
	}

	// No RC in qualification -> dropped with SuppressedByPublishingHeld.
	forge.setTags(
		github.Tag{Name: "v3.8.0-rc.1", SHA: loopRC1SHA},
		github.Tag{Name: "v3.8.0-rc.1-discarded", SHA: loopRC1SHA},
	)
	if err := fireOneGitHubWorkflowRunSchedule(ctx, deps, sched, run, nil, now); err != nil {
		t.Fatalf("fireOneGitHubWorkflowRunSchedule (idle): %v", err)
	}
	skips, err := store.ScheduleSkips(ctx, sched.ID)
	if err != nil || len(skips) != 1 || skips[0].Reason != model.SuppressedByPublishingHeld || skips[0].TaskID != triageID {
		t.Fatalf("skips (idle) = %+v, %v; want dropped publishingHeld skip naming %s", skips, err, triageID)
	}

	// RC in qualification -> fires instead of dropping.
	forge.setTags(
		github.Tag{Name: "v3.8.0-rc.1", SHA: loopRC1SHA},
		github.Tag{Name: "v3.8.0-rc.1-discarded", SHA: loopRC1SHA},
		github.Tag{Name: "v3.8.0-rc.2", SHA: loopFeatSHA},
	)
	run2 := run
	run2.RunID = 7002
	run2.Ref = "v3.8.0-rc.2"
	run2.HeadSHA = loopFeatSHA
	if err := fireOneGitHubWorkflowRunSchedule(ctx, deps, sched, run2, nil, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("fireOneGitHubWorkflowRunSchedule (qualifying): %v", err)
	}
	tasks := tasksWithTag(t, ctx, store, model.ScheduleFiringTag(sched.ID))
	if len(tasks) != 1 {
		t.Fatalf("tasks (qualifying) = %+v; want 1 tick filed while v3.8.0-rc.2 qualifies", tasks)
	}
}

func TestReleaseLoopIdleRespectsPublishingHoldAndDescribesTriageTask(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	optInReleaseLoop(t, ctx, store, repo, now)
	triageID := seedPublishingHold(t, ctx, store, repo, now)

	forge := newFakeReleaseLoopForge()
	forge.setTags(github.Tag{Name: "v3.7.0", SHA: loopRelSHA})
	forge.setCommits(loopRelSHA, "main", github.Commit{SHA: loopFeatSHA, Message: "feat: ship something"})
	forge.setStage0(loopFeatSHA, "success", "success")

	// checkReleaseLoopIdle treats an active publishing hold as deliberate (not idle).
	chk, err := checkReleaseLoopIdle(ctx, store, forge, forge, repo, "main", now)
	if err != nil {
		t.Fatalf("checkReleaseLoopIdle: %v", err)
	}
	if chk.owed {
		t.Fatalf("checkReleaseLoopIdle.owed = true while publishing is held by task %s, want false", triageID)
	}

	// describeReleaseTrainTick explicitly names the holding triage task when
	// the schedule has an active SuppressedByPublishingHeld skip.
	sched, err := store.GetSchedule(ctx, "sched-train-periodic")
	if err != nil || sched == nil {
		t.Fatal(err)
	}
	if _, err := store.RecordScheduleSkip(ctx, model.ScheduleSkip{
		ScheduleID: sched.ID, Occurrence: now, At: now,
		Reason: model.SuppressedByPublishingHeld, TaskID: triageID,
		Detail: "publishing held by task " + triageID + " (acme/widgets main)",
	}); err != nil {
		t.Fatal(err)
	}
	note, taskID, err := describeReleaseTrainTick(ctx, store, []model.Schedule{*sched}, repo, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("describeReleaseTrainTick: %v", err)
	}
	if taskID != triageID || !strings.Contains(note, "deliberately held") || !strings.Contains(note, triageID) {
		t.Errorf("describeReleaseTrainTick = (%q, %q), want deliberately held naming task %s", note, taskID, triageID)
	}
}
