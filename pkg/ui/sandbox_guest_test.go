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

// guestHealth is the sandbox-guest half of GET /api/sandboxes, which is
// also what read_deployment_health hands an agent.
type guestHealth struct {
	SandboxGuest *ui.SandboxGuestPublishing `json:"sandboxGuest"`
}

func guestServer(t *testing.T, image string) (*ui.Server, *ui.Client, *model.Store) {
	t.Helper()
	client, store, _ := testClient(t)
	client.Config.Sandboxes = fakeSandboxHealth{}
	client.Config.SandboxImage = &ui.SandboxImage{Image: image, Expected: image}
	return ui.NewServerWithClient(client), client, store
}

// The reading that was missing (grain/task-397): whether anything is
// still publishing the sandbox guest this deployment runs.
//
// The health response already named the guest image, and on 2026-09-20
// that was all anybody could see -- `guest:sha-7746d62`, a day old,
// with nothing to say that the repository's release switch was off and
// so no merge had published a newer one. Every branch's CI paid for
// that in an inline ~1GB guest build; the switch and the cost were
// three readings apart and nobody joined them up.
func TestSandboxHealthSaysWhenNothingIsPublishingTheSandboxGuest(t *testing.T) {
	t.Parallel()
	srv, _, store := guestServer(t, "ghcr.io/bwsalmon/grain/guest:sha-7746d62")
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}

	// Releases on: the cache follows the branch, and the pane says so
	// rather than warning about a healthy deployment.
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62f1c0b4a5e", RunID: "run-1",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	got := decode[guestHealth](t, do(t, srv, http.MethodGet, "/api/sandboxes", "")).SandboxGuest
	if got == nil {
		t.Fatal("sandboxGuest = nil, want the guest reading for a deployment that names an image")
	}
	if got.Repo != repo.String() || !got.Publishing {
		t.Fatalf("sandboxGuest = %+v, want %s publishing", got, repo)
	}
	// The guest is the one main's own tip built, which is the "matches"
	// case: a reading that cried stale here would cry it permanently.
	if got.Behind || got.GuestCommit != "7746d62" || got.Branch != "main" {
		t.Fatalf("sandboxGuest = %+v, want the guest built from main's recorded tip", got)
	}
	if !strings.Contains(got.Detail, "publish the sandbox guest") {
		t.Errorf("detail = %q, want it to say the guest is still being published", got.Detail)
	}

	// Releases off: the same image, the same tip, and now a reading that
	// says what that costs and how to fill the cache without turning
	// publishing back on.
	if err := store.PutRepoConfig(t.Context(), model.RepoConfig{
		Repo: repo, Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}
	got = decode[guestHealth](t, do(t, srv, http.MethodGet, "/api/sandboxes", "")).SandboxGuest
	if got == nil || got.Publishing {
		t.Fatalf("sandboxGuest = %+v, want publishing false once releases are off", got)
	}
	for _, want := range []string{
		"Release workflows are switched off for bwsalmon/grain",
		"main",
		"builds one inline",
		".grain/workflows/image.yml:guest-cache",
	} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail = %q, want it to mention %q", got.Detail, want)
		}
	}
}

// And the other half of "how far behind": a guest built from a commit
// that is not the branch's tip says so, whether or not publishing is on.
func TestSandboxHealthSaysWhenTheGuestIsNotTheBranchTipsOwn(t *testing.T) {
	t.Parallel()
	srv, _, store := guestServer(t, "ghcr.io/bwsalmon/grain/guest:sha-7746d62")
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "375d7cce3aa1", RunID: "run-1",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	got := decode[guestHealth](t, do(t, srv, http.MethodGet, "/api/sandboxes", "")).SandboxGuest
	if got == nil || !got.Behind || got.BranchCommit != "375d7cce3aa1" {
		t.Fatalf("sandboxGuest = %+v, want it behind main's recorded tip", got)
	}
	// Behind with publishing on is an ordinary gap, not an alarm: most
	// merges change none of the guest inputs, so the guest an older
	// commit built is still the one this tree's key resolves to.
	if !strings.Contains(got.Detail, "an ordinary gap") {
		t.Errorf("detail = %q, want a gap under a healthy publisher read as ordinary", got.Detail)
	}
}

