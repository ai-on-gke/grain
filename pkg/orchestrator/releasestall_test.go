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

// Releases gap 6: a merge into a repository on a forge other than
// GitHub starts its release, and one that does not is said out loud.
package orchestrator_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// fixtureForgeHost is a made-up forge off the default host, standing in
// for an edition's own git host.
const fixtureForgeHost = "forge.example.test"

// fixtureForge is the daemon's forge client as it answers for a
// repository off the default host: asked with the repository's stored
// owner ("host/owner"), it is routed to the repository's own forge and
// answers from there; asked with the bare owner it is GitHub answering
// for a repository GitHub does not have. down makes the forge itself
// unreachable.
type fixtureForge struct {
	github.Client // nil: anything else asked of it panics

	mu   sync.Mutex
	repo model.RepoRef
	def  string
	tips map[string]string
	down error
}

func newFixtureForge(repo model.RepoRef, def, tip string) *fixtureForge {
	return &fixtureForge{repo: repo, def: def, tips: map[string]string{def: tip}}
}

func (f *fixtureForge) ours(owner, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if owner != f.repo.StoredOwner() || name != f.repo.Name {
		return &github.Error{Status: 404, Body: []byte(`{"message":"Not Found"}`),
			URL: fmt.Sprintf("GET https://api.github.com/repos/%s/%s", owner, name)}
	}
	return f.down
}

func (f *fixtureForge) DefaultBranch(owner, name string) (string, error) {
	if err := f.ours(owner, name); err != nil {
		return "", err
	}
	return f.def, nil
}

func (f *fixtureForge) GetBranchHead(owner, name, branch string) (*github.BranchHead, error) {
	if err := f.ours(owner, name); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if sha := f.tips[branch]; sha != "" {
		return &github.BranchHead{SHA: sha}, nil
	}
	return nil, nil
}

func (f *fixtureForge) setDown(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = err
}

// WorkflowOptions is ci.Runner.WorkflowOptions: the workflows a test set
// with answerWorkflows, or NopRunner's refusal when it set none.
func (f *fakeCI) WorkflowOptions(ctx context.Context, req ci.Request) ([]ci.WorkflowOption, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.workflowOptions == nil {
		return ci.NopRunner{}.WorkflowOptions(ctx, req)
	}
	if req.Event != ci.EventRelease {
		return nil, nil
	}
	return f.workflowOptions, nil
}

func (f *fakeCI) answerWorkflows(options ...ci.WorkflowOption) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workflowOptions = append([]ci.WorkflowOption{}, options...)
}

var releaseWorkflow = ci.WorkflowOption{Path: ".grain/workflows/release.yml", Name: "release"}

func editionRepo() model.RepoRef {
	return model.RepoRef{Host: fixtureForgeHost, Owner: "acme", Name: "edition"}
}

func releaseStallNotification(t *testing.T, ctx context.Context, store *model.Store,
	repo model.RepoRef) *model.Notification {

	t.Helper()
	n, err := store.OpenNotification(ctx, model.NotificationSubject(model.FamilyReleaseNotStarting, repo.String()))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The gap itself: a merge recorded into an edition's trunk on its own
// forge is released, because which branch is trunk is asked of that
// forge rather than of GitHub.
func TestAMergeIntoTrunkOnAnotherForgeStartsItsRelease(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := editionRepo()
	forge := newFixtureForge(repo, "main", "1111111111111111111111111111111111111111")
	runner := newFakeCI()
	runner.answerWorkflows(releaseWorkflow)
	releasingRepo(t, ctx, store, repo)
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", StartedAt: baseTime, PendingTaskID: "t1", PendingMerges: 1,
	}); err != nil {
		t.Fatal(err)
	}

	runBaseBranchReleaseReconciler(t, ctx, store, forge, runner, baseTime)

	released := runner.releaseRuns()
	if len(released) != 1 {
		t.Fatalf("grain started %d release runs for the merge into %s main, want one: "+
			"its default branch has to be read from its own forge, not GitHub", len(released), repo)
	}
	if req := released[0]; req.Repo != repo.String() || req.Ref != "main" ||
		req.Commit != "1111111111111111111111111111111111111111" || !req.TrustedRelease {
		t.Fatalf("the release is %+v, want a trusted release of %s main at its tip", req, repo)
	}
	if n := releaseStallNotification(t, ctx, store, repo); n != nil {
		t.Fatalf("a release that started raised %q", n.Title)
	}
}

