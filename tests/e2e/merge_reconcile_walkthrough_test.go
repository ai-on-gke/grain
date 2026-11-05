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

package e2e

// docs/test-plans/2026-11-03.md §7, driven through orchestrator.RunCycle
// -- dispatch, a scripted agent that really pushes, and the merge queue's
// own reconcilers -- against githubsim. The external-check stall
// threshold is lowered with orchestrator.SetExternalCheckStallThreshold,
// which is package state, so none of these tests is parallel: Go runs
// every non-parallel test before it releases the parallel ones, and each
// restores the threshold in t.Cleanup. The report is
// docs/test-runs/2026-11-03-merge-reconcile.md.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// reconcileStall is the lowered external-check stall threshold every
// walkthrough here runs under, in place of the 30-minute default.
const reconcileStall = 2 * time.Minute

type reconcileWorld struct {
	w      *world
	sim    *githubsim.Sim
	deps   orchestrator.Deps
	owner  string
	name   string
	repo   model.RepoRef
	clock  time.Time
	cycles int
}

func newReconcileWorld(t *testing.T, name string) *reconcileWorld {
	t.Helper()
	t.Cleanup(orchestrator.SetExternalCheckStallThreshold(reconcileStall))
	w := newWorld(t)
	const owner = "acme"
	w.newRepo(owner, name)
	bare := filepath.Join(w.upstreamDir, owner, name+".git")
	sim := githubsim.New(owner, name, bare, "main")
	client := github.NewClient(sim, nil)
	return &reconcileWorld{
		w: w, sim: sim, owner: owner, name: name,
		repo:  model.RepoRef{Owner: owner, Name: name},
		deps:  orchestrator.Deps{Store: w.store, Client: client, Sandboxes: worldSandboxes{w}, MaxWorkers: 1},
		clock: baseTime,
	}
}

// openPullRequest files an auto-merge (or not) task, dispatches it, and has
// its scripted run push one commit touching file, which opens its PR.
func (r *reconcileWorld) openPullRequest(id, file string, autoMerge bool) string {
	r.w.t.Helper()
	created := r.clock
	if err := r.w.store.PutTask(r.w.ctx, model.Task{
		ID: id, Intent: model.IntentImplement, Title: "task " + id,
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human("alice")}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human("alice")},
		Target:   &r.repo, Binding: model.BindingDirective, AutoMerge: autoMerge, CreatedAt: &created,
	}); err != nil {
		r.w.t.Fatal(err)
	}
	branch := model.BranchName(id)
	cmd := "rm -rf work && git clone " + r.w.remote(r.owner, r.name) + " work && cd work && " +
		"git checkout -b " + branch + " && echo 'change for " + id + "' > " + file + " && " +
		"git add " + file + " && git commit -q -m 'agent commit for " + id + "' && git push origin " + branch
	r.deps.Framework = scriptedFramework(
		append([]antigravity.Step{}, toolCall("run_command", map[string]any{"command": cmd}), finalText("pushed "+branch)))
	r.cycle("dispatch " + id)
	if st, err := r.w.store.State(r.w.ctx, id); err != nil || (st != model.StateCompleted && st != model.StateAwaitingSubmit) {
		r.w.t.Fatalf("%s state = %q (%v), want completed or awaiting_submit after its push", id, st, err)
	}
	if prOf(r.sim, branch) == nil {
		r.w.t.Fatalf("no pull request opened for %s", branch)
	}
	return branch
}

func (r *reconcileWorld) cycle(what string) {
	r.w.t.Helper()
	r.cycles++
	if err := orchestrator.RunCycle(r.w.ctx, r.deps, r.clock); err != nil {
		r.w.t.Fatalf("RunCycle (%s, t=%s): %v", what, r.clock.Sub(baseTime), err)
	}
}

// at moves the clock to baseTime+d and runs one cycle there.
func (r *reconcileWorld) at(d time.Duration, what string) {
	r.w.t.Helper()
	r.clock = baseTime.Add(d)
	r.cycle(what)
}

