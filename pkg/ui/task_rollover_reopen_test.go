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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A task that was rolled over is not reopened, by Reopen or by "Reopen
// and send": its successor carries the work on, and a reopened
// predecessor would be a second live copy of it with no pull request,
// goal or dependents (docs/test-runs/2026-11-11-durable-tasks.md,
// finding 4). The comment is refused before it is written.
func TestRolledOverTaskIsNotReopened(t *testing.T) {
	client, store, ctx := testClient(t)
	durable := string(model.AdmissionDurable)
	tk, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "durable", Description: "the work", Approved: true, NoRepo: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateTask(ctx, tk.ID, ui.UpdateTaskRequest{Admission: &durable}); err != nil {
		t.Fatal(err)
	}
	result, err := client.RolloverTask(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Reopen(ctx, tk.ID); err == nil || !strings.Contains(err.Error(), "task "+result.Successor) {
		t.Errorf("Reopen of a rolled-over task = %v, want a refusal naming task %s", err, result.Successor)
	}
	before, err := store.Comments(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AddCommentReopening(ctx, tk.ID, "are you there?", nil, "alice"); err == nil {
		t.Error("Reopen and send on a rolled-over task was accepted")
	}
	after, err := store.Comments(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("the refused comment was written anyway (%d comments, was %d)", len(after), len(before))
	}
	if state, err := store.State(ctx, tk.ID); err != nil || state != model.StateClosed {
		t.Errorf("rolled-over task is %s (err %v), want closed", state, err)
	}
}
