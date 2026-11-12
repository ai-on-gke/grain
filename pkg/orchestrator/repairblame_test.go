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
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// blameClient is the three reads recordRepairBlame makes of the forge:
// the repair's own commits, the red range, and which pull request merged
// a commit.
type blameClient struct {
	github.Client
	prCommits []github.Commit
	compare   []github.Commit
	mergedBy  map[string]int // commit -> pull request number
}

func (c blameClient) ListPullRequestCommits(_, _ string, _ int) ([]github.Commit, error) {
	return c.prCommits, nil
}

func (c blameClient) CompareCommits(_, _, _, _ string) ([]github.Commit, error) {
	return c.compare, nil
}

func (c blameClient) MergedPullRequestForCommit(_, _, sha string) (*github.PullRequest, error) {
	if n, ok := c.mergedBy[sha]; ok {
		return &github.PullRequest{Number: n}, nil
	}
	return nil, nil
}

var blameRepo = model.RepoRef{Owner: "acme", Name: "widgets"}

const (
	blameGreen = "aaaa000000000000000000000000000000000000"
	blameRed   = "bbbb000000000000000000000000000000000000"
	blameOther = "cccc000000000000000000000000000000000000"
)

// putBlameFixture files the culprit -- task 10, whose pull request #5
// merged blameRed -- and repair, and returns the repair's pull request.
func putBlameFixture(t *testing.T, store *model.Store, repair model.Task) model.PullRequestRef {
	t.Helper()
	ctx := context.Background()
	culprit := model.Task{
		ID: "10", Title: "Change the widget", Target: &blameRepo,
		Links: []model.Link{{Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: blameRepo, Number: 5}.String()}},
	}
	if err := store.PutTask(ctx, culprit); err != nil {
		t.Fatal(err)
	}
	repair.Target = &blameRepo
	if err := store.PutTask(ctx, repair); err != nil {
		t.Fatal(err)
	}
	return model.PullRequestRef{Repo: blameRepo, Number: 9}
}

func blameLinks(t *testing.T, store *model.Store, id string, kind model.LinkKind) []string {
	t.Helper()
	task, err := store.GetTask(context.Background(), id)
	if err != nil || task == nil {
		t.Fatalf("reading %s: %v", id, err)
	}
	return model.TaskLinkTargets(*task, kind)
}

func wantBlame(t *testing.T, store *model.Store, repair, culprit string) {
	t.Helper()
	if got := blameLinks(t, store, repair, model.LinkBlames); !slices.Equal(got, []string{culprit}) {
		t.Errorf("%s blames %v, want [%s]", repair, got, culprit)
	}
	if got := blameLinks(t, store, culprit, model.LinkBlamedBy); !slices.Equal(got, []string{repair}) {
		t.Errorf("%s is blamed by %v, want [%s]", culprit, got, repair)
	}
}

func wantNoBlame(t *testing.T, store *model.Store, repair, culprit string) {
	t.Helper()
	if got := blameLinks(t, store, repair, model.LinkBlames); len(got) != 0 {
		t.Errorf("%s blames %v, want nothing", repair, got)
	}
	if got := blameLinks(t, store, culprit, model.LinkBlamedBy); len(got) != 0 {
		t.Errorf("%s is blamed by %v, want nothing", culprit, got)
	}
}

func baseRepair() model.Task {
	repair := model.Task{ID: "20", Title: "Fix grain CI on acme/widgets main", Target: &blameRepo, Base: "main"}
	model.SetFixesBaseBranch(&repair, true)
	return repair
}

// A base-branch repair whose red range is one commit blames the task that
// merged it, with nothing in its own commits to say so.
func TestBaseBranchRepairBlamesTheOnlyMergeInItsRedRange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	ref := putBlameFixture(t, store, baseRepair())
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: blameRepo, Branch: "main", Commit: blameRed, Conclusion: model.BaseBranchCIFailure,
		RedStreak: 1, RedStreakJobs: []string{"tests/go"}, RedStreakSince: blameRed, RedStreakTip: blameRed,
		LastGreenCommit: blameGreen,
	}); err != nil {
		t.Fatal(err)
	}
	client := blameClient{
		prCommits: []github.Commit{{SHA: "f1", Message: "fix: repair the widget test"}},
		compare:   []github.Commit{{SHA: blameRed, Parents: []string{blameGreen}}},
		mergedBy:  map[string]int{blameRed: 5},
	}
	task, _ := store.GetTask(ctx, "20")
	if err := recordRepairBlame(ctx, store, client, *task, ref, "main"); err != nil {
		t.Fatal(err)
	}
	wantBlame(t, store, "20", "10")
	// Merging again (a second observation of the same merge) adds nothing.
	if err := recordRepairBlame(ctx, store, client, *task, ref, "main"); err != nil {
		t.Fatal(err)
	}
	wantBlame(t, store, "20", "10")
}

