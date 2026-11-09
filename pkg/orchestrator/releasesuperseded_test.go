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

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A burst of merges and the release runs it cancels (grain/task-3334).
//
// release.yml keeps one release run in flight and one pending per branch,
// so nine merges in seven minutes on 2026-11-06 ended eight runs
// `cancelled`, each superseded by a newer pending one -- and each was
// counted as a failed release: a not-publishing alert and release-fix
// tasks for a branch whose newest release went on to publish.

func releaseRun(id int64, sha, status, conclusion string, at time.Time) githubsim.WorkflowRun {
	return githubsim.WorkflowRun{
		ID: id, Name: "release", Path: ".github/workflows/release.yml",
		HeadBranch: "main", HeadSHA: sha, Event: "push",
		Status: status, Conclusion: conclusion, RunAttempt: 1, CreatedAt: at,
	}
}

func TestABurstOfSupersededReleaseRunsFilesNothingAndRaisesNoAlert(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	defer orchestrator.SetGitHubCIBudgetLow(false)()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	commits := []string{headSHA(t, client, repo, "main")}
	for range 4 {
		pushBranch(t, sim.BareRepo, "main")
		commits = append(commits, headSHA(t, client, repo, "main"))
	}
	// commits[0]'s release is running, [1]..[3] were cancelled as each
	// newer merge's run replaced the pending one, and [4]'s is pending.
	sim.WorkflowRuns = []githubsim.WorkflowRun{
		releaseRun(1301, commits[0], "in_progress", "", baseTime),
		releaseRun(1302, commits[1], "completed", "cancelled", baseTime.Add(time.Minute)),
		releaseRun(1303, commits[2], "completed", "cancelled", baseTime.Add(2*time.Minute)),
		releaseRun(1304, commits[3], "completed", "cancelled", baseTime.Add(3*time.Minute)),
		releaseRun(1305, commits[4], "queued", "", baseTime.Add(4*time.Minute)),
	}
	deps := orchestrator.Deps{Store: store, Client: client}
	runGitHubCITails(t, ctx, deps, baseTime.Add(5*time.Minute))

	rel, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || rel == nil {
		t.Fatalf("GetBaseBranchRelease = %+v, %v", rel, err)
	}
	if rel.Failures != 0 {
		t.Errorf("superseded runs counted as %d failed releases: %+v", rel.Failures, rel)
	}
	if rel.RunID != "1304" || !strings.Contains(rel.Detail, "superseded by "+commits[4][:12]+" (run 1305)") {
		t.Errorf("row = %+v, want the last cancelled run shown as superseded by %s (run 1305)", rel, commits[4][:12])
	}
	if !rel.Superseded() {
		t.Errorf("row %+v is not Superseded(): the UI and the base-repair gate would read the branch as red", rel)
	}
	if rel.FailingSince == nil || !rel.FailingSince.Equal(baseTime.Add(time.Minute)) {
		t.Errorf("FailingSince = %v, want the first superseded run's start %v: nothing has published since",
			rel.FailingSince, baseTime.Add(time.Minute))
	}
	if ids, _ := store.OpenTasksWithTag(ctx, orchestrator.BaseReleaseTriageBranchTag(repo, "main")); len(ids) != 0 {
		t.Errorf("superseded release runs filed release triage: %v", ids)
	}
	if findings, err := store.BaseBranchPublishingFindings(ctx, baseTime.Add(time.Hour)); err != nil || len(findings) != 0 {
		t.Errorf("BaseBranchPublishingFindings an hour into the burst = %+v, %v; want none", findings, err)
	}

	// Nothing at all publishing for longer than BaseBranchNotPublishingFor
	// while merges keep superseding each other is still worth saying.
	stale := baseTime.Add(time.Minute + model.BaseBranchNotPublishingFor + time.Minute)
	findings, err := store.BaseBranchPublishingFindings(ctx, stale)
	if err != nil || len(findings) != 1 || findings[0].Failures != 0 ||
		!strings.Contains(findings[0].Detail, "superseded by a newer merge") {
		t.Errorf("BaseBranchPublishingFindings with nothing published for %s = %+v, %v; want one, with no failures",
			model.BaseBranchNotPublishingFor, findings, err)
	}

	// The running release publishes, and then the newest one does: nothing
	// is owed.
	// updated_at is when a completed run finished, as GitHub serves it:
	// the in-flight release finishes an hour in, well after the burst.
	sim.WorkflowRuns[0].Status, sim.WorkflowRuns[0].Conclusion = "completed", "success"
	sim.WorkflowRuns[0].UpdatedAt = baseTime.Add(60 * time.Minute)
	runGitHubCITails(t, ctx, deps, baseTime.Add(60*time.Minute))
	if findings, _ := store.BaseBranchPublishingFindings(ctx, stale); len(findings) != 0 {
		t.Errorf("BaseBranchPublishingFindings after the running release published = %+v", findings)
	}
	sim.WorkflowRuns[4].Status, sim.WorkflowRuns[4].Conclusion = "completed", "success"
	sim.WorkflowRuns[4].UpdatedAt = baseTime.Add(120 * time.Minute)
	runGitHubCITails(t, ctx, deps, baseTime.Add(120*time.Minute))
	rel, _ = store.GetBaseBranchRelease(ctx, repo, "main")
	if rel == nil || rel.RunID != "1305" || rel.Conclusion != "success" || rel.Failures != 0 || rel.FailingSince != nil {
		t.Fatalf("row after the newest release published = %+v, want 1305 success with no streak", rel)
	}
	if findings, _ := store.BaseBranchPublishingFindings(ctx, stale); len(findings) != 0 {
		t.Errorf("BaseBranchPublishingFindings after the newest release published = %+v", findings)
	}
	if ids, _ := store.OpenTasksWithTag(ctx, orchestrator.BaseReleaseTriageBranchTag(repo, "main")); len(ids) != 0 {
		t.Errorf("release triage open after a burst whose newest release published: %v", ids)
	}
}

