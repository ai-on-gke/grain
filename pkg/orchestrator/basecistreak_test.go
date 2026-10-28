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

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A base branch whose tip moves faster than a confirmation re-run
// finishes is confirmed red by a streak of red tips instead
// (grain/task-1034). These drive the reconciler through a branch that
// moves under every run, which is what bwsalmon/grain's main did on
// 2026-10-23.

// streakBranch is one watched base branch, a fake grain CI and an enabled
// pipeline steward, with the helpers to move the branch and conclude its
// runs.
type streakBranch struct {
	t      *testing.T
	ctx    context.Context
	store  *model.Store
	sim    *githubsim.Sim
	client *github.RESTClient
	runner *fakeCI
	repo   model.RepoRef
	at     time.Time
	pushes int
}

func newStreakBranch(t *testing.T) *streakBranch {
	t.Helper()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	b := &streakBranch{t: t, ctx: ctx, store: store, sim: sim, client: client, runner: newFakeCI(),
		repo: model.RepoRef{Owner: "acme", Name: "widgets"}, at: baseTime}
	id := model.PipelineStewardTemplateID
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: id, TemplateID: &id,
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 30},
		Overlap:    model.OverlapSkip, Enabled: true,
		NextRunAt: baseTime.Add(24 * time.Hour), CreatedAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	// The seed a first merge writes: a branch nothing has judged yet, so
	// the first cycle starts a run at the tip.
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: b.repo, Branch: "main", StartedAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	return b
}

// cycle runs the base-branch reconciler once, a minute after the last.
func (b *streakBranch) cycle() {
	b.t.Helper()
	b.at = b.at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(b.t, b.ctx, b.store, b.client, b.runner, b.at); err != nil {
		b.t.Fatal(err)
	}
}

func (b *streakBranch) row() model.BaseBranchCI {
	b.t.Helper()
	row, err := b.store.GetBaseBranchCI(b.ctx, b.repo, "main")
	if err != nil || row == nil {
		b.t.Fatalf("main's row: %+v (%v)", row, err)
	}
	return *row
}

// tip judges the branch's current tip: it starts the run, moves the
// branch on underneath it when moveOn is set, concludes it with jobs, and
// collects. It returns the commit judged.
func (b *streakBranch) tip(moveOn bool, conclusion ci.Conclusion, jobs ...ci.JobRun) string {
	b.t.Helper()
	b.cycle()
	row := b.row()
	if row.RunID == "" || row.Conclusion != "" {
		b.t.Fatalf("no run in flight at %s: %+v", shortSHAForTest(row.Commit), row)
	}
	if moveOn {
		b.push()
	}
	b.runner.finish(row.RunID, conclusion, jobs...)
	b.cycle()
	return row.Commit
}

func (b *streakBranch) push() {
	b.t.Helper()
	b.pushes++
	name := "CHANGE" + string(rune('A'+b.pushes)) + ".md"
	pushFileToBranch(b.t, b.sim.BareRepo, "main", name, name, "change "+name)
}

// wakes is how many times the pipeline steward has been woken about a
// confirmed red -- each wake is a note on its standing task. The task is
// marked done after every reading, so a later wake has to file a note of
// its own rather than finding the steward still busy.
func (b *streakBranch) wakes() int {
	b.t.Helper()
	steward, err := b.store.OpenTaskWithTag(b.ctx, model.ScheduleFiringTag(model.PipelineStewardTemplateID))
	if err != nil {
		b.t.Fatal(err)
	}
	if steward == nil {
		return 0
	}
	comments, err := b.store.Comments(b.ctx, steward.ID)
	if err != nil {
		b.t.Fatal(err)
	}
	n := 0
	for _, c := range comments {
		if strings.Contains(c.Body, "is confirmed red") {
			n++
		}
	}
	done := b.at
	if err := b.store.ObserveField(b.ctx, steward.ID, done, func(o *model.Observation) { o.CompletedAt = &done }); err != nil {
		b.t.Fatal(err)
	}
	return n
}

func failing(names ...string) []ci.JobRun {
	var out []ci.JobRun
	for _, name := range names {
		out = append(out, ci.JobRun{Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml",
			JobID: name, Name: name, Conclusion: ci.ConclusionFailure})
	}
	return out
}

func passing(names ...string) []ci.JobRun {
	var out []ci.JobRun
	for _, name := range names {
		out = append(out, ci.JobRun{Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml",
			JobID: name, Name: name, Conclusion: ci.ConclusionSuccess})
	}
	return out
}

