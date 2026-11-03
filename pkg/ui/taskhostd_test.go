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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A task filed with a hostd selector carries it, it can be changed and
// cleared afterwards, a selector that is not one -- or that no hostd in
// the registry answers -- is refused, and each attempt shows the hostd,
// instance and owner its run recorded.
func TestTaskHostdPinIsFiledSetAndShownOnItsAttempts(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	registerHostds(t, store,
		model.Hostd{Name: "gpu-box", Labels: map[string]string{"gpu": "yes", "zone": "b"}},
		model.Hostd{Name: "laptop"})
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "train it", Description: "on a GPU", Approved: true, Hostd: "zone=b, gpu=yes",
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil || detail.Hostd != "gpu=yes,zone=b" {
		t.Fatalf("filed hostd = %q, %v; want the selector, labels sorted", detail.Hostd, err)
	}
	if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "bad", Approved: true, Hostd: "Not A Hostd"}); err == nil {
		t.Error("CreateTask accepted a hostd that is neither a name nor labels")
	}
	// A selector no hostd answers is refused when it is set, not at
	// every run's setup (docs/test-runs/2026-11-03-hostd-routing.md).
	for _, unmet := range []string{"ws-nope", "gpu=no", "gpu=yes,zone=a", "gone"} {
		if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "unmet", Approved: true, Hostd: unmet}); err == nil {
			t.Errorf("CreateTask accepted hostd %q, which no hostd in the registry answers", unmet)
		}
		if err := c.SetTaskHostd(ctx, task.ID, unmet); err == nil {
			t.Errorf("SetTaskHostd accepted hostd %q, which no hostd in the registry answers", unmet)
		}
	}
	if err := c.SetTaskHostd(ctx, task.ID, "laptop"); err != nil {
		t.Fatal(err)
	}
	if detail, _ = c.GetTask(ctx, task.ID); detail.Hostd != "laptop" {
		t.Errorf("hostd after SetTaskHostd = %q, want laptop", detail.Hostd)
	}
	if err := c.SetTaskHostd(ctx, task.ID, "a=b,=c"); err == nil {
		t.Error("SetTaskHostd accepted a label with no key")
	}
	if err := c.SetTaskHostd(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	if detail, _ = c.GetTask(ctx, task.ID); detail.Hostd != "" {
		t.Errorf("hostd after clearing = %q, want the default", detail.Hostd)
	}

	started := c.Now().UTC()
	if err := store.StartRun(ctx, model.Run{ID: task.ID + "-1", TaskID: task.ID, Sandbox: "s1", Attempt: 1, StartedAt: started}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTaskRunHostd(ctx, model.TaskRunHostd{
		RunID: task.ID + "-1", TaskID: task.ID, Hostd: "laptop", Instance: "laptop.example.com", Owner: "alice@example.com", At: started.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	detail, err = c.GetTask(ctx, task.ID)
	if err != nil || len(detail.Attempts) != 1 {
		t.Fatalf("attempts = %+v, %v", detail.Attempts, err)
	}
	if h := detail.Attempts[0].Hostd; h == nil || h.Hostd != "laptop" || h.Instance != "laptop.example.com" || h.Owner != "alice@example.com" {
		t.Errorf("attempt 1's hostd = %+v, want laptop on laptop.example.com, owned by alice@example.com", h)
	}
}

// registerHostds puts hostds in the registry; the last is "gone",
// registered and revoked.
func registerHostds(t *testing.T, store *model.Store, hostds ...model.Hostd) {
	t.Helper()
	ctx := context.Background()
	for _, h := range append(hostds, model.Hostd{Name: "gone", Labels: map[string]string{"gpu": "yes", "zone": "a"}}) {
		if _, _, err := store.CreateHostd(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.RevokeHostd(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
}
