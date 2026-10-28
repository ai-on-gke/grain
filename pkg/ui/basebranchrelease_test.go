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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A repository that has quietly stopped shipping (grain/task-511).
//
// The failure mode these pin is the one grain/task-461 left behind on
// purpose: a failed release holds nothing, files nothing and retries
// nothing, so the only thing that can notice one is somebody reading a
// surface. What is pinned is both halves of that -- the finding is made
// when a branch really has stopped publishing, and it is not made on a
// publisher having one bad afternoon, because a surface that cries every
// time a flaky release job goes red is one that gets scrolled past.

// publishHealth is the release half of GET /api/sandboxes, which is also
// what read_deployment_health hands an agent.
type publishHealth struct {
	BaseBranchPublishing []ui.BaseBranchPublishing `json:"baseBranchPublishing"`
}

func publishingOf(t *testing.T, srv *ui.Server) []ui.BaseBranchPublishing {
	t.Helper()
	return decode[publishHealth](t, do(t, srv, http.MethodGet, "/api/sandboxes", "")).BaseBranchPublishing
}

// The sentence the row was written for every cycle and nobody ever read:
// this repository is merging changes and shipping none of them, since a
// particular moment.
func TestSandboxHealthSaysWhenABaseBranchHasStoppedPublishing(t *testing.T) {
	t.Parallel()
	srv, store := gateServer(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// One failed release is noise. This deployment's publisher has been
	// flaky for weeks (grain/task-456) and the next merge asks again, so
	// nothing is said about it.
	first := baseTime.Add(-90 * time.Minute)
	finished := baseTime.Add(-70 * time.Minute)
	if err := store.PutBaseBranchRelease(t.Context(), model.BaseBranchRelease{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "rel-1",
		StartedAt: first, FinishedAt: &finished, Conclusion: "failure",
		Failures: 1, FailingSince: &first,
	}); err != nil {
		t.Fatal(err)
	}
	if got := publishingOf(t, srv); len(got) != 0 {
		t.Fatalf("baseBranchPublishing = %+v after one failed release, want none: a reading that "+
			"fires on every red release is one nobody reads", got)
	}

	// Three in a row is not a bad afternoon. Every merge since the first
	// of them has published nothing, and the newest release is still
	// going -- which is the ordinary state of a branch that keeps
	// merging, and must not hide the finding.
	third := baseTime.Add(-10 * time.Minute)
	if err := store.PutBaseBranchRelease(t.Context(), model.BaseBranchRelease{
		Repo: repo, Branch: "main", Commit: "9f21ab0", RunID: "rel-3",
		StartedAt: third, Failures: 3, FailingSince: &first,
	}); err != nil {
		t.Fatal(err)
	}
	got := publishingOf(t, srv)
	if len(got) != 1 {
		t.Fatalf("baseBranchPublishing = %+v, want main reported as shipping nothing", got)
	}
	found := got[0]
	if found.Repo != repo.String() || found.Branch != "main" || found.Failures != 3 {
		t.Fatalf("finding = %+v, want acme/widgets main with three failed releases", found)
	}
	if !found.RunInFlight || found.RunID != "rel-3" {
		t.Fatalf("finding = %+v, want the release in flight named for a reader to open", found)
	}
	for _, want := range []string{
		"Nothing has published from acme/widgets main since",
		"1h ago",
		"3 releases have been started since and every one of them published nothing",
		"merging changes and shipping none of them",
		"run rel-3",
	} {
		if !strings.Contains(found.Detail, want) {
			t.Errorf("detail = %q, want it to contain %q", found.Detail, want)
		}
	}
	// The one thing this surface must never say, in any wording: that
	// something is being held up. It is not, deliberately (grain/task-461),
	// and a reader talked into putting that hold back is the whole cost
	// of getting this wrong.
	if !strings.Contains(found.Detail, "Nothing is held up by that") {
		t.Errorf("detail = %q, want it to say that nothing is held up by this", found.Detail)
	}

	// And a release that publishes ends it: the streak is cleared on the
	// row, and the pane goes quiet without anybody dismissing anything.
	published := baseTime.Add(-2 * time.Minute)
	if err := store.PutBaseBranchRelease(t.Context(), model.BaseBranchRelease{
		Repo: repo, Branch: "main", Commit: "9f21ab0", RunID: "rel-3",
		StartedAt: third, FinishedAt: &published, Conclusion: "success",
	}); err != nil {
		t.Fatal(err)
	}
	if got := publishingOf(t, srv); len(got) != 0 {
		t.Fatalf("baseBranchPublishing = %+v once a release published, want none", got)
	}
}

// The repository that merges twice a week, which the count above cannot
// catch: one failed release is the whole of the evidence there is, and
// the repository has still shipped nothing for a day.
func TestSandboxHealthSaysWhenABranchHasPublishedNothingForADay(t *testing.T) {
	t.Parallel()
	srv, store := gateServer(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	since := baseTime.Add(-30 * time.Hour)
	finished := baseTime.Add(-29 * time.Hour)
	if err := store.PutBaseBranchRelease(t.Context(), model.BaseBranchRelease{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "rel-1",
		StartedAt: since, FinishedAt: &finished, Conclusion: "errored",
		Failures: 1, FailingSince: &since,
	}); err != nil {
		t.Fatal(err)
	}
	got := publishingOf(t, srv)
	if len(got) != 1 {
		t.Fatalf("baseBranchPublishing = %+v after a day of shipping nothing, want the branch "+
			"reported whatever the count says", got)
	}
	for _, want := range []string{
		"30h ago",
		"one release has been started since and it published nothing, the last of them ending errored",
		"Run rel-1 is the latest attempt",
		"no retry is owed to the commit that failed",
	} {
		if !strings.Contains(got[0].Detail, want) {
			t.Errorf("detail = %q, want it to contain %q", got[0].Detail, want)
		}
	}
}

// A repository whose releases an operator has switched off publishes
// nothing on purpose, and its row stops moving wherever the last release
// left it. Reporting that would be a warning about a deployment doing
// exactly what it was told -- the mistake basebranchgate.go had to be
// argued out of making about the same switch.
func TestNothingIsSaidAboutARepositoryWithReleasesSwitchedOff(t *testing.T) {
	t.Parallel()
	srv, store := gateServer(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	since := baseTime.Add(-40 * time.Hour)
	finished := baseTime.Add(-39 * time.Hour)
	if err := store.PutBaseBranchRelease(t.Context(), model.BaseBranchRelease{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "rel-1",
		StartedAt: since, FinishedAt: &finished, Conclusion: "failure",
		Failures: 4, FailingSince: &since,
	}); err != nil {
		t.Fatal(err)
	}
	if got := publishingOf(t, srv); len(got) != 1 {
		t.Fatalf("baseBranchPublishing = %+v, want the finding while this repository is trying "+
			"to publish", got)
	}
	if err := store.PutRepoConfig(t.Context(), model.RepoConfig{
		Repo: repo, Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}
	if got := publishingOf(t, srv); len(got) != 0 {
		t.Fatalf("baseBranchPublishing = %+v with releases switched off, want none: a repository "+
			"publishing nothing on purpose is not a repository that cannot", got)
	}
}

// The repository's own release pane carries the same finding, beside the
// switch that says whether a merge is meant to publish at all. The
// switch is a setting and reads the same on the day it was chosen; this
// is what has become of it.
func TestTheReleasePaneSaysWhenTheRepositoryHasStoppedShipping(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	srv := ui.NewServerWithClient(client)

	got := decode[ui.RepoReleaseSettings](t, do(t, srv, http.MethodGet,
		"/api/repos/acme/widgets/release", ""))
	if got.BaseBranchPublishing != nil {
		t.Fatalf("baseBranchPublishing = %+v on a repository that has published nothing at all, "+
			"want nil", got.BaseBranchPublishing)
	}

	since := baseTime.Add(-33 * time.Hour)
	finished := baseTime.Add(-32 * time.Hour)
	if err := store.PutBaseBranchRelease(ctx, model.BaseBranchRelease{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "rel-7",
		StartedAt: since, FinishedAt: &finished, Conclusion: "failure",
		Failures: 9, FailingSince: &since,
	}); err != nil {
		t.Fatal(err)
	}
	got = decode[ui.RepoReleaseSettings](t, do(t, srv, http.MethodGet,
		"/api/repos/acme/widgets/release", ""))
	if got.BaseBranchPublishing == nil {
		t.Fatal("baseBranchPublishing = nil, want the pane to carry the finding")
	}
	if got.BaseBranchPublishing.Branch != "main" || got.BaseBranchPublishing.Failures != 9 {
		t.Fatalf("baseBranchPublishing = %+v, want main's nine failed releases",
			got.BaseBranchPublishing)
	}
	if !strings.Contains(got.BaseBranchPublishing.Detail, "33h ago") {
		t.Errorf("detail = %q, want it to say how long this has been true",
			got.BaseBranchPublishing.Detail)
	}
}

func TestBaseBranchPublishingReportsActiveTriageTaskAndGating(t *testing.T) {
	t.Parallel()
	srv, store := gateServer(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	since := baseTime.Add(-2 * time.Hour)
	finished := baseTime.Add(-100 * time.Minute)
	if err := store.PutBaseBranchRelease(t.Context(), model.BaseBranchRelease{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "rel-1",
		StartedAt: since, FinishedAt: &finished, Conclusion: "failure",
		Failures: 3, FailingSince: &since,
	}); err != nil {
		t.Fatal(err)
	}

	// Put active triage task tagged with BaseReleaseTriageBranchTag
	triageTask := model.Task{
		ID:     "triage-99",
		Title:  "Fix base branch release on acme/widgets main",
		Target: &repo,
		Base:   "main",
		Tags:   []string{model.BaseReleaseTriageBranchTag(repo, "main")},
	}
	if err := store.PutTask(t.Context(), triageTask); err != nil {
		t.Fatal(err)
	}

	got := publishingOf(t, srv)
	if len(got) != 1 {
		t.Fatalf("baseBranchPublishing = %+v, want 1 entry", got)
	}
	found := got[0]
	if !found.Gated {
		t.Errorf("found.Gated = false, want true")
	}
	if found.TriageTaskID != "triage-99" {
		t.Errorf("found.TriageTaskID = %q, want triage-99", found.TriageTaskID)
	}
	if !strings.Contains(found.Detail, "Further releases on this branch are held pending resolution of triage task triage-99") {
		t.Errorf("found.Detail = %q, want mention of triage task triage-99 hold", found.Detail)
	}
}
