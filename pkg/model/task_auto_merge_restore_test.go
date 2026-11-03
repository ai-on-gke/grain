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
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A write that drops the last of grain's holds on a change hands back
// the auto-merge the hold took, whichever writer drops it -- unless the
// change is somebody's explicit "no", a hold ended in a person's
// decision, or approvals are still owed.
func TestDroppingTheLastHoldHandsAutoMergeBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	held := func(id string, links ...model.Link) model.Task {
		return model.Task{ID: id, Title: id, Body: "b", Target: &repo, Links: links}
	}
	security := model.Link{Kind: model.LinkSecurityHold, Target: "v1"}
	humanOnly := model.Link{Kind: model.LinkHumanOnlyHold, Target: "v1"}
	path := model.Link{Kind: model.LinkPathTriggerHold, Target: "on-findings"}

	cases := []struct {
		name   string
		task   model.Task
		mutate func(*model.Task)
		want   bool
	}{
		{"last hold cleared", held("1", security), func(t *model.Task) { t.Links = nil }, true},
		{"human-only hold lifted by a carried clearance", held("2", humanOnly), func(t *model.Task) { t.Links = nil }, true},
		{"another hold remains", held("3", security, path),
			func(t *model.Task) { t.Links = []model.Link{path} }, false},
		{"ended for a person", held("4", security), func(t *model.Task) {
			t.Links = nil
			model.AwaitPersonForAutoMerge(t, "review r1 left findings")
		}, false},
		{"declined", held("5", security), func(t *model.Task) {
			t.Links = nil
			model.DeclineAutoMerge(t, "human:alice@example.com")
		}, false},
		{"approvals owed", held("6", security), func(t *model.Task) {
			t.Links = []model.Link{model.ApprovalRequiredLink("builtin:owner", []string{"alice@example.com"})}
		}, false},
		{"nothing was held", held("7"), func(t *model.Task) { t.Title = "renamed" }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t)
			if err := store.PutTask(ctx, c.task); err != nil {
				t.Fatal(err)
			}
			if err := store.UpdateTask(ctx, c.task.ID, func(t *model.Task) error { c.mutate(t); return nil }); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetTask(ctx, c.task.ID)
			if err != nil || got == nil {
				t.Fatalf("reading: %v", err)
			}
			if got.AutoMerge != c.want {
				t.Fatalf("autoMerge = %v, want %v (links %+v)", got.AutoMerge, c.want, got.Links)
			}
			events, err := store.TaskEvents(ctx, c.task.ID)
			if err != nil {
				t.Fatal(err)
			}
			on := slices.ContainsFunc(events, func(e model.TaskEvent) bool {
				return e.Kind == model.TaskEventAutoMergeOn && strings.Contains(e.Reason, "cleared")
			})
			if on != c.want {
				t.Fatalf("events = %+v, want a restoring auto-merge-on: %v", events, c.want)
			}
		})
	}
}

// Whatever turns auto-merge back on settles what a person was asked, so
// the mark that kept grain's own releases off goes with it.
func TestTurningAutoMergeOnLiftsAwaitsPerson(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := model.Task{ID: "1", Title: "t", Body: "b", Target: &repo}
	model.AwaitPersonForAutoMerge(&task, "review r1 left findings")
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error { t.AutoMerge = true; return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if !got.AutoMerge || model.AutoMergeAwaitsPerson(*got) {
		t.Fatalf("autoMerge=%v links=%+v, want on with the mark gone", got.AutoMerge, got.Links)
	}
}
