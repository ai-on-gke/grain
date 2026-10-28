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

package postgres_test

// grain CI's own runs, against a real database: the tables `grain ci
// show` and the CI page read a finished run out of, and the ones
// orchestrator.collectCIRun asks about a run that started before the
// daemon it is talking to did.

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ciOutcome is the part of a job that says how it passed rather than
// whether it did.
func ciOutcome(job model.CIJob) model.CIJob {
	return model.CIJob{
		Flake: job.Flake, Quarantined: job.Quarantined, QuarantinedTests: job.QuarantinedTests,
		Status: job.Status, Warning: job.Warning, FailureSummary: job.FailureSummary,
		BaseCommit: job.BaseCommit,
	}
}

func ciRun(id string, started time.Time) model.CIRun {
	return model.CIRun{
		ID: id, Key: "key-" + id, Repo: "owner/payments-api", Ref: "grain/task-1",
		Commit: "9f8e7d6c", Event: "push", RequestedBy: "a1b2", StartedAt: started,
		Jobs: []model.CIJob{
			{
				Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml", JobID: "unit",
				Name: "unit tests", Key: "job-" + id, Sandbox: "grain-ci-" + id,
				StartedAt: started, FinishedAt: started.Add(time.Minute), Conclusion: "success",
				Queued:      18 * time.Minute,
				QueuedCause: "budget",
				QueuedReason: "for room in this deployment's sandbox budget -- memory: " +
					"52.0 GiB is committed of this deployment's 64.0 GiB sandbox budget",
				RetryCount:       1,
				RetryReason:      `step "go test": --- FAIL: TestOne`,
				Flake:            true,
				Quarantined:      true,
				QuarantinedTests: []string{"TestOne", "TestTwo"},
				Status:           "passed (retry)",
				Warning:          "passed (quarantined): 2 test(s) failed but are quarantined: TestOne, TestTwo",
				FailureSummary:   `{"tests":[{"name":"TestOne","location":"one_test.go:12","quarantined":true}]}`,
				BaseCommit:       "0a1b2c3d",
				Steps: []model.CIStep{
					{Name: "go build", Conclusion: "success", StartedAt: started,
						FinishedAt: started.Add(30 * time.Second)},
					{Name: "go test", Conclusion: "failure", ExitCode: 2, Output: "--- FAIL: TestOne\n",
						Error: "", StartedAt: started.Add(30 * time.Second),
						FinishedAt: started.Add(time.Minute)},
				},
			},
			{
				Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml", JobID: "publish",
				Name: "publish", Key: "job2-" + id, Conclusion: "skipped",
				Error: "a job it needs did not succeed",
				Steps: []model.CIStep{{Name: "upload", Conclusion: "skipped", ExitCode: -1}},
			},
		},
	}
}