// The case that happened: red at A, the branch has moved by the time the
// run concludes, and red again on the same job at B. Confirmed at B, and
// the steward is woken once, told the streak and where it began.
func TestTheSameJobRedOnConsecutiveTipsConfirmsTheBranch(t *testing.T) {
	t.Parallel()
	b := newStreakBranch(t)
	green := b.tip(true, ci.ConclusionSuccess, passing("frontend")...)
	a := b.tip(true, ci.ConclusionFailure, failing("frontend")...)
	if row := b.row(); row.Confirmed() || row.RedStreak != 1 {
		t.Fatalf("after one red tip main reads confirmed=%v streak=%d, want an unconfirmed streak of 1",
			row.Confirmed(), row.RedStreak)
	}
	if n := b.wakes(); n != 0 {
		t.Fatalf("one red tip woke the steward %d times", n)
	}
	tipB := b.tip(true, ci.ConclusionFailure, failing("frontend")...)

	row := b.row()
	if row.Commit != tipB || !row.Confirmed() {
		t.Fatalf("main reads %s confirmed=%v, want confirmed red at B %s",
			shortSHAForTest(row.Commit), row.Confirmed(), shortSHAForTest(tipB))
	}
	if row.RedStreak != 2 || row.RedStreakSince != a || row.LastGreenCommit != green {
		t.Fatalf("streak reads %d since %s after green %s, want 2 since %s after %s", row.RedStreak,
			shortSHAForTest(row.RedStreakSince), shortSHAForTest(row.LastGreenCommit),
			shortSHAForTest(a), shortSHAForTest(green))
	}
	want := "tests/frontend red on 2 consecutive tips since " + shortSHAForTest(a)
	if !strings.Contains(row.ConfirmDetail, want) {
		t.Errorf("confirmation reads %q, want it to say %q", row.ConfirmDetail, want)
	}
	if n := b.wakes(); n != 1 {
		t.Fatalf("the steward was woken %d times, want once", n)
	}
	steward, _ := b.store.OpenTaskWithTag(b.ctx, model.ScheduleFiringTag(model.PipelineStewardTemplateID))
	comments, _ := b.store.Comments(b.ctx, steward.ID)
	note := comments[len(comments)-1].Body
	for _, s := range []string{want, "last green " + shortSHAForTest(green), "first red " + shortSHAForTest(a)} {
		if !strings.Contains(note, s) {
			t.Errorf("the steward's wake note %q does not say %q", note, s)
		}
	}

	// It goes on: red at C on the same job is still confirmed, but the
	// steward already knows about this stretch.
	b.tip(true, ci.ConclusionFailure, failing("frontend")...)
	if row := b.row(); !row.Confirmed() || row.RedStreak != 3 {
		t.Fatalf("at C main reads confirmed=%v streak=%d, want confirmed on a streak of 3",
			row.Confirmed(), row.RedStreak)
	}
	if n := b.wakes(); n != 1 {
		t.Fatalf("the steward was woken %d times over one stretch, want once", n)
	}
}

// A tip that passes the streak's jobs ends it.
func TestAGreenTipResetsTheRedStreak(t *testing.T) {
	t.Parallel()
	b := newStreakBranch(t)
	b.tip(true, ci.ConclusionFailure, failing("frontend")...)
	green := b.tip(true, ci.ConclusionSuccess, passing("frontend")...)
	if row := b.row(); row.RedStreak != 0 || row.LastGreenCommit != green {
		t.Fatalf("after a green tip the streak reads %d (last green %s), want none after %s",
			row.RedStreak, shortSHAForTest(row.LastGreenCommit), shortSHAForTest(green))
	}
	b.tip(true, ci.ConclusionFailure, failing("frontend")...)
	if row := b.row(); row.Confirmed() || row.RedStreak != 1 {
		t.Fatalf("red, green, red reads confirmed=%v streak=%d, want an unconfirmed streak of 1",
			row.Confirmed(), row.RedStreak)
	}
	if n := b.wakes(); n != 0 {
		t.Fatalf("the steward was woken %d times for two reds a green apart", n)
	}
}

