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

// The pipeline steward (model.PipelineStewardTemplateID): that every
// deployment gets its six-hourly safety wake, that a quiet queue costs
// it no run at all, that each kind of trouble the gate checks for wakes
// it, that what it is granted is exactly the api presets it is
// documented to hold and nothing else a person alone may grant, and that
// it is a durable judgement task: a goal, pipeline.* subscriptions, the
// judgement model (docs/design/durable-tasks.md#DT-6, DT-11).

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestSystemSchedulesSeedsThePipelineStewardEverySixHours(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	now := time.Date(2026, 3, 1, 16, 7, 0, 0, time.UTC)
	if err := reconcileSystemSchedules(ctx, systemScheduleDeps(store), now); err != nil {
		t.Fatalf("reconcileSystemSchedules: %v", err)
	}
	sched, err := store.GetSchedule(ctx, model.PipelineStewardTemplateID)
	if err != nil || sched == nil {
		t.Fatalf("a fresh deployment has no pipeline steward schedule (%v)", err)
	}
	if sched.TemplateID == nil || *sched.TemplateID != model.PipelineStewardTemplateID {
		t.Errorf("the seeded schedule fires %v, want the pipeline steward", sched.TemplateID)
	}
	want := model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 6 * 60}
	if sched.Recurrence != want {
		t.Errorf("cadence = %+v, want %+v", sched.Recurrence, want)
	}
	if err := sched.Recurrence.Validate(); err != nil {
		t.Errorf("the seeded cadence does not validate: %v", err)
	}
	if sched.Overlap.Allows() {
		t.Error("the pipeline steward's firings may overlap; two stewards acting on one queue at once is the thing to avoid")
	}
	if !sched.Enabled {
		t.Error("the pipeline steward is seeded switched off")
	}
	if want := now.Add(6 * time.Hour); !sched.NextRunAt.Equal(want) {
		t.Errorf("first firing %s, want %s", sched.NextRunAt, want)
	}
}

// The grants resolve to the presets task 441 shipped, to priority-dispatch
// and durable, and to nothing else a person alone may grant. And it is a
// durable judgement task: pipeline-healthy goal (rest on met, notify at
// the limit), pipeline.* subscriptions, and the judgement model.
func TestThePipelineStewardHoldsTheDocumentedPresetsAndNothingDangerous(t *testing.T) {
	t.Parallel()
	tmpl := model.SystemTemplate(model.PipelineStewardTemplateID, stateRepo)
	if tmpl == nil {
		t.Fatal("this build ships no pipeline steward template")
	}
	if !model.IsStandingTemplateID(tmpl.ID) {
		t.Error("the pipeline steward is not a standing template; every firing would file a new task with no handover")
	}
	if tmpl.NothingNeededAction != model.NothingNeededAck {
		t.Errorf("NothingNeededAction = %q, want ack: a closed standing task loses its handover", tmpl.NothingNeededAction)
	}
	var names []string
	for _, g := range tmpl.Grants {
		names = append(names, g.Capability)
	}
	for _, want := range []string{"api:task-admin", "api:dispatch-control", "api:ci-read", "self-debug",
		model.PriorityDispatchCapability, model.DurableCapability} {
		if !slices.Contains(names, want) {
			t.Errorf("grants = %v, want %s", names, want)
		}
	}
	for _, name := range names {
		// priority-dispatch and durable are a person's to grant to any
		// other task; the steward's template carries them by construction.
		if model.IsPersonOnlyGrant(name) && name != model.PriorityDispatchCapability && name != model.DurableCapability {
			t.Errorf("grant %s is one only a person may give", name)
		}
		g, isAPI, err := model.ParseAPIGrant(name)
		if !isAPI {
			continue
		}
		if err != nil {
			t.Errorf("grant %s does not resolve: %v", name, err)
			continue
		}
		if g.Preset == "" {
			t.Errorf("grant %s is a bare scope, want one of the presets", name)
		}
		if g.Dangerous() {
			t.Errorf("grant %s carries a dangerous scope", name)
		}
	}
	set := model.APIGrantsOf(names)
	for _, scope := range []model.APIScope{"tasks:read", "tasks:write", "merge:write", "ci:write", "ci:read", "dispatch:write", "deployment:read"} {
		if !set.Has(scope) {
			t.Errorf("the pipeline steward cannot %s", scope)
		}
	}
	for _, scope := range []model.APIScope{"releases:write", "merge:force", "capabilities:admin", "settings:write",
		"secrets:write", "deployment:upgrade", "deployment:restart", "deployment:reboot"} {
		if set.Has(scope) {
			t.Errorf("the pipeline steward holds %s, which it must not by default", scope)
		}
	}
	if g := tmpl.Goal; g == nil || g.Kind != model.GoalPipelineHealthy || g.OnMet != model.GoalRestOnMet ||
		g.OnLimit != model.GoalNotifyOnLimit {
		t.Errorf("goal = %+v, want pipeline-healthy, on_met rest, on_limit notify", tmpl.Goal)
	}
	if !model.IsJudgementModel(tmpl.Model) {
		t.Errorf("model = %q, want the symbolic %q", tmpl.Model, model.JudgementModelName)
	}
	if subs := model.StandingTemplateSubscriptions(tmpl.ID); !slices.Equal(subs, []string{"pipeline.*"}) {
		t.Errorf("subscriptions = %v, want [pipeline.*]", subs)
	}
	// The prompt holds the four rules (DT-12) and files no repairs.
	for _, want := range []string{"grain read deploy://pipeline-changes --cursor <cursor>", "grain task read <id>", "record-disagrees",
		"at most 15", "grain task message", "grain notify", "Never file a repair"} {
		if !strings.Contains(tmpl.Body, want) {
			t.Errorf("the prompt does not say %q", want)
		}
	}
	for _, gone := range []string{"api_message_task_agent", "api_post_task_message", "If no repair for it is open"} {
		if strings.Contains(tmpl.Body, gone) {
			t.Errorf("the prompt still says %q", gone)
		}
	}
}