func (r *reconcileWorld) obs(id string) *model.Observation {
	r.w.t.Helper()
	o, err := r.w.store.GetObservation(r.w.ctx, id)
	if err != nil || o == nil {
		r.w.t.Fatalf("observation of %s: %v (%v)", id, o, err)
	}
	return o
}

// branchMerges counts POST /merges calls -- the external nudge's
// merge-the-base-in -- the sim accepted for this repository.
func (r *reconcileWorld) branchMerges() int {
	n := 0
	for _, c := range r.sim.Calls {
		if c.Method == "POST" && c.Path == "/repos/"+r.owner+"/"+r.name+"/merges" {
			n++
		}
	}
	return n
}

func (r *reconcileWorld) stallNotifications(taskID string) []model.Notification {
	r.w.t.Helper()
	ns, err := r.w.store.ListNotifications(r.w.ctx, model.NotificationFilter{
		Open: true, Family: model.FamilyMergeStalled, Limit: 100,
	})
	if err != nil {
		r.w.t.Fatal(err)
	}
	var out []model.Notification
	for _, n := range ns {
		if n.TaskID == taskID {
			out = append(out, n)
		}
	}
	return out
}

func (r *reconcileWorld) notesContaining(taskID, substr string) []string {
	r.w.t.Helper()
	cs, err := r.w.store.Comments(r.w.ctx, taskID)
	if err != nil {
		r.w.t.Fatal(err)
	}
	var out []string
	for _, c := range cs {
		if strings.Contains(c.Body, substr) {
			out = append(out, c.Body)
		}
	}
	return out
}

func prOf(sim *githubsim.Sim, head string) *githubsim.PullRequest {
	for i := range sim.PullRequests {
		if sim.PullRequests[i].Head == head {
			return &sim.PullRequests[i]
		}
	}
	return nil
}

// claPending is a green in-repo Actions check beside cla/google, posted by
// a third-party app and stuck queued in check suite suite.
func claPending(suite int64) []github.CheckRun {
	return []github.CheckRun{
		{ID: suite*10 + 1, Name: "Tests / unit", Status: "completed", Conclusion: success(), App: "github-actions"},
		{ID: suite*10 + 2, CheckSuiteID: suite, Name: "cla/google", Status: "queued", App: "google-cla"},
	}
}

func claGreen(suite int64) []github.CheckRun {
	checks := claPending(suite)
	checks[1].Status, checks[1].Conclusion = "completed", success()
	return checks
}

