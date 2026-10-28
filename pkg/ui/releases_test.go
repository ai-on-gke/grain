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

// Release management's own tests (bwsalmon/agents#571), following
// client_test.go's own discipline: a real embedded SQLite store, no
// GitHub anywhere in sight -- CreateRelease, CutCandidate and
// PromoteCandidate only ever write a row; pkg/orchestrator.SyncReleases
// (its own tests) is what actually talks to GitHub.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

var widgets = model.RepoRef{Owner: "acme", Name: "widgets"}

// cutTestCandidate creates a fresh release named "myfeat" for repo,
// marks it provisioned (standing in for the releases reconciler, which
// none of these store-only tests run), and returns its own first
// candidate -- CreateRelease's own "also cuts its first candidate"
// (bwsalmon/agents#571).
func cutTestCandidate(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef) model.Candidate {
	t.Helper()
	r, err := store.CreateRelease(ctx, repo, "myfeat", baseTime)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := store.MarkReleaseProvisioned(ctx, r.ID); err != nil {
		t.Fatalf("mark provisioned: %v", err)
	}
	c, err := store.CurrentCandidateForRelease(ctx, r.ID)
	if err != nil || c == nil {
		t.Fatalf("current candidate: (%+v, %v)", c, err)
	}
	return *c
}

