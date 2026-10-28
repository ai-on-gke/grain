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

package ci

// grain/task-299: reading a fault out of a store and saying it in a
// sentence, which three readers now do (the panes, the verdict comment
// on a pull request, and an agent's own tools).

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// faultStore is a run store as grouping meets one: a listing with no
// step output in it, and the whole run behind an id -- the shape that
// makes reading runs back the only honest way to prove a fault.
type faultStore struct {
	runs []Run
	// gets counts the full reads, since the bound on them is the whole
	// reason the listing is consulted first.
	gets int
}

func (s *faultStore) List() []Run {
	listed := make([]Run, 0, len(s.runs))
	for _, run := range s.runs {
		summary := run
		summary.Jobs = make([]JobRun, 0, len(run.Jobs))
		for _, job := range run.Jobs {
			job.Steps = nil
			summary.Jobs = append(summary.Jobs, job)
		}
		listed = append(listed, summary)
	}
	return listed
}

func (s *faultStore) Get(id string) (Run, bool) {
	s.gets++
	for _, run := range s.runs {
		if run.ID == id {
			return run, true
		}
	}
	return Run{}, false
}

func TestRecentSharedFaultsReadsBackOnlyTheRunsThatCouldProveAFault(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	green := failedRun("r0", "main", "0000000", now.Add(-5*time.Minute), "image", "build the image", proxy405)
	green.Conclusion = ConclusionSuccess
	green.Jobs[0].Conclusion = ConclusionSuccess
	old := failedRun("r4", "grain/task-99", "ddddddd4", now.Add(-40*time.Hour), "image", "build the image", proxy405)
	store := &faultStore{runs: []Run{
		green,
		failedRun("r1", "grain/task-238", "aaaaaaa1", now.Add(-10*time.Minute), "image", "build the image", proxy405),
		failedRun("r2", "grain/task-246", "bbbbbbb2", now.Add(-40*time.Minute), "image", "build the image", proxy405),
		old,
	}}

	faults := RecentSharedFaults(store, now, 0, 0)
	if len(faults) != 1 || faults[0].Seen() != 2 {
		t.Fatalf("want one fault on two branches, got %+v", faults)
	}
	if store.gets != 2 {
		t.Fatalf("read back %d runs whole, want only the two that failed inside the window", store.gets)
	}
	if got := faults[0].Summary("grain/task-238"); got != "Image is failing the same way on 1 other branch right now -- one fault, not this branch's." {
		t.Fatalf("summary = %q", got)
	}
}

// A store nothing can be asked of is silence, not an error: every caller
// is annotating a report worth giving without it.
func TestRecentSharedFaultsIsSilentWithNoSource(t *testing.T) {
	if faults := RecentSharedFaults(nil, time.Now(), 0, 0); faults != nil {
		t.Fatalf("a nil source produced %+v", faults)
	}
}

// The limit is what keeps a deployment that has been red all day from
// turning "is this red mine?" into a table scan.
func TestRecentSharedFaultsStopsAtTheLimit(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	var runs []Run
	for i := 0; i < 10; i++ {
		runs = append(runs, failedRun(
			"r"+string(rune('0'+i)), "grain/task-"+string(rune('0'+i)), "aaaaaa"+string(rune('0'+i)),
			now.Add(-time.Duration(i)*time.Minute), "image", "build the image", proxy405))
	}
	store := &faultStore{runs: runs}
	faults := RecentSharedFaults(store, now, 0, 3)
	if store.gets != 3 {
		t.Fatalf("read %d runs whole, want the 3 asked for", store.gets)
	}
	if len(faults) != 1 || faults[0].Seen() != 3 {
		t.Fatalf("want the fault proved from three branches, got %+v", faults)
	}
}