// A guest reference this deployment cannot attribute to a repository is
// left out entirely rather than reported against a guess: naming the
// wrong repository's release switch as the cause of somebody's slow CI
// is worse than the silence this reading replaces.
func TestSandboxHealthReportsNoGuestForAnUnattributableImage(t *testing.T) {
	t.Parallel()
	srv, client, _ := guestServer(t, "example.invalid/some-other-guest:v1")

	if got := decode[guestHealth](t, do(t, srv, http.MethodGet, "/api/sandboxes", "")).SandboxGuest; got != nil {
		t.Fatalf("sandboxGuest = %+v, want nothing for an image no repository owns", got)
	}
	// Nor for a deployment with no sandbox image at all -- a
	// host-directory backend, or a UI not colocated with a pool.
	client.Config.SandboxImage = nil
	if body := do(t, srv, http.MethodGet, "/api/sandboxes", "").Body.String(); strings.Contains(body, "sandboxGuest") {
		t.Errorf("body = %s, want no guest reading where there is no guest to name", body)
	}
}

// The reading this one was missing (grain/task-230): a release switch
// that is on, and nothing coming out from under it.
//
// This is the failure that ran for three days with nobody driving it.
// `.grain/workflows/release.yml`'s images job failed on every merge to
// main from the 15th of September to the 19th, releases switched on the
// whole time, so nothing published `guest:inputs-<key>` after the 16th
// and every branch in flight built a ~1GB guest inline instead. The
// pane's answer to "is anything publishing a guest" was read off the
// switch alone, so for all three days it said yes.
func TestSandboxHealthSaysWhenTheReleaseRunHasStoppedPublishing(t *testing.T) {
	t.Parallel()
	srv, _, store := guestServer(t, "ghcr.io/bwsalmon/grain/guest:sha-7746d62")
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	finished := time.Date(2026, 9, 16, 15, 22, 0, 0, time.UTC)

	// A run still going is not a run that failed to publish: it has not
	// published *yet*, which is not a state worth warning about.
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62f1c0b4a5e", RunID: "run-1",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	got := decode[guestHealth](t, do(t, srv, http.MethodGet, "/api/sandboxes", "")).SandboxGuest
	if got == nil || got.Stalled || got.LastPublish != "" {
		t.Fatalf("sandboxGuest = %+v, want nothing said about a release run still in flight", got)
	}

	// Failed: the switch is on, the guest is not being published, and
	// the line says so -- with what the run said, and that the merge
	// queue is already holding the branch over it.
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62f1c0b4a5e", RunID: "run-1",
		StartedAt: finished.Add(-30 * time.Minute), FinishedAt: &finished,
		Conclusion: "failure", Detail: "release images failed at the container e2e suite",
	}); err != nil {
		t.Fatal(err)
	}
	got = decode[guestHealth](t, do(t, srv, http.MethodGet, "/api/sandboxes", "")).SandboxGuest
	if got == nil || !got.Publishing || !got.Stalled {
		t.Fatalf("sandboxGuest = %+v, want publishing on and stalled", got)
	}
	if got.LastPublish != "failure" || got.LastPublishCommit != "7746d62f1c0b4a5e" ||
		got.LastPublishAt == nil || !got.LastPublishAt.Equal(finished) {
		t.Fatalf("sandboxGuest = %+v, want the failed run named, with its commit and time", got)
	}
	for _, want := range []string{
		"switched on for bwsalmon/grain",
		"last release run on main",
		"failed",
		"published nothing",
		"2026-09-16 15:22Z",
		"builds one inline",
		"release images failed at the container e2e suite",
		"files a repair task",
	} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail = %q, want it to mention %q", got.Detail, want)
		}
	}

	// Cancelled is the sharper hole and the one this deployment's main
	// was actually in: it is not a verdict on the tree, so the merge
	// queue holds nothing and files nothing, nobody is named, and no
	// guest is published either. This line is the only trace, so it is
	// the one that has to name the way to start another run.
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62f1c0b4a5e", RunID: "run-2",
		StartedAt: finished.Add(-30 * time.Minute), FinishedAt: &finished,
		Conclusion: "cancelled",
	}); err != nil {
		t.Fatal(err)
	}
	got = decode[guestHealth](t, do(t, srv, http.MethodGet, "/api/sandboxes", "")).SandboxGuest
	if got == nil || !got.Stalled || got.LastPublish != "cancelled" {
		t.Fatalf("sandboxGuest = %+v, want a cancelled release run read as publishing nothing", got)
	}
	for _, want := range []string{
		"was cancelled before it finished",
		"not a verdict on the tree",
		"no repair task was filed",
		"POST /api/repos/bwsalmon/grain/release-runs",
	} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail = %q, want it to mention %q", got.Detail, want)
		}
	}

	// A success says nothing new: the ordinary reading is restored
	// rather than a green run earning a second sentence of its own.
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62f1c0b4a5e", RunID: "run-3",
		StartedAt: finished.Add(-30 * time.Minute), FinishedAt: &finished,
		Conclusion: "success",
	}); err != nil {
		t.Fatal(err)
	}
	got = decode[guestHealth](t, do(t, srv, http.MethodGet, "/api/sandboxes", "")).SandboxGuest
	if got == nil || got.Stalled || got.LastPublish != "success" {
		t.Fatalf("sandboxGuest = %+v, want a published release read as publishing", got)
	}
	if !strings.Contains(got.Detail, "publish the sandbox guest") {
		t.Errorf("detail = %q, want the ordinary reading back once a release publishes", got.Detail)
	}
}

