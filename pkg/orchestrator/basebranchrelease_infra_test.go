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
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// finishAt ends a run as finish does, at a recorded time -- what the
// engine stamps on every run it concludes, and what an automatic re-run's
// delay is measured from.
func (f *fakeCI) finishAt(id string, conclusion ci.Conclusion, at time.Time, jobs ...ci.JobRun) {
	f.finish(id, conclusion, jobs...)
	f.mu.Lock()
	defer f.mu.Unlock()
	run := f.runs[id]
	run.FinishedAt = &at
	f.runs[id] = run
}

// A release of trunk that ends with an `infra` verdict is re-run -- the
// jobs that did not pass, uncached, after the retry delay -- before it is
// counted as a failed release or anybody is filed to triage it
// (docs/design/release-from-trunk.md, RFT-2). Once the re-runs are spent
// it is the failed release it always was.
func TestAnInfraReleaseIsReRunBeforeItIsTriaged(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasingRepo(t, ctx, store, repo)
	tip := headSHA(t, client, repo, "main")
	judged := baseTime.Add(-time.Hour)
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: tip, RunID: "judge-1",
		StartedAt: judged, FinishedAt: &judged, Conclusion: string(ci.ConclusionSuccess),
	}); err != nil {
		t.Fatal(err)
	}

	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime)
	first := runner.lastRequest(t)
	if first.Event != ci.EventRelease || first.Priority != ci.PriorityReleaseGate {
		t.Fatalf("trunk's release asked for %+v, want a release at PriorityReleaseGate", first)
	}
	jobs := []ci.JobRun{
		{WorkflowPath: ".grain/workflows/release.yml", JobID: "build", Conclusion: ci.ConclusionSuccess},
		{WorkflowPath: ".grain/workflows/release.yml", JobID: "images", Conclusion: ci.ConclusionErrored,
			Error: "acquiring capability package-publish: HTTP 429 Too Many Requests"},
	}
	triaged := func() bool {
		t.Helper()
		has, err := store.HasTaskWithTag(ctx, orchestrator.BaseReleaseTriageBranchTag(repo, "main"))
		if err != nil {
			t.Fatal(err)
		}
		return has
	}

	at := baseTime
	for n := 1; n <= orchestrator.MaxInfraRetries; n++ {
		row, err := store.GetBaseBranchRelease(ctx, repo, "main")
		if err != nil || row == nil || !row.InFlight() {
			t.Fatalf("re-run %d: the release row is %+v (%v), want one in flight", n, row, err)
		}
		ended := at.Add(20 * time.Minute)
		runner.finishAt(row.RunID, ci.ConclusionErrored, ended, jobs...)

		// Inside the delay: nothing new, nothing counted, nothing triaged.
		runBaseBranchReleaseReconciler(t, ctx, store, client, runner, ended.Add(time.Minute))
		if got := len(runner.releaseRuns()); got != n {
			t.Fatalf("re-run %d: %d release runs inside the retry delay, want %d", n, got, n)
		}
		if triaged() {
			t.Fatalf("re-run %d: an infra verdict was triaged before its re-runs were spent", n)
		}

		at = ended.Add(10 * time.Minute)
		runBaseBranchReleaseReconciler(t, ctx, store, client, runner, at)
		req := runner.lastRequest(t)
		if want := []string{".grain/workflows/release.yml:images"}; !slices.Equal(req.Jobs, want) ||
			!req.Recheck || !req.TrustedRelease || req.Commit != tip || req.Priority != ci.PriorityReleaseGate ||
			!strings.HasSuffix(req.RequestedBy, fmt.Sprintf("#infra-retry-%d", n)) {
			t.Fatalf("re-run %d asked for %+v, want %v uncached at %s as a trusted release gate", n, req, want, tip)
		}
		row, err = store.GetBaseBranchRelease(ctx, repo, "main")
		if err != nil || row == nil || !row.InFlight() || row.Failures != 0 {
			t.Fatalf("re-run %d: the release row is %+v (%v), want the re-run in flight and no failure counted", n, row, err)
		}
	}

	row, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("release row missing: %v", err)
	}
	ended := at.Add(20 * time.Minute)
	runner.finishAt(row.RunID, ci.ConclusionErrored, ended, jobs...)
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, ended.Add(time.Hour))
	if row, err = store.GetBaseBranchRelease(ctx, repo, "main"); err != nil || row == nil ||
		row.Conclusion != string(ci.ConclusionErrored) || row.Failures != 1 {
		t.Fatalf("the release row is %+v (%v), want an errored release counted once its re-runs were spent", row, err)
	}
	if !triaged() {
		t.Fatal("no triage was filed for a release whose re-runs were spent")
	}
}

// Only trunk is released: a `candidate` row left over from the
// stabilization-branch workflow publishes nothing (RFT-2 retires it).
func TestOnlyTheDefaultBranchIsReleased(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasingRepo(t, ctx, store, repo)
	tip := headSHA(t, client, repo, "main")
	judged := baseTime.Add(-time.Hour)
	for _, branch := range []string{"candidate", "main"} {
		if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
			Repo: repo, Branch: branch, Commit: tip, RunID: "judge-" + branch,
			StartedAt: judged, FinishedAt: &judged, Conclusion: string(ci.ConclusionSuccess),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(time.Duration(i)*time.Minute))
		for _, req := range runner.releaseRuns() {
			if req.Ref != "main" {
				t.Fatalf("a release was started for %s, want trunk's alone", req.Ref)
			}
		}
	}
	if got := len(runner.releaseRuns()); got != 1 {
		t.Fatalf("%d release runs, want main's one", got)
	}
	if row, err := store.GetBaseBranchRelease(ctx, repo, "candidate"); err != nil || row != nil {
		t.Fatalf("candidate's release row is %+v (%v), want none", row, err)
	}
}