// The whole of what durability buys, as one test: a run written, read
// back entire, and every field of it still saying what it said --
// including the two things that only a store gets wrong (a zero time
// that must not come back as year one having happened, and plan order).
func TestACIRunComesBackAsItWasWritten(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	run := ciRun("3f2a1c", now)
	run.FinishedAt = now.Add(2 * time.Minute)
	run.Conclusion = "failure"
	run.CancelReason = "stopped by hand: the base branch is being reverted"
	run.CancelledBy = "operator"
	if err := store.PutCIRun(ctx, run); err != nil {
		t.Fatalf("putting a CI run: %v", err)
	}

	got, err := store.GetCIRun(ctx, "3f2a1c")
	if err != nil || got == nil {
		t.Fatalf("reading it back: %v, %v", got, err)
	}
	if got.Repo != run.Repo || got.Ref != run.Ref || got.Commit != run.Commit ||
		got.Event != run.Event || got.RequestedBy != run.RequestedBy || got.Key != run.Key {
		t.Errorf("the run's own columns came back changed: %+v", *got)
	}
	if !got.StartedAt.Equal(run.StartedAt) || !got.FinishedAt.Equal(run.FinishedAt) {
		t.Errorf("times came back as %v/%v, want %v/%v",
			got.StartedAt, got.FinishedAt, run.StartedAt, run.FinishedAt)
	}
	if got.Conclusion != "failure" || got.Interrupted {
		t.Errorf("conclusion came back as %q (interrupted %v)", got.Conclusion, got.Interrupted)
	}
	// The note whoever stopped the run left. It is the reason this record
	// is worth keeping after the run itself is forgotten, so it has to
	// survive the round trip like everything else here.
	if got.CancelReason != run.CancelReason {
		t.Errorf("cancel reason came back as %q, want %q", got.CancelReason, run.CancelReason)
	}
	// And who stopped it, which the note cannot say and which decides
	// whether the merge queue starts another run for the same commit
	// (ci.CancelSource, orchestrator's ciReplacementFor).
	if got.CancelledBy != run.CancelledBy {
		t.Errorf("cancelled by came back as %q, want %q", got.CancelledBy, run.CancelledBy)
	}
	if len(got.Jobs) != 2 || got.Jobs[0].JobID != "unit" || got.Jobs[1].JobID != "publish" {
		t.Fatalf("jobs came back as %+v -- plan order is what a reader scans down", got.Jobs)
	}
	if got.Jobs[1].Error != "a job it needs did not succeed" {
		t.Errorf("a skipped job's reason was lost: %+v", got.Jobs[1])
	}
	// A job that never ran has no timestamps, and "never" has to read
	// back as never rather than as the year one.
	if !got.Jobs[1].StartedAt.IsZero() || !got.Jobs[1].FinishedAt.IsZero() {
		t.Errorf("a job that never ran came back with times on it: %+v", got.Jobs[1])
	}
	if len(got.Jobs[0].Steps) != 2 || got.Jobs[0].Steps[1].Output != "--- FAIL: TestOne\n" ||
		got.Jobs[0].Steps[1].ExitCode != 2 {
		t.Errorf("steps came back as %+v", got.Jobs[0].Steps)
	}
	// How long this job waited for a machine and what held it. The row's
	// timestamps say how long it ran; without these two nobody reading
	// the run next week can tell a job that was starved from one that was
	// slow (ci.JobRun.Queued, grain/task-326). Kept as milliseconds, so
	// the assertion is that a duration survives that trip intact.
	if got.Jobs[0].Queued != run.Jobs[0].Queued {
		t.Errorf("queueing came back as %s, want %s", got.Jobs[0].Queued, run.Jobs[0].Queued)
	}
	// A job retried after its first attempt failed, and why -- which the
	// CI page draws on a job still running its retry (grain/task-1345).
	if got.Jobs[0].RetryCount != 1 || got.Jobs[0].RetryReason != run.Jobs[0].RetryReason {
		t.Errorf("retry came back as %d/%q, want 1/%q",
			got.Jobs[0].RetryCount, got.Jobs[0].RetryReason, run.Jobs[0].RetryReason)
	}
	// A job that passed only on its retry, or only because the tests it
	// failed are quarantined, and the failures it printed -- without
	// which it reads back as a clean pass and query_test_history has
	// nothing to count (grain/task-1512).
	if !reflect.DeepEqual(ciOutcome(got.Jobs[0]), ciOutcome(run.Jobs[0])) {
		t.Errorf("flake/quarantine came back as %+v, want %+v",
			ciOutcome(got.Jobs[0]), ciOutcome(run.Jobs[0]))
	}
	if !reflect.DeepEqual(ciOutcome(got.Jobs[1]), ciOutcome(model.CIJob{})) {
		t.Errorf("a job that neither flaked nor quarantined came back as %+v", ciOutcome(got.Jobs[1]))
	}
	if got.Jobs[0].QueuedReason != run.Jobs[0].QueuedReason {
		t.Errorf("what held the job came back as %q, want %q",
			got.Jobs[0].QueuedReason, run.Jobs[0].QueuedReason)
	}
	// The cause beside the sentence, which is the half that can be added
	// up: a report of this deployment's queueing buckets on this word
	// rather than on the prose above it (metrics.CIQueueing).
	if got.Jobs[0].QueuedCause != run.Jobs[0].QueuedCause {
		t.Errorf("the cause came back as %q, want %q",
			got.Jobs[0].QueuedCause, run.Jobs[0].QueuedCause)
	}
	// And a job that never waited waited for nothing, rather than coming
	// back with somebody else's number on it.
	if got.Jobs[1].Queued != 0 || got.Jobs[1].QueuedReason != "" || got.Jobs[1].QueuedCause != "" {
		t.Errorf("a job that never queued came back with a wait on it: %s %q %q",
			got.Jobs[1].Queued, got.Jobs[1].QueuedCause, got.Jobs[1].QueuedReason)
	}
}

// A run is rewritten as it goes -- once when it is planned and once per
// job as they finish -- so the second write has to replace the first
// rather than adding to it.
func TestRewritingACIRunReplacesItsJobsRatherThanAddingToThem(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	run := ciRun("3f2a1c", now)
	if err := store.PutCIRun(ctx, run); err != nil {
		t.Fatalf("putting: %v", err)
	}
	run.Jobs = run.Jobs[:1]
	run.Conclusion = "success"
	if err := store.PutCIRun(ctx, run); err != nil {
		t.Fatalf("rewriting: %v", err)
	}
	got, err := store.GetCIRun(ctx, "3f2a1c")
	if err != nil || got == nil {
		t.Fatalf("reading back: %v, %v", got, err)
	}
	if len(got.Jobs) != 1 || len(got.Jobs[0].Steps) != 2 {
		t.Errorf("the rewrite left %d job(s) behind: %+v", len(got.Jobs), got.Jobs)
	}
}

