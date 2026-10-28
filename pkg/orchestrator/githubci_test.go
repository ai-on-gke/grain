package orchestrator_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ghwebhook"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func runGitHubCITails(t *testing.T, ctx context.Context, deps orchestrator.Deps, now time.Time) {
	t.Helper()
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == "github-ci-tails" {
			if err := r.Reconcile(ctx, deps, now); err != nil {
				t.Fatalf("github-ci-tails reconciler: %v", err)
			}
			return
		}
	}
	t.Fatal("github-ci-tails reconciler is not registered in orchestrator.Reconcilers()")
}

func logReads(sim *githubsim.Sim) []string {
	var out []string
	for _, c := range sim.Calls {
		if strings.Contains(c.Path, "/actions/jobs/") && strings.HasSuffix(c.Path, "/logs") {
			out = append(out, c.Path)
		}
	}
	return out
}

func TestGitHubCIWebhookIngestionDeduplicatesRedeliveries(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	started := baseTime.Add(-4 * time.Minute)
	done := baseTime.Add(-time.Minute)
	job := ghwebhook.Event{
		Event: "workflow_job", Action: "completed", Repo: "acme/widgets",
		SenderLogin: "github-actions[bot]", SenderType: "Bot",
		CheckRunID: 4242, CheckName: "unit", CheckStatus: "completed", CheckConclusion: "failure",
		CheckApp: "github-actions", WorkflowName: "ci", WorkflowRun: 77, RunAttempt: 1,
		HeadRef: "feature-a", HeadSHA: "abc123", FailingStep: "go test",
		CheckStartedAt: &started, CheckCompletedAt: &done,
		HTMLURL: "https://github.com/acme/widgets/actions/runs/77/job/4242",
	}
	first, redelivered := job, job
	first.Seq, first.DeliveryID = 1, "d-1"
	redelivered.Seq, redelivered.DeliveryID = 2, "d-2"
	// The same job's check_run delivery: no workflow, but the same id.
	checkRun := ghwebhook.Event{
		Seq: 3, DeliveryID: "d-3", Event: "check_run", Action: "completed", Repo: "acme/widgets",
		SenderLogin: "github-actions[bot]", SenderType: "Bot",
		CheckRunID: 4242, CheckName: "unit", CheckConclusion: "failure", CheckApp: "github-actions",
		HeadSHA: "abc123",
	}
	// Not recorded: a job still running, and grain's own check.
	running := ghwebhook.Event{
		Seq: 4, DeliveryID: "d-4", Event: "workflow_job", Action: "in_progress", Repo: "acme/widgets",
		CheckRunID: 4243, CheckName: "lint", CheckStatus: "in_progress",
	}
	own := ghwebhook.Event{
		Seq: 5, DeliveryID: "d-5", Event: "check_run", Action: "completed", Repo: "acme/widgets",
		CheckRunID: 4244, CheckName: "grain/review", CheckConclusion: "success",
	}
	// A third-party check: its output is its failure tail.
	thirdParty := ghwebhook.Event{
		Seq: 6, DeliveryID: "d-6", Event: "check_run", Action: "completed", Repo: "acme/widgets",
		CheckRunID: 5000, CheckName: "coverage", CheckConclusion: "failure", CheckApp: "codecov",
		HeadSHA: "abc123", HeadRef: "feature-a", CheckOutput: "Coverage dropped below 80%: 71.2%",
	}
	source := &fakeEventSource{events: []ghwebhook.Event{first, redelivered, checkRun, running, own, thirdParty}}
	deps := orchestrator.Deps{Store: store, GitHubEvents: source}
	runGitHubEventsReconciler(t, ctx, deps, baseTime)
	if source.acked != 6 {
		t.Fatalf("acked = %d, want 6", source.acked)
	}

	jobs, err := store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("recorded %d jobs, want 2 (one per check run): %+v", len(jobs), jobs)
	}
	got, _ := store.GetGitHubCIJob(ctx, "acme/widgets", 4242)
	if got == nil || got.Source != "github" || got.Workflow != "ci" || got.FailingStep != "go test" ||
		got.Ref != "feature-a" || got.Commit != "abc123" || got.RunID != 77 ||
		got.RunURL != "https://github.com/acme/widgets/actions/runs/77" || got.Via != "webhook" ||
		got.StartedAt == nil || !got.CompletedAt.Equal(done) {
		t.Fatalf("actions job = %+v", got)
	}
	if got.FailureTail != "" {
		t.Errorf("an Actions job's tail comes from its log, not its webhook: %q", got.FailureTail)
	}
	cov, _ := store.GetGitHubCIJob(ctx, "acme/widgets", 5000)
	if cov == nil || cov.TailSource != "output" || !strings.Contains(cov.FailureTail, "71.2%") {
		t.Fatalf("third-party check = %+v", cov)
	}
}