// §7 item 1, behind its base: one nudge per head (a merge commit), never a
// merge into base while cla/google is pending, the row naming the check,
// its age and the nudge, then pipeline.merge-stalled once the merged head
// sits out a second threshold.
func TestMergeReconcileStalledExternalCheckBehindBaseNudgesOnceThenEscalates(t *testing.T) {
	r := newReconcileWorld(t, "nudge-behind")
	branch := r.openPullRequest("ext-behind", "BEHIND.md", true)
	r.w.pushConflictingCommit(r.owner, r.name, "main", "MAIN.md", "base moved", "advance main")
	setMergeable(r.sim, branch, true)
	r.sim.CheckRuns[branch] = claPending(501)

	r.at(time.Minute, "pending clock starts")
	r.at(2*time.Minute, "under the threshold")
	if n := r.branchMerges(); n != 0 {
		t.Fatalf("nudged before the threshold: %d base merges", n)
	}
	if o := r.obs("ext-behind"); o.MergeQueueNote != "`cla/google` pending 1m" {
		t.Fatalf("row note under the threshold = %q", o.MergeQueueNote)
	}

	r.at(4*time.Minute, "past the threshold")
	if n := r.branchMerges(); n != 1 {
		t.Fatalf("base merges past the threshold = %d, want 1", n)
	}
	if !r.w.branchContains(r.owner, r.name, branch, "main") {
		t.Fatal("the PR branch does not contain main after the nudge")
	}
	pr := prOf(r.sim, branch)
	if pr.Merged {
		t.Fatal("the PR merged while cla/google was pending")
	}
	if got := r.sim.Comments[pr.Number]; len(got) != 1 || !strings.Contains(got[0].Body, "merged `main` to re-trigger it") {
		t.Fatalf("PR comments = %+v, want one naming the base merge", got)
	}
	if got := r.notesContaining("ext-behind", "merged `main` to re-trigger it"); len(got) != 1 {
		t.Fatalf("task notes about the nudge = %q, want one", got)
	}

	// Main moves again: the merged head is still the nudged one, so no
	// second merge, and the row says so.
	r.w.pushConflictingCommit(r.owner, r.name, "main", "MAIN2.md", "base moved again", "advance main again")
	r.at(5*time.Minute, "nudged head, new clock")
	r.at(6*time.Minute, "nudged head, under the second threshold")
	if n := r.branchMerges(); n != 1 {
		t.Fatalf("base merges on the nudged head = %d, want still 1", n)
	}
	if o := r.obs("ext-behind"); !strings.HasSuffix(o.MergeQueueNote, "; nudged once") {
		t.Fatalf("row note on the nudged head = %q, want it to say nudged once", o.MergeQueueNote)
	}
	if n := r.stallNotifications("ext-behind"); len(n) != 0 {
		t.Fatalf("escalated before the second threshold: %+v", n)
	}
	r.at(8*time.Minute, "past the second threshold")
	r.at(9*time.Minute, "and once more")
	if n := r.stallNotifications("ext-behind"); len(n) != 1 || n[0].Subject != "pipeline.merge-stalled:ext-behind" {
		t.Fatalf("stall notifications = %+v, want exactly pipeline.merge-stalled:ext-behind", n)
	}
	if n := r.branchMerges(); n != 1 || prOf(r.sim, branch).Merged {
		t.Fatalf("after escalation: %d base merges, merged=%v; want 1 and unmerged", n, prOf(r.sim, branch).Merged)
	}

	// The check reports: the PR merges and the notification clears.
	r.sim.CheckRuns[branch] = claGreen(501)
	r.at(10*time.Minute, "cla/google reports")
	if !prOf(r.sim, branch).Merged {
		t.Fatal("the PR did not merge once cla/google turned green")
	}
	if n := r.stallNotifications("ext-behind"); len(n) != 0 {
		t.Fatalf("stall notification outlived the merge: %+v", n)
	}
}

// §7 item 1, up to date: nothing is pushed, the check suite is re-requested
// once with a PR comment and a task note, and the second threshold raises
// pipeline.merge-stalled.
func TestMergeReconcileStalledExternalCheckUpToDateRerequestsOnceThenEscalates(t *testing.T) {
	r := newReconcileWorld(t, "nudge-current")
	branch := r.openPullRequest("ext-current", "CURRENT.md", true)
	setMergeable(r.sim, branch, true)
	r.sim.CheckRuns[branch] = claPending(601)
	headBefore := r.w.log1(r.owner, r.name, branch, "%H")

	r.at(time.Minute, "pending clock starts")
	r.at(4*time.Minute, "past the threshold")
	r.at(5*time.Minute, "nudged")
	if got := r.sim.RerequestedCheckSuites; len(got) != 1 || got[0] != 601 {
		t.Fatalf("re-requested suites = %v, want [601] once", got)
	}
	// The nudge asks /merges first; GitHub answers 204 for a branch
	// already up to date, and nothing may have been pushed.
	if head := r.w.log1(r.owner, r.name, branch, "%H"); head != headBefore {
		t.Fatalf("an up-to-date branch moved from %s to %s", headBefore, head)
	}
	pr := prOf(r.sim, branch)
	if got := r.sim.Comments[pr.Number]; len(got) != 1 || !strings.HasSuffix(got[0].Body, "re-requested it.") {
		t.Fatalf("PR comments = %+v, want one saying it re-requested the check", got)
	}
	if got := r.notesContaining("ext-current", "re-requested it."); len(got) != 1 {
		t.Fatalf("task notes = %q, want one", got)
	}
	if o := r.obs("ext-current"); !strings.HasSuffix(o.MergeQueueNote, "; nudged once") {
		t.Fatalf("row note = %q, want it to say nudged once", o.MergeQueueNote)
	}

	r.at(7*time.Minute, "past the second threshold")
	if got := r.sim.RerequestedCheckSuites; len(got) != 1 {
		t.Fatalf("re-requested suites = %v, want still one", got)
	}
	if n := r.stallNotifications("ext-current"); len(n) != 1 {
		t.Fatalf("stall notifications = %+v, want one", n)
	}
	if prOf(r.sim, branch).Merged {
		t.Fatal("merged while pending")
	}
}

