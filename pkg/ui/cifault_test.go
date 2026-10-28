package ui_test

// grain/task-296: a red build that is not this branch's own says so on
// the row, and names the open pull request that fixes it.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakePRFiles is ui.PullRequestFileLister with its answers written down.
type fakePRFiles struct {
	files map[int][]string
	calls int
}

func (f *fakePRFiles) ListPullRequestFiles(_, _ string, number int) ([]string, error) {
	f.calls++
	return f.files[number], nil
}

const mirror405 = `+ debootstrap --arch=amd64 trixie /build/root
reading scripts/granule/guest-inputs.sh
E: Failed getting release file from the package cache
E: proxy CONNECT returned 405 Method Not Allowed
debootstrap exited 1`

func faultRun(id, ref string, at time.Time, output string) ci.Run {
	finished := at
	return ci.Run{
		ID: id, Repo: "acme/widgets", Ref: ref, Commit: "c" + id,
		FinishedAt: &finished, Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{{
			JobID: "image", Name: "image", Conclusion: ci.ConclusionFailure,
			FinishedAt: &finished,
			Steps: []ci.StepRun{
				{Name: "build the image", Conclusion: ci.ConclusionFailure, ExitCode: 1, Output: output},
			},
		}},
	}
}

// The listing is the one place a reader meets every branch at once, so
// it is where the fault has to be said.
func TestListCIRunsReportsOneFaultAcrossBranches(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{runs: []ci.Run{
		faultRun("run-1", "grain/task-238", baseTime.Add(-10*time.Minute), mirror405),
		faultRun("run-2", "grain/task-246", baseTime.Add(-40*time.Minute), mirror405),
		faultRun("run-3", "grain/task-251", baseTime.Add(-90*time.Minute), mirror405),
	}}
	srv, _ := ciServer(t, runner)

	rec := do(t, srv, http.MethodGet, "/api/ci/runs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	got := decode[ui.CIRunsResponse](t, rec)
	if len(got.Faults) != 1 {
		t.Fatalf("want one fault, got %d: %+v", len(got.Faults), got.Faults)
	}
	fault := got.Faults[0]
	if fault.Seen() != 3 || fault.JobID != "image" {
		t.Fatalf("fault = %+v", fault.SharedFault)
	}
	if others := fault.Others("grain/task-238"); len(others) != 2 {
		t.Fatalf("a row on one branch is told about the other two: %v", others)
	}
	if _, ok := ci.FaultFor(toShared(got.Faults), "run-2", "image"); !ok {
		t.Fatal("a run in the fault cannot find it by id")
	}
}

// Nothing is claimed about a single branch's own red build.
func TestListCIRunsReportsNoFaultForOneBranch(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{runs: []ci.Run{
		faultRun("run-1", "grain/task-238", baseTime.Add(-10*time.Minute), mirror405),
	}}
	srv, _ := ciServer(t, runner)
	got := decode[ui.CIRunsResponse](t, do(t, srv, http.MethodGet, "/api/ci/runs", ""))
	if len(got.Faults) != 0 {
		t.Fatalf("a lone failure was called a shared fault: %+v", got.Faults)
	}
}

// The other half: the fix was already written and sitting unmerged.
func TestListCIRunsNamesTheOpenPullRequestThatTouchesTheFile(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{runs: []ci.Run{
		faultRun("run-1", "grain/task-238", baseTime.Add(-10*time.Minute), mirror405),
		faultRun("run-2", "grain/task-246", baseTime.Add(-40*time.Minute), mirror405),
	}}
	client, store, ctx := testClient(t)
	client.Config.CI = runner
	files := &fakePRFiles{files: map[int][]string{
		26: {"scripts/granule/guest-inputs.sh"},
		// An open change that touches something else entirely must not be
		// offered as the fix for this.
		31: {"ui/src/state.js"},
	}}
	client.Config.GitHubPullRequestFiles = files
	openPullRequest(t, client, store, ctx, "acme/widgets#26")
	openPullRequest(t, client, store, ctx, "acme/widgets#31")
	srv := ui.NewServerWithClient(client)

	got := decode[ui.CIRunsResponse](t, do(t, srv, http.MethodGet, "/api/ci/runs", ""))
	if len(got.Faults) != 1 {
		t.Fatalf("want one fault, got %d", len(got.Faults))
	}
	fixes := got.Faults[0].Fixes
	if len(fixes) != 1 {
		t.Fatalf("want exactly the change that touches the file, got %+v", fixes)
	}
	if fixes[0].PullRequest != "acme/widgets#26" {
		t.Fatalf("named the wrong pull request: %+v", fixes[0])
	}
	if len(fixes[0].Files) != 1 || fixes[0].Files[0] != "scripts/granule/guest-inputs.sh" {
		t.Fatalf("the row does not show why it named it: %+v", fixes[0])
	}

	// One computation is shared by every poll, and what a pull request
	// touches is remembered for longer still: a pane asking every five
	// seconds must not be a GitHub call every five seconds.
	before := files.calls
	do(t, srv, http.MethodGet, "/api/ci/runs", "")
	if files.calls != before {
		t.Fatalf("a second poll cost %d more GitHub calls", files.calls-before)
	}
}

// A branch that is red with the fault plainly does not fix it, however
// well its own change matches the file.
func TestListCIRunsDoesNotNameAFailingBranchAsItsOwnFix(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{runs: []ci.Run{
		faultRun("run-1", "grain/task-1", baseTime.Add(-10*time.Minute), mirror405),
		faultRun("run-2", "grain/task-2", baseTime.Add(-40*time.Minute), mirror405),
	}}
	client, store, ctx := testClient(t)
	client.Config.CI = runner
	client.Config.GitHubPullRequestFiles = &fakePRFiles{files: map[int][]string{
		9: {"scripts/granule/guest-inputs.sh"},
	}}
	task := openPullRequest(t, client, store, ctx, "acme/widgets#9")
	if branch := model.BranchName(task); branch != "grain/task-1" && branch != "grain/task-2" {
		t.Fatalf("this test needs the pull request to be open from a failing branch, got %q", branch)
	}
	srv := ui.NewServerWithClient(client)

	got := decode[ui.CIRunsResponse](t, do(t, srv, http.MethodGet, "/api/ci/runs", ""))
	if len(got.Faults) != 1 {
		t.Fatalf("want one fault, got %d", len(got.Faults))
	}
	if fixes := got.Faults[0].Fixes; len(fixes) != 0 {
		t.Fatalf("a branch failing the fault was named as its fix: %+v", fixes)
	}
}

// A deployment with no GitHub access still gets the fault; it is the
// naming that goes quiet.
func TestListCIRunsReportsAFaultWithNoGitHubAccess(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{runs: []ci.Run{
		faultRun("run-1", "grain/task-238", baseTime.Add(-10*time.Minute), mirror405),
		faultRun("run-2", "grain/task-246", baseTime.Add(-40*time.Minute), mirror405),
	}}
	srv, _ := ciServer(t, runner)
	got := decode[ui.CIRunsResponse](t, do(t, srv, http.MethodGet, "/api/ci/runs", ""))
	if len(got.Faults) != 1 || len(got.Faults[0].Fixes) != 0 {
		t.Fatalf("faults = %+v", got.Faults)
	}
}

// A task's own pane is where a red verdict is read and where somebody
// decides to spend a repair on the branch, so the fault has to reach it
// too -- and off the one computation, not a second analysis per pane.
func TestTaskDetailCarriesTheFaultItsRunIsIn(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{runs: []ci.Run{
		faultRun("run-1", "grain/task-1", baseTime.Add(-10*time.Minute), mirror405),
		faultRun("run-2", "grain/task-246", baseTime.Add(-40*time.Minute), mirror405),
	}}
	client, store, ctx := testClient(t)
	client.Config.CI = runner
	task := create(t, client, ctx)
	if err := store.Observe(ctx, model.Observation{
		TaskID: task.ID,
		CI:     &model.CIStatus{RunID: "run-1", Conclusion: "failure"},
	}); err != nil {
		t.Fatal(err)
	}

	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.CI == nil || len(detail.CI.Faults) != 1 {
		t.Fatalf("the pane was not told its red is shared: %+v", detail.CI)
	}
	if got := detail.CI.Faults[0].Others("grain/task-1"); len(got) != 1 || got[0] != "grain/task-246" {
		t.Fatalf("fault names the wrong branches: %v", got)
	}

	// A run in no fault leaves the pane exactly as it was.
	other := create(t, client, ctx)
	if err := store.Observe(ctx, model.Observation{
		TaskID: other.ID,
		CI:     &model.CIStatus{RunID: "run-9", Conclusion: "failure"},
	}); err != nil {
		t.Fatal(err)
	}
	detail, err = client.GetTask(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.CI == nil || len(detail.CI.Faults) != 0 {
		t.Fatalf("a run in no fault was given one: %+v", detail.CI)
	}
}

// openPullRequest is a finished task with an open pull request on it --
// what Store.OpenPullRequestLinks answers with, and the only kind of
// change a fault is ever allowed to name.
func openPullRequest(t *testing.T, c *ui.Client, store *model.Store, ctx context.Context, ref string) string {
	t.Helper()
	task := create(t, c, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: ref})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	completedAt := baseTime.Add(-time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &completedAt}); err != nil {
		t.Fatal(err)
	}
	return task.ID
}

