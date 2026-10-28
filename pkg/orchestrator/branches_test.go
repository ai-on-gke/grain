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

package orchestrator_test

// SyncBranches' own tests (bwsalmon/agents#638), against a real bare git
// repo behind githubsim.Sim -- releases_test.go's own doc comment on why
// branch creation needs a real git repo behind it, not just a
// FakeTransport standing in for GitHub, applies equally here.

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestSyncBranchesCreatesAPendingBranchAtTheDefaultBranchTip(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	b, err := store.CreateBranch(ctx, repo, "myfeat", baseTime)
	if err != nil {
		t.Fatalf("create branch: %v", err)
	}

	if err := orchestrator.SyncBranches(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncBranches: %v", err)
	}

	mainHead := mustHead(t, client, "acme", "widgets", "main")
	if got := mustHead(t, client, "acme", "widgets", "myfeat"); got != mainHead {
		t.Fatalf("got branch tip %q, want it pinned at main's own tip %q", got, mainHead)
	}

	got, err := store.ListBranches(ctx, repo)
	if err != nil || len(got) != 1 {
		t.Fatalf("list branches: (%+v, %v)", got, err)
	}
	if got[0].ID != b.ID || got[0].Status != model.BranchCreated || got[0].LastError != "" {
		t.Fatalf("got %+v, want created with no error", got[0])
	}
}

func TestSyncBranchesIsIdempotentOnceCreated(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if _, err := store.CreateBranch(ctx, repo, "myfeat", baseTime); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if err := orchestrator.SyncBranches(ctx, store, client, baseTime); err != nil {
		t.Fatalf("first SyncBranches: %v", err)
	}
	// The branch already advanced to created, so a second run finds
	// nothing pending and must not try (and fail) to create it again.
	if err := orchestrator.SyncBranches(ctx, store, client, baseTime); err != nil {
		t.Fatalf("second SyncBranches: %v", err)
	}
}

func TestSyncBranchesAdoptsABranchThatAlreadyExistsOnGitHub(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// A branch somebody pushed by hand, ahead of main -- grain/task-176's
	// own "if a branch already exists on a repo, we should be able to add
	// it to grain."
	pushBranch(t, sim.BareRepo, "myfeat")
	existing := mustHead(t, client, "acme", "widgets", "myfeat")

	if _, err := store.CreateBranch(ctx, repo, "myfeat", baseTime); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if err := orchestrator.SyncBranches(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncBranches: %v", err)
	}

	got, err := store.ListBranches(ctx, repo)
	if err != nil || len(got) != 1 {
		t.Fatalf("list branches: (%+v, %v)", got, err)
	}
	if got[0].Status != model.BranchAdopted || got[0].LastError != "" {
		t.Fatalf("got %+v, want adopted with no error", got[0])
	}
	// Adoption is a bookkeeping act: the commits the branch already
	// pointed at are still what it points at, not the default branch tip
	// a create would have pinned it to.
	if now := mustHead(t, client, "acme", "widgets", "myfeat"); now != existing {
		t.Fatalf("got branch tip %q, want the existing %q left alone", now, existing)
	}
}

func TestSyncBranchesIsIdempotentOnceAdopted(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	pushBranch(t, sim.BareRepo, "myfeat")
	if _, err := store.CreateBranch(ctx, repo, "myfeat", baseTime); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if err := orchestrator.SyncBranches(ctx, store, client, baseTime); err != nil {
		t.Fatalf("first SyncBranches: %v", err)
	}
	// An adopted branch is as terminal as a created one: the next cycle
	// finds nothing pending and goes nowhere near GitHub for it.
	if err := orchestrator.SyncBranches(ctx, store, client, baseTime); err != nil {
		t.Fatalf("second SyncBranches: %v", err)
	}

	got, err := store.ListBranches(ctx, repo)
	if err != nil || len(got) != 1 {
		t.Fatalf("list branches: (%+v, %v)", got, err)
	}
	if got[0].Status != model.BranchAdopted {
		t.Fatalf("got status %q, want still adopted", got[0].Status)
	}
}

func TestSyncBranchesAdoptsTheDefaultBranchItself(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// "main" is the seeded default branch, and asking to create a fresh
	// branch by that name used to be the collision GitHub answers 422 to
	// (CreateBranch's own doc comment) and grain recorded as an error
	// forever. It is now the plainest case of adopting what is there.
	if _, err := store.CreateBranch(ctx, repo, "main", baseTime); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if err := orchestrator.SyncBranches(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncBranches: %v", err)
	}

	got, err := store.ListBranches(ctx, repo)
	if err != nil || len(got) != 1 {
		t.Fatalf("list branches: (%+v, %v)", got, err)
	}
	if got[0].Status != model.BranchAdopted || got[0].LastError != "" {
		t.Fatalf("got %+v, want adopted with no error", got[0])
	}
}

func TestSyncBranchesRecordsAnErrorWhenTheDefaultBranchIsMissing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if _, err := store.CreateBranch(ctx, repo, "myfeat", baseTime); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	run(t, sim.BareRepo, "git", "update-ref", "-d", "refs/heads/main")

	if err := orchestrator.SyncBranches(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncBranches: %v", err)
	}

	got, err := store.ListBranches(ctx, repo)
	if err != nil || len(got) != 1 {
		t.Fatalf("list branches: (%+v, %v)", got, err)
	}
	if got[0].Status != model.BranchPending || got[0].LastError == "" {
		t.Fatalf("got %+v, want still pending with an error recorded", got[0])
	}
}

// A request naming the branch to cut from (grain/task-2317's new-task
// form) is created at that branch's tip, not the default branch's.
func TestSyncBranchesCreatesAPendingBranchFromTheBranchItNames(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	pushCommitSaying(t, sim.BareRepo, "release/1.2", "Only on the release branch")

	if _, _, err := store.RequestBranchFrom(ctx, repo, "hotfix", "release/1.2", baseTime); err != nil {
		t.Fatalf("request branch: %v", err)
	}
	if err := orchestrator.SyncBranches(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncBranches: %v", err)
	}

	from := mustHead(t, client, "acme", "widgets", "release/1.2")
	if from == mustHead(t, client, "acme", "widgets", "main") {
		t.Fatal("release/1.2 should have moved past main for this test to mean anything")
	}
	if got := mustHead(t, client, "acme", "widgets", "hotfix"); got != from {
		t.Fatalf("got branch tip %q, want it cut at release/1.2's own tip %q", got, from)
	}
	got, err := store.ListBranches(ctx, repo)
	if err != nil || len(got) != 1 {
		t.Fatalf("list branches: (%+v, %v)", got, err)
	}
	if got[0].Status != model.BranchCreated || got[0].From != "release/1.2" {
		t.Fatalf("got %+v, want created from release/1.2", got[0])
	}
}

// A base that is not on GitHub leaves the request pending with the
// reason on it, rather than quietly cutting from the default branch.
func TestSyncBranchesRecordsAnErrorWhenTheNamedBaseIsMissing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if _, _, err := store.RequestBranchFrom(ctx, repo, "hotfix", "no-such-branch", baseTime); err != nil {
		t.Fatalf("request branch: %v", err)
	}
	if err := orchestrator.SyncBranches(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncBranches: %v", err)
	}
	got, err := store.ListBranches(ctx, repo)
	if err != nil || len(got) != 1 {
		t.Fatalf("list branches: (%+v, %v)", got, err)
	}
	if got[0].Status != model.BranchPending || !strings.Contains(got[0].LastError, "no-such-branch") {
		t.Fatalf("got %+v, want still pending with the missing base named", got[0])
	}
}