// A red range holding more than one merge names nobody: the repair stays
// unattributed rather than blaming the last merge in it.
func TestBaseBranchRepairBlamesNobodyForAnAmbiguousRedRange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	ref := putBlameFixture(t, store, baseRepair())
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: blameRepo, Branch: "main", Commit: blameRed, Conclusion: model.BaseBranchCIFailure,
		RedStreak: 1, RedStreakJobs: []string{"tests/go"}, RedStreakSince: blameRed, RedStreakTip: blameRed,
		LastGreenCommit: blameGreen,
	}); err != nil {
		t.Fatal(err)
	}
	client := blameClient{
		prCommits: []github.Commit{{SHA: "f1", Message: "fix: repair the widget test"}},
		compare: []github.Commit{
			{SHA: blameOther, Parents: []string{blameGreen}},
			{SHA: blameRed, Parents: []string{blameOther}},
		},
		mergedBy: map[string]int{blameRed: 5, blameOther: 6},
	}
	task, _ := store.GetTask(ctx, "20")
	if err := recordRepairBlame(ctx, store, client, *task, ref, "main"); err != nil {
		t.Fatal(err)
	}
	wantNoBlame(t, store, "20", "10")
}

// A Stage 1 repair has no red range recorded; a Fixes-commit: trailer on
// its commits names the commit, and the task that merged it is blamed.
func TestStage1RepairBlamesTheMergeItsFixesCommitTrailerNames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repair := model.Task{ID: "20", Title: "Fix the red Stage 1 (Tests) at bbbb0000", Tags: []string{Stage1RepairTag(blameRepo)}}
	ref := putBlameFixture(t, store, repair)
	client := blameClient{
		prCommits: []github.Commit{{SHA: "f1", Message: "fix: repair the widget test\n\nBisected the red range.\n\nFixes-commit: bbbb000\nOrigin: task 20"}},
		mergedBy:  map[string]int{"bbbb000": 5},
	}
	if err := recordRepairBlame(ctx, store, client, repair, ref, "main"); err != nil {
		t.Fatal(err)
	}
	wantBlame(t, store, "20", "10")
}

// An [rc-blocker <tag>] task's Blames: trailer names the culprit outright.
func TestRCBlockerBlamesTheTaskItsBlamesTrailerNames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repair := model.Task{ID: "20", Title: model.RCBlockerMarker("v1.4.0-rc.2") + " widget test fails"}
	ref := putBlameFixture(t, store, repair)
	client := blameClient{
		prCommits: []github.Commit{
			{SHA: "f1", Message: "test: reproduce the widget failure"},
			{SHA: "f2", Message: "fix: repair the widget test\n\nBlames: task 10\nBlames: task 404"},
		},
	}
	if err := recordRepairBlame(ctx, store, client, repair, ref, "main"); err != nil {
		t.Fatal(err)
	}
	// Task 404 does not exist, so it is skipped rather than linked.
	wantBlame(t, store, "20", "10")
}

// A Blames: trailer is agent-written, so a task of another repository is
// not blamed by it: a repair reaches only merges into its own repository.
func TestRepairBlamesTrailerCannotNameAnotherRepositorysTask(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	other := model.RepoRef{Owner: "acme", Name: "gadgets"}
	if err := store.PutTask(ctx, model.Task{ID: "11", Title: "Change the gadget", Target: &other}); err != nil {
		t.Fatal(err)
	}
	repair := model.Task{ID: "20", Title: model.RCBlockerMarker("v1.4.0-rc.2") + " widget test fails"}
	ref := putBlameFixture(t, store, repair)
	client := blameClient{
		prCommits: []github.Commit{{SHA: "f1", Message: "fix: repair the widget test\n\nBlames: task 11"}},
	}
	if err := recordRepairBlame(ctx, store, client, repair, ref, "main"); err != nil {
		t.Fatal(err)
	}
	wantNoBlame(t, store, "20", "11")
}

// A trailer is the whole answer: a base-branch repair whose Fixes-commit
// names a commit no task merged blames nobody, even with a one-commit red
// range that would otherwise name task 10.
func TestRepairTrailerNamingAnUnmergedCommitBlamesNobody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	ref := putBlameFixture(t, store, baseRepair())
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: blameRepo, Branch: "main", Commit: blameRed, Conclusion: model.BaseBranchCIFailure,
		RedStreakSince: blameRed, LastGreenCommit: blameGreen,
	}); err != nil {
		t.Fatal(err)
	}
	client := blameClient{
		prCommits: []github.Commit{{SHA: "f1", Message: "fix: it\n\nFixes-commit: " + blameOther}},
		compare:   []github.Commit{{SHA: blameRed, Parents: []string{blameGreen}}},
		mergedBy:  map[string]int{blameRed: 5},
	}
	task, _ := store.GetTask(ctx, "20")
	if err := recordRepairBlame(ctx, store, client, *task, ref, "main"); err != nil {
		t.Fatal(err)
	}
	wantNoBlame(t, store, "20", "10")
}

// A task that is no repair is never read for a blame at all: its client
// here would panic on the first call.
func TestOrdinaryMergeRecordsNoBlame(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	task := model.Task{ID: "20", Title: "Add a widget"}
	ref := putBlameFixture(t, store, task)
	if err := recordRepairBlame(ctx, store, struct{ github.Client }{}, task, ref, "main"); err != nil {
		t.Fatal(err)
	}
	wantNoBlame(t, store, "20", "10")
}
