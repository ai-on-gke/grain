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

// mergequeue_lanes_walkthrough_test.go is §2 of
// docs/test-plans/2026-10-13.md run on a real queue: merge-queue lanes per
// (repository, base branch) (5c2776f62), the contention telemetry the
// queue records as it works (0c7590e63), and grain CI's exact-base failure
// eviction (cbad2978a). The report is
// docs/test-runs/2026-10-13-merge-queue-lanes-contention.md.
//
// What is real and what is stood in for is breakerWorld's (circuit_breaker_
// cache_telemetry_walkthrough_test.go): orchestrator.RunCycle over SQLite,
// the daemon's CI service with its DirCache, and the API; GitHub is
// githubsim, a CI job's machine is a local shell. One more edge here:
// unreadablePullSim, GitHub answering 502 for one pull request.
package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// laneRules hold a change under held/ on GitHub's checks as well as grain
// CI's, which is how a main change is kept "still in CI" for as long as a
// test wants: its GitHub check stays in_progress.
const laneRules = `rules:
  - id: held
    paths: ["held/**"]
    ci:
      mode: both
`

// unreadablePullSim is GitHub with one pull request whose read fails.
type unreadablePullSim struct {
	inner github.Transport

	mu   sync.Mutex
	path string
}

func (u *unreadablePullSim) fail(number int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.path = ""
	if number > 0 {
		u.path = fmt.Sprintf("/repos/acme/widgets/pulls/%d", number)
	}
}

func (u *unreadablePullSim) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	u.mu.Lock()
	failing := u.path != "" && method == http.MethodGet && strings.SplitN(path, "?", 2)[0] == u.path
	u.mu.Unlock()
	if failing {
		return github.ApiResponse{Status: http.StatusBadGateway, Body: []byte(`{"message":"upstream timeout"}`)}, nil
	}
	return u.inner.Request(method, path, headers, body)
}

// pushBranch creates branch at main's tip.
func (w *breakerWorld) pushBranch(branch string) string {
	w.t.Helper()
	dir := filepath.Join(w.t.TempDir(), "b")
	run(w.t, filepath.Dir(dir), "git", "clone", "-q", "-b", "main", w.remote, dir)
	run(w.t, dir, "git", "push", "-q", "origin", "main:"+branch)
	return w.head(branch)
}

// queuedChange files an auto-merge task on base (empty: none named) whose
// run has already pushed path and opened a pull request -- the queue's
// input, without a dispatch.
func (w *breakerWorld) queuedChange(id, base, path string) (model.Task, int) {
	w.t.Helper()
	actor := human("operator")
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "change " + id, Body: "please",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: actor},
		Target:   &widgets, Base: base,
		Binding:   model.BindingDirective,
		Grants:    []model.Grant{{Capability: "grain-ci"}},
		AutoMerge: true,
	}
	// Filed behind everything already queued, as a new task is.
	order, err := w.store.OrderKeyForNewTask(w.ctx, false)
	if err != nil {
		w.t.Fatal(err)
	}
	task.OrderKey = order
	if err := w.store.PutTask(w.ctx, task); err != nil {
		w.t.Fatal(err)
	}
	from := base
	if from == "" {
		from = "main"
	}
	dir := filepath.Join(w.t.TempDir(), "c")
	run(w.t, filepath.Dir(dir), "git", "clone", "-q", "-b", from, w.remote, dir)
	run(w.t, dir, "git", "checkout", "-q", "-b", task.BranchName())
	mustWriteFile(w.t, filepath.Join(dir, path), id+"\n")
	run(w.t, dir, "git", "add", "-A")
	run(w.t, dir, "git", "-c", "user.email=c@example.com", "-c", "user.name=c", "commit", "-q", "-m", "change "+id)
	run(w.t, dir, "git", "push", "-q", "origin", task.BranchName())
	pr, err := orchestrator.EnsurePullRequest(w.ctx, w.store, w.deps.Client, task, w.now)
	if err != nil {
		w.t.Fatalf("opening %s's pull request: %v", id, err)
	}
	task.Links = append(task.Links, model.Link{
		Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: widgets, Number: pr.Number}.String(),
	})
	if err := w.store.PutTask(w.ctx, task); err != nil {
		w.t.Fatal(err)
	}
	completed := w.now
	if err := w.store.Observe(w.ctx, model.Observation{TaskID: id, CompletedAt: &completed}); err != nil {
		w.t.Fatal(err)
	}
	return task, pr.Number
}