func TestListingCIRunsIsNewestFirstAndCarriesNoOutput(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	for i, id := range []string{"older", "newer", "newest"} {
		run := ciRun(id, now.Add(time.Duration(i)*time.Hour))
		run.Conclusion = "success"
		if err := store.PutCIRun(ctx, run); err != nil {
			t.Fatalf("putting %s: %v", id, err)
		}
	}
	got, err := store.ListCIRuns(ctx, 0)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(got) != 3 || got[0].ID != "newest" || got[2].ID != "older" {
		t.Fatalf("listing is newest first: %+v", got)
	}
	for _, job := range got[0].Jobs {
		for _, step := range job.Steps {
			if step.Output != "" {
				t.Errorf("a listing carries no step output, and this one has %q", step.Output)
			}
		}
	}
	// The jobs themselves are exactly what the listing is for.
	if len(got[0].Jobs) != 2 || got[0].Jobs[0].Conclusion != "success" {
		t.Errorf("a listed run keeps its jobs: %+v", got[0].Jobs)
	}

	limited, err := store.ListCIRuns(ctx, 2)
	if err != nil || len(limited) != 2 || limited[0].ID != "newest" {
		t.Errorf("a limited listing is the newest n: %+v (%v)", limited, err)
	}
}

// The narrowing pass, against the database it is written for: which runs
// match is decided in SQL over every run stored, and the page a caller
// then asks for is read back in the order it decided on
// (grain/task-289). pkg/ci's own tests are where the vocabulary is
// checked against the in-memory filter; this is about the SQL.
func TestMatchingCIRunsNarrowsOverTheWholeHistory(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	// Three runs of one repo and one of another, two branches, a job
	// that only the last of them has, and a run still going.
	for i, id := range []string{"oldest", "middle", "newest"} {
		run := ciRun(id, now.Add(time.Duration(i)*time.Hour))
		run.Conclusion = "success"
		run.FinishedAt = now.Add(time.Duration(i)*time.Hour + time.Minute)
		if err := store.PutCIRun(ctx, run); err != nil {
			t.Fatalf("putting %s: %v", id, err)
		}
	}
	other := ciRun("other", now.Add(4*time.Hour))
	other.Repo = "owner/gadgets"
	other.Ref = "main"
	other.Conclusion = "failure"
	other.Jobs[0].JobID, other.Jobs[0].Name = "e2e", "ui e2e"
	if err := store.PutCIRun(ctx, other); err != nil {
		t.Fatalf("putting other: %v", err)
	}
	// Two runs with no conclusion, which are two different answers: one
	// whose job is on a machine, and one whose jobs have not begun.
	working := ciRun("working", now.Add(5*time.Hour))
	working.Jobs[0].Conclusion, working.Jobs[1].Conclusion = "", ""
	working.Jobs[0].FinishedAt = time.Time{}
	if err := store.PutCIRun(ctx, working); err != nil {
		t.Fatalf("putting working: %v", err)
	}
	queued := ciRun("queued", now.Add(6*time.Hour))
	queued.Jobs[0].Conclusion, queued.Jobs[1].Conclusion = "", ""
	queued.Jobs[0].StartedAt, queued.Jobs[0].FinishedAt = time.Time{}, time.Time{}
	if err := store.PutCIRun(ctx, queued); err != nil {
		t.Fatalf("putting queued: %v", err)
	}

	ids := func(q model.CIRunQuery) string {
		t.Helper()
		stamps, err := store.MatchCIRuns(ctx, q)
		if err != nil {
			t.Fatalf("matching %+v: %v", q, err)
		}
		var out []string
		for _, stamp := range stamps {
			out = append(out, stamp.ID)
		}
		return strings.Join(out, " ")
	}
	for _, tc := range []struct {
		name  string
		query model.CIRunQuery
		want  string
	}{
		{"everything", model.CIRunQuery{}, "queued working other newest middle oldest"},
		{"one repo, however it is typed", model.CIRunQuery{Repo: "OWNER/Payments-API"},
			"queued working newest middle oldest"},
		{"a branch by its tail", model.CIRunQuery{Ref: "TASK-1"}, "queued working newest middle oldest"},
		{"one verdict", model.CIRunQuery{Conclusions: []string{"failure"}}, "other"},
		{"still going", model.CIRunQuery{Running: true}, "working"},
		{"waiting for a machine", model.CIRunQuery{Pending: true}, "queued"},
		{"one job", model.CIRunQuery{Job: "ui e2e"}, "other"},
		{"a job by its id", model.CIRunQuery{Job: "E2E"}, "other"},
		{"a window, which a run that has not started is inside",
			model.CIRunQuery{Since: now.Add(3 * time.Hour)}, "queued working other"},
		{"a window with an end", model.CIRunQuery{Since: now.Add(time.Hour), Until: now.Add(4 * time.Hour)},
			"newest middle"},
		{"a verdict or a state", model.CIRunQuery{Conclusions: []string{"failure"}, Pending: true}, "queued other"},
		{"nothing like it", model.CIRunQuery{Repo: "owner/nothing"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ids(tc.query); got != tc.want {
				t.Errorf("matched %q, want %q", got, tc.want)
			}
		})
	}

	if n, err := store.CountCIRuns(ctx); err != nil || n != 6 {
		t.Errorf("counting the stored runs = %d (%v), want 6", n, err)
	}

	// The page read back is in the order it was asked for, carries each
	// run's jobs and no step's output, and an id that is no longer
	// stored is left out rather than being an error -- the ids came from
	// a read of their own, and a run pruned between the two is a race
	// rather than a failure.
	page, err := store.ListCIRunsByID(ctx, []string{"newest", "oldest", "pruned-since"})
	if err != nil {
		t.Fatalf("reading a page back: %v", err)
	}
	if len(page) != 2 || page[0].ID != "newest" || page[1].ID != "oldest" {
		t.Fatalf("the page is the ids in the order asked for: %+v", page)
	}
	if len(page[0].Jobs) != 2 {
		t.Errorf("a page carries each run's jobs: %+v", page[0].Jobs)
	}
	for _, job := range page[0].Jobs {
		for _, step := range job.Steps {
			if step.Output != "" {
				t.Errorf("a page carries no step output, and this one has %q", step.Output)
			}
		}
	}
}

