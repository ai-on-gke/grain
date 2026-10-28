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

// grain/task-1549: a task's NothingNeededAction, from the API's side --
// carried onto the task on create, editable afterwards, and refused when
// it names neither choice.

import (
	"errors"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestCreateTaskCarriesTheNothingNeededAction(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "check the flag", NothingNeededAction: "ack"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.NothingNeededAction != "ack" {
		t.Errorf("the response says %q, want ack", task.NothingNeededAction)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: (%+v, %v)", stored, err)
	}
	if stored.NothingNeededAction != model.NothingNeededAck {
		t.Errorf("the stored task says %q, want ack", stored.NothingNeededAction)
	}

	close := "close"
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{NothingNeededAction: &close}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	stored, err = store.GetTask(ctx, task.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: (%+v, %v)", stored, err)
	}
	if stored.NothingNeededAction != model.NothingNeededClose {
		t.Errorf("after the edit the stored task says %q, want close", stored.NothingNeededAction)
	}
}

func TestCreateTaskRefusesANothingNeededActionThatIsNeitherChoice(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "t", NothingNeededAction: "delete"})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}
