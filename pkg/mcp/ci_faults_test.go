package mcp

// grain/task-299: the agent half of a shared fault. A repair run reads
// its verdict through these two tools, so this is where "your `image`
// job failed, and so did the same job on five other branches inside the
// last hour" has to be said -- or the afternoon grain/task-296 was
// written for repeats itself one task at a time.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
)

// mirrorFault is one deployment-wide fault as the daemon has already
// grouped it: the same job, failing identically, on three branches, one
// of which is the run this agent is waiting on.
func mirrorFault(ownRun, ownRef string) CIFault {
	at := time.Now().Add(-10 * time.Minute)
	return CIFault{SharedFault: ci.SharedFault{
		Repo: "acme/widgets", JobID: "tests", JobName: "tests",
		Step: "go test", Kind: ci.ConclusionFailure, Digest: "deadbeef",
		Refs: []string{ownRef, "grain/task-246", "grain/task-251"},
		Runs: []ci.FaultRun{
			{RunID: ownRun, Ref: ownRef, At: at},
			{RunID: "ci-246", Ref: "grain/task-246", At: at.Add(-5 * time.Minute)},
			{RunID: "ci-251", Ref: "grain/task-251", At: at.Add(-9 * time.Minute)},
		},
		Paths: []string{"scripts/granule/guest-inputs.sh"},
	}}
}

func TestWaitForChecksSaysWhenTheRedIsSharedAndNamesTheFix(t *testing.T) {
	fault := mirrorFault("ci-1", "grain/task-1")
	fault.Fixes = []CIFaultFix{{
		PullRequest: "acme/widgets#1591", Task: "291",
		Title: "Serve the debootstrap mirror over http",
		Files: []string{"scripts/granule/guest-inputs.sh"},
	}}
	runner := &fakeGrainCIRunner{faults: []CIFault{fault}}
	tool := ciTools(runner)[0]
	result := tool.Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	for _, want := range []string{
		"Shared fault: tests is failing the same way on 2 other branches right now",
		"Also failing on: grain/task-246, grain/task-251",
		"grain CI run ci-246 on grain/task-246",
		"Every one of them names: scripts/granule/guest-inputs.sh",
		"acme/widgets#1591",
		"Serve the debootstrap mirror over http",
		"from task 291",
		"not this branch's own",
	} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("a red verdict on a shared fault lacks %q:\n%s", want, result.Text)
		}
	}
}

// The rule the agent path must not weaken: a branch that is the only one
// grain has a recent run for is a branch with a problem, and nothing
// here may dress that up as somebody else's.
func TestWaitForChecksSaysNothingSharedAboutThisBranchAlone(t *testing.T) {
	// A fault this run's job is not part of -- some other job, on other
	// branches -- plus, in the same list, one whose only ref is this
	// branch. Neither is news for this verdict.
	own := mirrorFault("ci-1", "grain/task-1")
	own.Refs = []string{"grain/task-1"}
	own.Runs = own.Runs[:1]
	elsewhere := mirrorFault("ci-9", "grain/task-9")
	elsewhere.JobID, elsewhere.JobName, elsewhere.Digest = "frontend", "frontend", "cafe"
	runner := &fakeGrainCIRunner{faults: []CIFault{own, elsewhere}}
	tool := ciTools(runner)[0]
	result := tool.Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if strings.Contains(result.Text, "Shared fault") || strings.Contains(result.Text, "not this branch's own") {
		t.Fatalf("a lone red build was reported as shared:\n%s", result.Text)
	}
	if !strings.Contains(result.Text, "Job tests: failure") {
		t.Fatalf("the verdict itself went missing:\n%s", result.Text)
	}
}

// A run whose job the daemon's fault list has never heard of -- because
// it concluded seconds ago, behind a cache, after the page of runs the
// grouping read -- is still matched, on the digest its own failure has
// (ci.FaultForJob). This is the ordinary case for wait_for_checks.
func TestWaitForChecksMatchesAFaultItsOwnRunIsNotListedIn(t *testing.T) {
	failed := time.Now().Add(-time.Minute)
	output := "E: Failed getting release file from the package cache\n" +
		"E: proxy CONNECT returned 405 Method Not Allowed\ndebootstrap exited 1\n"
	mine := ci.Run{
		ID: "ci-new", Repo: "acme/widgets", Ref: "grain/task-1", Commit: "abcdef012345",
		Conclusion: ci.ConclusionFailure, FinishedAt: &failed,
		Jobs: []ci.JobRun{{JobID: "image", Name: "image", Conclusion: ci.ConclusionFailure,
			FinishedAt: &failed,
			Steps: []ci.StepRun{{Name: "build the image", Conclusion: ci.ConclusionFailure,
				ExitCode: 1, Output: output}}}},
	}
	theirs := mine
	theirs.ID, theirs.Ref = "ci-246", "grain/task-246"
	sig, ok := ci.FailureSignature(theirs, theirs.Jobs[0])
	if !ok {
		t.Fatal("the failure this test is built on has no signature")
	}
	fault := CIFault{SharedFault: ci.SharedFault{
		Repo: "acme/widgets", JobID: "image", JobName: "image", Step: "build the image",
		Kind: ci.ConclusionFailure, Digest: sig.Digest,
		Refs: []string{"grain/task-246", "grain/task-251"},
		Runs: []ci.FaultRun{
			{RunID: "ci-246", Ref: "grain/task-246", At: failed},
			{RunID: "ci-251", Ref: "grain/task-251", At: failed},
		},
	}}
	runner := &fakeGrainCIRunner{run: &mine, faults: []CIFault{fault}}
	tool := ciTools(runner)[0]
	result := tool.Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if !strings.Contains(result.Text, "image is failing the same way on 2 other branches") {
		t.Fatalf("a fault the run is not listed in went unreported:\n%s", result.Text)
	}
}

