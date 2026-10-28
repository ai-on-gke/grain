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
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// durable is a person's to grant and implies priority dispatch, in the
// Task predicate and in the store's own SQL twin alike.
func TestDurableImpliesPriorityDispatchAndIsAPersonsToGrant(t *testing.T) {
	t.Parallel()
	if !model.IsPersonOnlyGrant(model.DurableCapability) {
		t.Fatal("durable is not a person-only grant")
	}
	parent := model.Task{ID: "steward", Grants: []model.Grant{
		{Capability: model.DurableCapability, Via: model.GrantByGrain},
		{Capability: "self-debug", Via: model.GrantByGrain}}}
	child := model.Task{Grants: []model.Grant{
		{Capability: model.DurableCapability, Via: model.GrantByLabel},
		{Capability: "self-debug", Via: model.GrantByLabel}}}
	if got := model.EscalatedCapabilities(child, parent); !slices.Equal(got, []string{model.DurableCapability}) {
		t.Fatalf("EscalatedCapabilities = %v, want durable alone", got)
	}
	tmpl := model.Template{ID: "durable-worker", Grants: []model.Grant{{Capability: model.DurableCapability}}}
	if err := model.TemplateWithinTask(parent, tmpl); err == nil {
		t.Fatal("an agent holding durable may file from a template granting it")
	}
	durable := model.Task{Grants: []model.Grant{{Capability: model.DurableCapability}}}
	if !durable.IsDurable() || !durable.HasPriorityDispatch() {
		t.Fatalf("a durable task: IsDurable = %v, HasPriorityDispatch = %v, want both true",
			durable.IsDurable(), durable.HasPriorityDispatch())
	}
	priority := model.Task{Grants: []model.Grant{{Capability: model.PriorityDispatchCapability}}}
	if priority.IsDurable() {
		t.Fatal("priority-dispatch alone reads as durable")
	}

	store, ctx := open(t)
	task := durableTestTask("d", true)
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if got, err := store.HasPriorityDispatch(ctx, "d"); err != nil || !got {
		t.Fatalf("Store.HasPriorityDispatch(durable) = %v, %v; want true", got, err)
	}
	if got, err := store.IsDurable(ctx, "d"); err != nil || !got {
		t.Fatalf("Store.IsDurable(durable) = %v, %v; want true", got, err)
	}
}

func durableTestTask(id string, durable bool) model.Task {
	tk := model.Task{
		ID: id, Intent: model.IntentImplement, Title: id,
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: human},
	}
	if durable {
		tk.Grants = []model.Grant{{Capability: model.DurableCapability, Via: model.GrantByLabel}}
	}
	return tk
}

// Durable runs are admitted outside the worker count, and held to
// Limits.Durable instead: with one worker busy and a ceiling of two, two
// durable runs start and the third is refused with ErrDurableCeiling,
// which is ErrAtCapacity; an ordinary task is not held by the durable
// ceiling at all.
func TestStartRunHoldsDurableRunsToTheirOwnCeiling(t *testing.T) {
	store, ctx := open(t)
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	limits := model.Limits{Workers: 1, Durable: 2}
	for _, tk := range []model.Task{durableTestTask("w", false), durableTestTask("d1", true),
		durableTestTask("d2", true), durableTestTask("d3", true)} {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	start := func(id string) error {
		return store.StartRun(ctx, model.Run{ID: id + "-1", TaskID: id, Attempt: 1, StartedAt: at}, limits)
	}
	if err := start("w"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"d1", "d2"} {
		if err := start(id); err != nil {
			t.Fatalf("starting %s with the worker pool full: %v, want it admitted outside it", id, err)
		}
	}
	if n, err := store.LiveDurableRuns(ctx); err != nil || n != 2 {
		t.Fatalf("LiveDurableRuns = %d, %v; want 2", n, err)
	}
	err := start("d3")
	if !errors.Is(err, model.ErrDurableCeiling) || !errors.Is(err, model.ErrAtCapacity) {
		t.Fatalf("starting a third durable run = %v, want ErrDurableCeiling (an ErrAtCapacity)", err)
	}
	if err := store.FinishRun(ctx, "d1-1", at.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := start("d3"); err != nil {
		t.Fatalf("starting d3 once a durable run finished: %v", err)
	}
}

// A setup suspension can name when its task may restart, and how many
// suspensions of a kind a task has had in a row is counted from its
// latest run back.
func TestSuspensionResumeAtAndSuspensionsInARow(t *testing.T) {
	store, ctx := open(t)
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if err := store.PutTask(ctx, durableTestTask("d", true)); err != nil {
		t.Fatal(err)
	}
	if got, err := store.SuspensionResumeAt(ctx, "d"); err != nil || got != nil {
		t.Fatalf("SuspensionResumeAt with no suspension = %v, %v; want nil", got, err)
	}
	for i, kind := range []model.SuspensionKind{model.SuspendSetup, model.SuspendIdle, model.SuspendSetup, model.SuspendSetup} {
		started := at.Add(time.Duration(i) * time.Hour)
		id := "d-" + string(rune('1'+i))
		if err := store.StartRun(ctx, model.Run{ID: id, TaskID: "d", Attempt: i + 1, StartedAt: started}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		resume := started.Add(2 * time.Minute)
		if err := store.Suspend(ctx, model.Suspension{RunID: id, Kind: kind, ResumeAt: &resume,
			CreatedAt: started.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.SuspensionResumeAt(ctx, "d")
	if want := at.Add(3*time.Hour + 2*time.Minute); err != nil || got == nil || !got.Equal(want) {
		t.Fatalf("SuspensionResumeAt = %v, %v; want %v", got, err, want)
	}
	if n, err := store.SuspensionsInARow(ctx, "d", model.SuspendSetup); err != nil || n != 2 {
		t.Fatalf("SuspensionsInARow(setup) = %d, %v; want 2 (an idle one breaks the row)", n, err)
	}
	if streak, err := store.FailureStreak(ctx, "d"); err != nil || (streak != nil && streak.Count != 0) {
		t.Fatalf("FailureStreak after suspensions = %+v, %v; want no count", streak, err)
	}
}
