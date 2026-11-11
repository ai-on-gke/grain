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
	"reflect"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A Reads or Writes edit is a repo#read or repo#write grant given or
// taken (unified-grants Q5): each one the edit changes is a
// grant-attached or grant-detached event naming the grant's canonical
// string, its with: normalized, and one the edit keeps is neither.
func TestUpdateTaskNotesRepoGrantAttachAndDetach(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "t", Approved: true, Repo: "acme/widgets",
		Reads:  []string{"acme/shared-lib"},
		Writes: []ui.WriteTarget{{Repo: "acme/widgets-state", Base: "deploy"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	reads := []string{"acme/shared-lib", "acme/schema"}
	writes := []ui.WriteTarget{}
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Reads: &reads, Writes: &writes}); err != nil {
		t.Fatal(err)
	}

	events, err := store.TaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		if e.Kind == model.TaskEventGrantAttached || e.Kind == model.TaskEventGrantDetached {
			got = append(got, string(e.Kind)+" "+e.Detail)
		}
	}
	want := []string{
		`grant-detached https://grain.dev/capabilities/repo#write={"base":"deploy","repo":"acme/widgets-state"}`,
		`grant-attached https://grain.dev/capabilities/repo#read={"repo":"acme/schema"}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("grant events = %q, want %q", got, want)
	}
}

// An edit that leaves Reads and Writes as they were notes no grant.
func TestUpdateTaskNotesNoGrantForAnUnchangedRepoList(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "t", Approved: true, Reads: []string{"acme/shared-lib"},
	})
	if err != nil {
		t.Fatal(err)
	}
	same := []string{"acme/shared-lib"}
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Reads: &same}); err != nil {
		t.Fatal(err)
	}
	events, err := store.TaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == model.TaskEventGrantAttached || e.Kind == model.TaskEventGrantDetached {
			t.Fatalf("unexpected %s %s for an unchanged reads list", e.Kind, e.Detail)
		}
	}
}