// §7 item 1, held for a person: a PR whose task is not auto-merge (it waits
// for a Submit) is never nudged, commented on or escalated, however long
// cla/google stays pending.
func TestMergeReconcileStalledExternalCheckOnAPullRequestHeldForAPersonIsNeverNudged(t *testing.T) {
	r := newReconcileWorld(t, "nudge-held")
	branch := r.openPullRequest("ext-held", "HELD.md", false)
	r.w.pushConflictingCommit(r.owner, r.name, "main", "MAIN.md", "base moved", "advance main")
	setMergeable(r.sim, branch, true)
	r.sim.CheckRuns[branch] = claPending(701)
	for _, d := range []time.Duration{1, 4, 8, 15, 40} {
		r.at(d*time.Minute, "held")
	}
	if st, _ := r.w.store.State(r.w.ctx, "ext-held"); st != model.StateAwaitingSubmit {
		t.Fatalf("ext-held state = %q, want awaiting_submit", st)
	}
	pr := prOf(r.sim, branch)
	if n, rr, cs := r.branchMerges(), len(r.sim.RerequestedCheckSuites), len(r.sim.Comments[pr.Number]); n+rr+cs != 0 {
		t.Fatalf("a PR held for a person was nudged: %d base merges, %d re-requests, %d comments", n, rr, cs)
	}
	if n := r.stallNotifications("ext-held"); len(n) != 0 {
		t.Fatalf("a PR held for a person was escalated: %+v", n)
	}
	if pr.Merged {
		t.Fatal("a PR held for a person merged")
	}
}

// §7 item 2: two auto-merge members of one lane, both waiting on
// cla/google. The first one's check reports and it merges; on that same
// cycle the second one's stall is acted on (a merge commit, since the
// first's merge left it behind) and, a threshold later, escalated -- not
// left unread behind the member that merged.
func TestMergeReconcileSecondLaneMembersStallEscalatesWhileTheFirstMerges(t *testing.T) {
	r := newReconcileWorld(t, "two-members")
	first := r.openPullRequest("lane-1", "ONE.md", true)
	r.clock = r.clock.Add(time.Second)
	second := r.openPullRequest("lane-2", "TWO.md", true)
	setMergeable(r.sim, first, true)
	setMergeable(r.sim, second, true)
	r.sim.CheckRuns[first] = claPending(801)
	r.sim.CheckRuns[second] = claPending(802)

	r.at(time.Minute, "both pending")
	r.at(2*time.Minute, "both under the threshold")
	if prOf(r.sim, first).Merged || prOf(r.sim, second).Merged {
		t.Fatal("a member merged while cla/google was pending")
	}

	r.sim.CheckRuns[first] = claGreen(801)
	r.at(4*time.Minute, "first green, second past the threshold")
	if !prOf(r.sim, first).Merged {
		t.Fatal("the first member did not merge once its check reported")
	}
	if n := r.branchMerges(); n != 1 {
		t.Fatalf("base merges = %d, want one nudge on the second member in the cycle the first merged", n)
	}
	if !r.w.branchContains(r.owner, r.name, second, "main") {
		t.Fatal("the second member's branch was not brought up to the main the first merged into")
	}
	if got := r.notesContaining("lane-2", "to re-trigger it"); len(got) != 1 {
		t.Fatalf("second member's nudge notes = %q, want one", got)
	}

	r.at(5*time.Minute, "second's merged head, new clock")
	r.at(8*time.Minute, "past the second threshold")
	if n := r.stallNotifications("lane-2"); len(n) != 1 {
		t.Fatalf("second member's stall notifications = %+v, want one", n)
	}
	if n := r.stallNotifications("lane-1"); len(n) != 0 {
		t.Fatalf("the merged first member was escalated: %+v", n)
	}
	if prOf(r.sim, second).Merged {
		t.Fatal("the second member merged while pending")
	}
}