func TestALiveCIRunIsFoundByItsKeyAndAFinishedOneIsNot(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	live := ciRun("live", now)
	if err := store.PutCIRun(ctx, live); err != nil {
		t.Fatalf("putting: %v", err)
	}
	got, err := store.LiveCIRun(ctx, live.Key)
	if err != nil || got == nil || got.ID != "live" {
		t.Fatalf("a run with no conclusion is live: %+v (%v)", got, err)
	}
	live.Conclusion = "success"
	live.FinishedAt = now.Add(time.Minute)
	if err := store.PutCIRun(ctx, live); err != nil {
		t.Fatalf("finishing: %v", err)
	}
	got, err = store.LiveCIRun(ctx, live.Key)
	if err != nil || got != nil {
		t.Errorf("a finished run is not live: %+v (%v)", got, err)
	}
}

// The other half of Live: a run nobody is running any more must not look
// live, or the next request is deduplicated against a run that died with
// the process that started it.
func TestRecoveringInterruptedCIRunsStampsThemAndLeavesTheirJobsReadable(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutCIRun(ctx, ciRun("interrupted", now)); err != nil {
		t.Fatalf("putting: %v", err)
	}
	finished := ciRun("finished", now)
	finished.Conclusion = "success"
	if err := store.PutCIRun(ctx, finished); err != nil {
		t.Fatalf("putting: %v", err)
	}

	at := now.Add(time.Hour)
	stamped, err := store.RecoverInterruptedCIRuns(ctx, "cancelled", "the daemon stopped", at)
	if err != nil {
		t.Fatalf("recovering: %v", err)
	}
	if stamped != 1 {
		t.Errorf("stamped %d run(s), want the one that was still going", stamped)
	}

	got, err := store.GetCIRun(ctx, "interrupted")
	if err != nil || got == nil {
		t.Fatalf("reading it back: %v, %v", got, err)
	}
	if got.Conclusion != "cancelled" || !got.Interrupted || got.Error != "the daemon stopped" {
		t.Errorf("an interrupted run reads %+v", *got)
	}
	if !got.FinishedAt.Equal(at) {
		t.Errorf("it finished at %v, want the moment it was recovered (%v)", got.FinishedAt, at)
	}
	// The point of stamping rather than deleting: what the daemon was in
	// the middle of is still there to look at.
	if len(got.Jobs) != 2 || len(got.Jobs[0].Steps) != 2 {
		t.Errorf("recovery lost the jobs that had already run: %+v", got.Jobs)
	}
	if live, err := store.LiveCIRun(ctx, got.Key); err != nil || live != nil {
		t.Errorf("an interrupted run is not live: %+v (%v)", live, err)
	}
	// And a run that had already concluded is not touched by any of it.
	if after, err := store.GetCIRun(ctx, "finished"); err != nil || after == nil ||
		after.Interrupted || after.Conclusion != "success" {
		t.Errorf("a finished run was changed by recovery: %+v (%v)", after, err)
	}
}

