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
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func openMergeStalledNotifications(t *testing.T, ctx context.Context, store *model.Store, taskID string) []model.Notification {
	t.Helper()
	ns, err := store.ListNotifications(ctx, model.NotificationFilter{
		Open:   true,
		Family: model.FamilyMergeStalled,
		Limit:  100,
	})
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	var out []model.Notification
	for _, n := range ns {
		if n.TaskID == taskID {
			out = append(out, n)
		}
	}
	return out
}

func taskNotesContaining(t *testing.T, ctx context.Context, store *model.Store, taskID, substr string) []string {
	t.Helper()
	cs, err := store.Comments(ctx, taskID)
	if err != nil {
		t.Fatalf("Comments(%s): %v", taskID, err)
	}
	var bodies []string
	for _, c := range cs {
		if strings.Contains(c.Body, substr) {
			bodies = append(bodies, c.Body)
		}
	}
	return bodies
}

// A pending external check (cla/google) past the 30m threshold on a branch
// behind its base: the base is merged into the PR branch once, the PR never
// merges into base while the check is pending, and both a PR comment and a
// task note are posted naming the check, the old head and the base merge.
func TestStalledExternalCheckBehindBaseMergesBaseOnceAndPostsNoteAndComment(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t-ext-behind", repo)

	prBefore := prByHead(t, sim, branch)
	oldDetail, err := client.GetPullRequest("acme", "widgets", prBefore.Number)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	oldShort := oldDetail.HeadSHA[:7]

	// Advance main so the PR branch is behind its base.
	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "new base commit", "advance main")
	setMergeable(sim, true)

	// In-repo Actions passed, but cla/google is stuck pending.
	sim.CheckRuns[branch] = []github.CheckRun{
		{ID: 10, Name: "Tests / unit", Status: "completed", Conclusion: strPtr("success"), App: "github-actions"},
		{ID: 11, CheckSuiteID: 501, Name: "cla/google", Status: "queued", App: "google-cla"},
	}

	// Start the pending clock at baseTime.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests at t=0: %v", err)
	}
	if n := mergeWrites(sim, repo); n != 0 {
		t.Fatalf("merge writes at t=0 = %d, want 0", n)
	}
	if mergedBranch(sim, branch) {
		t.Fatalf("PR merged while cla/google was pending")
	}

	// Before threshold (15m): row names check and age, no nudge yet.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(15*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests at t=15m: %v", err)
	}
	if state, note := mergeQueueStateOf(t, ctx, store, task.ID); state != "waiting_ci" || note != "`cla/google` pending 15m" {
		t.Fatalf("row at 15m = (%q, %q), want (\"waiting_ci\", \"`cla/google` pending 15m\")", state, note)
	}
	if n := mergeWrites(sim, repo); n != 0 {
		t.Fatalf("merge writes at t=15m = %d, want 0", n)
	}

	// Past threshold (47m): base is merged once into the PR branch, PR comment
	// and task note are posted, and PR still does not merge while pending.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(47*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests at t=47m: %v", err)
	}
	if n := mergeWrites(sim, repo); n != 1 {
		t.Fatalf("merge writes at t=47m = %d, want 1", n)
	}
	if !branchContains(t, sim.BareRepo, branch, "main") {
		t.Fatalf("PR branch %s does not contain main after nudge merge", branch)
	}
	if mergedBranch(sim, branch) {
		t.Fatalf("PR merged into main while cla/google was still pending")
	}

	wantMsg := "`cla/google` was pending for 47m on `" + oldShort + "`; merged `main` to re-trigger it."
	prComments := sim.Comments[prBefore.Number]
	if len(prComments) != 1 || prComments[0].Body != wantMsg {
		t.Fatalf("PR comments = %+v, want [%q]", prComments, wantMsg)
	}
	notes := taskNotesContaining(t, ctx, store, task.ID, "merged `main` to re-trigger it")
	if len(notes) != 1 || notes[0] != wantMsg {
		t.Fatalf("task notes = %q, want [%q]", notes, wantMsg)
	}

	// No second nudge even if main moves again while the merged head is pending;
	// after a second threshold on the merged head, escalation fires instead.
	pushFileToBranch(t, sim.BareRepo, "main", "MAIN2.md", "another base commit", "advance main again")
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(52*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests at t=52m: %v", err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(60*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests at t=60m: %v", err)
	}
	if n := mergeWrites(sim, repo); n != 1 {
		t.Fatalf("merge writes after subsequent syncs = %d, want 1 (no second nudge)", n)
	}
	if state, note := mergeQueueStateOf(t, ctx, store, task.ID); state != "waiting_ci" || note != "`cla/google` pending 8m; nudged once" {
		t.Fatalf("row at 60m on nudged head = (%q, %q), want (\"waiting_ci\", \"`cla/google` pending 8m; nudged once\")", state, note)
	}

	// At t=85m (33m on the new head since 52m, past the second 30m threshold):
	// escalates via pipeline.merge-stalled without merging main a second time.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(85*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests at t=85m: %v", err)
	}
	if n := mergeWrites(sim, repo); n != 1 {
		t.Fatalf("merge writes at t=85m = %d, want 1", n)
	}
	if notifs := openMergeStalledNotifications(t, ctx, store, task.ID); len(notifs) != 1 {
		t.Fatalf("notifications at t=85m = %d, want 1", len(notifs))
	}

	// Once cla/google reports success on the new head, the PR merges cleanly and
	// clears the stall notification.
	sim.CheckRuns[branch] = []github.CheckRun{
		{ID: 20, Name: "Tests / unit", Status: "completed", Conclusion: strPtr("success"), App: "github-actions"},
		{ID: 21, CheckSuiteID: 502, Name: "cla/google", Status: "completed", Conclusion: strPtr("success"), App: "google-cla"},
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(86*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests at t=86m: %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatalf("PR did not merge after cla/google turned green on the new head")
	}
	if notifs := openMergeStalledNotifications(t, ctx, store, task.ID); len(notifs) != 0 {
		t.Fatalf("stall notification remained open after PR merged: %+v", notifs)
	}
}