// Consecutive tips failing different jobs are two different reds, not one
// red twice.
func TestDifferentJobsRedOnConsecutiveTipsDoNotConfirm(t *testing.T) {
	t.Parallel()
	b := newStreakBranch(t)
	b.tip(true, ci.ConclusionFailure, failing("frontend")...)
	tipB := b.tip(true, ci.ConclusionFailure, append(failing("go"), passing("frontend")...)...)
	row := b.row()
	if row.Confirmed() {
		t.Fatal("two tips failing different jobs confirmed the branch")
	}
	if row.RedStreak != 1 || row.RedStreakSince != tipB || strings.Join(row.RedStreakJobs, ",") != "tests/go" {
		t.Fatalf("streak reads %d on %v since %s, want a new streak of 1 on tests/go at B",
			row.RedStreak, row.RedStreakJobs, shortSHAForTest(row.RedStreakSince))
	}
	if n := b.wakes(); n != 0 {
		t.Fatalf("the steward was woken %d times", n)
	}
}

// A failure grain CI replayed from its cache is that job's verdict on that
// tree, and counts like any other.
func TestACachedFailureCountsTowardTheStreak(t *testing.T) {
	t.Parallel()
	b := newStreakBranch(t)
	b.tip(true, ci.ConclusionFailure, failing("frontend")...)
	cached := failing("frontend")
	cached[0].Cached = true
	b.tip(true, ci.ConclusionFailure, cached...)
	if row := b.row(); !row.Confirmed() || row.RedStreak != 2 {
		t.Fatalf("a cached failure on the second tip reads confirmed=%v streak=%d, want confirmed on 2",
			row.Confirmed(), row.RedStreak)
	}
	if n := b.wakes(); n != 1 {
		t.Fatalf("the steward was woken %d times, want once", n)
	}
}

// Where the branch holds still long enough for a confirmation re-run to
// confirm a tip the ordinary way, the streak reaching its length at the
// next tip does not wake the steward a second time -- and on the tip
// where the streak is long enough, no re-run is asked for on top of it.
func TestTheNormalConfirmationAndTheStreakWakeTheStewardOnce(t *testing.T) {
	t.Parallel()
	b := newStreakBranch(t)
	a := b.tip(false, ci.ConclusionFailure, failing("frontend")...)
	row := b.row()
	if row.Confirmation != model.BaseCIConfirming {
		t.Fatalf("a red that held still reads %q, want its confirmation re-run going", row.Confirmation)
	}
	b.runner.finish(row.ConfirmRunID, ci.ConclusionFailure, failing("frontend")...)
	b.cycle()
	if row := b.row(); !row.Confirmed() || row.Commit != a {
		t.Fatalf("main reads confirmed=%v at %s, want confirmed by its re-run at A", row.Confirmed(),
			shortSHAForTest(row.Commit))
	}
	if n := b.wakes(); n != 1 {
		t.Fatalf("the ordinary confirmation woke the steward %d times, want once", n)
	}

	b.push()
	rechecks := b.runner.rechecks()
	tipB := b.tip(false, ci.ConclusionFailure, failing("frontend")...)
	row = b.row()
	if row.Commit != tipB || !row.Confirmed() || row.RedStreak != 2 {
		t.Fatalf("at B main reads %s confirmed=%v streak=%d, want confirmed on a streak of 2",
			shortSHAForTest(row.Commit), row.Confirmed(), row.RedStreak)
	}
	if n := b.runner.rechecks() - rechecks; n != 0 {
		t.Fatalf("%d confirmation re-runs were asked for at B, whose streak already confirms it", n)
	}
	if n := b.wakes(); n != 1 {
		t.Fatalf("the steward was woken %d times for one red stretch, want once", n)
	}
}

// The length is a deployment setting: at three, two red tips are not
// enough.
func TestTheRedStreakLengthIsADeploymentSetting(t *testing.T) {
	t.Parallel()
	b := newStreakBranch(t)
	cfg := model.DefaultConfig()
	cfg.BaseCIRedStreak = 3
	if err := b.store.PutConfig(b.ctx, cfg); err != nil {
		t.Fatal(err)
	}
	b.tip(true, ci.ConclusionFailure, failing("frontend")...)
	b.tip(true, ci.ConclusionFailure, failing("frontend")...)
	if row := b.row(); row.Confirmed() {
		t.Fatal("two red tips confirmed a branch whose deployment asks for three")
	}
	b.tip(true, ci.ConclusionFailure, failing("frontend")...)
	if row := b.row(); !row.Confirmed() || row.RedStreak != 3 {
		t.Fatalf("three red tips read confirmed=%v streak=%d, want confirmed", row.Confirmed(), row.RedStreak)
	}
}