// Releases switched off and the last run red is one silence, not two.
//
// The reading for a switched-off publisher already says the cache has
// stopped following the branch and how to fill it; adding "and the last
// run failed" to that would be a second sentence about the same absence,
// and would bury the remedy that is the useful half of the first.
func TestSandboxHealthDoesNotReportAStalledReleaseWhenReleasesAreOff(t *testing.T) {
	t.Parallel()
	srv, _, store := guestServer(t, "ghcr.io/bwsalmon/grain/guest:sha-7746d62")
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	finished := time.Now().UTC()
	if err := store.PutRepoConfig(t.Context(), model.RepoConfig{
		Repo: repo, Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62f1c0b4a5e", RunID: "run-1",
		StartedAt: finished.Add(-time.Hour), FinishedAt: &finished, Conclusion: "failure",
	}); err != nil {
		t.Fatal(err)
	}

	got := decode[guestHealth](t, do(t, srv, http.MethodGet, "/api/sandboxes", "")).SandboxGuest
	if got == nil || got.Publishing || got.Stalled {
		t.Fatalf("sandboxGuest = %+v, want the switch reading alone when releases are off", got)
	}
	if !strings.Contains(got.Detail, "Release workflows are switched off") {
		t.Errorf("detail = %q, want the switched-off reading kept whole", got.Detail)
	}
}