func toShared(faults []ui.CIFault) []ci.SharedFault {
	out := make([]ci.SharedFault, 0, len(faults))
	for _, f := range faults {
		out = append(out, f.SharedFault)
	}
	return out
}

func TestCISharedFaultsRefreshesOnNewFailedRunAndBlockedOwnerPromotion(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{runs: []ci.Run{
		faultRun("run-1", "grain/task-1", baseTime.Add(-10*time.Minute), mirror405),
	}}
	client, store, ctx := testClient(t)
	client.Config.CI = runner
	files := &fakePRFiles{files: map[int][]string{
		26: {"scripts/granule/guest-inputs.sh"},
	}}
	client.Config.GitHubPullRequestFiles = files
	t1 := create(t, client, ctx)
	t2 := create(t, client, ctx)
	openPullRequest(t, client, store, ctx, "acme/widgets#26")
	srv := ui.NewServerWithClient(client)

	// 1. Poll when only task #1 has failed: no shared fault yet.
	got := decode[ui.CIRunsResponse](t, do(t, srv, http.MethodGet, "/api/ci/runs", ""))
	if len(got.Faults) != 0 {
		t.Fatalf("expected 0 faults when only 1 branch has failed, got %+v", got.Faults)
	}

	// 2. Task #2 fails inside the 30s TTL window: cache invalidates on the new failed run
	// and reports #1 as the queue-order fault owner.
	runner.runs = append(runner.runs, faultRun("run-2", "grain/task-2", baseTime.Add(-5*time.Minute), mirror405))
	got = decode[ui.CIRunsResponse](t, do(t, srv, http.MethodGet, "/api/ci/runs", ""))
	if len(got.Faults) != 1 {
		t.Fatalf("expected 1 shared fault after #2 failed, got %+v", got.Faults)
	}
	if got.Faults[0].OwnerTaskID != t1.ID || got.Faults[0].OwnerRef != "grain/task-1" {
		t.Fatalf("expected owner %s (grain/task-1), got %q (%q)", t1.ID, got.Faults[0].OwnerTaskID, got.Faults[0].OwnerRef)
	}
	callsAfterFirstGrouping := files.calls

	// 3. Task #1 becomes merge-blocked inside the 30s TTL window: polling re-resolves
	// ownership to #2 without re-fetching PR files from GitHub.
	blockedAt := baseTime.Add(-time.Minute)
	if err := store.Observe(ctx, model.Observation{
		TaskID:              t1.ID,
		MergeQueueBlockedAt: &blockedAt,
	}); err != nil {
		t.Fatal(err)
	}
	got = decode[ui.CIRunsResponse](t, do(t, srv, http.MethodGet, "/api/ci/runs", ""))
	if len(got.Faults) != 1 {
		t.Fatalf("expected 1 shared fault, got %+v", got.Faults)
	}
	if got.Faults[0].OwnerTaskID != t2.ID || got.Faults[0].OwnerRef != "grain/task-2" {
		t.Fatalf("expected promoted owner %s (grain/task-2) after #1 blocked, got %q (%q)", t2.ID, got.Faults[0].OwnerTaskID, got.Faults[0].OwnerRef)
	}
	if files.calls != callsAfterFirstGrouping {
		t.Fatalf("expected cached Fixes without extra GitHub PR file calls (%d), got %d", callsAfterFirstGrouping, files.calls)
	}
}