// firePipelineStewardAt seeds the schedule and runs the schedule
// reconciler at its first firing, returning the schedule afterwards and
// the task it filed, if any.
func firePipelineStewardAt(t *testing.T, ctx context.Context, store *model.Store, seeded time.Time) (model.Schedule, *model.Task) {
	t.Helper()
	deps := systemScheduleDeps(store)
	if err := reconcileSystemSchedules(ctx, deps, seeded); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	sched, err := store.GetSchedule(ctx, model.PipelineStewardTemplateID)
	if err != nil || sched == nil {
		t.Fatalf("GetSchedule: %v %v", sched, err)
	}
	if err := reconcileSchedule(ctx, deps, sched.NextRunAt); err != nil {
		t.Fatalf("reconcileSchedule: %v", err)
	}
	after, _ := store.GetSchedule(ctx, model.PipelineStewardTemplateID)
	task, err := store.OpenTaskWithTag(ctx, model.ScheduleFiringTag(model.PipelineStewardTemplateID))
	if err != nil {
		t.Fatal(err)
	}
	return *after, task
}

var pipelineStewardSeeded = time.Date(2026, 3, 1, 16, 0, 0, 0, time.UTC)

func TestAQuietQueueCostsThePipelineStewardNoRun(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main", RunID: "r1",
		Commit: "abc", StartedAt: pipelineStewardSeeded, Conclusion: "success"}); err != nil {
		t.Fatal(err)
	}
	due := pipelineStewardSeeded.Add(model.PipelineStewardEveryNMinutes * time.Minute)
	sched, task := firePipelineStewardAt(t, ctx, store, pipelineStewardSeeded)
	if task != nil {
		t.Fatalf("a quiet queue filed pipeline steward task %s", task.ID)
	}
	if !sched.NextRunAt.After(due) {
		t.Errorf("next run %s, want the cadence moved past the quiet occurrence at %s", sched.NextRunAt, due)
	}
	if sched.LastRunAt != nil {
		t.Errorf("last run %s, want none: nothing ran", sched.LastRunAt)
	}
	skips, err := store.ScheduleSkips(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(skips) != 1 || skips[0].Reason != model.SkippedQuiet || skips[0].Outcome != model.ScheduleSkipDropped {
		t.Errorf("skips = %+v, want the one quiet occurrence recorded as dropped", skips)
	}
}