func TestPruningCIRunsKeepsTheNewestAndNeverALiveOne(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	for i, id := range []string{"oldest", "middle", "newest"} {
		run := ciRun(id, now.Add(time.Duration(i)*time.Hour))
		run.Conclusion = "success"
		if err := store.PutCIRun(ctx, run); err != nil {
			t.Fatalf("putting %s: %v", id, err)
		}
	}
	// Older than every finished run above, and still going.
	live := ciRun("live", now.Add(-time.Hour))
	if err := store.PutCIRun(ctx, live); err != nil {
		t.Fatalf("putting the live run: %v", err)
	}

	deleted, err := store.PruneCIRuns(ctx, 2)
	if err != nil {
		t.Fatalf("pruning: %v", err)
	}
	if deleted != 1 {
		t.Errorf("pruned %d run(s), want the one finished run past the limit", deleted)
	}
	if got, err := store.GetCIRun(ctx, "oldest"); err != nil || got != nil {
		t.Errorf("the oldest finished run should be gone: %+v (%v)", got, err)
	}
	if got, err := store.GetCIRun(ctx, "live"); err != nil || got == nil {
		t.Errorf("a run still going is never pruned, whatever the limit says: %+v (%v)", got, err)
	}
	// Its children go with it: a step row left behind would be output
	// nothing can ever read, kept forever.
	var steps int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `ci_step` WHERE `run_id` = 'oldest'").Scan(&steps); err != nil {
		t.Fatalf("counting steps: %v", err)
	}
	if steps != 0 {
		t.Errorf("pruning a run left %d of its step rows behind", steps)
	}

	if deleted, err := store.PruneCIRuns(ctx, 0); err != nil || deleted != 0 {
		t.Errorf("keep of zero means no limit rather than delete everything: %d (%v)", deleted, err)
	}
}

// The output sweep is the other half of pruning, and it is counted in
// bytes rather than in runs (grain/task-269): what it costs a deployment
// is disk, and one run's output is anything between nothing and the
// per-run cap.
func TestSweepingCIStepOutputClearsTheOldestUntilItFits(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	// Three finished runs, an hour apart, each holding the same amount of
	// output, and one still going that is older than all of them.
	for i, id := range []string{"oldest", "middle", "newest"} {
		run := ciRun(id, now.Add(time.Duration(i)*time.Hour))
		run.Conclusion = "success"
		run.Jobs[0].Steps[1].Output = strings.Repeat("x", 1024)
		if err := store.PutCIRun(ctx, run); err != nil {
			t.Fatalf("putting %s: %v", id, err)
		}
	}
	live := ciRun("live", now.Add(-time.Hour))
	live.Jobs[0].Steps[1].Output = strings.Repeat("x", 1024)
	if err := store.PutCIRun(ctx, live); err != nil {
		t.Fatalf("putting the live run: %v", err)
	}

	// Room for two runs' worth. The live run is charged for and never
	// cleared, so what has to go is the oldest finished one.
	sweep, err := store.PruneCIStepOutput(ctx, model.CIStepOutputBounds{MaxBytes: 2 * 1024})
	if err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	if sweep.Runs != 1 || sweep.Bytes != 1024 {
		t.Errorf("swept %+v, want the one finished run whose output did not fit", sweep)
	}
	// Kept is what is left behind, and it is larger than the ceiling here
	// on purpose: the live run's output is charged against the bound and
	// never cleared, so a reader of this number sees the table rather
	// than the part of it the sweep may touch.
	if sweep.Kept != 3*1024 {
		t.Errorf("kept %d bytes, want the two newest runs' and the live one's", sweep.Kept)
	}
	if got, err := store.GetCIRun(ctx, "oldest"); err != nil || got == nil {
		t.Fatalf("the oldest run lost its record: %+v (%v)", got, err)
	} else if got.Jobs[0].Steps[1].Output != "" {
		t.Error("the oldest run kept its output past the ceiling")
	} else if got.Conclusion != "success" || len(got.Jobs) != 2 {
		t.Errorf("a record must outlive its own output: %+v", got)
	}
	if got, err := store.GetCIRun(ctx, "newest"); err != nil || got == nil ||
		got.Jobs[0].Steps[1].Output == "" {
		t.Errorf("the newest run should keep its output: %+v (%v)", got, err)
	}
	if got, err := store.GetCIRun(ctx, "live"); err != nil || got == nil ||
		got.Jobs[0].Steps[1].Output == "" {
		t.Errorf("a run still going is never cleared, whatever the bounds say: %+v (%v)", got, err)
	}

	// The age bound, with room to spare under the ceiling.
	if _, err := store.PruneCIStepOutput(ctx,
		model.CIStepOutputBounds{MaxBytes: 1 << 30, Before: now.Add(90 * time.Minute)}); err != nil {
		t.Fatalf("sweeping by age: %v", err)
	}
	var kept int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `ci_step` WHERE `run_id` = 'middle' AND `output` != ''").Scan(&kept); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if kept != 0 {
		t.Error("output older than the age bound was kept where there was room for it")
	}

	// And bounds that say nothing sweep nothing, rather than everything.
	if sweep, err := store.PruneCIStepOutput(ctx, model.CIStepOutputBounds{}); err != nil || sweep.Runs != 0 {
		t.Errorf("empty bounds mean no limit rather than delete everything: %+v (%v)", sweep, err)
	}
	if got, err := store.GetCIRun(ctx, "newest"); err != nil || got == nil ||
		got.Jobs[0].Steps[1].Output == "" {
		t.Errorf("the newest run's output did not survive an empty sweep: %+v (%v)", got, err)
	}
}

