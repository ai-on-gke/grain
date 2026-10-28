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
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// SyncBranches performs the GitHub-side effect every requested branch
// still declares but has not yet had carried out -- bwsalmon/agents#638's
// own "create a new branch on a repo... This should just create the given
// branch in github", and grain/task-176's own "if a branch already exists
// on a repo, we should be able to add it to grain": ui.Client.CreateBranch
// only ever writes a row, and this is what creates the branch at the
// repo's current default branch tip, or adopts the one already there, the
// same declarative-intent-then-reconcile split SyncReleases already holds
// pkg/ui to.
func SyncBranches(ctx context.Context, store *model.Store, client github.Client, now time.Time) error {
	var errs []error

	branches, err := store.PendingBranches(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading pending branches: %w", err)
	}
	for _, b := range branches {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := syncBranch(ctx, store, client, b); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// syncBranch gets b.Name onto grain's books: it creates the branch on
// GitHub at the current tip of b.From (the repo's default branch when
// that is empty), or -- if the name is
// already there -- adopts the ref that exists, grain/task-176's own "if a
// branch already exists on a repo, we should be able to add it to
// grain."
//
// Adoption touches nothing on GitHub. A name somebody pushed by hand
// points at commits grain knows nothing about, so the one thing this
// must never do is move it back to the default branch tip on the way to
// recording it; the existence check below is what keeps the create off
// that path entirely.
//
// A GitHub call failing here is recorded onto b's own LastError and
// reported as nil, not returned, the same "one pending row's own failure
// must not cost every other pending one this cycle" reasoning
// syncCandidate already documents.
func syncBranch(ctx context.Context, store *model.Store, client github.Client, b model.Branch) error {
	owner, repo := b.Repo.Owner, b.Repo.Name

	existing, err := client.GetBranchHead(owner, repo, b.Name)
	if err != nil {
		return recordBranchError(ctx, store, b.ID, fmt.Errorf("reading branch %q: %w", b.Name, err))
	}
	if existing != nil {
		return markBranchAdopted(ctx, store, b.ID)
	}

	// The branch to cut from: the one the request names (b.From,
	// grain/task-2317's new-task form), or the default branch when it
	// names none, which is every request the repo page makes.
	from, what := b.From, "base branch"
	if from == "" {
		def, err := client.DefaultBranch(owner, repo)
		if err != nil {
			return recordBranchError(ctx, store, b.ID, fmt.Errorf("reading default branch: %w", err))
		}
		from, what = def, "default branch"
	}
	head, err := client.GetBranchHead(owner, repo, from)
	if err != nil {
		return recordBranchError(ctx, store, b.ID, fmt.Errorf("reading %s %q: %w", what, from, err))
	}
	if head == nil {
		return recordBranchError(ctx, store, b.ID, fmt.Errorf("%s %q does not exist", what, from))
	}
	if err := client.CreateBranch(owner, repo, b.Name, head.SHA); err != nil {
		// Somebody may have pushed the same name between the check above
		// and this call, which GitHub answers with a 422 (CreateBranch's
		// own doc comment). A branch that exists is a branch to adopt
		// whether this found it before trying or by trying, so the race
		// gets the same answer the check does rather than an error a
		// human has to read and re-request their way out of.
		if raced, headErr := client.GetBranchHead(owner, repo, b.Name); headErr == nil && raced != nil {
			return markBranchAdopted(ctx, store, b.ID)
		}
		return recordBranchError(ctx, store, b.ID, fmt.Errorf("creating branch %q: %w", b.Name, err))
	}

	if err := store.MarkBranchCreated(ctx, b.ID); err != nil {
		return fmt.Errorf("orchestrator: marking branch %d created: %w", b.ID, err)
	}
	return nil
}

// markBranchAdopted records that b.Name was already on GitHub and grain
// has taken it as it stands. Unlike recordBranchError, a store failure
// here is the cycle's own to report: nothing about this branch is left to
// retry, so swallowing it would leave a row saying pending forever with
// nothing to say why.
func markBranchAdopted(ctx context.Context, store *model.Store, id int64) error {
	if err := store.MarkBranchAdopted(ctx, id); err != nil {
		return fmt.Errorf("orchestrator: marking branch %d adopted: %w", id, err)
	}
	return nil
}

// recordBranchError saves err onto the branch's own LastError for a UI to
// surface, and reports success to its own caller -- see syncBranch's own
// doc comment on why one branch's own failure must not cost every other
// pending one this cycle.
func recordBranchError(ctx context.Context, store *model.Store, id int64, err error) error {
	if markErr := store.MarkBranchError(ctx, id, err.Error()); markErr != nil {
		return fmt.Errorf("orchestrator: recording error on branch %d (%v): %w", id, err, markErr)
	}
	return nil
}
