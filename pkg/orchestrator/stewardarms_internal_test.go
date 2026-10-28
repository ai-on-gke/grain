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

// The pipeline steward's mechanical duties as reconcilers
// (docs/design/durable-tasks.md#DT-6): stalled tasks and stale ports
// raise and clear their notifications, a person's time-bound hold keeps
// a cancelled run cancelled, and an unresolvable judgement model is said.

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

func putOldTask(t *testing.T, ctx context.Context, store *model.Store, id string, links []model.Link) time.Time {
	t.Helper()
	long := pipelineStewardSeeded.Add(-30 * 24 * time.Hour)
	approval := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "op"}}
	if err := store.PutTask(ctx, model.Task{ID: id, Title: id, Intent: model.IntentImplement,
		CreatedAt: &long, Approval: &approval, ApprovedAt: &long, Links: links}); err != nil {
		t.Fatal(err)
	}
	return long
}

func TestAStalledTaskRaisesPipelineTaskStalledUntilItMoves(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	deps := systemScheduleDeps(store)
	now := pipelineStewardSeeded
	putOldTask(t, ctx, store, "stuck", nil)

	if err := reconcileStallNotifications(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	n := openNotification(t, ctx, store, model.NotificationSubject(FamilyTaskStalled, "stuck"))
	if n == nil {
		t.Fatal("a task queued for thirty days raised no pipeline.task-stalled notification")
	}
	if n.TaskID != "stuck" {
		t.Errorf("notification task = %q, want stuck", n.TaskID)
	}
	// Raised once, not once a cycle.
	if err := reconcileStallNotifications(ctx, deps, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := openNotification(t, ctx, store, model.NotificationSubject(FamilyTaskStalled, "stuck")); n == nil || n.RaiseCount != 1 {
		t.Errorf("notification after a second cycle = %+v, want the same one raised once", n)
	}

	// A person holds it: it is no longer stalled, and the notification goes.
	if err := store.SetDeferred(ctx, "stuck", &now, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := reconcileStallNotifications(ctx, deps, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := openNotification(t, ctx, store, model.NotificationSubject(FamilyTaskStalled, "stuck")); n != nil {
		t.Errorf("a deferred task's stall notification is still open: %+v", n)
	}
}

func TestAPortWithNoProgressRaisesPipelinePortStale(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	deps := systemScheduleDeps(store)
	putOldTask(t, ctx, store, "source", nil)
	putOldTask(t, ctx, store, "port", []model.Link{{Kind: model.LinkPortOf, Target: "source"}})

	if err := reconcileStallNotifications(ctx, deps, pipelineStewardSeeded); err != nil {
		t.Fatal(err)
	}
	if n := openNotification(t, ctx, store, model.NotificationSubject(FamilyPortStale, "port")); n == nil {
		t.Error("a port with no progress for thirty days raised no pipeline.port-stale notification")
	}
	if n := openNotification(t, ctx, store, model.NotificationSubject(FamilyTaskStalled, "port")); n != nil {
		t.Errorf("a stale port was also raised as a stalled task: %+v", n)
	}
}

func TestATimeBoundHoldKeepsAPersonsCancellationUntilItExpires(t *testing.T) {
	t.Parallel()
	now := pipelineStewardSeeded
	until := now.Add(time.Hour)
	cancelled := model.CIStatus{Conclusion: string(ci.ConclusionCancelled), CancelledBy: string(ci.CancelByOperator)}
	held := model.Task{ID: "t", DeferredAt: &now, DeferredUntil: &until}

	if !operatorCIHold(held, cancelled, now.Add(time.Minute)) {
		t.Error("a person's cancellation under a hold that has not expired is not held")
	}
	if operatorCIHold(held, cancelled, until.Add(time.Minute)) {
		t.Error("a person's cancellation is still held after the hold expired")
	}
	if operatorCIHold(model.Task{ID: "t"}, cancelled, now) {
		t.Error("a cancellation with no hold at all is held")
	}
	bySystem := cancelled
	bySystem.CancelledBy = string(ci.CancelBySystem)
	if operatorCIHold(held, bySystem, now) {
		t.Error("grain's own cancellation is held by a person's deferral")
	}
}

func TestAnUnresolvableJudgementModelIsSaidAndClearsOnceOneIsOffered(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	deps := systemScheduleDeps(store)
	now := pipelineStewardSeeded
	if err := reconcileSystemSchedules(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		cfg = &model.Config{}
	}

	// No connection offering anything: nothing runs, and nothing is said.
	if err := reconcileJudgementModel(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	if n := openNotification(t, ctx, store, model.JudgementModelUnresolvedSubject); n != nil {
		t.Errorf("a deployment with no models was told about the judgement model: %+v", n)
	}

	cfg.AgentBackends = []model.AgentBackend{{ID: "agy", Framework: model.AgentFrameworkAntigravity,
		Models: []string{"gemini-pro-coordinator"}}}
	if err := store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	if err := reconcileJudgementModel(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	if n := openNotification(t, ctx, store, model.JudgementModelUnresolvedSubject); n == nil {
		t.Fatal("no ranked model offered, and no durable.no-judgement-model notification")
	}

	cfg.AgentBackends[0].Models = append(cfg.AgentBackends[0].Models, "opus-5.5")
	if err := store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	if err := reconcileJudgementModel(ctx, deps, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := openNotification(t, ctx, store, model.JudgementModelUnresolvedSubject); n != nil {
		t.Errorf("opus-5.5 is offered, and the notification is still open: %+v", n)
	}
}

// A goal task rests deferred rather than in awaiting_reply; a
// notification it subscribes to still wakes it.
func TestANotificationWakesATaskRestingOnItsMetGoal(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	now := pipelineStewardSeeded
	putOldTask(t, ctx, store, "steward", nil)
	if err := store.SetTaskGoal(ctx, "steward", *model.PipelineStewardGoal(), "test", now); err != nil {
		t.Fatal(err)
	}
	g, err := store.TaskGoal(ctx, "steward")
	if err != nil || g == nil {
		t.Fatalf("TaskGoal: %v %v", g, err)
	}
	g.MetAt = &now
	if err := store.SaveTaskGoalProgress(ctx, *g); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDeferred(ctx, "steward", &now, nil, now); err != nil {
		t.Fatal(err)
	}
	if resting, err := store.RestingTask(ctx, "steward"); err != nil || !resting {
		t.Fatalf("RestingTask = %v, %v; want a task resting on its met goal", resting, err)
	}
	if err := store.SetTaskNotificationSubscriptions(ctx, "steward", model.PipelineStewardSubscriptions); err != nil {
		t.Fatal(err)
	}
	woke, err := store.WakeNotificationSubscribers(ctx, model.Notification{
		Subject: model.NotificationSubject(FamilyPortStale, "9"), Family: FamilyPortStale, Title: "stale"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(woke) != 1 || woke[0] != "steward" {
		t.Fatalf("woke %v, want the resting steward", woke)
	}
	task, err := store.GetTask(ctx, "steward")
	if err != nil || task == nil || task.DeferredAt != nil {
		t.Errorf("after the wake the task is %+v (%v), want it no longer deferred", task, err)
	}
}

// listOnlyCI is a ci.Runner that answers List and nothing else.
type listOnlyCI struct {
	ci.Runner
	runs []ci.Run
}

func (l listOnlyCI) List() []ci.Run { return l.runs }

// A job is a known flake only where it has flaked in the same
// repository: another repository's job of the same name buys no re-run.
func TestAKnownFlakeIsKnownOnlyInItsOwnRepository(t *testing.T) {
	t.Parallel()
	deps := Deps{CI: listOnlyCI{runs: []ci.Run{
		{ID: "r1", Repo: "o/other", Jobs: []ci.JobRun{{JobID: "go", Name: "go", Flake: true}}},
		{ID: "r2", Repo: "o/repo", Jobs: []ci.JobRun{{JobID: "lint", Name: "lint"}}},
	}}}
	if knownFlakyJob(deps, "o/repo", "go") {
		t.Error("o/other's flake of go made go a known flake in o/repo")
	}
	if !knownFlakyJob(deps, "o/other", "go") {
		t.Error("go flaked in o/other and is not a known flake there")
	}
	if knownFlakyJob(deps, "o/repo", "lint") {
		t.Error("lint never flaked and is a known flake")
	}
}