// What the sweep would be sweeping, read without sweeping it: the
// figure the Storage section holds against the ceiling
// (grain/task-290).
func TestCIStepOutputUsageCountsWhatTheSweepWouldCharge(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if usage, err := store.CIStepOutputUsage(ctx); err != nil || usage.Bytes != 0 || usage.Runs != 0 ||
		!usage.Oldest.IsZero() {
		t.Fatalf("an empty history holds nothing: %+v (%v)", usage, err)
	}
	for i, id := range []string{"oldest", "newest"} {
		run := ciRun(id, now.Add(time.Duration(i)*time.Hour))
		run.Conclusion = "success"
		run.Jobs[0].Steps[1].Output = strings.Repeat("x", 1024)
		if err := store.PutCIRun(ctx, run); err != nil {
			t.Fatalf("putting %s: %v", id, err)
		}
	}
	// A run still going is charged for here exactly as the sweep charges
	// it, so the reading describes the table the bound is applied to.
	live := ciRun("live", now.Add(2*time.Hour))
	live.Jobs[0].Steps[1].Output = strings.Repeat("x", 512)
	if err := store.PutCIRun(ctx, live); err != nil {
		t.Fatalf("putting the live run: %v", err)
	}
	usage, err := store.CIStepOutputUsage(ctx)
	if err != nil {
		t.Fatalf("measuring: %v", err)
	}
	if usage.Runs != 3 || usage.Steps != 3 || usage.Bytes != 2*1024+512 {
		t.Errorf("measured %+v, want every run that has output and every byte of it", usage)
	}
	if !usage.Oldest.Equal(now) {
		t.Errorf("oldest run with output is %v, want %v", usage.Oldest, now)
	}

	// Sweeping the oldest takes it out of the reading, and the reading's
	// remainder is the sweep's own Kept -- the two are the same
	// accounting, which is the whole point of measuring it here.
	sweep, err := store.PruneCIStepOutput(ctx, model.CIStepOutputBounds{MaxBytes: 1024 + 512})
	if err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	usage, err = store.CIStepOutputUsage(ctx)
	if err != nil {
		t.Fatalf("measuring after the sweep: %v", err)
	}
	if usage.Bytes != sweep.Kept {
		t.Errorf("usage %d bytes, sweep kept %d -- they must agree", usage.Bytes, sweep.Kept)
	}
	if usage.Runs != 2 || !usage.Oldest.Equal(now.Add(time.Hour)) {
		t.Errorf("after the sweep: %+v, want the two newest runs and the middle one's start", usage)
	}
}

func TestGettingACIRunThatWasNeverStoredIsNotAnError(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	got, err := store.GetCIRun(ctx, "nothing")
	if err != nil || got != nil {
		t.Errorf("a run nobody has reads as nil with no error: %+v (%v)", got, err)
	}
}