func TestGitHubCIPollRecordsConcludedChecksOnce(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	fail, pass := "failure", "success"
	checks := []github.CheckRun{
		{ID: 11, Name: "build", Status: "completed", Conclusion: &pass, App: "github-actions"},
		{ID: 12, Name: "lint", Status: "completed", Conclusion: &fail, App: "github-actions"},
		{ID: 13, Name: "e2e", Status: "in_progress"},
		{ID: 14, Name: "grain CI", Status: "completed", Conclusion: &pass},
		{ID: 15, Name: "license", Status: "completed", Conclusion: &fail, App: "fossa", Output: "2 incompatible licenses"},
	}
	orchestrator.RecordPolledChecks(ctx, store, repo, "feature-b", "def456", checks, baseTime)
	orchestrator.RecordPolledChecks(ctx, store, repo, "feature-b", "def456", checks, baseTime.Add(time.Minute))
	jobs, err := store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 3 {
		t.Fatalf("recorded %d, want build, lint and license: %+v", len(jobs), jobs)
	}
	for _, j := range jobs {
		if j.Via != "poll" || j.Commit != "def456" || j.Ref != "feature-b" {
			t.Errorf("job = %+v", j)
		}
		if !j.UpdatedAt.Equal(baseTime) {
			t.Errorf("%s rewritten on the second, unchanged poll (updated %v)", j.JobName, j.UpdatedAt)
		}
	}
	lic, _ := store.GetGitHubCIJob(ctx, "acme/widgets", 15)
	if lic.FailureTail != "2 incompatible licenses" || lic.TailSource != "output" {
		t.Errorf("license check tail = %q/%q", lic.FailureTail, lic.TailSource)
	}
}

