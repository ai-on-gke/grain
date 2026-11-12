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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// optInStage1 declares the release train's Stage 1 trigger on repo, which
// is what opts it in to Stage 1 repairs.
func optInStage1(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef) {
	t.Helper()
	tmpl := "release-train"
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-train-stage1", Title: "Release train (Stage 1 verdict)", Target: repo, Base: "main",
		Enabled: true, Overlap: model.OverlapSkip, TemplateID: &tmpl,
		Recurrence: model.Recurrence{
			Kind: model.RecurrenceGitHubWorkflowRun,
			GitHubWorkflowRun: &model.GitHubWorkflowRunTrigger{
				Workflow: "nightly.yml", Branches: []string{"v*-rc.*"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// stage1Run is a concluded nightly.yml run on ref, with its jobs recorded
// the way ensureGitHubWorkflowRunJobs records them.
func stage1Run(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef, id int64, ref, event,
	conclusion string, at time.Time, jobs map[string]string) (ConcludedGitHubWorkflowRun, []model.GitHubCIJob) {

	t.Helper()
	run := ConcludedGitHubWorkflowRun{
		Repo: repo, WorkflowName: "nightly", WorkflowPath: ".github/workflows/nightly.yml", RunID: id, Attempt: 1,
		Conclusion: conclusion, HeadSHA: "abcdef0123456789abcdef", Ref: ref, Event: event,
		HTMLURL:     "https://github.example/acme/widgets/actions/runs/" + strings.TrimSpace(time.Duration(id).String()),
		CompletedAt: &at,
	}
	var out []model.GitHubCIJob
	n := int64(0)
	for name, c := range jobs {
		n++
		j := model.GitHubCIJob{
			Repo: repo.String(), CheckRunID: id*100 + n, Source: model.GitHubCISource, App: model.GitHubActionsApp,
			Ref: ref, Commit: run.HeadSHA, Workflow: "nightly", RunID: id, RunAttempt: 1, JobName: name,
			Conclusion: c, CompletedAt: &at, RunURL: run.HTMLURL, UpdatedAt: at,
		}
		if j.Failed() {
			j.FailingStep, j.FailureTail = "go test", "--- FAIL: TestLongThing ("+name+")"
		}
		if err := store.PutGitHubCIJob(ctx, j); err != nil {
			t.Fatal(err)
		}
		out = append(out, j)
	}
	return run, out
}

func stage1Repairs(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef) []string {
	t.Helper()
	ids, err := store.OpenTasksWithTag(ctx, Stage1RepairTag(repo))
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// A red Stage 1 files exactly one repair at priority 9 with the failures,
// the run and the commit; the next red run updates that repair rather than
// filing a second; a green Stage 1 closes it.
func TestARedStage1HasExactlyOneRepairUntilStage1IsGreen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	optInStage1(t, ctx, store, repo)
	deps := Deps{Store: store}
	now := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)

	run, jobs := stage1Run(t, ctx, store, repo, 41, "v3.5.0-rc.1", "push", "failure", now,
		map[string]string{"go-long": "failure", "kvm": "failure", "lint": "success"})
	if err := handleStage1Verdict(ctx, deps, run, jobs, now); err != nil {
		t.Fatal(err)
	}
	ids := stage1Repairs(t, ctx, store, repo)
	if len(ids) != 1 {
		t.Fatalf("a red Stage 1 filed %d repairs, want 1", len(ids))
	}
	repair, err := store.GetTask(ctx, ids[0])
	if err != nil || repair == nil {
		t.Fatal(err)
	}
	if repair.Priority != Stage1RepairPriority || Stage1RepairPriority != 9 {
		t.Errorf("repair priority %d, want 9", repair.Priority)
	}
	for _, want := range []string{"go-long", "kvm", "TestLongThing", run.HTMLURL, run.HeadSHA, "v3.5.0-rc.1",
		// The trailers recordRepairBlame reads: a Stage 1 repair has no
		// other evidence of the merge it repaired.
		"Fixes-commit: <sha>", "Blames: task <id>"} {
		if !strings.Contains(repair.Body, want) {
			t.Errorf("repair body does not name %q:\n%s", want, repair.Body)
		}
	}
	if strings.Contains(repair.Body, "**lint**") {
		t.Errorf("repair body names a green job:\n%s", repair.Body)
	}
	g, err := store.TaskGoal(ctx, repair.ID)
	if err != nil || g == nil || g.Kind != model.GoalNightlyGreen || g.OnMet != model.GoalCloseOnMet {
		t.Fatalf("repair goal = %+v (%v), want nightly-green on_met close", g, err)
	}

	// Somebody lowers its priority; the same run delivered again (webhook
	// and poll) and then a second red run, on main, update it -- never a
	// second repair (cf. #4093's duplicate review).
	if err := store.UpdateTask(ctx, repair.ID, func(t *model.Task) error { t.Priority = 3; return nil }); err != nil {
		t.Fatal(err)
	}
	later := now.Add(2 * time.Hour)
	run2, jobs2 := stage1Run(t, ctx, store, repo, 42, "main", "schedule", "failure", later,
		map[string]string{"ui-e2e": "failure"})
	for range 2 {
		if err := handleStage1Verdict(ctx, deps, run2, jobs2, later); err != nil {
			t.Fatal(err)
		}
	}
	if ids := stage1Repairs(t, ctx, store, repo); len(ids) != 1 || ids[0] != repair.ID {
		t.Fatalf("a further red Stage 1 left repairs %v, want only %s", ids, repair.ID)
	}
	repair, _ = store.GetTask(ctx, repair.ID)
	if repair.Priority != Stage1RepairPriority {
		t.Errorf("an updated repair is at priority %d, want %d kept", repair.Priority, Stage1RepairPriority)
	}
	comments, _ := store.Comments(ctx, repair.ID)
	found := false
	for _, c := range comments {
		found = found || (strings.Contains(c.Body, "ui-e2e") && strings.Contains(c.Body, run2.HTMLURL))
	}
	if !found {
		t.Errorf("the second red run's failures were not appended to %s", repair.ID)
	}

	// A red run on a branch that is no Stage 1 files nothing.
	other, otherJobs := stage1Run(t, ctx, store, repo, 43, "feature-x", "push", "failure", later,
		map[string]string{"go-long": "failure"})
	if err := closeTaskBy(ctx, store, repair.ID, later, stage1Repairer, "test", func(o *model.Observation) { o.ClosedAt = &later }); err != nil {
		t.Fatal(err)
	}
	if err := handleStage1Verdict(ctx, deps, other, otherJobs, later); err != nil {
		t.Fatal(err)
	}
	if ids := stage1Repairs(t, ctx, store, repo); len(ids) != 0 {
		t.Fatalf("a red run on feature-x filed %v", ids)
	}

	// Red again on an rc: a new repair (the last one is closed) ...
	red3, redJobs3 := stage1Run(t, ctx, store, repo, 44, "v3.5.0-rc.2", "push", "failure", later.Add(time.Hour),
		map[string]string{"image-vulns": "failure"})
	if err := handleStage1Verdict(ctx, deps, red3, redJobs3, later.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ids = stage1Repairs(t, ctx, store, repo)
	if len(ids) != 1 {
		t.Fatalf("an rc-only red Stage 1 filed %d repairs, want 1", len(ids))
	}
	// ... which the next green Stage 1 closes.
	green := later.Add(3 * time.Hour)
	run3, jobs3 := stage1Run(t, ctx, store, repo, 45, "v3.5.0-rc.3", "push", "success", green,
		map[string]string{"go-long": "success", "kvm": "success", "image-vulns": "success", "loadtest": "skipped"})
	if err := handleStage1Verdict(ctx, deps, run3, jobs3, green); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, ids[0]); st != model.StateClosed {
		t.Fatalf("a green Stage 1 left repair %s %s, want closed", ids[0], st)
	}
}

// A repository that has not declared the train's Stage 1 trigger never
// gets a repair, whatever its nightly.yml does.
func TestStage1RepairsNeedTheTrainsTrigger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "gadgets"}
	now := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
	run, jobs := stage1Run(t, ctx, store, repo, 51, "main", "schedule", "failure", now,
		map[string]string{"go-long": "failure"})
	if err := handleStage1Verdict(ctx, Deps{Store: store}, run, jobs, now); err != nil {
		t.Fatal(err)
	}
	if ids := stage1Repairs(t, ctx, store, repo); len(ids) != 0 {
		t.Fatalf("a repository that did not opt in got repairs %v", ids)
	}
}

// A repair queued and never started for an hour raises
// pipeline.nightly-red-unattended naming it; one that has not waited that
// long does not, and the notification clears once the repair closes.
func TestAnUnattendedStage1RepairRaisesANotification(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	optInStage1(t, ctx, store, repo)
	deps := Deps{Store: store}
	now := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
	run, jobs := stage1Run(t, ctx, store, repo, 61, "v3.5.0-rc.1", "push", "failure", now,
		map[string]string{"go-long": "failure"})
	if err := handleStage1Verdict(ctx, deps, run, jobs, now); err != nil {
		t.Fatal(err)
	}
	ids := stage1Repairs(t, ctx, store, repo)
	if len(ids) != 1 {
		t.Fatalf("want one repair, have %v", ids)
	}
	subject := stage1Subject(repo)

	if err := reconcileStage1Repairs(ctx, deps, now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.OpenNotification(ctx, subject); n != nil {
		t.Fatalf("raised %s after 30 minutes queued: %s", subject, n.Body)
	}

	if err := reconcileStage1Repairs(ctx, deps, now.Add(61*time.Minute)); err != nil {
		t.Fatal(err)
	}
	n, err := store.OpenNotification(ctx, subject)
	if err != nil || n == nil {
		t.Fatalf("no %s after an hour queued (%v)", subject, err)
	}
	if n.TaskID != ids[0] || !strings.Contains(n.Body, ids[0]) || !strings.Contains(n.Body, "never started") {
		t.Errorf("notification does not name the repair and what it waits on: %+v", n)
	}
	if n.Family != model.FamilyNightlyRedUnattended || !strings.HasSuffix(model.FamilyNightlyRedUnattended, "nightly-red-unattended") {
		t.Errorf("notification family %q", n.Family)
	}

	// Deferred for longer than the patience: still unattended, and says so.
	deferredAt := now.Add(2 * time.Hour)
	if err := store.UpdateTask(ctx, ids[0], func(t *model.Task) error { t.DeferredAt = &deferredAt; return nil }); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, ids[0]); st == model.StateDeferred {
		repair, _ := store.GetTask(ctx, ids[0])
		why, err := stage1Unattended(ctx, store, *repair, deferredAt.Add(4*time.Hour))
		if err != nil || !strings.Contains(why, "deferred") {
			t.Errorf("a repair deferred 4h is unattended because %q (%v), want deferred", why, err)
		}
	}

	closeAt := now.Add(5 * time.Hour)
	if err := closeTaskBy(ctx, store, ids[0], closeAt, stage1Repairer, "test", func(o *model.Observation) { o.ClosedAt = &closeAt }); err != nil {
		t.Fatal(err)
	}
	if err := reconcileStage1Repairs(ctx, deps, now.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.OpenNotification(ctx, subject); n != nil {
		t.Fatalf("%s still open once the repair closed", subject)
	}
}

// A scheduled run on main that nightly.yml's idle-gate turned off
// (idle-gate passed, every Stage 1 job and stage-1 skipped, the run
// success) neither files a repair nor closes an open one.
func TestASkippedScheduledStage1NeitherFilesARepairNorClosesOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	optInStage1(t, ctx, store, repo)
	deps := Deps{Store: store}
	now := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)

	noop, noopJobs := stage1Run(t, ctx, store, repo, 71, "main", "schedule", "success", now,
		map[string]string{
			"idle-gate":                      "success",
			"go, shuffled and repeated (1)":  "skipped",
			"upgrade from previous versions": "skipped",
			"stage 1 qualification verdict":  "skipped",
		})
	if err := handleStage1Verdict(ctx, deps, noop, noopJobs, now); err != nil {
		t.Fatal(err)
	}
	if ids := stage1Repairs(t, ctx, store, repo); len(ids) != 0 {
		t.Fatalf("an idle-gated scheduled run filed repairs %v", ids)
	}

	redAt := now.Add(time.Hour)
	red, redJobs := stage1Run(t, ctx, store, repo, 72, "v3.5.0-rc.1", "push", "failure", redAt,
		map[string]string{"idle-gate": "success", "go-long": "failure", "stage 1 qualification verdict": "failure"})
	if err := handleStage1Verdict(ctx, deps, red, redJobs, redAt); err != nil {
		t.Fatal(err)
	}
	ids := stage1Repairs(t, ctx, store, repo)
	if len(ids) != 1 {
		t.Fatalf("want 1 repair, got %v", ids)
	}
	if err := reconcileStage1Repairs(ctx, deps, redAt.Add(61*time.Minute)); err != nil {
		t.Fatal(err)
	}
	subject := stage1Subject(repo)
	if n, _ := store.OpenNotification(ctx, subject); n == nil {
		t.Fatalf("expected %s before the idle-gated run", subject)
	}

	skipAt := redAt.Add(2 * time.Hour)
	noop2, noopJobs2 := stage1Run(t, ctx, store, repo, 73, "main", "schedule", "success", skipAt,
		map[string]string{"idle-gate": "success", "go-long": "skipped", "stage 1 qualification verdict": "skipped"})
	if err := handleStage1Verdict(ctx, deps, noop2, noopJobs2, skipAt); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, ids[0]); st == model.StateClosed {
		t.Fatalf("an idle-gated scheduled run closed Stage 1 repair %s", ids[0])
	}
	if n, _ := store.OpenNotification(ctx, subject); n == nil {
		t.Fatalf("an idle-gated scheduled run cleared %s while repair %s is open", subject, ids[0])
	}
}