func TestCISharedFaultsSkipsRunnerListWithinTTLAndSharesListedRuns(t *testing.T) {
	t.Parallel()
	now := baseTime
	runner := &fakeCIRunner{runs: []ci.Run{
		faultRun("run-1", "grain/task-1", now.Add(-10*time.Minute), mirror405),
		faultRun("run-2", "grain/task-2", now.Add(-5*time.Minute), mirror405),
	}}
	client, _, ctx := testClient(t)
	client.Now = func() time.Time { return now }
	client.Config.CI = runner
	srv := ui.NewServerWithClient(client)

	// 1. Direct CISharedFaults calls within ciFaultTTL (30s) only call runner.List() once.
	faults := client.CISharedFaults(ctx)
	if len(faults) != 1 {
		t.Fatalf("expected 1 shared fault, got %+v", faults)
	}
	if runner.listCalls != 1 {
		t.Fatalf("expected 1 runner.List() call on cold cache, got %d", runner.listCalls)
	}
	if runner.getCalls != 2 {
		t.Fatalf("expected 2 runner.Get() calls on cold cache, got %d", runner.getCalls)
	}

	now = now.Add(5 * time.Second)
	faults = client.CISharedFaults(ctx)
	if len(faults) != 1 {
		t.Fatalf("expected 1 shared fault on warm cache, got %+v", faults)
	}
	if runner.listCalls != 1 {
		t.Fatalf("expected CISharedFaults within TTL not to call runner.List() again, got %d calls", runner.listCalls)
	}
	if runner.getCalls != 2 {
		t.Fatalf("expected CISharedFaults within TTL not to call runner.Get() again, got %d calls", runner.getCalls)
	}

	// 2. GET /api/ci/runs shares its Find() page with fault computation so runner.List() is not called.
	now = now.Add(35 * time.Second) // expire TTL
	runner.runs = append([]ci.Run{
		faultRun("run-3", "grain/task-3", now.Add(-1*time.Minute), mirror405),
	}, runner.runs...)

	got := decode[ui.CIRunsResponse](t, do(t, srv, http.MethodGet, "/api/ci/runs", ""))
	if len(got.Faults) != 1 || got.Faults[0].Seen() != 3 {
		t.Fatalf("expected 1 fault across 3 branches after TTL expiry, got %+v", got.Faults)
	}
	if runner.listCalls != 1 {
		t.Fatalf("expected GET /api/ci/runs to reuse Find() page without calling runner.List(), got %d listCalls", runner.listCalls)
	}
	// Already-classified concluded runs run-1 and run-2 must not be re-fetched via runner.Get(id).
	if runner.getsByID["run-1"] != 1 || runner.getsByID["run-2"] != 1 {
		t.Fatalf("expected concluded runs run-1 and run-2 to be read once across cache refreshes, got getsByID=%v", runner.getsByID)
	}
	if runner.getsByID["run-3"] != 1 {
		t.Fatalf("expected new failed run-3 to be read once, got getsByID=%v", runner.getsByID)
	}
}