func TestOldestLiveWorkStartedAtCombinesAgentAndCI(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	agentStarted := now.Add(-2 * time.Hour)
	ciStarted := now.Add(-time.Hour)

	if err := store.PutTask(ctx, task("live-agent", true)); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "agent-run", TaskID: "live-agent", Sandbox: "agent-run",
		Attempt: 1, StartedAt: agentStarted,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	run := ciRun("live-ci", ciStarted)
	if err := store.PutCIRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	oldest, live, err := store.OldestLiveWorkStartedAt(ctx)
	if err != nil || !live || !oldest.Equal(agentStarted) {
		t.Fatalf("oldest with both live = %v, %v, %v; want agent start %v", oldest, live, err, agentStarted)
	}

	if err := store.FinishRun(ctx, "agent-run", now, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	oldest, live, err = store.OldestLiveWorkStartedAt(ctx)
	if err != nil || !live || !oldest.Equal(ciStarted) {
		t.Fatalf("oldest with only CI live = %v, %v, %v; want CI start %v", oldest, live, err, ciStarted)
	}

	run.Conclusion = "success"
	run.FinishedAt = now
	if err := store.PutCIRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	oldest, live, err = store.OldestLiveWorkStartedAt(ctx)
	if err != nil || live || !oldest.IsZero() {
		t.Fatalf("oldest while idle = %v, %v, %v; want zero, false, nil", oldest, live, err)
	}
}

// The narrow write a run in flight makes over and over: one step's
// output, and nothing else disturbed -- not the other step of that job,
// not the other job, not the run's own row.
func TestWritingOneStepsOutputLeavesTheRestOfTheRunAlone(t *testing.T) {
	store, _, ctx := openStore(t)
	run := ciRun("3f2a1c", now)
	if err := store.PutCIRun(ctx, run); err != nil {
		t.Fatalf("putting: %v", err)
	}
	if err := store.PutCIStepOutput(ctx, "3f2a1c", 0, 1, "--- FAIL: TestOne\nand then some more\n"); err != nil {
		t.Fatalf("writing one step's output: %v", err)
	}
	got, err := store.GetCIRun(ctx, "3f2a1c")
	if err != nil || got == nil {
		t.Fatalf("reading back: %v, %v", got, err)
	}
	if len(got.Jobs) != 2 || len(got.Jobs[0].Steps) != 2 || len(got.Jobs[1].Steps) != 1 {
		t.Fatalf("the run's shape changed under a one-column write: %+v", got.Jobs)
	}
	if out := got.Jobs[0].Steps[1].Output; out != "--- FAIL: TestOne\nand then some more\n" {
		t.Errorf("the step's output came back as %q", out)
	}
	if got.Jobs[0].Steps[1].Name != "go test" || got.Jobs[0].Steps[1].Conclusion != "failure" ||
		got.Jobs[0].Steps[1].ExitCode != 2 || !got.Jobs[0].Steps[1].FinishedAt.Equal(now.Add(time.Minute)) {
		t.Errorf("the step's other columns moved: %+v", got.Jobs[0].Steps[1])
	}
	if got.Jobs[0].Steps[0].Name != "go build" || got.Jobs[1].JobID != "publish" ||
		got.Conclusion != run.Conclusion || !got.StartedAt.Equal(now) {
		t.Errorf("something else in the run moved: %+v", *got)
	}
}

// A step that is not there is an error rather than a write that lands
// nowhere: the caller read the row moments ago, so its absence means the
// run has been pruned or the indexes are wrong.
func TestWritingTheOutputOfAStepThatIsNotStoredIsAnError(t *testing.T) {
	store, _, ctx := openStore(t)
	if err := store.PutCIRun(ctx, ciRun("3f2a1c", now)); err != nil {
		t.Fatalf("putting: %v", err)
	}
	if err := store.PutCIStepOutput(ctx, "3f2a1c", 0, 7, "nowhere to put this"); err == nil {
		t.Error("writing to a step index the run does not have was accepted")
	}
	if err := store.PutCIStepOutput(ctx, "no-such-run", 0, 0, "nor this"); err == nil {
		t.Error("writing to a run that was never stored was accepted")
	}
}

func TestChunkedAppendOnlyStorageForLiveCIStepLogs(t *testing.T) {
	store, db, ctx := openStore(t)
	run := ciRun("live-run", now)
	run.Jobs[0].Steps[0].Output = ""
	if err := store.PutCIRun(ctx, run); err != nil {
		t.Fatalf("putting: %v", err)
	}

	// Stream chunks via PutCIStepOutput
	chunks := []string{"chunk 1\n", "chunk 2\n", "chunk 3\n"}
	accumulated := ""
	for i, chunk := range chunks {
		accumulated += chunk
		if err := store.PutCIStepOutput(ctx, "live-run", 0, 0, accumulated); err != nil {
			t.Fatalf("putting chunk %d: %v", i, err)
		}
	}

	// Check that ci_step_log_chunk has 3 rows
	var chunkCount int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `ci_step_log_chunk` WHERE `run_id` = 'live-run' AND `job_index` = 0 AND `step_index` = 0").Scan(&chunkCount); err != nil {
		t.Fatalf("counting chunks: %v", err)
	}
	if chunkCount != 3 {
		t.Errorf("chunk count in ci_step_log_chunk = %d, want 3", chunkCount)
	}

	// ci_step.output in database should still be empty (not overwritten during live streaming)
	var stepOutput string
	if err := db.QueryRowContext(ctx,
		"SELECT `output` FROM `ci_step` WHERE `run_id` = 'live-run' AND `job_index` = 0 AND `step_index` = 0").Scan(&stepOutput); err != nil {
		t.Fatalf("reading ci_step output: %v", err)
	}
	if stepOutput != "" {
		t.Errorf("ci_step output = %q, want empty string before conclusion", stepOutput)
	}

	// Reading via GetCIRun should concatenate the live chunks
	got, err := store.GetCIRun(ctx, "live-run")
	if err != nil || got == nil {
		t.Fatalf("GetCIRun: %v, %v", got, err)
	}
	if got.Jobs[0].Steps[0].Output != "chunk 1\nchunk 2\nchunk 3\n" {
		t.Errorf("GetCIRun output = %q, want %q", got.Jobs[0].Steps[0].Output, "chunk 1\nchunk 2\nchunk 3\n")
	}

	// Upon step conclusion / PutCIRun, whole output is written to ci_step and chunks are deleted
	run.Jobs[0].Steps[0].Output = "chunk 1\nchunk 2\nchunk 3\n"
	run.Jobs[0].Steps[0].Conclusion = "success"
	if err := store.PutCIRun(ctx, run); err != nil {
		t.Fatalf("putting concluded run: %v", err)
	}

	// ci_step_log_chunk should now be cleared
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `ci_step_log_chunk` WHERE `run_id` = 'live-run'").Scan(&chunkCount); err != nil {
		t.Fatalf("counting chunks: %v", err)
	}
	if chunkCount != 0 {
		t.Errorf("chunk count after conclusion = %d, want 0", chunkCount)
	}

	// ci_step.output should now contain the whole output
	if err := db.QueryRowContext(ctx,
		"SELECT `output` FROM `ci_step` WHERE `run_id` = 'live-run' AND `job_index` = 0 AND `step_index` = 0").Scan(&stepOutput); err != nil {
		t.Fatalf("reading ci_step output: %v", err)
	}
	if stepOutput != "chunk 1\nchunk 2\nchunk 3\n" {
		t.Errorf("concluded ci_step output = %q, want %q", stepOutput, "chunk 1\nchunk 2\nchunk 3\n")
	}
}

