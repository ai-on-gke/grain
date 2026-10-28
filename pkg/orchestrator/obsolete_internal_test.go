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

package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func goneBase(repo model.RepoRef, base string) *baseGoneError {
	return &baseGoneError{Repo: repo, Base: base, err: errors.New("orchestrator: cloning " + repo.String() +
		" into work: grain: base branch '" + base + "' does not exist on " + repo.String())}
}

// Tasks 1728, 1748 and 1753 failed four and five times each against a base
// that had merged and been deleted. A gone base stays gone, so the first
// failure is the last: a review is closed, since there is nothing left
// for it to read, and is never offered to dispatch again.
func TestReviewWhoseBaseIsGoneIsClosedAtTheFirstFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	now := time.Date(2026, 10, 3, 18, 53, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	bot := model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}}
	review := model.Task{
		ID: "1753", Title: "Security review: something", Target: &repo, Base: model.BranchName("1736"),
		Origin:   model.Origin{Attribution: bot, Reason: model.ReasonReview},
		Approval: &bot, Binding: model.BindingDirective,
		Links: []model.Link{{Kind: model.LinkProposedBy, Target: "1736"}},
	}
	if err := store.PutTask(ctx, review); err != nil {
		t.Fatal(err)
	}

	if err := stopOnMissingBase(ctx, store, review, goneBase(repo, review.Base), now); err != nil {
		t.Fatal(err)
	}
	if st, err := store.State(ctx, review.ID); err != nil || st != model.StateClosed {
		t.Fatalf("state = %q (%v), want closed", st, err)
	}
	comments, err := store.Comments(ctx, review.ID)
	if err != nil || len(comments) != 1 {
		t.Fatalf("comments = %+v (%v), want one", comments, err)
	}
	for _, want := range []string{"1736", "grain/task-1736", "nothing left to review"} {
		if !strings.Contains(comments[0].Body, want) {
			t.Errorf("comment %q does not mention %q", comments[0].Body, want)
		}
	}
}

// Any other task is parked on a comment: where it should point now is a
// human's call, and a reply puts it back in the queue.
func TestTaskWhoseBaseIsGoneIsParkedAtTheFirstFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	now := time.Date(2026, 10, 3, 18, 53, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	human := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
	task := model.Task{
		ID: "t1", Title: "stacked work", Target: &repo, Base: "grain/task-9",
		Origin:   model.Origin{Attribution: human, Reason: model.ReasonDirect},
		Approval: &human, Binding: model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	if err := stopOnMissingBase(ctx, store, task, goneBase(repo, task.Base), now); err != nil {
		t.Fatal(err)
	}
	if st, err := store.State(ctx, task.ID); err != nil || st != model.StateAwaitingReply {
		t.Fatalf("state = %q (%v), want awaiting_reply -- not queued for another attempt", st, err)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil || len(comments) != 1 {
		t.Fatalf("comments = %+v (%v), want one", comments, err)
	}
	for _, want := range []string{"grain/task-9", "acme/widgets", "does not exist", "reply"} {
		if !strings.Contains(comments[0].Body, want) {
			t.Errorf("comment %q does not mention %q", comments[0].Body, want)
		}
	}
}

func TestBaseGoneFatallyIsOnlyTheFatalArm(t *testing.T) {
	t.Parallel()
	fatal := "grain: base branch 'grain/task-1' does not exist on acme/widgets -- it may have been merged and deleted; retarget this task at a branch that exists"
	if !baseGoneFatally(fatal, "grain/task-1") {
		t.Error("the fatal arm was not recognised")
	}
	if baseGoneFatally(fatal+"\ngrain: continuing 'grain/task-2', which is already on the remote", "grain/task-1") {
		t.Error("the survivable arm was read as fatal")
	}
	if baseGoneFatally("fatal: could not read from remote repository", "grain/task-1") {
		t.Error("an unrelated clone failure was read as a missing base")
	}
}