// mergedInto reports whether branch's tip is on base.
func (w *breakerWorld) mergedInto(branch, base string) bool {
	return w.branchExists(branch) && exec.Command("git", "--git-dir", w.bare, "merge-base", "--is-ancestor",
		"refs/heads/"+branch, "refs/heads/"+base).Run() == nil
}

// note is the row note the API serves for id, as the Merge Queue page
// and the task pane show it.
func (w *breakerWorld) note(id string) string {
	w.t.Helper()
	var detail struct {
		MergeQueueState string `json:"mergeQueueState"`
		MergeQueueNote  string `json:"mergeQueueNote"`
	}
	w.get("/api/tasks/"+id, &detail)
	return detail.MergeQueueNote
}

var behindTask = regexp.MustCompile(`behind task (\S+) in the (\S+) (\S+) queue`)

func TestMergeQueueLanesContentionWalkthrough(t *testing.T) {
	t.Parallel()
	w := newBreakerWorld(t)
	w.scriptChanges()
	pulls := &unreadablePullSim{inner: w.sim}
	w.deps.Client = github.NewClient(pulls, nil)
	w.pushMain("merge rules", map[string]string{model.MergeRulesPath: laneRules})
	if got := w.sync(); got == nil || got.LastError != "" {
		t.Fatalf("the merge rules did not sync: %+v", got)
	}
	// max_merges_per_repo is the default, 1: one merge (or batch) per lane
	// a cycle.
	if cfg, err := w.store.GetConfig(w.ctx); err != nil || (cfg != nil && cfg.MaxMergesPerRepo > 1) {
		t.Fatalf("config: %+v, %v; want max merges per repo branch 1", cfg, err)
	}
	if !w.ticks(10, func() bool { r := w.baseRow(); return r != nil && r.Conclusion == string(ci.ConclusionSuccess) }) {
		t.Fatalf("main was never judged green: %+v", w.baseRow())
	}
	w.pushBranch("candidate")

	// Step 2: A1 on main, held in CI by its GitHub check; A2 behind it on
	// main; B1 and B2 on candidate, green -- and the first candidate batch
	// fails once, so its recheck passes (a flake).
	a1, _ := w.queuedChange("t-a1", "main", "held/a1.txt")
	a1Branch := a1.BranchName()
	w.sim.mu.Lock()
	w.sim.sim.CheckRuns[a1Branch] = []github.CheckRun{{Name: "build", Status: "in_progress"}}
	w.sim.mu.Unlock()
	w.tick()
	a2, _ := w.queuedChange("t-a2", "main", "changes/a2.txt")
	w.sandboxes.mu.Lock()
	w.sandboxes.failBranch = "candidate"
	w.sandboxes.mu.Unlock()
	w.sandboxes.failFirstBatch(t)
	b1, _ := w.queuedChange("t-b1", "candidate", "changes/b1.txt")
	b2, _ := w.queuedChange("t-b2", "candidate", "changes/b2.txt")

	notes := map[string][]string{}
	record := func() {
		for _, id := range []string{a1.ID, a2.ID, b1.ID, b2.ID} {
			n := w.note(id)
			if n != "" && (len(notes[id]) == 0 || notes[id][len(notes[id])-1] != n) {
				notes[id] = append(notes[id], n)
			}
		}
	}
	candidateDone := func() bool {
		record()
		return w.mergedInto(b1.BranchName(), "candidate") && w.mergedInto(b2.BranchName(), "candidate")
	}
	if !w.ticks(30, candidateDone) {
		t.Fatalf("candidate changes did not merge while main's head was in CI: b1 %s, b2 %s; notes %v",
			w.state(b1.ID), w.state(b2.ID), notes)
	}
	for i := 0; i < 2; i++ {
		w.tick()
		record()
	}
	// There is no order between pull requests (task 2346): a1 is held by
	// its own CI and nothing else, and a2 merges past it.
	if w.mergedInto(a1Branch, "main") {
		t.Fatalf("a1 merged while its own CI was still running")
	}
	if !w.ticks(10, func() bool { record(); return w.mergedInto(a2.BranchName(), "main") }) {
		t.Fatalf("a2 did not merge while a1 was in CI: %s, notes %q", w.state(a2.ID), notes[a2.ID])
	}
	for id, ns := range notes {
		t.Logf("%s notes: %q", id, ns)
	}
	// No row names another change as the one in front, and none names
	// the whole repository's queue.
	for id, ns := range notes {
		for _, n := range ns {
			if strings.Contains(n, "acme/widgets queue") || strings.Contains(n, "acme/widgets's queue") {
				t.Errorf("%s's note names the whole repository's queue: %q", id, n)
			}
			if behindTask.MatchString(n) || strings.Contains(n, "behind task") {
				t.Errorf("%s's note names another change as in front of it: %q", id, n)
			}
		}
	}

	// Step 3: a task that names no base, whose pull request this cycle
	// cannot read, is queued ahead of B3 on candidate. A member nobody can
	// read holds itself and nothing else (task 2346): B3 merges anyway,
	// and n0 waits for its read to come back.
	n0, n0PR := w.queuedChange("t-n0", "", "changes/n0.txt")
	pulls.fail(n0PR)
	b3, _ := w.queuedChange("t-b3", "candidate", "changes/b3.txt")
	if !w.ticks(20, func() bool { w.markAllClean(); return w.mergedInto(b3.BranchName(), "candidate") }) {
		t.Fatalf("b3 did not merge while a no-base task's pull request could not be read: %s, note %q", w.state(b3.ID), w.note(b3.ID))
	}
	if w.mergedInto(n0.BranchName(), "main") {
		t.Fatalf("n0 merged while its pull request could not be read")
	}
	t.Logf("with %s's pull request unreadable: b3 note %q, n0 note %q", n0.ID, w.note(b3.ID), w.note(n0.ID))
	pulls.fail(0)
	if !w.ticks(20, func() bool { return w.mergedInto(n0.BranchName(), "main") }) {
		t.Logf("n0 (no base, so main's lane, behind a1) has not merged: %q", w.note(n0.ID))
	}

	// Release A1: main's lane drains.
	success := "success"
	w.sim.mu.Lock()
	w.sim.sim.CheckRuns[a1Branch] = []github.CheckRun{{Name: "build", Status: "completed", Conclusion: &success}}
	w.sim.mu.Unlock()
	if !w.ticks(30, func() bool {
		return w.mergedInto(a1Branch, "main") && w.mergedInto(a2.BranchName(), "main") && w.mergedInto(n0.BranchName(), "main")
	}) {
		t.Fatalf("main's lane did not drain once a1's check passed: a1 %s %q, a2 %s %q, n0 %s %q",
			w.state(a1.ID), w.note(a1.ID), w.state(a2.ID), w.note(a2.ID), w.state(n0.ID), w.note(n0.ID))
	}
	w.ticks(3, func() bool { return false })

	// Step 4 (flake): the contention the queue recorded, by reason.
	events, err := w.store.MergeQueueContentionEvents(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	byReason := map[string]int{}
	for _, e := range events {
		t.Logf("contention: %s %s %s (%s)", e.Repo, e.TaskID, e.Kind, e.Reason)
		byReason[e.Kind+"/"+e.Reason]++
	}
	if byReason[model.MergeQueueRetest+"/"+model.MergeQueueFlakeRecheck] < 1 {
		t.Errorf("no flake-recheck retest recorded, though the first candidate batch failed once: %v", byReason)
	}
	records, _ := w.store.MergeBatchRecords(w.ctx)
	for _, r := range records {
		t.Logf("batch %s: %d member(s), outcome %s, merged %d, splits %d", shortID(r.RunID), r.Size, r.Outcome, r.Merged, r.Splits)
	}

	ids := []string{a1.ID, a2.ID, b1.ID, b2.ID, n0.ID, b3.ID}
	var waits []time.Duration
	for _, id := range ids {
		o, _ := w.store.GetObservation(w.ctx, id)
		if o == nil || o.PrMergedAt == nil || o.MergeQueueEnteredAt == nil {
			t.Fatalf("%s merged with no queue entry or merge recorded: %+v", id, o)
		}
		waits = append(waits, o.PrMergedAt.Sub(*o.MergeQueueEnteredAt))
		t.Logf("%s: entered %s, merged %s, waited %s", id, o.MergeQueueEnteredAt.Format(time.TimeOnly),
			o.PrMergedAt.Format(time.TimeOnly), waits[len(waits)-1])
	}
	q := url.Values{
		"section": {"merges"},
		"since":   {baseTime.Add(-time.Hour).Format(time.RFC3339)},
		"until":   {w.now.Add(24 * time.Hour).Format(time.RFC3339)},
	}
	var got metricsMerges
	w.get("/api/metrics?"+q.Encode(), &got)
	c := got.Merges.Contention
	t.Logf("/api/metrics merges: landed %d; queue wait n=%d min %.1fs max %.1fs; rebases %d retests %d total %d",
		got.Merges.Landed, c.QueueWaitTime.N, c.QueueWaitTime.MinSeconds, c.QueueWaitTime.MaxSeconds,
		c.RebaseRetest.Rebases, c.RebaseRetest.Retests, c.RebaseRetest.TotalContention)
	if got.Merges.Landed != len(ids) {
		t.Errorf("landed = %d, want %d", got.Merges.Landed, len(ids))
	}
	if c.QueueWaitTime.N != len(ids) || !sameSeconds(c.QueueWaitTime.MinSeconds, slices.Min(waits)) ||
		!sameSeconds(c.QueueWaitTime.MaxSeconds, slices.Max(waits)) {
		t.Errorf("queue wait = %+v, want %d samples, min %s max %s", c.QueueWaitTime, len(ids), slices.Min(waits), slices.Max(waits))
	}
	rebases, retests := 0, 0
	for k, n := range byReason {
		if strings.HasPrefix(k, model.MergeQueueRebase+"/") {
			rebases += n
		} else {
			retests += n
		}
	}
	if c.RebaseRetest.Rebases != rebases || c.RebaseRetest.Retests != retests || c.RebaseRetest.TotalContention != rebases+retests {
		t.Errorf("rebase/retest = %+v, want the queue's own records %v", c.RebaseRetest, byReason)
	}
}

// startCI is one grain CI run asked for directly of the daemon's CI
// service, against base, waited for.
func (w *breakerWorld) startCI(ref, commit, base string) ci.Run {
	w.t.Helper()
	run, err := w.service.Start(w.ctx, ci.Request{Repo: "acme/widgets", Ref: ref, Commit: commit,
		Event: ci.EventPush, BaseCommit: base})
	if err != nil {
		w.t.Fatalf("starting grain CI on %s: %v", ref, err)
	}
	w.waitForIdleCI()
	got, ok := w.service.Get(run.ID)
	if !ok {
		w.t.Fatalf("run %s is gone", run.ID)
	}
	return got
}

// Step 5: a failure cached against base X is evicted by a green verdict at
// X and by nothing else; a pass and a failure against another base stay;
// the same fingerprint against base Y does not replay X's failure; and the
// base each job ran against reads back through the CI run API.
func TestCIFailureEvictedOnlyByGreenAtItsOwnBase(t *testing.T) {
	t.Parallel()
	w := newBreakerWorld(t)
	w.scriptChanges()
	if !w.ticks(10, func() bool { r := w.baseRow(); return r != nil && r.Conclusion == string(ci.ConclusionSuccess) }) {
		t.Fatalf("main was never judged green: %+v", w.baseRow())
	}
	y := w.pushBranch("candidate")

	// P passes, cached (its base is main's tip at the time).
	w.pushBranch("grain/task-p")
	w.pushOutsideARun("grain/task-p", "p.txt")
	pHead := w.head("grain/task-p")
	p := w.startCI("grain/task-p", pHead, w.head("main"))
	pJob := onlyJob(t, p)
	if p.Conclusion != ci.ConclusionSuccess {
		t.Fatalf("P: %s", p.Conclusion)
	}

	// Main goes red at X: every run fails while the switch is armed.
	red := filepath.Join(t.TempDir(), "red")
	mustWriteFile(t, red, "on\n")
	w.sandboxes.mu.Lock()
	w.sandboxes.red = red
	w.sandboxes.mu.Unlock()
	x := w.pushMain("X", map[string]string{"X.md": "x\n"})
	w.ticks(3, func() bool { r := w.baseRow(); return r.Commit == x && r.Conclusion != "" })
	if r := w.baseRow(); r.Commit != x || !r.Broken() {
		t.Fatalf("main's row after X: %+v, want red at %s", r, shortID(x))
	}
	// V fails against X; U fails against Y, candidate's tip.
	w.pushBranch("grain/task-v")
	w.pushOutsideARun("grain/task-v", "v.txt")
	vHead := w.head("grain/task-v")
	v := w.startCI("grain/task-v", vHead, x)
	vJob := onlyJob(t, v)
	w.pushBranch("grain/task-u")
	w.pushOutsideARun("grain/task-u", "u.txt")
	u := w.startCI("grain/task-u", w.head("grain/task-u"), y)
	uJob := onlyJob(t, u)
	for name, k := range map[string]ci.Key{"V": vJob.Key, "U": uJob.Key} {
		if found, concl, _ := w.cacheEntry(k); !found || concl != ci.ConclusionFailure {
			t.Fatalf("%s: cache holds %v %s, want a failure", name, found, concl)
		}
	}
	if _, _, base := w.cacheEntry(vJob.Key); base != x {
		t.Fatalf("V's failure cached against %q, want X %s", base, x)
	}
	if _, _, base := w.cacheEntry(uJob.Key); base != y {
		t.Fatalf("U's failure cached against %q, want Y %s", base, y)
	}

	// The base reads back through the API, from the stored run.
	var apiRun struct {
		Jobs []struct {
			BaseCommit string `json:"baseCommit"`
			Conclusion string `json:"conclusion"`
		} `json:"jobs"`
	}
	w.get("/api/ci/runs/"+v.ID, &apiRun)
	if len(apiRun.Jobs) != 1 || apiRun.Jobs[0].BaseCommit != x {
		t.Errorf("GET /api/ci/runs/%s jobs = %+v, want one job with baseCommit %s", v.ID, apiRun.Jobs, x)
	}
	if stored, err := w.store.GetCIRun(w.ctx, v.ID); err != nil || stored == nil || len(stored.Jobs) != 1 ||
		stored.Jobs[0].BaseCommit != x {
		t.Errorf("the stored run %s: %+v, %v; want ci_job.base_commit %s", v.ID, stored, err, x)
	}

	// Main goes green at X: the switch is turned off and owner, the
	// deployment's admin account, asks for main to be run again
	// (Repository -> Run grain CI), at X. Asking needs a repository or
	// system admin.
	if err := os.Remove(red); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ui.RequestBaseBranchRun(w.as("owner"), "acme/widgets", ui.BaseBranchRunRequest{Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if !w.ticks(6, func() bool { r := w.baseRow(); return r.Commit == x && r.Conclusion == string(ci.ConclusionSuccess) }) {
		t.Fatalf("main never went green at X: %+v", w.baseRow())
	}
	if found, concl, base := w.cacheEntry(vJob.Key); found {
		t.Errorf("green at X left V's %s against %s cached", concl, shortID(base))
	}
	if found, concl, _ := w.cacheEntry(uJob.Key); !found || concl != ci.ConclusionFailure {
		t.Errorf("green at X evicted U's failure against Y: %v %s", found, concl)
	}
	if found, concl, _ := w.cacheEntry(pJob.Key); !found || concl != ci.ConclusionSuccess {
		t.Errorf("green at X evicted P's pass: %v %s", found, concl)
	}
	again := w.startCI("grain/task-p", pHead, w.head("main"))
	if j := onlyJob(t, again); !j.Cached || again.Conclusion != ci.ConclusionSuccess {
		t.Errorf("P again: %s cached=%v, want its pass from cache", again.Conclusion, j.Cached)
	}

	// Same fingerprint, other base: U's failure was against Y; asked at X
	// it is not replayed (the switch is off, so it runs and passes).
	uAgain := w.startCI("grain/task-u", w.head("grain/task-u"), x)
	if j := onlyJob(t, uAgain); j.Key != uJob.Key || j.Cached || uAgain.Conclusion != ci.ConclusionSuccess {
		t.Errorf("U at X: key same %v, %s cached=%v; want a fresh pass, not Y's failure replayed",
			j.Key == uJob.Key, uAgain.Conclusion, j.Cached)
	}
	t.Logf("X=%s Y=%s: V evicted on green at X, U (Y) kept until asked at X, P's pass kept and hit",
		shortID(x), shortID(y))
}