// seedActionsJobs seeds sim with n jobs on main and records each in the
// store as a concluded Actions job with the given conclusion.
func seedActionsJobs(t *testing.T, ctx context.Context, store *model.Store, sim *githubsim.Sim,
	conclusions []string, now time.Time) []int64 {
	t.Helper()
	jobs := make([]githubsim.WorkflowJob, len(conclusions))
	for i, c := range conclusions {
		jobs[i] = githubsim.WorkflowJob{Name: "job", Conclusion: c,
			Log: "2026-08-27T11:59:00.0000000Z ##[group]Run go test\n2026-08-27T11:59:01.0000000Z --- FAIL: TestThing\n"}
	}
	sim.WorkflowJobs = map[string][]githubsim.WorkflowJob{"main": jobs}
	ids := make([]int64, len(conclusions))
	for i, c := range conclusions {
		ids[i] = sim.WorkflowJobID("main", i)
		done := now.Add(-time.Minute)
		if err := store.PutGitHubCIJob(ctx, model.GitHubCIJob{
			Repo: "acme/widgets", CheckRunID: ids[i], App: "github-actions", JobName: "job",
			Conclusion: c, CompletedAt: &done, Ref: "main", UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

func TestGitHubCITailsAreReadOnlyForFailedJobs(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	defer orchestrator.SetGitHubCIBudgetLow(false)()
	ids := seedActionsJobs(t, ctx, store, sim, []string{"failure", "success", "cancelled"}, baseTime)

	deps := orchestrator.Deps{Store: store, Client: client}
	runGitHubCITails(t, ctx, deps, baseTime)
	runGitHubCITails(t, ctx, deps, baseTime.Add(time.Minute))

	reads := logReads(sim)
	if len(reads) != 1 || !strings.Contains(reads[0], "/actions/jobs/"+strconv.FormatInt(ids[0], 10)+"/logs") {
		t.Fatalf("log reads = %v, want exactly the failed job's, once", reads)
	}
	got, _ := store.GetGitHubCIJob(ctx, "acme/widgets", ids[0])
	if !got.TailFetched || got.TailSource != "log" || !strings.Contains(got.FailureTail, "--- FAIL: TestThing") {
		t.Fatalf("failed job after tail read = %+v", got)
	}
}

func TestGitHubCITailsRespectTheHourlyCapAndALowBudget(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	conclusions := make([]string, orchestrator.GitHubCITailsPerHour+10)
	for i := range conclusions {
		conclusions[i] = "failure"
	}
	seedActionsJobs(t, ctx, store, sim, conclusions, baseTime)
	deps := orchestrator.Deps{Store: store, Client: client}

	restore := orchestrator.SetGitHubCIBudgetLow(true)
	runGitHubCITails(t, ctx, deps, baseTime)
	restore()
	if reads := logReads(sim); len(reads) != 0 {
		t.Fatalf("read %d logs while the budget was low, want none", len(reads))
	}

	defer orchestrator.SetGitHubCIBudgetLow(false)()
	for i := 0; i < 20; i++ {
		runGitHubCITails(t, ctx, deps, baseTime.Add(time.Duration(i)*time.Minute))
	}
	if reads := logReads(sim); len(reads) != orchestrator.GitHubCITailsPerHour {
		t.Fatalf("read %d logs within the hour, want the cap of %d", len(reads), orchestrator.GitHubCITailsPerHour)
	}
	// An hour on, the rest are read -- if they are still young enough to
	// correlate, which these are.
	for i := 0; i < 3; i++ {
		runGitHubCITails(t, ctx, deps, baseTime.Add(61*time.Minute+time.Duration(i)*time.Minute))
	}
	if reads := logReads(sim); len(reads) != len(conclusions) {
		t.Fatalf("read %d logs after the hour, want all %d", len(reads), len(conclusions))
	}
}

func TestGitHubSharedFaultAcrossThreeBranchesAnnotatesTasksAndPRs(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	defer orchestrator.SetGitHubCIBudgetLow(false)()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	t1, b1 := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	t2, b2 := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	t3, b3 := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t3", repo)

	// Open a fixer PR touching the file the shared fault names.
	_, fixBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t-fix", repo)
	pushFile(t, sim.BareRepo, fixBranch, "pkg/ci/sharedfault.go", "// fix\n")
	if err := store.UpdateTask(ctx, "t-fix", func(tk *model.Task) error {
		tk.Title = "Fix GitHub fault normalizer"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fixPR := pullRequestNumberFor(t, sim, fixBranch)

	sim.WorkflowJobs = map[string][]githubsim.WorkflowJob{
		b1: {{
			Name:       "test",
			Conclusion: "failure",
			Log: "2026-08-27T11:50:00.1234567Z ##[group]Run go test ./...\n" +
				"2026-08-27T11:50:01.0000000Z --- FAIL: TestSharedFaults (0.04s)\n" +
				"2026-08-27T11:50:01.0000000Z     /home/runner/work/widgets/widgets/pkg/ci/sharedfault.go:88: unexpected digest mismatch\n" +
				"2026-08-27T11:50:01.0000000Z ##[error]Process completed with exit code 1.\n",
		}},
		b2: {{
			Name:       "test",
			Conclusion: "failure",
			Log: "2026-08-27T11:55:10.9999999Z ##[group]Run go test ./...\n" +
				"2026-08-27T11:55:11.5000000Z --- FAIL: TestSharedFaults (0.19s)\n" +
				"2026-08-27T11:55:11.5000000Z     D:\\a\\widgets\\widgets\\pkg\\ci\\sharedfault.go:88: unexpected digest mismatch\n" +
				"2026-08-27T11:55:11.5000000Z ##[error]Process completed with exit code 1.\n",
		}},
		b3: {{
			Name:       "test",
			Conclusion: "failure",
			Log: "2026-08-27T11:58:42.0000001Z ##[group]Run go test ./...\n" +
				"2026-08-27T11:58:43.0000000Z --- FAIL: TestSharedFaults (1.02s)\n" +
				"2026-08-27T11:58:43.0000000Z     /home/runner/work/widgets/widgets/pkg/ci/sharedfault.go:88: unexpected digest mismatch\n" +
				"2026-08-27T11:58:43.0000000Z ##[error]Process completed with exit code 1.\n",
		}},
	}

	for i, br := range []string{b1, b2, b3} {
		jobID := sim.WorkflowJobID(br, 0)
		done := baseTime.Add(-time.Duration(10-i) * time.Minute)
		if err := store.PutGitHubCIJob(ctx, model.GitHubCIJob{
			Repo:        repo.String(),
			CheckRunID:  jobID,
			App:         "github-actions",
			Workflow:    "CI",
			JobName:     "test",
			FailingStep: "Run go test ./...",
			Conclusion:  "failure",
			CompletedAt: &done,
			Ref:         br,
			Commit:      fmt.Sprintf("deadbeef0%d", i+1),
			UpdatedAt:   baseTime,
		}); err != nil {
			t.Fatal(err)
		}
	}

	deps := orchestrator.Deps{Store: store, Client: client}
	runGitHubCITails(t, ctx, deps, baseTime)
	// Running a second cycle must not duplicate the task or PR comments.
	runGitHubCITails(t, ctx, deps, baseTime.Add(time.Minute))

	for _, tc := range []struct {
		task      model.Task
		branch    string
		otherRefs string
	}{
		{t1, b1, b3 + ", " + b2},
		{t2, b2, b3 + ", " + b1},
		{t3, b3, b2 + ", " + b1},
	} {
		bodies := commentBodies(t, ctx, store, tc.task.ID)
		if len(bodies) != 1 {
			t.Fatalf("task %s comments = %q, want 1 shared-fault comment", tc.task.ID, bodies)
		}
		for _, want := range []string{
			"GitHub CI failure at `",
			"CI / test is failing the same way on 2 other branches right now",
			"Also failing on: " + tc.otherRefs + ".",
		} {
			if !strings.Contains(bodies[0], want) {
				t.Errorf("task %s comment missing %q:\n%s", tc.task.ID, want, bodies[0])
			}
		}

		prNum := pullRequestNumberFor(t, sim, tc.branch)
		prBody := onlyPRComment(t, client, repo, prNum)
		for _, want := range []string{
			"**This red is shared.**",
			"CI / test is failing the same way on 2 other branches right now",
			"- Also failing on: " + tc.otherRefs,
			"- Every one of those runs names: pkg/ci/sharedfault.go",
			fmt.Sprintf("Open already, touching pkg/ci/sharedfault.go: acme/widgets#%d", fixPR),
			"Fix GitHub fault normalizer",
			"from task t-fix",
		} {
			if !strings.Contains(prBody, want) {
				t.Errorf("PR #%d comment missing %q:\n%s", prNum, want, prBody)
			}
		}
	}
}

func TestGitHubSharedFaultDoesNotJoinDifferentFailuresOrGrainCI(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	defer orchestrator.SetGitHubCIBudgetLow(false)()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	t1, b1 := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	t2, b2 := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	// Two different failures on GitHub CI, and one grain-CI run on another
	// branch with the exact same text as b1's GitHub failure.
	sim.WorkflowJobs = map[string][]githubsim.WorkflowJob{
		b1: {{
			Name:       "image",
			Conclusion: "failure",
			Log:        "2026-08-27T11:50:00.0000000Z " + strings.ReplaceAll(mirror405, "\n", "\n2026-08-27T11:50:01.0000000Z "),
		}},
		b2: {{
			Name:       "image",
			Conclusion: "failure",
			Log: "2026-08-27T11:52:00.0000000Z --- FAIL: TestCompletelyDifferent (0.02s)\n" +
				"2026-08-27T11:52:00.0000000Z     pkg/other/other_test.go:19: expected 1, got 2\n" +
				"2026-08-27T11:52:00.0000000Z FAIL\n",
		}},
	}
	done := baseTime.Add(-2 * time.Minute)
	for i, br := range []string{b1, b2} {
		if err := store.PutGitHubCIJob(ctx, model.GitHubCIJob{
			Repo:        repo.String(),
			CheckRunID:  sim.WorkflowJobID(br, 0),
			App:         "github-actions",
			JobName:     "image",
			FailingStep: "build the image",
			Conclusion:  "failure",
			CompletedAt: &done,
			Ref:         br,
			Commit:      fmt.Sprintf("cafe0%d", i+1),
			UpdatedAt:   baseTime,
		}); err != nil {
			t.Fatal(err)
		}
	}

	runner := newFakeCI()
	runner.remember(otherBranchRun("ci-grain-1", "grain/task-999", done, mirror405))

	deps := orchestrator.Deps{Store: store, Client: client, CI: runner}
	runGitHubCITails(t, ctx, deps, baseTime)

	for _, tk := range []model.Task{t1, t2} {
		if bodies := commentBodies(t, ctx, store, tk.ID); len(bodies) != 0 {
			t.Errorf("expected no shared-fault comment on task %s, got %q", tk.ID, bodies)
		}
	}
}