func TestCreateReleaseRecordsAFreshRelease(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	r, err := client.CreateRelease(ctx, widgets, ui.CreateReleaseRequest{Name: "myfeat"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Repo != "acme/widgets" || r.Name != "myfeat" || r.Status != "provisioning" {
		t.Fatalf("got %+v", r)
	}
	if r.LatestBranch != "myfeat.latest" || r.ProdBranch != "myfeat" {
		t.Fatalf("got %+v, want latestBranch myfeat.latest, prodBranch myfeat", r)
	}
}

func TestCreateReleaseRejectsAnEmptyName(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	if _, err := client.CreateRelease(ctx, widgets, ui.CreateReleaseRequest{}); err == nil {
		t.Fatal("expected a validation error for an empty name")
	}
}

func TestCreateReleaseRefusesAnUnmergedNameCollision(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	if _, err := client.CreateRelease(ctx, widgets, ui.CreateReleaseRequest{Name: "myfeat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateRelease(ctx, widgets, ui.CreateReleaseRequest{Name: "myfeat"}); err == nil {
		t.Fatal("expected a validation error cutting a second release under the same unmerged name")
	}
}

// A release is the instruction to push latest, rc and prod branches into
// its repo, so a repo off the target repo list is refused before
// anything is written -- the boundary PutQualificationPlan already draws
// (refuseOffRepoAllowlist) -- and ReleaseCreation, which the pane reads
// in place of offering the form, says the same thing.
func TestCreateReleaseRefusesARepoOffTheAllowlist(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets"})
	missing := model.RepoRef{Owner: "nobody", Name: "missing"}

	_, err := client.CreateRelease(ctx, missing, ui.CreateReleaseRequest{Name: "ghost"})
	if _, ok := err.(*ui.ValidationError); !ok {
		t.Fatalf("got %v (%T), want a ValidationError", err, err)
	}
	if !strings.Contains(err.Error(), "nobody/missing") || !strings.Contains(err.Error(), "acme/widgets") {
		t.Fatalf("refusal %q should name the repo and the list as it stands", err)
	}
	if list, err := store.ListReleases(ctx, missing); err != nil || len(list) != 0 {
		t.Fatalf("ListReleases = (%+v, %v), want nothing written", list, err)
	}

	got := client.ReleaseCreation(ctx, missing)
	if got.Allowed || got.Reason != err.Error() {
		t.Fatalf("ReleaseCreation = %+v, want refused with %q", got, err.Error())
	}

	if got := client.ReleaseCreation(ctx, widgets); !got.Allowed || got.Reason != "" {
		t.Fatalf("ReleaseCreation(widgets) = %+v, want allowed", got)
	}
	if _, err := client.CreateRelease(ctx, widgets, ui.CreateReleaseRequest{Name: "myfeat"}); err != nil {
		t.Fatalf("CreateRelease on the list: %v", err)
	}
}

// An empty list is an unrestricted deployment (model.RepoAllowed), so a
// release may be created anywhere.
func TestCreateReleaseIsUnrestrictedWithNoAllowlist(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, nil)
	anywhere := model.RepoRef{Owner: "someone", Name: "else"}
	if got := client.ReleaseCreation(ctx, anywhere); !got.Allowed {
		t.Fatalf("ReleaseCreation = %+v, want allowed", got)
	}
	if _, err := client.CreateRelease(ctx, anywhere, ui.CreateReleaseRequest{Name: "myfeat"}); err != nil {
		t.Fatal(err)
	}
}

func TestListReleasesListsEveryReleaseForARepo(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	if _, err := client.CreateRelease(ctx, widgets, ui.CreateReleaseRequest{Name: "myfeat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateRelease(ctx, widgets, ui.CreateReleaseRequest{Name: "2.1"}); err != nil {
		t.Fatal(err)
	}
	got, err := client.ListReleases(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d releases, want 2: %+v", len(got), got)
	}
}

func TestGetReleaseReturns404ForAnUnknownName(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	if _, err := client.GetRelease(ctx, widgets, "myfeat"); err == nil {
		t.Fatal("expected an error for an unknown release name")
	}
}

func TestCutCandidateRequiresAnActiveRelease(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	if _, err := client.CutCandidate(ctx, widgets, "myfeat"); err == nil {
		t.Fatal("expected an error cutting a candidate for a release that does not exist")
	}
}

func TestCutCandidateAllocatesTheFirstOne(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	if _, err := client.CreateRelease(ctx, widgets, ui.CreateReleaseRequest{Name: "myfeat"}); err != nil {
		t.Fatal(err)
	}
	r, err := store.GetRelease(ctx, widgets, "myfeat")
	if err != nil || r == nil {
		t.Fatalf("release: (%+v, %v)", r, err)
	}
	if err := store.MarkReleaseProvisioned(ctx, r.ID); err != nil {
		t.Fatal(err)
	}

	list, err := client.ListCandidates(ctx, widgets, "myfeat")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Branch != "myfeat.rc.1" || list[0].Status != "cutting" {
		t.Fatalf("got %+v, want the one candidate CreateRelease already cut", list)
	}
}

// CutCandidate refuses a second cut while the release's current candidate
// has not been promoted yet -- the issue's own "current rc" is singular.
func TestCutCandidateRefusesASecondWhileOneIsUnpromoted(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	cutTestCandidate(t, ctx, store, widgets)
	if _, err := client.CutCandidate(ctx, widgets, "myfeat"); err == nil {
		t.Fatal("expected an error cutting a second candidate")
	}
}

func TestPromoteCandidateRequiresOneToExist(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	if _, err := client.PromoteCandidate(ctx, widgets, "myfeat"); err == nil {
		t.Fatal("expected an error promoting a release that does not exist")
	}
}

// "It cannot be promoted twice."
func TestPromoteCandidateRefusesASecondPromotion(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	c := cutTestCandidate(t, ctx, store, widgets)
	if err := store.MarkCandidateCut(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	promoted, err := client.PromoteCandidate(ctx, widgets, "myfeat")
	if err != nil {
		t.Fatal(err)
	}
	if promoted.Status != "promoting" {
		t.Fatalf("got %+v", promoted)
	}
	if err := store.MarkCandidatePromoted(ctx, c.ID, baseTime); err != nil {
		t.Fatal(err)
	}

	if _, err := client.PromoteCandidate(ctx, widgets, "myfeat"); err == nil {
		t.Fatal("expected an error promoting an already-promoted candidate")
	}
}

func TestRequestReleaseMergeRequiresAnActiveRelease(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	if _, err := client.RequestReleaseMerge(ctx, widgets, "myfeat"); err == nil {
		t.Fatal("expected an error requesting a merge for a release that does not exist")
	}
	r, err := client.CreateRelease(ctx, widgets, ui.CreateReleaseRequest{Name: "myfeat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.RequestReleaseMerge(ctx, widgets, "myfeat"); err == nil {
		t.Fatal("expected an error requesting a merge while still provisioning")
	}
	got, err := store.GetRelease(ctx, widgets, "myfeat")
	if err != nil || got == nil {
		t.Fatalf("release: (%+v, %v)", got, err)
	}
	if err := store.MarkReleaseProvisioned(ctx, got.ID); err != nil {
		t.Fatal(err)
	}
	merging, err := client.RequestReleaseMerge(ctx, widgets, "myfeat")
	if err != nil {
		t.Fatal(err)
	}
	if merging.Status != "merge_requested" {
		t.Fatalf("got %+v", merging)
	}
	if r.Name != "myfeat" {
		t.Fatalf("sanity: %+v", r)
	}
}

// --- HTTP routes -----------------------------------------------------

func TestReleaseRoutesCreateGetAndList(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/repos/acme/widgets/releases", `{"name":"myfeat"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Release](t, rec)
	if created.Name != "myfeat" || created.Status != "provisioning" {
		t.Fatalf("created = %+v", created)
	}

	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/releases/myfeat", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[ui.Release](t, rec)
	if got.Name != "myfeat" {
		t.Fatalf("got = %+v", got)
	}

	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/releases", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if list := decode[[]ui.Release](t, rec); len(list) != 1 {
		t.Fatalf("got %+v, want exactly one release", list)
	}

	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/releases/does-not-exist", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}

	// DELETE removes the release and its candidates.
	rec = do(t, srv, http.MethodDelete, "/api/repos/acme/widgets/releases/myfeat", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/releases/myfeat", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status after delete = %d, want 404: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodDelete, "/api/repos/acme/widgets/releases/myfeat", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("second delete status = %d, want 400 (no such release): %s", rec.Code, rec.Body)
	}
}

func TestReleaseCreationRejectionsAre400(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPost, "/api/repos/acme/widgets/releases", `{"name":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// The walkthrough's own reproduction (grain/task-1423): with the list
// naming only acme/widgets, POST to another repo's releases used to
// answer 201 with a provisioning release.
func TestReleaseRoutesRefuseARepoOffTheAllowlist(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	setDefaultTeamTargetRepos(t, client.Store, ctx, []string{"acme/widgets"})

	rec := do(t, srv, http.MethodGet, "/api/repos/nobody/missing/release-creation", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("release-creation status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.ReleaseCreation](t, rec); got.Allowed || got.Reason == "" {
		t.Fatalf("release-creation = %+v, want refused with a reason", got)
	}

	rec = do(t, srv, http.MethodPost, "/api/repos/nobody/missing/releases", `{"name":"ghost"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create status = %d, want 400: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodGet, "/api/repos/nobody/missing/releases", "")
	if list := decode[[]ui.Release](t, rec); len(list) != 0 {
		t.Fatalf("got %+v, want no release written", list)
	}

	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/release-creation", "")
	if got := decode[ui.ReleaseCreation](t, rec); !got.Allowed {
		t.Fatalf("release-creation(acme/widgets) = %+v, want allowed", got)
	}
}

func TestCandidateRoutesCutAndPromote(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	if _, err := client.CreateRelease(context.Background(), widgets, ui.CreateReleaseRequest{Name: "myfeat"}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodGet, "/api/repos/acme/widgets/releases/myfeat/candidates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %s", rec.Code, rec.Body)
	}
	cut := decode[[]ui.Candidate](t, rec)
	if len(cut) != 1 || cut[0].Status != "cutting" || cut[0].Branch != "myfeat.rc.1" {
		t.Fatalf("got %+v", cut)
	}

	// Cutting a second one before the first has promoted is a caller
	// mistake -- 400, not 500.
	rec = do(t, srv, http.MethodPost, "/api/repos/acme/widgets/releases/myfeat/candidates", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("second cut status = %d, want 400: %s", rec.Code, rec.Body)
	}

	// Promoting before the releases reconciler has advanced the candidate
	// out of "cutting" is a caller mistake -- 400, not 500.
	rec = do(t, srv, http.MethodPost, "/api/repos/acme/widgets/releases/myfeat/candidates/promote", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("premature promote status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestRequestReleaseMergeRoute(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	if _, err := client.CreateRelease(ctx, widgets, ui.CreateReleaseRequest{Name: "myfeat"}); err != nil {
		t.Fatal(err)
	}

	// Still provisioning -- a caller mistake, 400.
	rec := do(t, srv, http.MethodPost, "/api/repos/acme/widgets/releases/myfeat/merge", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}