// When the branch is already up to date with its base and the forge supports
// check-suite re-request: nothing is pushed, the check suite is re-requested
// once, the row note reports "nudged once", no second nudge occurs on the same
// head, and escalation fires after the second threshold.
func TestStalledExternalCheckUpToDateRerequestsOnceThenEscalatesAfterSecondThreshold(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t-ext-uptodate", repo)
	setMergeable(sim, true)

	pr := prByHead(t, sim, branch)
	detailBefore, err := client.GetPullRequest("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	headBefore := detailBefore.HeadSHA
	shortHead := headBefore[:7]

	sim.CheckRuns[branch] = []github.CheckRun{
		{ID: 30, Name: "Tests", Status: "completed", Conclusion: strPtr("success"), App: "github-actions"},
		{ID: 31, CheckSuiteID: 777, Name: "cla/google", Status: "queued", App: "google-cla"},
	}

	// Start clock at t=0.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests at t=0: %v", err)
	}

	// At t=35m (past first 30m threshold): branch is already up to date (204),
	// so no commit is pushed and suite 777 is re-requested once.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(35*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests at t=35m: %v", err)
	}
	detailAfter, err := client.GetPullRequest("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatalf("GetPullRequest after nudge: %v", err)
	}
	if detailAfter.HeadSHA != headBefore {
		t.Fatalf("head SHA changed from %s to %s on an already up-to-date branch (must not push empty commits)", headBefore, detailAfter.HeadSHA)
	}
	if len(sim.RerequestedCheckSuites) != 1 || sim.RerequestedCheckSuites[0] != 777 {
		t.Fatalf("RerequestedCheckSuites = %v, want [777]", sim.RerequestedCheckSuites)
	}
	wantMsg := "`cla/google` was pending for 35m on `" + shortHead + "`; re-requested it."
	if got := sim.Comments[pr.Number]; len(got) != 1 || got[0].Body != wantMsg {
		t.Fatalf("PR comments = %+v, want [%q]", got, wantMsg)
	}
	if notes := taskNotesContaining(t, ctx, store, task.ID, "re-requested it"); len(notes) != 1 || notes[0] != wantMsg {
		t.Fatalf("task notes = %q, want [%q]", notes, wantMsg)
	}
	if notifs := openMergeStalledNotifications(t, ctx, store, task.ID); len(notifs) != 0 {
		t.Fatalf("notifications at t=35m = %+v, want none before second threshold", notifs)
	}

	// At t=52m (17m after nudge, < second 30m threshold): no second nudge, no
	// escalation yet, and row note reads "`cla/google` pending 52m; nudged once".
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(52*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests at t=52m: %v", err)
	}
	if len(sim.RerequestedCheckSuites) != 1 {
		t.Fatalf("RerequestedCheckSuites at t=52m = %v, want still 1 (no second nudge on same head)", sim.RerequestedCheckSuites)
	}
	if len(sim.Comments[pr.Number]) != 1 {
		t.Fatalf("PR comments at t=52m = %d, want 1", len(sim.Comments[pr.Number]))
	}
	if state, note := mergeQueueStateOf(t, ctx, store, task.ID); state != "waiting_ci" || note != "`cla/google` pending 52m; nudged once" {
		t.Fatalf("row at 52m = (%q, %q), want (\"waiting_ci\", \"`cla/google` pending 52m; nudged once\")", state, note)
	}
	if notifs := openMergeStalledNotifications(t, ctx, store, task.ID); len(notifs) != 0 {
		t.Fatalf("notifications at t=52m = %+v, want none before second threshold", notifs)
	}

	// At t=66m (31m after the t=35m nudge -- past the second 30m threshold):
	// pipeline.merge-stalled:<task> is raised naming the check, head and wait.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(66*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests at t=66m: %v", err)
	}
	if len(sim.RerequestedCheckSuites) != 1 {
		t.Fatalf("RerequestedCheckSuites at t=66m = %v, want 1", sim.RerequestedCheckSuites)
	}
	notifs := openMergeStalledNotifications(t, ctx, store, task.ID)
	if len(notifs) != 1 {
		t.Fatalf("open pipeline.merge-stalled notifications at t=66m = %d, want 1", len(notifs))
	}
	if !strings.Contains(notifs[0].Body, "`cla/google`") ||
		!strings.Contains(notifs[0].Body, shortHead) ||
		!strings.Contains(notifs[0].Body, "1h6m") {
		t.Fatalf("notification body = %q, want check name, head %s and 1h6m wait", notifs[0].Body, shortHead)
	}
	if mergedBranch(sim, branch) {
		t.Fatalf("PR merged while cla/google was still pending")
	}
}

