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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func autoMergeEvents(t *testing.T, store *model.Store, ctx context.Context, id string) []model.TaskEvent {
	t.Helper()
	events, err := store.TaskEvents(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.TaskEvent
	for _, e := range events {
		if strings.HasPrefix(string(e.Kind), "auto-merge-") {
			out = append(out, e)
		}
	}
	return out
}

func storedTask(t *testing.T, store *model.Store, ctx context.Context, id string) model.Task {
	t.Helper()
	task, err := store.GetTask(ctx, id)
	if err != nil || task == nil {
		t.Fatalf("reading %s: %v", id, err)
	}
	return *task
}

// grainReleases is what each of grain's own holds does when it lets a
// change go (orchestrator's securityhold.go, pathtriggerhold.go,
// reviewquestions.go, repoguard.go): it drops its hold link and turns
// auto-merge back on, with nobody named.
func grainReleases(t *testing.T, store *model.Store, ctx context.Context, id string) {
	t.Helper()
	if err := store.UpdateTask(ctx, id, func(t *model.Task) error {
		t.AutoMerge = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Tasks 2329, 2371, 2380, 2387, 2395 and 2396: filed with propose_task's
// auto_merge: false under a parent that could auto-merge, they later read
// true and merged unreviewed. The explicit false is sticky now: no write of
// grain's own turns it back on, and every attempt says who made it.
func TestExplicitAutoMergeFalseStaysOffUntilAPersonSaysOtherwise(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "director", Description: "files work", Approved: true, AutoMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "design doc", Description: "wants the operator's review",
		ProposedBy: parent.ID, AutoMerge: false, DeclineAutoMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := storedTask(t, store, ctx, child.ID); got.AutoMerge || !model.AutoMergeDeclined(got) {
		t.Fatalf("filed: autoMerge=%v declined=%v, want off and declined", got.AutoMerge, model.AutoMergeDeclined(got))
	}

	// A PATCH that does not mention autoMerge leaves it, mark and all.
	title := "design doc, retitled"
	if _, err := client.UpdateTask(ctx, child.ID, ui.UpdateTaskRequest{Title: &title}); err != nil {
		t.Fatal(err)
	}
	if got := storedTask(t, store, ctx, child.ID); got.AutoMerge || !model.AutoMergeDeclined(got) {
		t.Fatalf("after an edit not naming autoMerge: autoMerge=%v declined=%v", got.AutoMerge, model.AutoMergeDeclined(got))
	}

	// A hold releasing -- twice, the way a release retried every cycle
	// would -- leaves it off, and says so once, naming the writer.
	grainReleases(t, store, ctx, child.ID)
	grainReleases(t, store, ctx, child.ID)
	if got := storedTask(t, store, ctx, child.ID); got.AutoMerge {
		t.Fatal("a release of grain's own turned an explicit auto_merge: false back on")
	}
	events := autoMergeEvents(t, store, ctx, child.ID)
	if len(events) != 1 || events[0].Kind != model.TaskEventAutoMergeKeptOff {
		t.Fatalf("events = %+v, want one auto-merge-kept-off", events)
	}
	if !strings.Contains(events[0].Detail, "grainReleases") {
		t.Errorf("kept-off detail = %q, want it to name the writer", events[0].Detail)
	}

	// A daemon restart re-runs Store.Init over the same database; the
	// field and its mark are both still there.
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if got := storedTask(t, store, ctx, child.ID); got.AutoMerge || !model.AutoMergeDeclined(got) {
		t.Fatalf("after a restart: autoMerge=%v declined=%v", got.AutoMerge, model.AutoMergeDeclined(got))
	}

	// A person turning it on is the one thing that does, and is recorded
	// as theirs.
	bob := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "bob@example.com"})
	on, off := true, false
	if _, err := client.UpdateTask(bob, child.ID, ui.UpdateTaskRequest{AutoMerge: &on}); err != nil {
		t.Fatal(err)
	}
	got := storedTask(t, store, ctx, child.ID)
	if !got.AutoMerge || model.AutoMergeDeclined(got) {
		t.Fatalf("after bob's PATCH to true: autoMerge=%v declined=%v", got.AutoMerge, model.AutoMergeDeclined(got))
	}
	events = autoMergeEvents(t, store, ctx, child.ID)
	if last := events[len(events)-1]; last.Kind != model.TaskEventAutoMergeOn ||
		last.By != (model.Principal{Kind: model.PrincipalHuman, ID: "bob@example.com"}) || last.Reason != "edited" {
		t.Fatalf("last event = %+v, want bob turning it on", last)
	}

	// And a PATCH to false is as sticky as the filer's false was.
	if _, err := client.UpdateTask(bob, child.ID, ui.UpdateTaskRequest{AutoMerge: &off}); err != nil {
		t.Fatal(err)
	}
	grainReleases(t, store, ctx, child.ID)
	if got := storedTask(t, store, ctx, child.ID); got.AutoMerge || !model.AutoMergeDeclined(got) {
		t.Fatalf("after bob's PATCH to false and a release: autoMerge=%v declined=%v",
			got.AutoMerge, model.AutoMergeDeclined(got))
	}

	// Every change is a step on the timeline.
	detail, err := client.GetTask(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, tr := range detail.Transitions {
		switch tr.State {
		case model.TransitionAutoMergeOn, model.TransitionAutoMergeOff, model.TransitionAutoMergeKeptOff:
			steps = append(steps, string(tr.State)+" by "+tr.By)
		}
	}
	want := "auto_merge_kept_off by automation grain, auto_merge_on by human bob@example.com, " +
		"auto_merge_off by human bob@example.com, auto_merge_kept_off by automation grain"
	if got := strings.Join(steps, ", "); got != want {
		t.Errorf("timeline auto-merge steps = %q, want %q", got, want)
	}
}

// Nothing explicit said, nothing sticky: a task filed with auto-merge on
// keeps it, and grain's own hold and release of it work as they always
// did.
func TestAutoMergeWithoutAnExplicitNoIsGrainsToHoldAndRelease(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "routine", Description: "lands itself", Approved: true, AutoMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := storedTask(t, store, ctx, task.ID); !got.AutoMerge || model.AutoMergeDeclined(got) {
		t.Fatalf("filed: autoMerge=%v declined=%v, want on", got.AutoMerge, model.AutoMergeDeclined(got))
	}
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.AutoMerge = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	grainReleases(t, store, ctx, task.ID)
	if got := storedTask(t, store, ctx, task.ID); !got.AutoMerge {
		t.Fatal("a hold's release did not hand back the auto-merge it took")
	}
	var kinds []string
	for _, e := range autoMergeEvents(t, store, ctx, task.ID) {
		kinds = append(kinds, string(e.Kind))
	}
	if got := strings.Join(kinds, " "); got != "auto-merge-off auto-merge-on" {
		t.Errorf("events = %q, want the hold and its release", got)
	}
}