// Each thing the gate looks for, on its own, is enough to wake it.
func TestEachKindOfTroubleWakesThePipelineSteward(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, ctx context.Context, store *model.Store)
	}{
		{"a red base branch", func(t *testing.T, ctx context.Context, store *model.Store) {
			if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main", RunID: "r1",
				Commit: "abc", StartedAt: pipelineStewardSeeded, Conclusion: model.BaseBranchCIFailure}); err != nil {
				t.Fatal(err)
			}
		}},
		{"an errored base branch run", func(t *testing.T, ctx context.Context, store *model.Store) {
			if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main", RunID: "r1",
				Commit: "abc", StartedAt: pipelineStewardSeeded, Conclusion: "errored"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"a stalled task", func(t *testing.T, ctx context.Context, store *model.Store) {
			long := pipelineStewardSeeded.Add(-30 * 24 * time.Hour)
			approval := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "op"}}
			if err := store.PutTask(ctx, model.Task{ID: "stuck", Title: "Stuck", Intent: model.IntentImplement,
				CreatedAt: &long, Approval: &approval, ApprovedAt: &long}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, ctx := recreateStore(t)
			tc.setup(t, ctx, store)
			_, task := firePipelineStewardAt(t, ctx, store, pipelineStewardSeeded)
			if task == nil {
				t.Fatalf("with %s the pipeline steward was not woken", tc.name)
			}
		})
	}
}

// A confirmed red wakes the standing task straight away, with the red
// named in the note it wakes to.
func TestWakePipelineStewardWakesTheIdleStandingTask(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	deps := systemScheduleDeps(store)
	now := pipelineStewardSeeded
	if err := reconcileSystemSchedules(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	wakePipelineSteward(ctx, deps, "acme/widgets main is confirmed red at abc1234", now.Add(time.Minute))
	task, err := store.OpenTaskWithTag(ctx, model.ScheduleFiringTag(model.PipelineStewardTemplateID))
	if err != nil || task == nil {
		t.Fatalf("no pipeline steward task after a wake (%v)", err)
	}
	done := now.Add(10 * time.Minute)
	if err := store.ObserveField(ctx, task.ID, done, func(o *model.Observation) { o.CompletedAt = &done }); err != nil {
		t.Fatal(err)
	}
	wakePipelineSteward(ctx, deps, "acme/widgets main is confirmed red at def5678", done.Add(time.Minute))
	again, _ := store.OpenTaskWithTag(ctx, model.ScheduleFiringTag(model.PipelineStewardTemplateID))
	if again == nil || again.ID != task.ID {
		t.Fatalf("the second wake filed %v, want the one standing task %s woken", again, task.ID)
	}
	if st, _ := store.State(ctx, task.ID); st != model.StateQueued {
		t.Errorf("the woken task reads %q, want queued", st)
	}
	comments, _ := store.Comments(ctx, task.ID)
	if len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, "def5678") {
		t.Errorf("the wake note does not name the red: %+v", comments)
	}
}

// A deployment that switched the job off is not woken behind its back.
func TestWakePipelineStewardLeavesADisabledScheduleAlone(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	deps := systemScheduleDeps(store)
	if err := reconcileSystemSchedules(ctx, deps, pipelineStewardSeeded); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSchedule(ctx, model.PipelineStewardTemplateID, func(sc *model.Schedule) error {
		sc.Enabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wakePipelineSteward(ctx, deps, "red", pipelineStewardSeeded.Add(time.Minute))
	if task, _ := store.OpenTaskWithTag(ctx, model.ScheduleFiringTag(model.PipelineStewardTemplateID)); task != nil {
		t.Errorf("a disabled pipeline steward was woken as task %s", task.ID)
	}
}
