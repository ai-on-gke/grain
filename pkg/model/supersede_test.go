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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

var widgets = model.RepoRef{Owner: "acme", Name: "widgets"}

// queuedTask files one approved, never-dispatched task -- the only kind
// a supersede may ever close.
func queuedTask(t *testing.T, store *model.Store, ctx context.Context,
	id string, target *model.RepoRef) model.Task {

	t.Helper()
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: id, Target: target,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: bot}, Reason: model.ReasonDirect,
		},
		Binding:    model.BindingDirective,
		Approval:   &model.Attribution{Actor: human},
		ApprovedAt: &now,
		CreatedAt:  &now,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing %s: %v", id, err)
	}
	return task
}

// The whole of what superseding does, in one call: the task it replaces
// is closed, both halves of the relation are on the record, and the
// closed task's own conversation says who closed it and what replaced
// it. An operator closing four stale revisions by hand had none of
// those three (grain/task-473).
func TestSupersedeClosesTheReplacedTaskAndLinksBothWays(t *testing.T) {
	store, ctx := open(t)
	old := queuedTask(t, store, ctx, "465", &widgets)
	replacement := queuedTask(t, store, ctx, "471", &widgets)

	result, err := store.Supersede(ctx, old.ID, replacement, "director", now)
	refusal := result.Refusal
	if err != nil {
		t.Fatal(err)
	}
	if refusal != "" {
		t.Fatalf("refusal = %q on a queued task nothing has run, want it superseded", refusal)
	}

	state, err := store.State(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateClosed {
		t.Errorf("state of the superseded task = %s, want closed", state)
	}
	stored, err := store.GetTask(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := linkTarget(stored.Links, model.LinkSupersededBy); got != replacement.ID {
		t.Errorf("superseded-by link = %q, want the replacement (%s)", got, replacement.ID)
	}

	comments, err := store.Comments(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("conversation = %d comments, want the one recording the close", len(comments))
	}
	if comments[0].Author.Actor.Kind != model.PrincipalAutomation ||
		comments[0].Author.OnBehalfOf == nil || comments[0].Author.OnBehalfOf.ID != "director" {
		t.Errorf("close recorded by %+v, want grain on behalf of the task that proposed the "+
			"replacement", comments[0].Author)
	}
	if want := model.SupersedeComment(replacement.ID, "director"); comments[0].Body != want {
		t.Errorf("close comment = %q, want %q", comments[0].Body, want)
	}
}

// The bound, stated once and checked everywhere: a task with a run
// behind it, one in any state but the three a never-dispatched task can be
// in, one with a pull request open, and one in another repository are
// all left for a human. Each of those is either work this would throw
// away or a decision that was never this tool's to make.
func TestSupersedeRefusesAnythingButANeverDispatchedTaskHere(t *testing.T) {
	t.Parallel()
	queued := model.Task{ID: "465", Target: &widgets}
	other := model.RepoRef{Owner: "acme", Name: "gadgets"}
	withPR := model.Task{
		ID: "465", Target: &widgets,
		Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#12"}},
	}

	for _, tt := range []struct {
		name        string
		old         model.Task
		state       model.State
		attempts    int
		replacement *model.RepoRef
		want        string
	}{
		{"queued here", queued, model.StateQueued, 0, &widgets, ""},
		{"proposed here", queued, model.StateProposed, 0, &widgets, ""},
		{"deferred here", queued, model.StateDeferred, 0, &widgets, ""},
		{"no repo either side", model.Task{ID: "465"}, model.StateQueued, 0, nil, ""},
		{"one attempt behind it", queued, model.StateQueued, 1, &widgets, "dispatched already"},
		{"deferred with attempt behind it", queued, model.StateDeferred, 1, &widgets, "dispatched already"},
		{"running", queued, model.StateRunning, 1, &widgets, "dispatched already"},
		{"completed", queued, model.StateCompleted, 0, &widgets, "it is completed"},
		{"already closed", queued, model.StateClosed, 0, &widgets, "it is closed"},
		{"a pull request open", withPR, model.StateQueued, 0, &widgets, "acme/widgets#12"},
		{"deferred with a pull request open", withPR, model.StateDeferred, 0, &widgets, "acme/widgets#12"},
		{"another repository", queued, model.StateQueued, 0, &other, "acme/gadgets"},
		{"replacement has no repo", queued, model.StateQueued, 0, nil, "no repository at all"},
		{"the old task has none", model.Task{ID: "465"}, model.StateQueued, 0, &widgets, "acme/widgets"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := model.SupersedeRefusal(tt.old, tt.state, tt.attempts, tt.replacement)
			switch {
			case tt.want == "" && got != "":
				t.Fatalf("refusal = %q, want a task that may be superseded", got)
			case tt.want == "":
			case got == "":
				t.Fatalf("no refusal, want one mentioning %q", tt.want)
			case !strings.Contains(got, tt.want):
				t.Fatalf("refusal = %q, want it to say %q", got, tt.want)
			}
		})
	}
}

func TestSupersedeClosesNeverDispatchedDeferredTask(t *testing.T) {
	store, ctx := open(t)
	old := queuedTask(t, store, ctx, "465", &widgets)
	wake := now.Add(time.Hour)
	if err := store.SetDeferred(ctx, old.ID, &now, &wake, now); err != nil {
		t.Fatal(err)
	}
	if state, err := store.State(ctx, old.ID); err != nil || state != model.StateDeferred {
		t.Fatalf("state before supersede = (%q, %v), want deferred", state, err)
	}
	replacement := queuedTask(t, store, ctx, "471", &widgets)

	result, err := store.Supersede(ctx, old.ID, replacement, "director", now)
	refusal := result.Refusal
	if err != nil {
		t.Fatal(err)
	}
	if refusal != "" {
		t.Fatalf("refusal = %q on a never-dispatched deferred task, want it superseded", refusal)
	}
	state, err := store.State(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateClosed {
		t.Errorf("state of the superseded deferred task = %s, want closed", state)
	}
	stored, err := store.GetTask(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := linkTarget(stored.Links, model.LinkSupersededBy); got != replacement.ID {
		t.Errorf("superseded-by link = %q, want the replacement (%s)", got, replacement.ID)
	}
}

// The state of the store at the moment of writing is what decides it,
// not the state a moment earlier: nothing holds the dispatch loop still
// between a proposal being written and its supersede landing, and the
// one thing this path promises is that it never closes a task somebody
// is already running.
func TestSupersedeChecksAgainAtTheMomentItWrites(t *testing.T) {
	store, ctx := open(t)
	old := queuedTask(t, store, ctx, "465", &widgets)
	replacement := queuedTask(t, store, ctx, "471", &widgets)

	// It qualified a moment ago; by the time the close runs it has been
	// dispatched.
	if _, refusal, err := store.SupersedeCandidate(ctx, old.ID, &widgets); err != nil {
		t.Fatal(err)
	} else if refusal != "" {
		t.Fatalf("refusal = %q before the run started, want none", refusal)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "465-1", TaskID: old.ID, Sandbox: "s1", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	result, err := store.Supersede(ctx, old.ID, replacement, "director", now)
	refusal := result.Refusal
	if err != nil {
		t.Fatal(err)
	}
	if refusal == "" {
		t.Fatal("the close went through on a task that had started running")
	}
	state, err := store.State(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state == model.StateClosed {
		t.Errorf("state = %s, want the running task left alone", state)
	}
	stored, err := store.GetTask(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := linkTarget(stored.Links, model.LinkSupersededBy); got != "" {
		t.Errorf("superseded-by link = %q on a task that was not superseded", got)
	}
}

// A name that resolves to nothing is a refusal like the rest rather than
// an error: the proposal behind it is real work, and it is filed with
// the note instead of being lost.
func TestSupersedeCandidateRefusesAnUnknownTask(t *testing.T) {
	store, ctx := open(t)
	old, refusal, err := store.SupersedeCandidate(ctx, "9999", &widgets)
	if err != nil {
		t.Fatalf("unknown task id = error %v, want a refusal", err)
	}
	if old != nil {
		t.Errorf("task = %+v, want none", old)
	}
	if !strings.Contains(refusal, "no task") {
		t.Errorf("refusal = %q, want it to say the id names no task", refusal)
	}
}

func linkTarget(links []model.Link, kind model.LinkKind) string {
	for _, l := range links {
		if l.Kind == kind {
			return l.Target
		}
	}
	return ""
}

// dependentOf files a queued task with the given links, the shape of a
// task waiting on another.
func dependentOf(t *testing.T, store *model.Store, ctx context.Context, id string,
	links ...model.Link) model.Task {

	t.Helper()
	task := queuedTask(t, store, ctx, id, &widgets)
	task.Links = links
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("linking %s: %v", id, err)
	}
	return task
}

func closeTask(t *testing.T, store *model.Store, ctx context.Context, id string) {
	t.Helper()
	if err := store.ObserveField(ctx, id, now, func(o *model.Observation) {
		o.ClosedAt = &now
	}); err != nil {
		t.Fatalf("closing %s: %v", id, err)
	}
}

func openBlockers(t *testing.T, store *model.Store, ctx context.Context, id string) int {
	t.Helper()
	n, err := store.OpenBlockers(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func storedLinks(t *testing.T, store *model.Store, ctx context.Context, id string) []model.Link {
	t.Helper()
	task, err := store.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return task.Links
}

func hasLink(links []model.Link, kind model.LinkKind, target string) bool {
	for _, l := range links {
		if l.Kind == kind && l.Target == target {
			return true
		}
	}
	return false
}

func commentBodies(t *testing.T, store *model.Store, ctx context.Context, id string) []string {
	t.Helper()
	comments, err := store.Comments(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range comments {
		out = append(out, c.Body)
	}
	return out
}

// A supersede is the replacement taking over the old task's job, so the
// tasks waiting on the old task go on waiting -- on the replacement --
// rather than being released by the close and dispatched alongside it
// (the director's supersedes of 22 Oct 2026). Both blocking kinds move,
// each keeping its kind, and each moved task says so on its own thread.
func TestSupersedeMovesOpenDependentsOntoTheReplacement(t *testing.T) {
	store, ctx := open(t)
	old := queuedTask(t, store, ctx, "465", &widgets)
	replacement := queuedTask(t, store, ctx, "471", &widgets)
	dependentOf(t, store, ctx, "480", model.Link{Kind: model.LinkDependsOn, Target: old.ID})
	dependentOf(t, store, ctx, "481", model.Link{Kind: model.LinkChildOf, Target: old.ID})

	result, err := store.Supersede(ctx, old.ID, replacement, "director", now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Refusal != "" {
		t.Fatalf("refusal = %q, want the supersede to happen", result.Refusal)
	}
	if got := strings.Join(result.Moved, ","); got != "480,481" {
		t.Errorf("moved = %q, want both dependents", got)
	}
	if len(result.Kept) != 0 {
		t.Errorf("kept = %+v, want none", result.Kept)
	}

	for id, kind := range map[string]model.LinkKind{"480": model.LinkDependsOn, "481": model.LinkChildOf} {
		links := storedLinks(t, store, ctx, id)
		if !hasLink(links, kind, replacement.ID) || hasLink(links, kind, old.ID) {
			t.Errorf("task %s links = %+v, want its %s link moved from %s to %s",
				id, links, kind, old.ID, replacement.ID)
		}
		if n := openBlockers(t, store, ctx, id); n != 1 {
			t.Errorf("task %s open blockers = %d after the supersede, want 1 (the replacement)", id, n)
		}
		bodies := commentBodies(t, store, ctx, id)
		if len(bodies) != 1 || !strings.Contains(bodies[0], "Now waits on task 471, which superseded task 465") {
			t.Errorf("task %s conversation = %q, want the one comment saying what it waits on now", id, bodies)
		}
	}

	closeTask(t, store, ctx, replacement.ID)
	if n := openBlockers(t, store, ctx, "480"); n != 0 {
		t.Errorf("open blockers = %d once the replacement closed, want 0", n)
	}
}

// The replacement may itself have been filed waiting on the task it
// replaces. Moving that link would point the replacement at itself, a
// wait nothing could ever end, so it stays where it is and the close
// satisfies it.
func TestSupersedeLeavesTheReplacementsOwnDependencyAlone(t *testing.T) {
	store, ctx := open(t)
	old := queuedTask(t, store, ctx, "465", &widgets)
	replacement := dependentOf(t, store, ctx, "471", model.Link{Kind: model.LinkDependsOn, Target: old.ID})

	result, err := store.Supersede(ctx, old.ID, replacement, "director", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Moved) != 0 {
		t.Errorf("moved = %v, want nothing", result.Moved)
	}
	if len(result.Kept) != 1 || result.Kept[0].ID != replacement.ID {
		t.Errorf("kept = %+v, want the replacement, with why", result.Kept)
	}
	links := storedLinks(t, store, ctx, replacement.ID)
	if hasLink(links, model.LinkDependsOn, replacement.ID) {
		t.Errorf("replacement links = %+v, want no link to itself", links)
	}
	if !hasLink(links, model.LinkDependsOn, old.ID) {
		t.Errorf("replacement links = %+v, want its link to %s left as it was", links, old.ID)
	}
	if n := openBlockers(t, store, ctx, replacement.ID); n != 0 {
		t.Errorf("replacement open blockers = %d, want 0 now its dependency is closed", n)
	}
}

// A dependent the replacement already waits on -- directly, or through
// another task -- cannot be moved onto the replacement without the two
// waiting on each other for good. It is left waiting on the closed task,
// which releases it, and its thread says why it was not moved.
func TestSupersedeDoesNotMoveADependentIntoACycle(t *testing.T) {
	for name, viaMiddle := range map[string]bool{"direct": false, "through another task": true} {
		t.Run(name, func(t *testing.T) {
			store, ctx := open(t)
			old := queuedTask(t, store, ctx, "465", &widgets)
			dependentOf(t, store, ctx, "480", model.Link{Kind: model.LinkDependsOn, Target: old.ID})
			waitsOn := "480"
			if viaMiddle {
				dependentOf(t, store, ctx, "490", model.Link{Kind: model.LinkDependsOn, Target: "480"})
				waitsOn = "490"
			}
			replacement := dependentOf(t, store, ctx, "471",
				model.Link{Kind: model.LinkDependsOn, Target: waitsOn})

			result, err := store.Supersede(ctx, old.ID, replacement, "director", now)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Moved) != 0 {
				t.Errorf("moved = %v, want nothing", result.Moved)
			}
			if len(result.Kept) != 1 || result.Kept[0].ID != "480" ||
				!strings.Contains(result.Kept[0].Reason, "wait on each other") {
				t.Errorf("kept = %+v, want 480 with the cycle as its reason", result.Kept)
			}
			links := storedLinks(t, store, ctx, "480")
			if !hasLink(links, model.LinkDependsOn, old.ID) || hasLink(links, model.LinkDependsOn, replacement.ID) {
				t.Errorf("480 links = %+v, want them left as they were", links)
			}
			if n := openBlockers(t, store, ctx, "480"); n != 0 {
				t.Errorf("480 open blockers = %d, want it released by the close", n)
			}
			bodies := commentBodies(t, store, ctx, "480")
			if len(bodies) != 1 || !strings.Contains(bodies[0], "was not moved") {
				t.Errorf("480 conversation = %q, want one comment saying why it was not moved", bodies)
			}
		})
	}
}

// A closed task has nothing left to wait for, so a supersede leaves it
// exactly as it was: no moved link, and no comment.
func TestSupersedeLeavesClosedDependentsAlone(t *testing.T) {
	store, ctx := open(t)
	old := queuedTask(t, store, ctx, "465", &widgets)
	replacement := queuedTask(t, store, ctx, "471", &widgets)
	dependentOf(t, store, ctx, "480", model.Link{Kind: model.LinkDependsOn, Target: old.ID})
	closeTask(t, store, ctx, "480")

	result, err := store.Supersede(ctx, old.ID, replacement, "director", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Moved) != 0 || len(result.Kept) != 0 {
		t.Errorf("result = %+v, want the closed dependent neither moved nor reported", result)
	}
	links := storedLinks(t, store, ctx, "480")
	if !hasLink(links, model.LinkDependsOn, old.ID) || hasLink(links, model.LinkDependsOn, replacement.ID) {
		t.Errorf("closed dependent links = %+v, want them untouched", links)
	}
	if bodies := commentBodies(t, store, ctx, "480"); len(bodies) != 0 {
		t.Errorf("closed dependent conversation = %q, want nothing added", bodies)
	}
}

// Only a supersede names a task to wait on instead. A plain close -- a
// human's cancel -- releases the tasks waiting on it, as it always has.
func TestAPlainCloseStillReleasesDependents(t *testing.T) {
	store, ctx := open(t)
	old := queuedTask(t, store, ctx, "465", &widgets)
	dependentOf(t, store, ctx, "480", model.Link{Kind: model.LinkDependsOn, Target: old.ID})
	if n := openBlockers(t, store, ctx, "480"); n != 1 {
		t.Fatalf("open blockers = %d before the close, want 1", n)
	}

	closeTask(t, store, ctx, old.ID)

	if n := openBlockers(t, store, ctx, "480"); n != 0 {
		t.Errorf("open blockers = %d after a plain close, want 0", n)
	}
	if links := storedLinks(t, store, ctx, "480"); !hasLink(links, model.LinkDependsOn, old.ID) {
		t.Errorf("links = %+v, want the link to the closed task left in place", links)
	}
}

// A refused supersede closes nothing, so it moves nothing either.
func TestARefusedSupersedeMovesNoDependents(t *testing.T) {
	store, ctx := open(t)
	old := queuedTask(t, store, ctx, "465", &widgets)
	dependentOf(t, store, ctx, "480", model.Link{Kind: model.LinkDependsOn, Target: old.ID})
	elsewhere := model.RepoRef{Owner: "acme", Name: "gadgets"}
	replacement := queuedTask(t, store, ctx, "471", &elsewhere)

	result, err := store.Supersede(ctx, old.ID, replacement, "director", now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Refusal == "" {
		t.Fatal("no refusal for a replacement in another repository, want one")
	}
	if len(result.Moved) != 0 {
		t.Errorf("moved = %v, want nothing", result.Moved)
	}
	if links := storedLinks(t, store, ctx, "480"); !hasLink(links, model.LinkDependsOn, old.ID) {
		t.Errorf("links = %+v, want them untouched", links)
	}
}