// The case the agent path is built on: the run asking the question is
// newer than the fault list it is asked against, so it is not in it.
func TestFaultForJobMatchesARunTheFaultListNeverSaw(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	theirs := []Run{
		failedRun("r1", "grain/task-238", "aaaaaaa1", now.Add(-10*time.Minute), "image", "build the image", proxy405),
		failedRun("r2", "grain/task-246", "bbbbbbb2", now.Add(-40*time.Minute), "image", "build the image", proxy405),
	}
	faults := SharedFaults(theirs, now, 0)
	if len(faults) != 1 {
		t.Fatalf("want one fault to match against, got %+v", faults)
	}
	mine := failedRun("r9", "grain/task-299", "eeeeeee9", now, "image", "build the image", proxy405)

	if _, ok := FaultFor(faults, mine.ID, "image"); ok {
		t.Fatal("the fault list already knows this run, so this test proves nothing")
	}
	fault, ok := FaultForJob(faults, mine, mine.Jobs[0])
	if !ok {
		t.Fatal("a failure with the same digest as a known fault was not matched to it")
	}
	if got := fault.Summary(mine.Ref); !strings.Contains(got, "on 2 other branches") {
		t.Fatalf("summary from a branch outside the fault = %q", got)
	}
}

// A different failure is a different fault, however alike the runs look.
func TestFaultForJobRefusesADifferentFailure(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	faults := SharedFaults([]Run{
		failedRun("r1", "grain/task-238", "aaaaaaa1", now.Add(-10*time.Minute), "image", "build the image", proxy405),
		failedRun("r2", "grain/task-246", "bbbbbbb2", now.Add(-40*time.Minute), "image", "build the image", proxy405),
	}, now, 0)
	mine := failedRun("r9", "grain/task-299", "eeeeeee9", now, "image", "build the image",
		"--- FAIL: TestThing\n    thing_test.go:12: wanted 3, got 4\nFAIL\nexit status 1")
	if fault, ok := FaultForJob(faults, mine, mine.Jobs[0]); ok {
		t.Fatalf("this branch's own test failure was called somebody else's fault: %+v", fault)
	}
}

// The rule every reader rests on, said once here so that no consumer has
// to re-derive it: a fault the reader is the only branch of has no
// sentence in it.
func TestSummaryIsEmptyForTheOnlyBranchInIt(t *testing.T) {
	fault := SharedFault{JobID: "image", JobName: "image", Refs: []string{"grain/task-238"}}
	if got := fault.Summary("grain/task-238"); got != "" {
		t.Fatalf("a branch was told about itself: %q", got)
	}
	if got := fault.OtherRefsPhrase("grain/task-238"); got != "" {
		t.Fatalf("branch list from a fault of one = %q", got)
	}
}

func TestOtherRefsPhraseCountsThePastTheFirstFew(t *testing.T) {
	fault := SharedFault{JobID: "image", JobName: "image", Refs: []string{
		"mine", "b1", "b2", "b3", "b4", "b5", "b6", "b7",
	}}
	got := fault.OtherRefsPhrase("mine")
	if got != "b1, b2, b3, b4, b5 and 2 more" {
		t.Fatalf("phrase = %q", got)
	}
	if want := "image is failing the same way on 7 other branches right now -- one fault, not this branch's."; fault.Summary("mine") != want {
		t.Fatalf("summary = %q, want %q", fault.Summary("mine"), want)
	}
}

func TestRecentSharedFaultsCachesConcludedRunClassificationsAcrossCalls(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	store := &faultStore{runs: []Run{
		failedRun("r1", "grain/task-238", "aaaaaaa1", now.Add(-10*time.Minute), "image", "build the image", proxy405),
		failedRun("r2", "grain/task-246", "bbbbbbb2", now.Add(-40*time.Minute), "image", "build the image", proxy405),
	}}

	faults := RecentSharedFaults(store, now, 0, 0)
	if len(faults) != 1 || faults[0].Seen() != 2 {
		t.Fatalf("first call faults = %+v", faults)
	}
	if store.gets != 2 {
		t.Fatalf("first call gets = %d, want 2", store.gets)
	}

	// A new concluded run lands and RecentSharedFaults is called again:
	// already-classified concluded runs r1 and r2 are reused without calling Get again.
	store.runs = append([]Run{
		failedRun("r3", "grain/task-251", "ccccccc3", now.Add(-2*time.Minute), "image", "build the image", proxy405),
	}, store.runs...)

	faults = RecentSharedFaults(store, now, 0, 0)
	if len(faults) != 1 || faults[0].Seen() != 3 {
		t.Fatalf("second call faults = %+v", faults)
	}
	if store.gets != 3 {
		t.Fatalf("second call gets = %d, want 3 (only new run r3 fetched)", store.gets)
	}
}