// When the branch is already up to date and the forge/app does not support
// re-requesting the check: nothing is pushed and escalation fires immediately.
func TestStalledExternalCheckUpToDateEscalatesWithoutPushWhenRerequestUnsupported(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	sim.DisableCheckRerequest = true
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t-ext-norereq", repo)
	setMergeable(sim, true)

	pr := prByHead(t, sim, branch)
	detailBefore, err := client.GetPullRequest("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	headBefore := detailBefore.HeadSHA

	sim.CheckRuns[branch] = []github.CheckRun{
		{ID: 41, CheckSuiteID: 888, Name: "cla/google", Status: "queued", App: "google-cla"},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests at t=0: %v", err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(40*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests at t=40m: %v", err)
	}

	detailAfter, err := client.GetPullRequest("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatalf("GetPullRequest after escalation: %v", err)
	}
	if detailAfter.HeadSHA != headBefore {
		t.Fatalf("head SHA changed from %s to %s; expected no push", headBefore, detailAfter.HeadSHA)
	}
	if len(sim.Comments[pr.Number]) != 0 {
		t.Fatalf("PR comments = %+v, want none when no nudge occurred", sim.Comments[pr.Number])
	}
	notifs := openMergeStalledNotifications(t, ctx, store, task.ID)
	if len(notifs) != 1 {
		t.Fatalf("open pipeline.merge-stalled notifications = %d, want 1", len(notifs))
	}
	if !strings.Contains(notifs[0].Body, "`cla/google`") || !strings.Contains(notifs[0].Body, headBefore[:7]) || !strings.Contains(notifs[0].Body, "40m") {
		t.Fatalf("notification body = %q, want cla/google, %s and 40m", notifs[0].Body, headBefore[:7])
	}
}