// The same gap one level down (grain/task-547): the dispatch that fills
// the cache when releases are off, and what happens when *it* is the
// thing publishing nothing.
//
// On 2026-09-21 this deployment's own main was in exactly that state.
// Releases were off, the guest cache job was following main of its own
// accord as designed, and the dispatch grain made for main's tip errored
// on a sandbox that never came up. Nothing read the row that recorded it
// -- model.BaseBranchDispatch.Conclusion is written down "so that 'the
// cache is cold' has somewhere to be read from", and until this nothing
// read from it -- so the pane went on saying the cache follows main,
// which is the same inference-for-a-fact this file's header was written
// about.
func TestSandboxHealthSaysWhenThePostMergeGuestCacheDispatchFilledNothing(t *testing.T) {
	t.Parallel()
	srv, _, store := guestServer(t, "ghcr.io/bwsalmon/grain/guest:sha-7746d62")
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	finished := time.Date(2026, 9, 21, 20, 4, 0, 0, time.UTC)

	// Releases off with package-publish granted: grain dispatches the
	// cache job on main without anybody naming it
	// (model.GuestCacheFollowsBaseBranch), so this is the deployment's
	// own shape rather than a contrived one.
	if err := store.PutRepoConfig(t.Context(), model.RepoConfig{
		Repo: repo, CICapabilities: []string{model.CapabilityPackagePublish},
		Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62f1c0b4a5e", RunID: "run-1",
		StartedAt: finished.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// A dispatch still going has filled nothing *yet*, which is not the
	// same claim as having failed to -- LastPublish's own rule, kept.
	dispatch := model.BaseBranchDispatch{
		Repo: repo, Branch: "main", Commit: "bc9e4d4a8f0e", RunID: "dispatch-1",
		Jobs: []string{model.SandboxGuestCacheJob}, StartedAt: finished.Add(-30 * time.Minute),
	}
	if err := store.PutBaseBranchDispatch(t.Context(), dispatch); err != nil {
		t.Fatal(err)
	}
	got := decode[guestHealth](t, do(t, srv, http.MethodGet, "/api/sandboxes", "")).SandboxGuest
	if got == nil || !got.CacheDispatched || got.CacheStalled || got.CacheDispatch != "" {
		t.Fatalf("sandboxGuest = %+v, want nothing said about a dispatch still in flight", got)
	}

	// Errored: the sandbox never came up, so no guest was built and no
	// `guest:inputs-` tag was pushed for that tree. Nothing retries it
	// for the same commit, so the line has to say that and name the
	// dispatch that would.
	dispatch.FinishedAt, dispatch.Conclusion = &finished, "errored"
	dispatch.Detail = "sandbox guest cache: the sandbox for this job never came up"
	if err := store.PutBaseBranchDispatch(t.Context(), dispatch); err != nil {
		t.Fatal(err)
	}
	got = decode[guestHealth](t, do(t, srv, http.MethodGet, "/api/sandboxes", "")).SandboxGuest
	if got == nil || !got.CacheStalled || got.CacheDispatch != "errored" {
		t.Fatalf("sandboxGuest = %+v, want the failed cache dispatch reported", got)
	}
	if got.CacheDispatchCommit != "bc9e4d4a8f0e" || got.CacheDispatchAt == nil ||
		!got.CacheDispatchAt.Equal(finished) {
		t.Fatalf("sandboxGuest = %+v, want the commit and time that dispatch was for", got)
	}
	for _, want := range []string{
		".grain/workflows/image.yml:guest-cache grain dispatched on main (bc9e4d4)",
		"never ran",
		"nothing filled the content-addressed guest cache",
		"2026-09-21 20:04Z",
		"Nothing retries a dispatch for the same commit",
		"builds one inline",
		"sandbox guest cache: the sandbox for this job never came up",
		"grain ci run -repo bwsalmon/grain -branch main -job .grain/workflows/image.yml:guest-cache",
	} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail = %q, want it to mention %q", got.Detail, want)
		}
	}

	// And a dispatch that filled it: the ordinary reading back, with the
	// run that filled it as the evidence for it rather than the setting
	// alone.
	dispatch.Conclusion, dispatch.Detail = "success", ""
	if err := store.PutBaseBranchDispatch(t.Context(), dispatch); err != nil {
		t.Fatal(err)
	}
	got = decode[guestHealth](t, do(t, srv, http.MethodGet, "/api/sandboxes", "")).SandboxGuest
	if got == nil || got.CacheStalled || got.CacheDispatch != "success" {
		t.Fatalf("sandboxGuest = %+v, want a dispatch that filled the cache read as healthy", got)
	}
	for _, want := range []string{
		"goes on following",
		"The last one ran for bc9e4d4 and filled it on 2026-09-21 20:04Z",
	} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail = %q, want it to mention %q", got.Detail, want)
		}
	}

	// A post-merge dispatch of some other job says nothing about the
	// guest either way: a repository that names its own jobs has a row
	// here, and reading its failure as a cold guest cache would be a
	// warning about a cache that job was never filling.
	dispatch.Jobs, dispatch.Conclusion = []string{".grain/workflows/other.yml:publish-docs"}, "failure"
	if err := store.PutBaseBranchDispatch(t.Context(), dispatch); err != nil {
		t.Fatal(err)
	}
	got = decode[guestHealth](t, do(t, srv, http.MethodGet, "/api/sandboxes", "")).SandboxGuest
	if got == nil || got.CacheStalled || got.CacheDispatch != "" {
		t.Fatalf("sandboxGuest = %+v, want a dispatch that never carried the guest job left out", got)
	}
}

