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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// closeAndReopenARunningTask is the 2026-10-30 sequence: a task with a
// live run and a CI run in flight, closed by somebody and reopened by
// somebody else seconds later.
func closeAndReopenARunningTask(t *testing.T) (*ui.Client, *model.Store, ui.Task, *fakeCIRunner) {
	t.Helper()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	runID := dispatch.RunID(task.ID, 1)
	if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: task.ID, Attempt: 1,
		StartedAt: baseTime.Add(time.Minute)}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	started := baseTime.Add(2 * time.Minute)
	if err := store.ObserveField(ctx, task.ID, started, func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: "b8d940e14a9aef1d", Commit: "abcdef", StartedAt: &started}
	}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeCIRunner{cancelOK: true}
	client.Config.CI = runner

	closedAt := baseTime.Add(10 * time.Minute)
	client.Now = func() time.Time { return closedAt }
	operator := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "bob@example.com"})
	if err := client.Close(operator, task.ID, ui.CloseOptions{Reason: "cycling in-flight tasks"}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// What the orchestrator does once it sees the closure.
	if err := store.FinishRun(ctx, runID, closedAt.Add(time.Second), "cancelled",
		"the task was closed while this run was still live"); err != nil {
		t.Fatal(err)
	}
	client.Now = func() time.Time { return closedAt.Add(5 * time.Second) }
	session := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: "1937"})
	if err := client.ReopenBecause(session, task.ID, ""); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	client.Now = func() time.Time { return baseTime.Add(time.Hour) }
	return client, store, task, runner
}

func TestCloseAndReopenOfARunningTaskAreTransitionsWithWhoDidThem(t *testing.T) {
	t.Parallel()
	client, _, task, runner := closeAndReopenARunningTask(t)
	if got := strings.Join(runner.cancelled, ","); got != "b8d940e14a9aef1d" {
		t.Fatalf("cancelled CI runs = %q, want the task's own", got)
	}
	detail, err := client.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var closed, reopened *ui.Transition
	var states []string
	for i, tr := range detail.Transitions {
		states = append(states, string(tr.State))
		switch tr.State {
		case model.StateClosed:
			if closed != nil {
				t.Errorf("two closed steps in %+v", detail.Transitions)
			}
			closed = &detail.Transitions[i]
		case model.TransitionReopened:
			reopened = &detail.Transitions[i]
		}
	}
	if got, want := strings.Join(states, " "), "proposed queued running closed reopened"; got != want {
		t.Fatalf("transition states = %q, want %q", got, want)
	}
	if closed == nil || reopened == nil {
		t.Fatalf("transitions = %+v, want a closed and a reopened step", detail.Transitions)
	}
	if closed.Inferred || reopened.Inferred {
		t.Errorf("a recorded close or reopen marked inferred: %+v, %+v", closed, reopened)
	}
	if closed.By != "human bob@example.com" || closed.Reason != "cycling in-flight tasks" {
		t.Errorf("closed = %+v, want bob's close with his reason", closed)
	}
	runID := dispatch.RunID(task.ID, 1)
	for _, want := range []string{"cancelled run " + runID + " (attempt 1)", "CI run b8d940e14a9aef1d"} {
		if !strings.Contains(closed.Detail, want) {
			t.Errorf("closed detail = %q, want it to say %q", closed.Detail, want)
		}
	}
	if reopened.By != "agent 1937" || !reopened.At.After(closed.At) {
		t.Errorf("reopened = %+v, want session 1937's reopen after the close at %s", reopened, closed.At)
	}
	if detail.State == model.StateClosed {
		t.Errorf("state = closed after the reopen")
	}
}

func TestPipelineChangesKeepACloseThatWasReopened(t *testing.T) {
	t.Parallel()
	client, _, task, _ := closeAndReopenARunningTask(t)
	page, err := client.PipelineChanges(t.Context(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var words []string
	var closeDetail string
	for _, c := range page.Changes {
		if c.ID != task.ID || c.Kind != ui.PipelineChangeTask {
			continue
		}
		words = append(words, c.From+">"+c.To)
		if c.To == "closed" {
			closeDetail = c.Detail
		}
	}
	want := ">filed filed>running running>closed closed>ended ended>reopened"
	if got := strings.Join(words, " "); got != want {
		t.Fatalf("task changes = %s, want %s", got, want)
	}
	if !strings.HasPrefix(closeDetail, "by human bob@example.com: cycling in-flight tasks; cancelled run ") {
		t.Errorf("close detail = %q, want who, why and what it cancelled", closeDetail)
	}
}

// Every attempt is its own start and end: a task run twice is not one
// span from the first start to the last ending.
func TestPipelineChangesListEveryAttempt(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	for n := 1; n <= 2; n++ {
		id := dispatch.RunID(task.ID, n)
		start := baseTime.Add(time.Duration(n) * time.Hour)
		if err := store.StartRun(ctx, model.Run{ID: id, TaskID: task.ID, Attempt: n, StartedAt: start},
			model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, id, start.Add(30*time.Minute), "failed", ""); err != nil {
			t.Fatal(err)
		}
	}
	client.Now = func() time.Time { return baseTime.Add(4 * time.Hour) }
	page, err := client.PipelineChanges(ctx, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range page.Changes {
		if c.ID == task.ID {
			got = append(got, c.To+"@"+c.At.Sub(baseTime).String())
		}
	}
	want := "filed@0s running@1h0m0s ended@1h30m0s running@2h0m0s ended@2h30m0s"
	if strings.Join(got, " ") != want {
		t.Fatalf("changes = %s, want %s", strings.Join(got, " "), want)
	}
}

func TestAgentCloseAndReopenRecordActingReasonHeader(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	srv := ui.NewServerWithClient(client)
	seedAPITask(t, client, "901", "acme/widgets", "api:tasks:write")
	task := create(t, client, ctx)

	client.Now = func() time.Time { return baseTime.Add(10 * time.Minute) }
	if code, body := asAgent(t, srv, "901", http.MethodPost, "/api/tasks/"+task.ID+"/close", "", "duplicate of 42"); code != http.StatusOK {
		t.Fatalf("close: %d %s", code, body)
	}
	client.Now = func() time.Time { return baseTime.Add(11 * time.Minute) }
	if code, body := asAgent(t, srv, "901", http.MethodPost, "/api/tasks/"+task.ID+"/reopen", "", "not a duplicate"); code != http.StatusOK {
		t.Fatalf("reopen: %d %s", code, body)
	}

	events, err := store.TaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Reason != "duplicate of 42" || events[1].Reason != "not a duplicate" {
		t.Fatalf("events = %+v, want close and reopen carrying ActingReasonHeader", events)
	}
}