func TestGitHubSharedFaultsDetectsThreeBranchesAndRejectsDifferentOrCrossSource(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	const tailA = `2026-09-18T16:40:01.0000000Z ##[group]Run go test ./...
2026-09-18T16:40:02.0000000Z --- FAIL: TestAuthHandshake (0.11s)
2026-09-18T16:40:02.0000000Z     /home/runner/work/widgets/widgets/pkg/auth/handshake.go:77: TLS certificate expired
2026-09-18T16:40:02.0000000Z ##[error]Process completed with exit code 1.`
	const tailB = `2026-09-18T16:45:01.0000000Z ##[group]Run go test ./...
2026-09-18T16:45:02.0000000Z --- FAIL: TestBillingInvoice (0.22s)
2026-09-18T16:45:02.0000000Z     /home/runner/work/widgets/widgets/pkg/billing/invoice.go:31: rounding mismatch: got 199, want 200
2026-09-18T16:45:02.0000000Z ##[error]Process completed with exit code 1.`

	mkJob := func(id int64, ref, commit, conclusion, tail string, minsAgo time.Duration) model.GitHubCIJob {
		done := now.Add(-minsAgo * time.Minute)
		return model.GitHubCIJob{
			Repo: "acme/widgets", CheckRunID: id, Source: model.GitHubCISource,
			Workflow: "ci", JobName: "unit", Conclusion: conclusion,
			FailingStep: "go test ./...", Ref: ref, Commit: commit,
			CompletedAt: &done, FailureTail: tail, TailFetched: tail != "",
			RecordedAt: done, UpdatedAt: done,
		}
	}

	// 1. Three unrelated branches with the same failure -> 1 shared fault (source=github).
	three := []model.GitHubCIJob{
		mkJob(1001, "grain/task-10", "aaa1111", "failure", tailA, 30),
		mkJob(1002, "grain/task-11", "bbb2222", "failure", tailA, 20),
		mkJob(1003, "grain/task-12", "ccc3333", "failure", tailA, 10),
	}
	faults := GitHubSharedFaults(three, now, 0)
	if len(faults) != 1 {
		t.Fatalf("GitHubSharedFaults(three) = %+v, want 1 fault", faults)
	}
	if faults[0].Source != FaultSourceGitHub || faults[0].JobID != "ci / unit" || faults[0].Step != "go test ./..." {
		t.Fatalf("fault = %+v, want source=github jobId='ci / unit' step='go test ./...'", faults[0])
	}
	if !slices.Equal(faults[0].Refs, []string{"grain/task-12", "grain/task-11", "grain/task-10"}) {
		t.Fatalf("fault refs = %v", faults[0].Refs)
	}
	if !slices.Equal(faults[0].Paths, []string{"pkg/auth/handshake.go"}) {
		t.Fatalf("fault paths = %v, want [pkg/auth/handshake.go]", faults[0].Paths)
	}

	// 2. Two different failures on two branches -> not joined.
	different := []model.GitHubCIJob{
		mkJob(2001, "grain/task-20", "ddd4444", "failure", tailA, 15),
		mkJob(2002, "grain/task-21", "eee5555", "failure", tailB, 5),
	}
	if got := GitHubSharedFaults(different, now, 0); len(got) != 0 {
		t.Fatalf("different failures joined: %+v", got)
	}

	// 3. A grain run will not match a GitHub fault in FaultForJob even with identical text.
	grainRun := failedRun("g-1", "grain/task-99", "fff6666", now, "ci / unit", "go test ./...", tailA)
	grainRun.Repo = "acme/widgets"
	grainRun.Source = FaultSourceGrain
	if f, ok := FaultForJob(faults, grainRun, grainRun.Jobs[0]); ok {
		t.Fatalf("FaultForJob matched grain run to GitHub fault: %+v", f)
	}
}