// The repository pane's half: the switch says, where it is, that it also
// governs the guest cache.
//
// It belongs beside the switch and not only in the health reading
// because the health reading is where an operator looks an hour later,
// after their branches have already spent that hour building guests. The
// pane is where the decision is taken.
func TestRepoReleaseSettingsSayWhenThisRepoPublishesTheSandboxGuest(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	client.Config.SandboxImage = &ui.SandboxImage{
		Image:    "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
		Expected: "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
	}
	srv := ui.NewServerWithClient(client)

	got := decode[ui.RepoReleaseSettings](t, do(t, srv, http.MethodGet, "/api/repos/bwsalmon/grain/release", ""))
	if !got.PublishesSandboxGuest || got.SandboxGuestCacheJob != ".grain/workflows/image.yml:guest-cache" {
		t.Fatalf("settings = %+v, want the guest publisher named with the job that fills the cache", got)
	}

	// Every other repository's release switch governs its own releases
	// and nothing of this deployment's sandboxes.
	other := decode[ui.RepoReleaseSettings](t, do(t, srv, http.MethodGet, "/api/repos/acme/widgets/release", ""))
	if other.PublishesSandboxGuest || other.SandboxGuestCacheJob != "" {
		t.Fatalf("settings = %+v, want no guest claim for a repository that publishes none", other)
	}
}

// And whether grain is already filling that cache without being asked
// (grain/task-457), which is what decides whether the pane warns or
// reports.
//
// The pane must not be able to disagree with the reconciler: it reads
// model.GuestCacheFollowsBaseBranch, the same expression
// orchestrator.startDueBaseBranchDispatch dispatches from, so "the cache
// is cold, go and name a job" cannot be shown for a branch grain is
// keeping warm.
func TestRepoReleaseSettingsSayWhenGrainFillsTheGuestCacheItself(t *testing.T) {
	t.Parallel()
	client, store, _ := testClient(t)
	client.Config.SandboxImage = &ui.SandboxImage{
		Image:    "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
		Expected: "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
	}
	srv := ui.NewServerWithClient(client)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}

	// Releases on: the release run fills the cache, so grain adds
	// nothing and the pane has nothing to claim.
	if err := store.PutRepoConfig(t.Context(), model.RepoConfig{
		Repo: repo, CICapabilities: []string{model.CapabilityPackagePublish},
	}); err != nil {
		t.Fatal(err)
	}
	got := decode[ui.RepoReleaseSettings](t, do(t, srv, http.MethodGet, "/api/repos/bwsalmon/grain/release", ""))
	if got.SandboxGuestCacheAutomatic {
		t.Fatal("the pane claims grain fills the cache while releases are on, when the release " +
			"run is what fills it: a second dispatch would be the same guest built twice")
	}

	// Releases off, and nothing named: this is the state an operator
	// leaves behind by pressing one button, and the cache follows the
	// branch anyway.
	if err := store.PutRepoConfig(t.Context(), model.RepoConfig{
		Repo: repo, CICapabilities: []string{model.CapabilityPackagePublish},
		Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}
	got = decode[ui.RepoReleaseSettings](t, do(t, srv, http.MethodGet, "/api/repos/bwsalmon/grain/release", ""))
	if got.Enabled || !got.SandboxGuestCacheAutomatic {
		t.Fatalf("settings = %+v, want the pane to say the guest cache goes on following the "+
			"branch with nobody having named the job", got)
	}
	if len(got.PostMergeDispatchJobs) != 0 {
		t.Fatalf("the pane shows %v in the operator's own list: what grain does for itself must "+
			"not look like something they asked for, or turning it off would mean deleting a "+
			"line that was never theirs", got.PostMergeDispatchJobs)
	}

	// And the one case still worth a warning: the job could not push, so
	// grain will not start one.
	if err := store.PutRepoConfig(t.Context(), model.RepoConfig{
		Repo: repo, Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}
	got = decode[ui.RepoReleaseSettings](t, do(t, srv, http.MethodGet, "/api/repos/bwsalmon/grain/release", ""))
	if got.SandboxGuestCacheAutomatic {
		t.Fatal("the pane claims grain fills the cache for a repository whose CI holds no " +
			"package-publish, where the job would stop on its first step")
	}
}
