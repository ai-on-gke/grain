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
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestCreateTaskLinksRevertsAndRevertedBy(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)

	origin, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:    "Original Feature Task",
		Approved: true,
	})
	if err != nil {
		t.Fatalf("creating origin task: %v", err)
	}

	revertTask, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:    "Revert Original Feature Task",
		Reverts:  origin.ID,
		Approved: true,
	})
	if err != nil {
		t.Fatalf("creating revert task: %v", err)
	}

	if revertTask.Reverts != origin.ID {
		t.Fatalf("revertTask.Reverts = %q, want %q", revertTask.Reverts, origin.ID)
	}

	originFetched, err := client.Task(ctx, origin.ID)
	if err != nil {
		t.Fatalf("fetching origin task: %v", err)
	}
	if originFetched.RevertedBy != revertTask.ID {
		t.Fatalf("originFetched.RevertedBy = %q, want %q", originFetched.RevertedBy, revertTask.ID)
	}

	// Verify links in the store
	storedRevert, err := store.GetTask(ctx, revertTask.ID)
	if err != nil {
		t.Fatalf("reading revert task from store: %v", err)
	}
	hasRevertLink := false
	for _, l := range storedRevert.Links {
		if l.Kind == model.LinkReverts && l.Target == origin.ID {
			hasRevertLink = true
			break
		}
	}
	if !hasRevertLink {
		t.Fatalf("storedRevert.Links missing LinkReverts to %s, got: %+v", origin.ID, storedRevert.Links)
	}

	storedOrigin, err := store.GetTask(ctx, origin.ID)
	if err != nil {
		t.Fatalf("reading origin task from store: %v", err)
	}
	hasRevertedByLink := false
	for _, l := range storedOrigin.Links {
		if l.Kind == model.LinkRevertedBy && l.Target == revertTask.ID {
			hasRevertedByLink = true
			break
		}
	}
	if !hasRevertedByLink {
		t.Fatalf("storedOrigin.Links missing LinkRevertedBy to %s, got: %+v", revertTask.ID, storedOrigin.Links)
	}
}