// §7 item 3: a PR the queue gave up on, its task closed, then merged by
// hand on the forge. The next cycle whose read is due records it merged,
// takes the blocked stamp and the row's last words off it, and a wait on
// the task until "merged" reads met as merged. Between the close and the
// merge that wait is not settled -- the PR can still land -- so a task
// parked on it until "merged" is held rather than resumed at the close
// (task 2757), and is dispatched once the merge is recorded.
func TestMergeReconcileHandMergeAfterCloseIsRecordedThroughTheCycle(t *testing.T) {
	r := newReconcileWorld(t, "hand-merge")
	branch := r.openPullRequest("blocked", "BLOCKED.md", true)
	r.parkUntilMerged("waiter", "blocked")
	setMergeable(r.sim, branch, true)
	r.sim.CheckRuns[branch] = claGreen(901)
	r.sim.RefuseMerges = "Repository rule violations found\n\nAll comments must be resolved."
	r.sim.BranchRules = map[string][]map[string]any{"main": {{"type": "required_review_thread_resolution"}}}

	r.clock = baseTime.Add(time.Minute)
	_ = orchestrator.RunCycle(r.w.ctx, r.deps, r.clock) // the refused merge is reported as an error
	if o := r.obs("blocked"); o.MergeQueueBlockedAt == nil || o.MergeQueueNote == "" {
		t.Fatalf("the refused merge was not handed to a person: state %q note %q", o.MergeQueueState, o.MergeQueueNote)
	}

	closedAt := baseTime.Add(3 * time.Minute)
	if err := r.w.store.ObserveField(r.w.ctx, "blocked", closedAt, func(o *model.Observation) { o.ClosedAt = &closedAt }); err != nil {
		t.Fatal(err)
	}
	r.at(4*time.Minute, "closed, PR still open")
	target, err := r.w.store.WaitTargetAt(r.w.ctx, "blocked", r.clock)
	if err != nil {
		t.Fatal(err)
	}
	if !target.PullRequestOpen {
		t.Fatalf("closed with its PR open: %+v, want PullRequestOpen", target)
	}
	if settled, met := target.Settled(true); settled || met {
		t.Fatalf("until-merged wait at close: settled=%v met=%v, want still waiting (PR still open)", settled, met)
	}
	if settled, met := target.Settled(false); !settled || !met {
		t.Fatalf("until-closed wait at close: settled=%v met=%v, want met", settled, met)
	}
	r.at(9*time.Minute, "closed, PR still open, a later cycle")
	if n := r.runs("waiter"); n != 1 {
		t.Fatalf("the waiter parked until merged has %d runs before the merge, want only the one that parked", n)
	}

	mergedAt := baseTime.Add(10 * time.Minute)
	pr := prOf(r.sim, branch)
	pr.State, pr.Merged, pr.MergedAt = "closed", true, mergedAt
	r.at(10*time.Minute+github.PollCap, "one idle-read cap after the hand merge")
	o := r.obs("blocked")
	if o.PrMergedAt == nil || !o.PrMergedAt.Equal(mergedAt) {
		t.Fatalf("PrMergedAt = %v, want %v", o.PrMergedAt, mergedAt)
	}
	if o.MergeQueueBlockedAt != nil || o.MergeQueueState != "" || o.MergeQueueNote != "" {
		t.Fatalf("still blocked after the hand merge: at %v state %q note %q",
			o.MergeQueueBlockedAt, o.MergeQueueState, o.MergeQueueNote)
	}
	target, err = r.w.store.WaitTargetAt(r.w.ctx, "blocked", r.clock)
	if err != nil {
		t.Fatal(err)
	}
	if settled, met := target.Settled(true); !settled || !met || target.Outcome != model.WaitOutcomeMerged {
		t.Fatalf("until-merged wait after the hand merge: settled=%v met=%v outcome %q, want met as merged",
			settled, met, target.Outcome)
	}
	r.at(11*time.Minute+github.PollCap, "the cycle after the merge was recorded")
	if n := r.runs("waiter"); n != 2 {
		t.Fatalf("the waiter has %d runs after the hand merge, want it resumed once", n)
	}
}