func TestChunkedAppendOnlyStorageRolloverElision(t *testing.T) {
	store, db, ctx := openStore(t)
	run := ciRun("rollover-run", now)
	run.Jobs[0].Steps[0].Output = ""
	if err := store.PutCIRun(ctx, run); err != nil {
		t.Fatalf("putting: %v", err)
	}

	// Emit initial chunks
	if err := store.PutCIStepOutput(ctx, "rollover-run", 0, 0, "head line 1\nhead line 2\n"); err != nil {
		t.Fatalf("putting chunk 1: %v", err)
	}
	if err := store.PutCIStepOutput(ctx, "rollover-run", 0, 0, "head line 1\nhead line 2\nmiddle line 1\n"); err != nil {
		t.Fatalf("putting chunk 2: %v", err)
	}

	// Middle elision / rollover happens: output becomes head + elision note + tail
	elidedOutput := "head line 1\n\n[grain ci] 1,024 bytes elided\n\nnewest tail line\n"
	if err := store.PutCIStepOutput(ctx, "rollover-run", 0, 0, elidedOutput); err != nil {
		t.Fatalf("putting elided chunk: %v", err)
	}

	// Verify that chunks were reset and log is not duplicated
	got, err := store.GetCIRun(ctx, "rollover-run")
	if err != nil || got == nil {
		t.Fatalf("GetCIRun: %v, %v", got, err)
	}
	if got.Jobs[0].Steps[0].Output != elidedOutput {
		t.Errorf("GetCIRun output = %q, want %q", got.Jobs[0].Steps[0].Output, elidedOutput)
	}

	var chunkCount int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `ci_step_log_chunk` WHERE `run_id` = 'rollover-run'").Scan(&chunkCount); err != nil {
		t.Fatalf("counting chunks: %v", err)
	}
	if chunkCount != 1 {
		t.Errorf("chunk count after elision = %d, want 1", chunkCount)
	}
}

func TestConcurrentPutCIStepOutput(t *testing.T) {
	store, _, ctx := openStore(t)
	const numRuns = 10
	const workers = 50
	const chunksPerWorker = 10

	for i := 0; i < numRuns; i++ {
		runID := fmt.Sprintf("concurrent-run-%d", i)
		run := ciRun(runID, now)
		run.Jobs[0].Steps[0].Output = ""
		if err := store.PutCIRun(ctx, run); err != nil {
			t.Fatalf("putting run %s: %v", runID, err)
		}
	}

	var wg sync.WaitGroup
	errCh := make(chan error, workers*chunksPerWorker)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			runID := fmt.Sprintf("concurrent-run-%d", workerID%numRuns)
			for c := 0; c < chunksPerWorker; c++ {
				output := fmt.Sprintf("chunk from worker %d step %d\n", workerID, c)
				if err := store.PutCIStepOutput(ctx, runID, 0, 0, output); err != nil {
					errCh <- fmt.Errorf("worker %d chunk %d: %w", workerID, c, err)
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent PutCIStepOutput failed: %v", err)
	}

	// Verify that each run's output contains appended chunks without error
	for i := 0; i < numRuns; i++ {
		runID := fmt.Sprintf("concurrent-run-%d", i)
		got, err := store.GetCIRun(ctx, runID)
		if err != nil || got == nil {
			t.Fatalf("GetCIRun(%s): %v", runID, err)
		}
		if got.Jobs[0].Steps[0].Output == "" {
			t.Errorf("expected non-empty output for run %s", runID)
		}
	}
}