func TestCIFaultsServesGitHubSharedFaultsAndAnnotatesTasks(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	client.Now = func() time.Time { return baseTime }
	client.Config.CI = &fakeCIRunner{runs: []ci.Run{
		// One grain failure with the same output: must not merge with GitHub failures.
		faultRun("grain-1", "grain/task-99", baseTime.Add(-20*time.Minute), mirror405),
	}}
	client.Config.GitHubPullRequestFiles = &fakePRFiles{files: map[int][]string{
		26: {"scripts/granule/guest-inputs.sh"},
	}}
	t1 := create(t, client, ctx)
	t2 := create(t, client, ctx)
	t3 := create(t, client, ctx)
	fixTaskID := openPullRequest(t, client, store, ctx, "acme/widgets#26")

	for i, branch := range []string{model.BranchName(t1.ID), model.BranchName(t2.ID), model.BranchName(t3.ID)} {
		done := baseTime.Add(-time.Duration(15-i*5) * time.Minute)
		if err := store.PutGitHubCIJob(ctx, model.GitHubCIJob{
			Repo: "acme/widgets", CheckRunID: int64(5001 + i), Source: model.GitHubCISource,
			Workflow: "ci", JobName: "image", Conclusion: "failure",
			FailingStep: "build the image", Ref: branch, Commit: "abc1234",
			CompletedAt: &done, FailureTail: mirror405, TailFetched: true,
			RecordedAt: done, UpdatedAt: done,
		}); err != nil {
			t.Fatal(err)
		}
	}

	srv := ui.NewServerWithClient(client)
	got := decode[ui.CIFaultsResponse](t, do(t, srv, http.MethodGet, "/api/ci/faults", ""))
	if !got.Enabled || len(got.Faults) != 1 {
		t.Fatalf("GET /api/ci/faults = %+v, want 1 GitHub fault (and grain-1 unjoined)", got)
	}
	f := got.Faults[0]
	if f.Source != ci.FaultSourceGitHub || f.Seen() != 3 || f.JobID != "ci / image" {
		t.Fatalf("fault = %+v, want source=github seen=3 jobId='ci / image'", f)
	}
	if len(f.Fixes) != 1 || f.Fixes[0].PullRequest != "acme/widgets#26" || f.Fixes[0].Task != fixTaskID {
		t.Fatalf("fixes = %+v, want acme/widgets#26 from task %s", f.Fixes, fixTaskID)
	}
	if f.OwnerTaskID != t1.ID {
		t.Fatalf("ownerTaskId = %q, want %q", f.OwnerTaskID, t1.ID)
	}

	detail, err := client.GetTask(ctx, t2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.CI != nil {
		t.Fatalf("detail.CI = %+v, want nil when task has no grain CI run", detail.CI)
	}
	if len(detail.CIFaults) != 1 || detail.CIFaults[0].Source != ci.FaultSourceGitHub {
		t.Fatalf("detail.CIFaults = %+v, want 1 github shared fault on task %s", detail.CIFaults, t2.ID)
	}
}