// A green verdict asks nothing: there is no failure for a fault to be
// about, and the question costs the daemon a read of every recent
// failing run.
func TestWaitForChecksDoesNotAskAboutFaultsForAGreenRun(t *testing.T) {
	green := ci.Run{ID: "ci-1", Ref: "grain/task-1", Commit: "abcdef012345",
		Conclusion: ci.ConclusionSuccess,
		Jobs:       []ci.JobRun{{JobID: "tests", Name: "tests", Conclusion: ci.ConclusionSuccess}}}
	runner := &fakeGrainCIRunner{run: &green}
	tool := ciTools(runner)[0]
	if result := tool.Handler(context.Background(), map[string]any{}); result.IsError {
		t.Fatal(result.Text)
	}
	if runner.faultReads != 0 {
		t.Fatalf("a passing run asked about shared faults %d times", runner.faultReads)
	}
}

// The listing is where an agent meets several branches at once, so it is
// where the fault is worth saying twice over -- once on the run, once as
// the advice under the list.
func TestListCIRunsSaysWhichOfItsRedRunsShareOneFault(t *testing.T) {
	runner := &fakeGrainCIRunner{faults: []CIFault{mirrorFault("ci-1", "grain/task-1")}}
	tool := ciTools(runner)[1]
	result := tool.Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if !strings.Contains(result.Text, "Shared fault: tests is failing the same way on 2 other branches") {
		t.Fatalf("the list said nothing about a fault it had been given:\n%s", result.Text)
	}
	if strings.Count(result.Text, "not this branch's own") != 1 {
		t.Fatalf("the advice under a list is said once, not per run:\n%s", result.Text)
	}
}

// Reading the log is where somebody sits down to diagnose, so it is the
// last place that should let them start on a failure that is not theirs
// without saying so.
func TestGetCILogSaysWhenTheFailureBeingReadIsShared(t *testing.T) {
	runner := &fakeGrainCIRunner{faults: []CIFault{mirrorFault("ci-1", "grain/task-1")}}
	tool := ciTools(runner)[2]
	result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-1"})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if !strings.Contains(result.Text, "Shared fault: tests is failing the same way on 2 other branches right now") ||
		!strings.Contains(result.Text, "Also failing on: grain/task-246, grain/task-251") {
		t.Fatalf("a log read of a shared failure said nothing about it:\n%s", result.Text)
	}
	if !strings.Contains(result.Text, "package failed") {
		t.Fatalf("the log itself went missing:\n%s", result.Text)
	}
}

// A job still running has not failed, so there is nothing to ask about
// -- and a tail is asked again every few seconds.
func TestGetCILogAsksNothingAboutAJobStillRunning(t *testing.T) {
	started := time.Now().Add(-time.Minute)
	running := ci.Run{ID: "ci-1", Repo: testCIRepo, Ref: "grain/task-1", Commit: "abcdef012345",
		Jobs: []ci.JobRun{{JobID: "tests", Name: "tests", StartedAt: &started,
			Steps: []ci.StepRun{{Name: "go test", Output: "running along"}}}}}
	runner := &fakeGrainCIRunner{run: &running, faults: []CIFault{mirrorFault("ci-1", "grain/task-1")}}
	tool := ciTools(runner)[2]
	if result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-1"}); result.IsError {
		t.Fatal(result.Text)
	}
	if runner.faultReads != 0 {
		t.Fatalf("tailing a running job asked about shared faults %d times", runner.faultReads)
	}
}

// The daemon not answering costs the annotation and nothing else: a
// verdict is worth giving without it.
func TestWaitForChecksStillReportsWhenFaultsCannotBeRead(t *testing.T) {
	runner := &failingFaultsCIRunner{}
	tool := ciTools(runner)[0]
	result := tool.Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if !strings.Contains(result.Text, "Job tests: failure") {
		t.Fatalf("a fault lookup that failed took the verdict with it:\n%s", result.Text)
	}
}

type failingFaultsCIRunner struct{ fakeGrainCIRunner }

func (f *failingFaultsCIRunner) GrainCISharedFaults(context.Context) ([]CIFault, error) {
	return nil, context.DeadlineExceeded
}

func TestCIToolsDescribeTheSharedFaultTheyReport(t *testing.T) {
	tools := ciTools(&fakeGrainCIRunner{})
	for _, tool := range tools[:2] {
		if !strings.Contains(tool.Description, "other branches") {
			t.Errorf("%s does not say it reports a shared fault: %s", tool.Name, tool.Description)
		}
	}
}