// A forge that cannot say which branch is trunk releases nothing, and
// that is no longer silent: past releaseNotStartingAfter it is one Inbox
// notification naming why, cleared once a release starts.
func TestAReleaseThatNeverStartsIsNotifiedAndClearedWhenItDoes(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := editionRepo()
	forge := newFixtureForge(repo, "main", "2222222222222222222222222222222222222222")
	forge.setDown(fmt.Errorf("the forge's API answered 502"))
	runner := newFakeCI()
	runner.answerWorkflows(releaseWorkflow)
	releasingRepo(t, ctx, store, repo)
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", StartedAt: baseTime, PendingTaskID: "t1", PendingMerges: 1,
	}); err != nil {
		t.Fatal(err)
	}

	runBaseBranchReleaseReconciler(t, ctx, store, forge, runner, baseTime)
	if n := releaseStallNotification(t, ctx, store, repo); n != nil {
		t.Fatalf("raised %q at once: a forge hiccup has to be given time to pass", n.Title)
	}
	at := baseTime.Add(45 * time.Minute)
	runBaseBranchReleaseReconciler(t, ctx, store, forge, runner, at)
	if len(runner.releaseRuns()) != 0 {
		t.Fatal("a release started without grain knowing which branch is trunk")
	}
	n := releaseStallNotification(t, ctx, store, repo)
	if n == nil {
		t.Fatal("45 minutes of merges releasing nothing raised no notification")
	}
	if !strings.Contains(n.Body, "default branch") || !strings.Contains(n.Body, "502") {
		t.Fatalf("the notification says %q, want it naming the default-branch read and its error", n.Body)
	}
	if n.Repo != repo.String() || n.Severity != model.NotificationWarning {
		t.Fatalf("the notification is %+v, want a warning on %s", n, repo)
	}

	forge.setDown(nil)
	at = at.Add(releaseStallRecheck)
	runBaseBranchReleaseReconciler(t, ctx, store, forge, runner, at)
	if len(runner.releaseRuns()) != 1 {
		t.Fatal("the forge answered again and no release started")
	}
	if n := releaseStallNotification(t, ctx, store, repo); n != nil {
		t.Fatalf("a release started and %q is still open", n.Title)
	}
}

// releaseStallRecheck is past the stall check's own interval
// (releaseStallCheckEvery).
const releaseStallRecheck = 6 * time.Minute

// A trunk no merge has enrolled -- base-branch CI never recorded one on
// this forge -- has no row for a release to follow, and the notification
// says that.
func TestATrunkWithNoBaseBranchRowIsNotified(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := editionRepo()
	forge := newFixtureForge(repo, "main", "3333333333333333333333333333333333333333")
	runner := newFakeCI()
	runner.answerWorkflows(releaseWorkflow)
	releasingRepo(t, ctx, store, repo)

	runBaseBranchReleaseReconciler(t, ctx, store, forge, runner, baseTime)
	runBaseBranchReleaseReconciler(t, ctx, store, forge, runner, baseTime.Add(time.Hour))
	n := releaseStallNotification(t, ctx, store, repo)
	if n == nil {
		t.Fatal("a trunk nothing releases raised no notification")
	}
	if !strings.Contains(n.Body, "no base-branch row") {
		t.Fatalf("the notification says %q, want it naming the missing base-branch row", n.Body)
	}
}

// A repository with no workflow answering `release` owes nothing, so
// nothing is raised for it however long its trunk goes unreleased.
func TestARepositoryWithNoReleaseWorkflowIsNotNotified(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := editionRepo()
	forge := newFixtureForge(repo, "main", "4444444444444444444444444444444444444444")
	runner := newFakeCI()
	runner.answerWorkflows()
	releasingRepo(t, ctx, store, repo)

	runBaseBranchReleaseReconciler(t, ctx, store, forge, runner, baseTime)
	runBaseBranchReleaseReconciler(t, ctx, store, forge, runner, baseTime.Add(time.Hour))
	if n := releaseStallNotification(t, ctx, store, repo); n != nil {
		t.Fatalf("a repository with no release workflow raised %q", n.Title)
	}
}