// Grain CI and in-repository GitHub Actions checks are never nudged by the
// external-check rule, even when the PR branch is behind its base.
func TestGrainCIAndInRepoActionsChecksAreNeverNudgedByExternalRule(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	tActions, bActions := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t-actions", repo)
	tGrain, bGrain := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t-grain", repo)

	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "base moved", "advance main")
	setMergeable(sim, true)

	sim.CheckRuns[bActions] = []github.CheckRun{
		{ID: 60, CheckSuiteID: 901, Name: "Tests / unit", Status: "in_progress", App: "github-actions",
			HTMLURL: "https://github.com/acme/widgets/actions/runs/100/job/200"},
	}
	sim.CheckRuns[bGrain] = []github.CheckRun{
		{ID: 61, CheckSuiteID: 902, Name: "grain/ci", Status: "in_progress", App: "grain"},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests at t=0: %v", err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(45*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests at t=45m: %v", err)
	}

	if n := mergeWrites(sim, repo); n != 0 {
		t.Fatalf("merge writes = %d, want 0 (Actions and Grain CI must never be nudged)", n)
	}
	if len(sim.RerequestedCheckSuites) != 0 || len(sim.RerequestedCheckRuns) != 0 {
		t.Fatalf("rerequests = suites %v runs %v, want none", sim.RerequestedCheckSuites, sim.RerequestedCheckRuns)
	}
	for _, id := range []string{tActions.ID, tGrain.ID} {
		if notifs := openMergeStalledNotifications(t, ctx, store, id); len(notifs) != 0 {
			t.Fatalf("task %s raised unexpected external stall notification: %+v", id, notifs)
		}
	}
}

// An in-flight run, a paused PR (person working on it), or a PR awaiting a
// Submit (!AutoMerge) is never nudged.
func TestInFlightPausedAndAwaitingSubmitPullRequestsAreNotNudged(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	tRunning, bRunning := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t-running", repo)
	tSubmit, bSubmit := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t-submit", repo)
	tPaused, bPaused := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t-paused", repo)

	// Start an in-flight run on tRunning (e.g. RunNow while PR is open).
	if err := store.StartRun(ctx, model.Run{
		ID: "run-in-flight", TaskID: tRunning.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// tSubmit is awaiting a human Submit (AutoMerge = false).
	tSubmit.AutoMerge = false
	if err := store.PutTask(ctx, tSubmit); err != nil {
		t.Fatalf("PutTask(tSubmit): %v", err)
	}

	// tPaused is paused by an operator working on it.
	if _, err := store.Pause(ctx, model.TaskPause{
		TaskID:   tPaused.ID,
		PausedBy: model.Principal{Kind: model.PrincipalHuman, ID: "alice"},
		PausedAt: baseTime,
	}); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "base moved", "advance main")
	setMergeable(sim, true)

	for _, b := range []string{bRunning, bSubmit, bPaused} {
		sim.CheckRuns[b] = []github.CheckRun{
			{ID: 70, CheckSuiteID: 950, Name: "cla/google", Status: "queued", App: "google-cla"},
		}
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests at t=0: %v", err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(50*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests at t=50m: %v", err)
	}

	if n := mergeWrites(sim, repo); n != 0 {
		t.Fatalf("merge writes = %d, want 0 for in-flight, paused, and awaiting-Submit PRs", n)
	}
	if len(sim.RerequestedCheckSuites) != 0 {
		t.Fatalf("rerequests = %v, want none", sim.RerequestedCheckSuites)
	}
	for _, b := range []string{bRunning, bSubmit, bPaused} {
		pr := prByHead(t, sim, b)
		if len(sim.Comments[pr.Number]) != 0 {
			t.Fatalf("PR %d (%s) got unexpected comment: %+v", pr.Number, b, sim.Comments[pr.Number])
		}
	}
}