func TestWaitForChecksAndGetCILogWhenFaultAssignedToCallingTask(t *testing.T) {
	fault := mirrorFault("ci-1", "grain/task-1")
	fault.OwnerTaskID = "1"
	fault.OwnerRef = "grain/task-1"
	runner := &fakeGrainCIRunner{faults: []CIFault{fault}}
	tools := ciTools(runner)

	waitRes := tools[0].Handler(context.Background(), map[string]any{})
	if waitRes.IsError {
		t.Fatal(waitRes.Text)
	}
	if !strings.Contains(waitRes.Text, "Assigned to you (Task #1)") ||
		!strings.Contains(waitRes.Text, "Fix it on your branch so the merge queue can advance") {
		t.Errorf("owner wait_for_checks output missing assignment guidance:\n%s", waitRes.Text)
	}
	for _, forbidden := range []string{"not this branch's own", "Do not attempt to fix this locally"} {
		if strings.Contains(waitRes.Text, forbidden) {
			t.Errorf("owner wait_for_checks output must not contain %q:\n%s", forbidden, waitRes.Text)
		}
	}

	logRes := tools[2].Handler(context.Background(), map[string]any{"run_id": "ci-1"})
	if logRes.IsError {
		t.Fatal(logRes.Text)
	}
	if !strings.Contains(logRes.Text, "Assigned to you (Task #1)") {
		t.Errorf("owner get_ci_log output missing assignment guidance:\n%s", logRes.Text)
	}
	if strings.Contains(logRes.Text, "not this branch's own") {
		t.Errorf("owner get_ci_log output must not tell owner not to fix:\n%s", logRes.Text)
	}
}

func TestWaitForChecksAndGetCILogWhenFaultAssignedToEarlierTask(t *testing.T) {
	fault := mirrorFault("ci-1", "grain/task-1")
	fault.OwnerTaskID = "246"
	fault.OwnerRef = "grain/task-246"
	runner := &fakeGrainCIRunner{faults: []CIFault{fault}}
	tools := ciTools(runner)

	waitRes := tools[0].Handler(context.Background(), map[string]any{})
	if waitRes.IsError {
		t.Fatal(waitRes.Text)
	}
	for _, want := range []string{
		"Assigned to Task #246 ([Task #246](/tasks/246)) ahead of you in the queue",
		"do not attempt to fix this on your branch",
	} {
		if !strings.Contains(waitRes.Text, want) {
			t.Errorf("non-owner wait_for_checks output missing %q:\n%s", want, waitRes.Text)
		}
	}

	logRes := tools[2].Handler(context.Background(), map[string]any{"run_id": "ci-1"})
	if logRes.IsError {
		t.Fatal(logRes.Text)
	}
	if !strings.Contains(logRes.Text, "Assigned to Task #246 ([Task #246](/tasks/246)) ahead of you in the queue") {
		t.Errorf("non-owner get_ci_log output missing owner link:\n%s", logRes.Text)
	}
}

type stagedGrainCIRunner struct {
	fakeGrainCIRunner
	stages []ci.Run
}

func (s *stagedGrainCIRunner) GrainCIRun(context.Context, string) (ci.Run, error) {
	idx := s.reads
	s.reads++
	if idx >= len(s.stages) {
		idx = len(s.stages) - 1
	}
	return s.stages[idx], nil
}

func TestWaitForChecksKeepsPollingWhileOnlyExternallyOwnedFaultsFailed(t *testing.T) {
	fault := mirrorFault("ci-1", "grain/task-1")
	fault.OwnerTaskID = "246"
	fault.OwnerRef = "grain/task-246"

	stage1 := ci.Run{
		ID: "ci-1", Repo: testCIRepo, Ref: "grain/task-1", Commit: "abcdef012345",
		Jobs: []ci.JobRun{
			{JobID: "tests", Name: "tests", Conclusion: ci.ConclusionFailure},
			{JobID: "unit", Name: "unit"}, // still running
		},
	}
	stage2 := ci.Run{
		ID: "ci-1", Repo: testCIRepo, Ref: "grain/task-1", Commit: "abcdef012345",
		Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{
			{JobID: "tests", Name: "tests", Conclusion: ci.ConclusionFailure},
			{JobID: "unit", Name: "unit", Conclusion: ci.ConclusionSuccess},
		},
	}
	runner := &stagedGrainCIRunner{
		fakeGrainCIRunner: fakeGrainCIRunner{faults: []CIFault{fault}},
		stages:            []ci.Run{stage1, stage2},
	}
	tool := ciTools(runner)[0]
	res := tool.Handler(context.Background(), map[string]any{})
	if res.IsError {
		t.Fatal(res.Text)
	}
	if runner.reads < 2 {
		t.Fatalf("expected waitForGrainCI to keep polling until unit finished, got %d reads", runner.reads)
	}
	if !strings.Contains(res.Text, "Job unit: success") {
		t.Errorf("expected final verdict to include completed unit job:\n%s", res.Text)
	}
}