// The newest commit's release failing is a real failure, whichever older
// runs it superseded: triage is filed for it, not for them.
func TestTheNewestReleaseFailingIsAFailureAndTheRunsItSupersededAreNot(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	defer orchestrator.SetGitHubCIBudgetLow(false)()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	older := headSHA(t, client, repo, "main")
	pushBranch(t, sim.BareRepo, "main")
	newest := headSHA(t, client, repo, "main")
	failed := releaseRun(1402, newest, "completed", "failure", baseTime.Add(time.Minute))
	failed.Jobs = []githubsim.WorkflowJob{{
		Name: "release images", Conclusion: "failure", FailingStep: "Build the images",
		Log: "2026-11-06T05:50:00.0000000Z error: building grain: exit status 1\n",
	}}
	sim.WorkflowRuns = []githubsim.WorkflowRun{
		releaseRun(1401, older, "completed", "cancelled", baseTime),
		failed,
	}
	deps := orchestrator.Deps{Store: store, Client: client}
	runGitHubCITails(t, ctx, deps, baseTime.Add(5*time.Minute))

	rel, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || rel == nil {
		t.Fatalf("GetBaseBranchRelease = %+v, %v", rel, err)
	}
	if rel.RunID != "1402" || rel.Conclusion != "failure" || rel.Failures != 1 {
		t.Errorf("row = %+v, want the newest release's failure counted once", rel)
	}
	if ids, _ := store.OpenTasksWithTag(ctx, orchestrator.BaseReleaseTriageBranchTag(repo, "main")); len(ids) != 1 {
		t.Errorf("release triage for the newest commit's failure = %v, want one", ids)
	}
	if filed, _ := store.HasTaskWithTag(ctx, orchestrator.BaseReleaseTriageTag(repo, "main", older)); filed {
		t.Errorf("filed release triage for %s, whose run was only superseded", older[:7])
	}
}

// A cancelled release with no newer run behind it -- somebody cancelled
// the newest, or it timed out of its queue -- is not superseded: nothing
// is going to publish that commit, so it still counts.
func TestACancelledReleaseWithNothingNewerStillCounts(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	defer orchestrator.SetGitHubCIBudgetLow(false)()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	tip := headSHA(t, client, repo, "main")
	sim.WorkflowRuns = []githubsim.WorkflowRun{releaseRun(1501, tip, "completed", "cancelled", baseTime)}
	runGitHubCITails(t, ctx, orchestrator.Deps{Store: store, Client: client}, baseTime.Add(time.Minute))
	rel, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || rel == nil || rel.Failures != 1 || strings.Contains(rel.Detail, "superseded") {
		t.Fatalf("row = %+v, %v; want the newest release's cancellation counted, not superseded", rel, err)
	}
}

// An on-demand build of one commit (release.yml dispatched with
// `commit: <sha>`) is recorded against main's head by GitHub, but it
// published an older commit: it says nothing about main's releases.
func TestAnOnDemandReleaseBuildIsNotRecordedAsTheBranchsRelease(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	defer orchestrator.SetGitHubCIBudgetLow(false)()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	tip := headSHA(t, client, repo, "main")
	onDemand := releaseRun(1601, tip, "completed", "success", baseTime)
	onDemand.Event = "workflow_dispatch"
	onDemand.DisplayTitle = "release 0123456789abcdef0123456789abcdef01234567 (on demand)"
	sim.WorkflowRuns = []githubsim.WorkflowRun{onDemand}
	runGitHubCITails(t, ctx, orchestrator.Deps{Store: store, Client: client}, baseTime.Add(time.Minute))
	if rel, err := store.GetBaseBranchRelease(ctx, repo, "main"); err != nil || rel != nil {
		t.Fatalf("an on-demand build was recorded as main's release: %+v, %v", rel, err)
	}

	// A push run whose commit subject happens to end with "(on demand)" is a
	// real branch release, not an on-demand workflow_dispatch, so it still
	// supersedes an earlier cancelled push run and records main's release.
	pushBranch(t, sim.BareRepo, "main")
	newer := headSHA(t, client, repo, "main")
	pushNamedLikeOnDemand := releaseRun(1603, newer, "completed", "success", baseTime.Add(2*time.Minute))
	pushNamedLikeOnDemand.DisplayTitle = "fix(release): build superseded commits (on demand)"
	sim.WorkflowRuns = []githubsim.WorkflowRun{
		onDemand,
		releaseRun(1602, tip, "completed", "cancelled", baseTime.Add(time.Minute)),
		pushNamedLikeOnDemand,
	}
	runGitHubCITails(t, ctx, orchestrator.Deps{Store: store, Client: client}, baseTime.Add(3*time.Minute))
	rel, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || rel == nil || rel.RunID != "1603" || rel.Conclusion != "success" || rel.Failures != 0 {
		t.Fatalf("push run ending in '(on demand)' was not recorded as a real release: %+v, %v", rel, err)
	}
}
