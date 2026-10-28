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

// The sub-case of each task's state, from the reader's end: every task
// read asks the classifier it was handed, with what that read already
// knows -- the observation, the tasks it is still waiting on, the wait
// dispatch recorded -- and the merge-queue row carries the sub-case the
// cycle recorded beside its state.
//
// Which sub-case a task is in is orchestrator's (taskReasonOf, held to its
// classification by its own tests); what is tested here is the wiring.

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// echoReasons is a ui.ReasonClassifier that says back what it was handed,
// so the test can see each read asked with the right facts.
type echoReasons struct{}

func (echoReasons) TaskReason(t model.Task, state model.State, obs *model.Observation, openDeps []string,
	wait *model.GrantWait) ui.Reason {
	r := ui.Reason{Reason: string(state) + "/echo", Class: "driven", Control: "Run now"}
	if len(openDeps) > 0 {
		r.Resource = "deps:" + openDeps[0]
	}
	if wait != nil {
		r.Resource = "wait:" + wait.Resource
	}
	if obs != nil && obs.MergeQueueBlockedAt != nil {
		r.Resource = "blocked"
	}
	return r
}

func (echoReasons) MergeReason(reason string) ui.Reason {
	if reason != "waiting_ci/grain_running" {
		return ui.Reason{}
	}
	return ui.Reason{Reason: reason, Class: "driven", Driver: "reconcileTaskCI"}
}

func reasonClient(t *testing.T, reasons ui.ReasonClassifier) (*ui.Client, *model.Store, context.Context) {
	t.Helper()
	ui.LogToTest(t)
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	client := ui.NewClient(ui.Config{
		Actor:         ui.DefaultActor("alice"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
		Reasons:       reasons,
	}, store)
	client.Now = func() time.Time { return baseTime }
	return client, store, ctx
}

func TestEveryTaskReadCarriesItsSubCase(t *testing.T) {
	t.Parallel()
	client, store, ctx := reasonClient(t, echoReasons{})
	task := create(t, client, ctx)
	if err := store.SetGrantWait(ctx, model.GrantWait{TaskID: task.ID, Capability: model.QuotaWaitCapability,
		Resource: "paused", WaitingAt: baseTime}); err != nil {
		t.Fatal(err)
	}
	listed := listOne(t, client, ctx, task.ID)
	if listed.Reason == nil || listed.Reason.Reason != "queued/echo" || listed.Reason.Resource != "wait:paused" ||
		listed.Reason.Control != "Run now" {
		t.Fatalf("listed Reason = %+v, want queued/echo with the grant wait and its control", listed.Reason)
	}
	one, err := client.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]*ui.Reason{"Task": one.Reason, "GetTask": detail.Reason} {
		if got == nil || *got != *listed.Reason {
			t.Errorf("%s Reason = %+v, want the list's %+v", name, got, listed.Reason)
		}
	}
	if listed.MergeQueueReason != nil {
		t.Errorf("a task with no merge-queue state has a merge-queue reason: %+v", listed.MergeQueueReason)
	}
}

func TestTheMergeQueueRowCarriesTheSubCaseTheCycleRecorded(t *testing.T) {
	t.Parallel()
	client, store, ctx := reasonClient(t, echoReasons{})
	task := create(t, client, ctx)
	at := baseTime
	if err := store.ObserveField(ctx, task.ID, at, func(o *model.Observation) {
		o.MergeQueueState, o.MergeQueueReason, o.MergeQueueStateAt = "waiting_ci", "waiting_ci/grain_running", &at
		o.MergeQueueBlockedAt = &at
	}); err != nil {
		t.Fatal(err)
	}
	listed := listOne(t, client, ctx, task.ID)
	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]ui.Task{"list": listed, "detail": detail.Task} {
		if got.MergeQueueReason == nil || got.MergeQueueReason.Driver != "reconcileTaskCI" {
			t.Errorf("%s MergeQueueReason = %+v, want waiting_ci/grain_running classified", name, got.MergeQueueReason)
		}
		if got.Reason == nil || got.Reason.Resource != "blocked" {
			t.Errorf("%s Reason = %+v: the classifier was not handed the merge queue's giving up", name, got.Reason)
		}
	}

	// A sub-case this build cannot classify still says its name.
	if err := store.ObserveField(ctx, task.ID, at, func(o *model.Observation) {
		o.MergeQueueReason = "waiting_ci/from_a_newer_build"
	}); err != nil {
		t.Fatal(err)
	}
	if got := listOne(t, client, ctx, task.ID).MergeQueueReason; got == nil ||
		*got != (ui.Reason{Reason: "waiting_ci/from_a_newer_build"}) {
		t.Errorf("an unknown sub-case reads back as %+v, want its name alone", got)
	}
}

func TestNoClassifierMeansNoTaskSubCase(t *testing.T) {
	t.Parallel()
	client, _, ctx := reasonClient(t, nil)
	task := create(t, client, ctx)
	if got := listOne(t, client, ctx, task.ID).Reason; got != nil {
		t.Errorf("Reason = %+v with no classifier wired, want nil", got)
	}
}