// parkUntilMerged files task id as parked on target until "merged", the
// way a run's wait_for_task park leaves it: a depends-on link to target,
// and the run that parked suspended with that ResumeOn. Its next run, if
// one is dispatched, only says it resumed.
func (r *reconcileWorld) parkUntilMerged(id, target string) {
	r.w.t.Helper()
	created := r.clock
	if err := r.w.store.PutTask(r.w.ctx, model.Task{
		ID: id, Intent: model.IntentImplement, Title: "task " + id,
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human("alice")}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human("alice")},
		Target:   &r.repo, Binding: model.BindingDirective, CreatedAt: &created,
		Links: []model.Link{{Kind: model.LinkDependsOn, Target: target}},
	}); err != nil {
		r.w.t.Fatal(err)
	}
	runID := id + "-1"
	if err := r.w.store.StartRun(r.w.ctx, model.Run{ID: runID, TaskID: id, Sandbox: "s-" + id, Attempt: 1, StartedAt: r.clock},
		model.Limits{}); err != nil {
		r.w.t.Fatal(err)
	}
	if err := r.w.store.Suspend(r.w.ctx, model.Suspension{RunID: runID, TaskID: id, Kind: model.SuspendRequested,
		Detail:    model.ParkedDetail([]string{target}),
		ResumeOn:  model.ResumeOn{Tasks: []string{target}, UntilMerged: true}.JSON(),
		CreatedAt: r.clock}); err != nil {
		r.w.t.Fatal(err)
	}
	r.deps.Framework = scriptedFramework([]antigravity.Step{finalText("resumed")})
}

// runs is how many runs task id has had.
func (r *reconcileWorld) runs(id string) int {
	r.w.t.Helper()
	runs, err := r.w.store.Runs(r.w.ctx, id)
	if err != nil {
		r.w.t.Fatal(err)
	}
	return len(runs)
}

// §7 item 6: a run that calls comment_on_issue with a short report and then
// writes a longer final text closes on the short report.
func TestMergeReconcileShortCommentOnIssueIsTheClosingNoteOverALongerFinalText(t *testing.T) {
	r := newReconcileWorld(t, "relay")
	fileIssue(r.w, "relay", human("erin"), r.repo)
	const short = "Nothing needed: the flake is already tracked."
	long := "I looked at the failing job in detail. " + strings.Repeat("Here is a long narrative about what I read. ", 20)
	r.deps.Framework = scriptedFramework(append([]antigravity.Step{},
		toolCall("comment_on_issue", map[string]any{"comment": short}), finalText(long)))
	r.cycle("relay run")
	if got := r.notesContaining("relay", short); len(got) != 1 || strings.TrimSpace(got[0]) != short {
		t.Fatalf("relayed notes = %q, want exactly the short report", got)
	}
	if got := r.notesContaining("relay", "long narrative"); len(got) != 0 {
		t.Fatalf("the longer final text was relayed as well: %q", got)
	}
}
