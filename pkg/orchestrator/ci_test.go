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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/sandboxaccess"
)

// grain's own CI is started immediately by wait_for_checks when an agent asks,
// and otherwise by the reconciler once an open pull request needs its required
// run. A task holding the grain-ci capability must pass the full suite on its
// exact head, and the reconciler copies its eventual verdict where the task is
// read.
//
// These tests drive the reconciler against a stub ci.Runner rather than a
// real ci.Service, for the reason ci.Runner exists at all:
// what is interesting here is the state machine around a run -- when one
// is started, when one is superseded, what is recorded, what is said and
// where -- none of which needs a sandbox, a checkout or a workflow file
// to be exercised. pkg/ci's own tests own the other half.

// fakeCI is ci.Runner with the runs held in a map a test
// writes: Start records the request and hands back a pending run, and a
// test concludes one by calling finish.
type fakeCI struct {
	ci.NopRunner
	mu sync.Mutex
	// requests is every ci.Request Start was asked for, in order.
	requests []ci.Request
	runs     map[string]ci.Run
	// cancelled is every run id Cancel was called with, reasons is the
	// note each of those calls carried, and sources who each said it was
	// asking on behalf of (ci.CancelSource).
	cancelled []string
	reasons   map[string]string
	sources   map[string]ci.CancelSource
	// forgotten stands in for a ci.RunStore that has lost a run -- one
	// pruned past the history this deployment keeps, or, on a deployment
	// keeping its runs in memory, a restart. Get answers false for these.
	forgotten map[string]bool
	// refusal is what Start answers with instead of starting anything,
	// the way ci.Service refuses a request it cannot plan: a selected job
	// that does not exist in the matching workflows, above all. Nil is a
	// service that starts whatever it is asked for.
	refusal error
	// evicted is every base commit EvictBaseTransitionFailures was
	// asked to evict cached failures for, in order.
	evicted []string
	next    int
	// failed is the last run finish concluded as a failure, per commit,
	// and rechecksAs what an uncached recheck of one of those becomes
	// (orchestrator's recheck.go): by default the failure reproduces --
	// concluded at once, with the same jobs -- which is a real failure,
	// failing twice. A test about flakes sets it to recheckPasses, or to
	// recheckPending to conclude the recheck by hand. A base branch's own
	// confirmation is never answered this way: its tests drive it.
	failed     map[string]ci.Run
	rechecksAs recheckBehaviour
	// flakes is what MarkFlake was told, by run id.
	flakes map[string][]string
}

type recheckBehaviour int

const (
	recheckPasses recheckBehaviour = iota + 1
	recheckPending
)

// MarkFlake is ci.Runner.MarkFlake: it records which jobs of which
// run were marked flaky, keyed by run id.
func (f *fakeCI) MarkFlake(id string, jobs []string, rechecks string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.flakes == nil {
		f.flakes = map[string][]string{}
	}
	f.flakes[id] = append(f.flakes[id], jobs...)
	_, ok := f.runs[id]
	return ok
}

func (f *fakeCI) flakesOf(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.flakes[id]...)
}

// answerRechecks sets what the next rechecks of a failed run become.
func (f *fakeCI) answerRechecks(as recheckBehaviour) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rechecksAs = as
}

func newFakeCI() *fakeCI {
	return &fakeCI{runs: map[string]ci.Run{}, forgotten: map[string]bool{},
		reasons: map[string]string{}, sources: map[string]ci.CancelSource{}}
}

func (f *fakeCI) Start(ctx context.Context, req ci.Request) (ci.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if f.refusal != nil {
		// Asked for and refused: the request is counted, because that is
		// the cost a caller that keeps asking is incurring, and no run
		// exists.
		return ci.Run{}, f.refusal
	}
	f.next++
	run := ci.Run{
		ID:          "run" + string(rune('0'+f.next)),
		Repo:        req.Repo,
		Ref:         req.Ref,
		Commit:      req.Commit,
		Event:       req.Event,
		RequestedBy: req.RequestedBy,
	}
	if prev, ok := f.failed[req.Commit]; ok && req.Recheck && f.rechecksAs != recheckPending &&
		!strings.HasPrefix(req.RequestedBy, "base-confirm:") {
		run.Conclusion = prev.Conclusion
		run.Jobs = append([]ci.JobRun(nil), prev.Jobs...)
		if f.rechecksAs == recheckPasses {
			run.Conclusion = ci.ConclusionSuccess
			for i := range run.Jobs {
				run.Jobs[i].Conclusion = ci.ConclusionSuccess
			}
		}
	}
	f.runs[run.ID] = run
	return run, nil
}

func (f *fakeCI) Get(id string) (ci.Run, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forgotten[id] {
		return ci.Run{}, false
	}
	run, ok := f.runs[id]
	return run, ok
}

// List is the rest of a run store as shared-fault detection reads it
// (ci.FaultRunSource): every run this deployment remembers, so a verdict
// can say whether other branches are failing the same way.
func (f *fakeCI) List() []ci.Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ci.Run, 0, len(f.runs))
	for _, run := range f.runs {
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// Find answers with every run List does, unfiltered: its one caller here
// (liveBranchCIRuns) narrows them to the repo and branch itself.
func (f *fakeCI) Find(_ ci.RunFilter, _ int) ci.RunPage {
	return ci.RunPage{Runs: f.List()}
}

func (f *fakeCI) EvictBaseTransitionFailures(ctx context.Context, baseCommit string) (int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evicted = append(f.evicted, baseCommit)
	return 1, true, nil
}

// evictedFor is whether a base transition eviction named commit.
func (f *fakeCI) evictedFor(commit string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.evicted {
		if c == commit {
			return true
		}
	}
	return false
}

// remember puts a run into this deployment's memory that nothing here
// started -- another branch's, which is what a shared fault is made of.
func (f *fakeCI) remember(run ci.Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs[run.ID] = run
}

func (f *fakeCI) Cancel(id, reason string, by ci.CancelSource) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
	f.reasons[id] = reason
	f.sources[id] = by
	run, ok := f.runs[id]
	if ok && !run.Conclusion.Done() {
		run.Conclusion = ci.ConclusionCancelled
		run.CancelReason = reason
		run.CancelledBy = by
		f.runs[id] = run
	}
	return ok
}

// interrupt ends a run the way a restart does: cancelled, because
// nothing concluded it, and marked as nobody's decision
// (ci.StoredRunStore.RecoverUnattachable).
func (f *fakeCI) interrupt(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run := f.runs[id]
	run.Conclusion = ci.ConclusionCancelled
	run.Interrupted = true
	run.Error = ci.InterruptedRunError
	f.runs[id] = run
}

// cancelByHand ends a run the way a person stopping it does: cancelled,
// with the note they left about why and their own hand on it
// (ci.Service.Cancel, ci.CancelByOperator).
func (f *fakeCI) cancelByHand(id, reason string, jobs ...ci.JobRun) {
	f.cancelledAs(id, ci.CancelByOperator, reason, jobs...)
}

// cancelBySystem ends a run the way grain's own doing does: a job that
// ran past its timeout, a daemon stopping under it, a supersede. The
// note is optional there and usually absent, which is exactly why the
// source is recorded separately from it.
func (f *fakeCI) cancelBySystem(id, reason string, jobs ...ci.JobRun) {
	f.cancelledAs(id, ci.CancelBySystem, reason, jobs...)
}

func (f *fakeCI) cancelledAs(id string, by ci.CancelSource, reason string, jobs ...ci.JobRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run := f.runs[id]
	run.Conclusion = ci.ConclusionCancelled
	run.CancelReason = reason
	run.CancelledBy = by
	run.Jobs = jobs
	f.runs[id] = run
}

// finish ends a run the way the engine would, with the conclusion and
// jobs a test wants to see reported.
func (f *fakeCI) finish(id string, conclusion ci.Conclusion, jobs ...ci.JobRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run := f.runs[id]
	run.Conclusion = conclusion
	run.Jobs = jobs
	f.runs[id] = run
	if conclusion == ci.ConclusionFailure || (conclusion == ci.ConclusionErrored && lostChannel(jobs)) {
		if f.failed == nil {
			f.failed = map[string]ci.Run{}
		}
		f.failed[run.Commit] = run
	}
}

// lostChannel reports whether any of jobs ended on a lost sandbox channel,
// the one errored run grain rechecks (startChannelLostRecheck).
func lostChannel(jobs []ci.JobRun) bool {
	for _, j := range jobs {
		if strings.HasPrefix(j.Error, sandboxaccess.ChannelLostFault) {
			return true
		}
	}
	return false
}

// refuse makes every later Start answer err and start nothing, and
// allow puts that back. It is how a test drives the one request grain CI
// really does refuse in the field: a base-branch job list naming a job
// no workflow of the branch declares any more (ci.selectPlanJobs).
func (f *fakeCI) refuse(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refusal = err
}

func (f *fakeCI) allow() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refusal = nil
}

func (f *fakeCI) lastRequest(t *testing.T) ci.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("nothing asked grain CI for a run")
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeCI) started() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// rechecks is how many of those runs were asked for with the cache
// bypassed (ci.Request.Recheck) -- the confirmations of a red base
// branch, which are the ones that have to be bounded.
func (f *fakeCI) rechecks() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, req := range f.requests {
		if req.Recheck {
			n++
		}
	}
	return n
}

// runCIReconciler runs the cycle's own "ci" reconciler -- found by name in
// Reconcilers(), so a change that dropped it from the cycle fails here
// rather than silently turning the feature off.
func runCIReconciler(t *testing.T, ctx context.Context, store *model.Store,
	client github.Client, runner ci.Runner) error {

	return runCIReconcilerAt(t, ctx, store, client, runner, baseTime)
}

func runCIReconcilerAt(t *testing.T, ctx context.Context, store *model.Store,
	client github.Client, runner ci.Runner, now time.Time) error {

	t.Helper()
	deps := orchestrator.Deps{Store: store, Client: client, CI: runner}
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == "ci" {
			return r.Reconcile(ctx, deps, now)
		}
	}
	t.Fatal(`no "ci" reconciler in the cycle: grain CI is wired to nothing`)
	return nil
}

// grantCI attaches the grain-ci capability to a task, which is the whole
// of the switch: per task here, and per repo or deployment-wide by the
// default sets that land in exactly this field when a task is created.
func grantCI(t *testing.T, ctx context.Context, store *model.Store, task model.Task) model.Task {
	t.Helper()
	task.Grants = append(task.Grants, model.Grant{Capability: grainci.CapabilityName})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	return task
}

func ciStatusOf(t *testing.T, ctx context.Context, store *model.Store, taskID string) *model.CIStatus {
	t.Helper()
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil {
		return nil
	}
	return obs.CI
}

func headSHA(t *testing.T, client github.Client, repo model.RepoRef, branch string) string {
	t.Helper()
	head, err := client.GetBranchHead(repo.Owner, repo.Name, branch)
	if err != nil || head == nil {
		t.Fatalf("reading %s's head: %v", branch, err)
	}
	return head.SHA
}

// startRequiredCI is the task-scoped wait_for_checks call's durable
// effect, stated here in orchestrator's own types: the UI starts the run
// and records it, and this package's reconciler only collects it.
func startRequiredCI(t *testing.T, ctx context.Context, store *model.Store,
	client github.Client, runner *fakeCI, task model.Task, repo model.RepoRef, branch string) *model.CIStatus {

	t.Helper()
	sha := headSHA(t, client, repo, branch)
	run, err := runner.Start(ctx, ci.Request{
		Repo: repo.String(), Ref: branch, Commit: sha, Event: ci.EventPush, RequestedBy: task.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := baseTime
	status := &model.CIStatus{RunID: run.ID, Commit: sha, StartedAt: &started}
	recordCIStatus(t, ctx, store, task.ID, status)
	return status
}

// The whole loop, in one test: a run is started against the branch's own
// head, the task records that it is waiting, and when the run comes back
// red the verdict lands on the task's record, in its conversation, and on
// the pull request.
func TestGrainCICollectsAnExplicitRunAndRecordsTheVerdict(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()
	startRequiredCI(t, ctx, store, client, runner, task, repo, branch)

	// The request names the repo, the branch, the commit that branch is
	// actually at, and the event a push is.
	req := runner.lastRequest(t)
	sha := headSHA(t, client, repo, branch)
	if req.Repo != repo.String() || req.Ref != branch || req.Commit != sha {
		t.Fatalf("request = %+v, want %s at %s on %s", req, repo, branch, sha)
	}
	if req.Event != ci.EventPush {
		t.Fatalf("event = %q, want %q: a branch a run pushed is a push", req.Event, ci.EventPush)
	}
	if req.RequestedBy != task.ID {
		t.Errorf("requestedBy = %q, want the task that owns the branch (%s)", req.RequestedBy, task.ID)
	}

	// And the task now says a run is in flight, against that commit, so
	// nothing starts a second one.
	status := ciStatusOf(t, ctx, store, task.ID)
	if status == nil || status.RunID == "" || status.Commit != sha {
		t.Fatalf("recorded status = %+v, want a run id against %s", status, sha)
	}
	if status.Done() {
		t.Fatalf("conclusion = %q, want none yet: the run has not finished", status.Conclusion)
	}
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatalf("second CI reconcile: %v", err)
	}
	if runner.started() != 1 {
		t.Fatalf("grain CI was asked for %d runs, want 1: the branch has not moved", runner.started())
	}
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 0 {
		t.Fatalf("comments = %q, want none while the run is still going", bodies)
	}

	// The run comes back red.
	runner.finish(status.RunID, ci.ConclusionFailure, ci.JobRun{
		Workflow: "checks", JobID: "test", Name: "test", Conclusion: ci.ConclusionFailure,
		Steps: []ci.StepRun{{
			Name: "go test", Conclusion: ci.ConclusionFailure, ExitCode: 1,
			Output: "--- FAIL: TestThing\n    thing_test.go:12: wanted 3, got 4\nFAIL\n",
		}},
	})
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatalf("collecting the verdict: %v", err)
	}

	// The record carries the answer itself, not a pointer at a run that
	// is about to be forgotten.
	status = ciStatusOf(t, ctx, store, task.ID)
	if status == nil || status.Conclusion != string(ci.ConclusionFailure) {
		t.Fatalf("recorded status = %+v, want a failure", status)
	}
	if !strings.Contains(status.Detail, "checks/test") {
		t.Errorf("detail = %q, want the failing job named in it", status.Detail)
	}
	if status.FinishedAt == nil {
		t.Error("FinishedAt was not recorded, so nothing can say when this verdict was reached")
	}
	if status.Green() {
		t.Error("a failing run reads as green")
	}

	// The task's own conversation gets only the verdict. A later agent can
	// read the run log without paying to replay it in every prompt.
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("comments = %q, want exactly one about the failed run", bodies)
	}
	if !strings.HasPrefix(bodies[0], "grain CI failure at `") ||
		strings.Contains(bodies[0], "checks/test") || strings.Contains(bodies[0], "wanted 3, got 4") {
		t.Errorf("comment = %q, want a terse failure verdict without job output", bodies[0])
	}

	// And so does the pull request, which is where a reviewer is.
	prComments, err := client.ListComments(repo.Owner, repo.Name, sim.PullRequests[0].Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(prComments) != 1 {
		t.Fatalf("pull request comments = %d, want one naming the failure", len(prComments))
	}

	// Said once. A cycle that comes round again with the same verdict
	// already recorded says nothing further.
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatalf("third CI reconcile: %v", err)
	}
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 1 {
		t.Fatalf("comments = %q, want still just the one: the verdict has not changed", bodies)
	}
}

// A green run gets a terse task update and no pull request comment, where
// it would be one comment per push for a fact a badge reports elsewhere.
func TestAGreenGrainCIRunIsReportedOnTheTaskAndNotOnThePullRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(status.RunID, ci.ConclusionSuccess, ci.JobRun{
		Workflow: "checks", JobID: "test", Name: "test", Conclusion: ci.ConclusionSuccess,
	})
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}

	if status = ciStatusOf(t, ctx, store, task.ID); !status.Green() {
		t.Fatalf("recorded status = %+v, want a pass", status)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 || !strings.HasPrefix(bodies[0], "grain CI success at `") {
		t.Fatalf("comments = %q, want one terse success verdict", bodies)
	}
	prComments, err := client.ListComments(repo.Owner, repo.Name, sim.PullRequests[0].Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(prComments) != 0 {
		t.Fatalf("pull request comments = %d, want none: a green build is not news on a pull request", len(prComments))
	}
}

// The capability makes a full-suite result mandatory, and the reconciler
// starts it when an open pull request has no run of its own. This is the
// backstop for an agent that exits without making its final wait_for_checks
// call.
func TestGrainCIStartsRequiredRunWhenTheAgentDidNotAsk(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()

	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	if runner.started() != 1 {
		t.Fatalf("grain CI started %d runs, want the required run", runner.started())
	}
	status := ciStatusOf(t, ctx, store, task.ID)
	if status == nil || status.RunID == "" || status.Commit != headSHA(t, client, repo, branch) {
		t.Fatalf("status = %+v, want the current branch head recorded as in flight", status)
	}
	// The required verdict is held to the base's workflows, so a branch
	// cannot pass its own gate by deleting it (bwsalmon/grain#2390).
	runner.mu.Lock()
	req := runner.requests[0]
	runner.mu.Unlock()
	if !req.GateOnBase || req.BaseRef != task.Base {
		t.Fatalf("required run asked for %+v, want it gated on the task's base %q", req, task.Base)
	}
}

// A task that is dispatched again pushes more commits to the same branch,
// and the verdict about the tree before them is not a second fact to keep
// -- it is a stale one to replace. The run testing that older tree is
// cancelled rather than left to spend sandboxes on an answer nobody will
// read.
func TestGrainCIStartsAgainWhenTheBranchMoves(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()

	first := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)

	// The task is dispatched again and pushes.
	pushFile(t, sim.BareRepo, branch, "thing.txt", "second attempt")
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}

	if runner.started() != 2 {
		t.Fatalf("grain CI started %d runs, want a replacement for the merged or pushed head", runner.started())
	}
	if len(runner.cancelled) != 1 || runner.cancelled[0] != first.RunID {
		t.Fatalf("cancelled runs = %v, want stale in-flight run %s", runner.cancelled, first.RunID)
	}
	// And it says so on the run. A cancellation grain makes on its own
	// behalf is one somebody will meet later with no way to ask why, so
	// it leaves the same kind of note a person would.
	if reason := runner.reasons[first.RunID]; !strings.Contains(reason, "superseded") {
		t.Errorf("reason recorded on the stale run = %q, want it to say it was superseded", reason)
	}
	if got := ciStatusOf(t, ctx, store, task.ID); got == nil || got.RunID == first.RunID || got.Commit != headSHA(t, client, repo, branch) {
		t.Fatalf("status = %+v, want a replacement run on the new head", got)
	}
}

func TestGrainCISupersedesLiveSelectiveRunsWhenBranchMovesOrRequiredRunStarts(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()

	first := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	// A selective run is started on the same branch (not recorded in Observation.CI).
	selectiveOld, err := runner.Start(ctx, ci.Request{
		Repo: repo.String(), Ref: branch, Commit: first.Commit,
		Event: ci.EventPush, RequestedBy: task.ID, Jobs: []string{"go"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Branch moves to a new commit, and another selective run is also in flight on the new commit.
	pushFile(t, sim.BareRepo, branch, "thing.txt", "second attempt")
	newSHA := headSHA(t, client, repo, branch)
	selectiveNew, err := runner.Start(ctx, ci.Request{
		Repo: repo.String(), Ref: branch, Commit: newSHA,
		Event: ci.EventPush, RequestedBy: task.ID, Jobs: []string{"lint"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}

	wantCancelled := map[string]bool{
		first.RunID:     true,
		selectiveOld.ID: true,
		selectiveNew.ID: true,
	}
	if len(runner.cancelled) != 3 {
		t.Fatalf("cancelled = %v, want %v", runner.cancelled, wantCancelled)
	}
	for _, id := range runner.cancelled {
		if !wantCancelled[id] {
			t.Errorf("unexpected cancelled run %s (want %v)", id, wantCancelled)
		}
	}
}

// A merge into the pull request branch is a push by another route. The pass
// before it describes the old tree, so the reconciler starts the required
// suite again against the merge commit rather than letting that commit land
// on its parents' verdicts.
func TestGrainCIStartsAgainAfterAMergeMovesTheBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()

	first := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(first.RunID, ci.ConclusionSuccess)
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}

	pushFile(t, sim.BareRepo, "main", "base.txt", "change merged ahead of this task")
	merged, err := client.MergeBranch(repo.Owner, repo.Name, branch, "main", "Merge main")
	if err != nil || !merged.Merged {
		t.Fatalf("merging main into %s: result=%+v err=%v", branch, merged, err)
	}
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}

	if runner.started() != 2 {
		t.Fatalf("grain CI started %d runs, want one before and one after the merge", runner.started())
	}
	status := ciStatusOf(t, ctx, store, task.ID)
	if status == nil || status.RunID == first.RunID || status.Commit != merged.SHA {
		t.Fatalf("status = %+v, want a new run on merge commit %s", status, merged.SHA)
	}
}

// A status can outlive the run it names: runs are kept for a while and
// then pruned, and a deployment holding them in memory loses them on
// every restart. A task left waiting on a run nobody has is a task that
// never gets an answer, so the status is forgotten and the next pass starts
// a replacement.
func TestGrainCIForgetsARunThisDaemonNoLongerRemembers(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()

	first := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.forgotten[first.RunID] = true

	// One cycle notices it is gone. The next starts a replacement.
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	if status := ciStatusOf(t, ctx, store, task.ID); status != nil {
		t.Fatalf("status = %+v, want it forgotten along with the run", status)
	}
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	if runner.started() != 2 {
		t.Fatalf("grain CI was asked for %d runs, want an automatic replacement", runner.started())
	}
	if status := ciStatusOf(t, ctx, store, task.ID); status == nil || status.RunID == first.RunID {
		t.Fatalf("status = %+v, want a replacement run", status)
	}
}

// The same thing arrived at from the other direction, now that a run
// survives the daemon that ran it: the run is still there, and it is
// still not an answer. A daemon that stopped mid-run leaves rows saying
// "no conclusion", which the next one stamps cancelled and interrupted
// -- and an interrupted run is nobody's verdict, so the task must not be
// told its branch was cancelled and left at that.
func TestInterruptedGrainCIRunIsAutomaticallyReplaced(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()

	first := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.interrupt(first.RunID)

	// One cycle notices and forgets it; the next starts a replacement.
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	if status := ciStatusOf(t, ctx, store, task.ID); status != nil {
		t.Fatalf("status = %+v, want it forgotten: an interrupted run is not a verdict", status)
	}
	// And nothing was announced about it. A cancelled conclusion nobody
	// asked for is not news for a pull request or for the task.
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 0 {
		t.Fatalf("comments = %q, want none about a run that was interrupted", bodies)
	}

	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	if runner.started() != 2 {
		t.Fatalf("grain CI was asked for %d runs, want an automatic replacement", runner.started())
	}
	status := ciStatusOf(t, ctx, store, task.ID)
	if status == nil || status.RunID == first.RunID {
		t.Fatalf("status = %+v, want the automatic replacement", status)
	}

	// A cancellation somebody asked for is replaced the same way, after
	// the same delay: a person cancelling a run is stopping that run, not
	// the task (TestAGrainCIRunCancelledByAPersonIsRetried).
	runner.cancelByHand(status.RunID, "not worth testing, I am reverting the base")
	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := ciStatusOf(t, ctx, store, task.ID); got == nil || got.Conclusion != "cancelled" {
		t.Fatalf("status = %+v, want the cancellation recorded as the verdict it is", got)
	}
	if runner.started() != 2 {
		t.Fatalf("grain CI was asked for %d runs, want none before the retry delay", runner.started())
	}
	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(time.Hour+5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if runner.started() != 3 {
		t.Fatalf("grain CI was asked for %d runs, want a replacement after the retry delay", runner.started())
	}
}

// A cancellation is a decision somebody made, and the note they left
// saying why is the only part of it written for whoever meets the run
// afterwards. The task's stored verdict and its own conversation are
// where a later run of that task reads it, so both carry the note rather
// than the bare word "cancelled" -- which, on its own, is what a
// supersede and a daemon restart also read as.
func TestGrainCIRecordsWhyARunWasCancelled(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()

	first := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	const why = "stopping this one by hand: the base branch is being reverted"
	runner.cancelByHand(first.RunID, why, ci.JobRun{
		Workflow: "checks", WorkflowPath: ".grain/workflows/checks.yml",
		JobID: "test", Name: "test", Conclusion: ci.ConclusionCancelled,
	})
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatalf("collecting the verdict: %v", err)
	}

	status := ciStatusOf(t, ctx, store, task.ID)
	if status == nil || status.Conclusion != string(ci.ConclusionCancelled) {
		t.Fatalf("recorded status = %+v, want the cancellation", status)
	}
	// The reason, rather than the jobs that were told to give up: the job
	// names say nothing a reader of this line did not already know from
	// the word "cancelled".
	if !strings.Contains(status.Detail, why) {
		t.Errorf("detail = %q, want the reason given for the cancellation", status.Detail)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 || !strings.Contains(bodies[0], why) {
		t.Fatalf("comments = %q, want one carrying the reason into the task's own thread", bodies)
	}
}

// What grain CI's verdict holds, which grain/task-246 left open and
// grain/task-302 decided: a red run holds the merge, on the same terms a
// red GitHub check does. The tests from here to the end of this file are
// that decision, one case each -- and every one of them is a question
// healthFrom answers for GitHub with some care, answered here for grain
// CI. withGrainCI is where the reasoning lives.

// greenOnGitHub is the other half of every case below: GitHub's own
// checks pass and the pull request is mergeable, so the only thing left
// that can hold the merge is grain CI's verdict.
func greenOnGitHub(sim *githubsim.Sim, branch string) {
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "tests", Status: "completed", Conclusion: strPtr("success")},
	}
}

// redGrainCI puts a concluded, failing grain CI verdict on a task's
// record the way a run of the reconciler would -- through the reconciler
// itself, so that what is judged below is what grain CI actually writes
// rather than a shape a test invented.
func redGrainCI(t *testing.T, ctx context.Context, store *model.Store,
	client github.Client, task model.Task) *model.CIStatus {

	t.Helper()
	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, *task.Target, model.BranchName(task.ID))
	runner.finish(status.RunID, ci.ConclusionFailure, ci.JobRun{
		Workflow: "checks", JobID: "test", Name: "test", Conclusion: ci.ConclusionFailure,
	})
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	status = ciStatusOf(t, ctx, store, task.ID)
	if status.Green() || !status.Done() {
		t.Fatalf("status = %+v, want a concluded red verdict", status)
	}
	return status
}

// recordCIStatus writes a verdict onto a task directly, for the cases
// that are about a verdict nothing would produce from here: one about a
// commit the branch has moved past, one this deployment's own engine
// never wrote.
func recordCIStatus(t *testing.T, ctx context.Context, store *model.Store, taskID string, status *model.CIStatus) {
	t.Helper()
	if err := store.ObserveField(ctx, taskID, baseTime, func(o *model.Observation) { o.CI = status }); err != nil {
		t.Fatal(err)
	}
}

// The decision itself. A red grain CI run keeps its pull request out of
// the merge queue, and the head gets the one automatic repair any other
// broken head gets -- told, in the comment that is the whole of a
// repair's brief, that grain CI is the thing that is red and which of its
// jobs said so.
func TestARedGrainCIRunHoldsTheMergeQueue(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	redGrainCI(t, ctx, store, client, task)
	greenOnGitHub(sim, branch)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if mergedBranch(sim, branch) {
		t.Fatal("a pull request whose grain CI run failed was merged on GitHub's checks alone")
	}
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a red grain CI run held the merge but earned no repair: " +
			"the head is now stuck with nobody sent to fix it")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	repair := bodies[len(bodies)-1]
	for _, want := range []string{"grain CI", "checks/test", branch} {
		if !strings.Contains(repair, want) {
			t.Errorf("the repair comment does not name %q:\n%s", want, repair)
		}
	}
}

// A run that has not finished holds the merge too, and holds it as
// pending rather than as failing: nothing is broken, so nothing is
// repaired and nobody is told anything. The wait is bounded by the same
// clock GitHub's own unfinished checks are waited on with
// (defaultCheckStallDeadline), which is what makes waiting safe.
func TestAGrainCIRunStillGoingHoldsTheMergeQueue(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()
	startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	if status := ciStatusOf(t, ctx, store, task.ID); status.Done() {
		t.Fatal("this test needs a run that has not concluded")
	}
	greenOnGitHub(sim, branch)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if mergedBranch(sim, branch) {
		t.Fatal("the pull request merged while grain CI was still running against its head")
	}
	if repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a run that is still going was read as a failure to repair")
	}
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 0 {
		t.Fatalf("the queue said something about a run that has not finished: %q", bodies)
	}
}

// A run this deployment could not do at all. It holds the merge -- the
// tree is exactly as untested as it is under a run still going -- but it
// holds it as pending: no repair is filed against a branch that has done
// nothing wrong, and the comment says whose problem it is.
//
// This is grain/task-361 end to end. Every job of this deployment's first
// kontur-backed runs died building its sandbox, and each one was reported
// as a failure: red pull requests, and a repair dispatched to fix code
// that had never been compiled.
func TestAGrainCIRunThisDeploymentCouldNotDoHoldsTheMergeWithoutBlamingTheBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)

	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(status.RunID, ci.ConclusionErrored, ci.JobRun{
		Workflow: "checks", JobID: "test", Name: "test", Conclusion: ci.ConclusionErrored,
		Error: "building a sandbox for this job: granule VM name is 50 bytes, over the 11-byte limit",
	})
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	greenOnGitHub(sim, branch)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if mergedBranch(sim, branch) {
		t.Fatal("a pull request grain CI never managed to test was merged as though it had been")
	}
	if repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a branch grain CI could not run was sent for repair: there is nothing wrong with it, " +
			"and the agent would be dispatched to fix this deployment's own sandboxes")
	}
	status = ciStatusOf(t, ctx, store, task.ID)
	if status.Green() || !status.Done() {
		t.Fatalf("status = %+v, want a concluded verdict", status)
	}
	if !strings.Contains(status.Detail, "could not run") || !strings.Contains(status.Detail, "11-byte limit") {
		t.Errorf("detail = %q, want it to say grain CI could not run and why", status.Detail)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) == 0 {
		t.Fatal("nothing was said about a run this deployment could not do")
	}
	verdict := bodies[len(bodies)-1]
	if !strings.HasPrefix(verdict, "grain CI could not run at `") ||
		strings.Contains(verdict, "checks/test") || strings.Contains(verdict, "11-byte limit") {
		t.Errorf("verdict comment = %q, want a terse infrastructure verdict", verdict)
	}
}

// Errored runs are retried after a quiet interval. Repeating the same
// infrastructure verdict adds no comments, while a later recovery is recorded
// and announced on the task (a passing run remains quiet on the pull request).
func TestAnErroredGrainCIRunRetriesQuietlyAndCanRecover(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	errorJob := ci.JobRun{Workflow: "checks", JobID: "test", Name: "test", Conclusion: ci.ConclusionErrored, Error: "no sandbox capacity"}
	runner.finish(status.RunID, ci.ConclusionErrored, errorJob)
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}

	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := runner.started(); got != 1 {
		t.Fatalf("runs before retry delay = %d, want 1", got)
	}
	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	status = ciStatusOf(t, ctx, store, task.ID)
	if status.Attempts != 2 || status.Done() {
		t.Fatalf("retry status = %+v, want pending attempt 2", status)
	}
	runner.finish(status.RunID, ci.ConclusionErrored, errorJob)
	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := len(commentBodies(t, ctx, store, task.ID)); got != 1 {
		t.Fatalf("task comments after identical retry = %d, want 1", got)
	}

	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	status = ciStatusOf(t, ctx, store, task.ID)
	runner.finish(status.RunID, ci.ConclusionSuccess)
	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	status = ciStatusOf(t, ctx, store, task.ID)
	if !status.Green() || status.Attempts != 3 {
		t.Fatalf("recovered status = %+v, want green attempt 3", status)
	}
	if got := len(commentBodies(t, ctx, store, task.ID)); got != 2 {
		t.Fatalf("task comments after recovery = %d, want initial error and recovery", got)
	}
	prComments, err := client.ListComments(repo.Owner, repo.Name, sim.PullRequests[0].Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(prComments) != 1 {
		t.Fatalf("pull request comments = %d, want only the initial error", len(prComments))
	}
}

// A deployment that stays broken gets three retries, not one run per
// reconciliation cycle forever. The merge queue's separate stall sighting is
// left untouched and can still hand the pending pull request to a person.
func TestAnErroredGrainCIRunStopsAfterFourAttempts(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	errorJob := ci.JobRun{Workflow: "checks", JobID: "test", Name: "test", Conclusion: ci.ConclusionErrored, Error: "backend unavailable"}
	for attempt := 1; attempt <= 4; attempt++ {
		runner.finish(status.RunID, ci.ConclusionErrored, errorJob)
		now := baseTime.Add(time.Duration(attempt-1) * 5 * time.Minute)
		if err := runCIReconcilerAt(t, ctx, store, client, runner, now); err != nil {
			t.Fatal(err)
		}
		if attempt < 4 {
			if err := runCIReconcilerAt(t, ctx, store, client, runner, now.Add(5*time.Minute)); err != nil {
				t.Fatal(err)
			}
			status = ciStatusOf(t, ctx, store, task.ID)
		}
	}
	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := runner.started(); got != 4 {
		t.Fatalf("runs = %d, want four bounded attempts", got)
	}
}

// grain/task-480: a commit spent its attempts erroring on a capability the
// repository did not grant. Granting it in Settings is the change that
// makes another run worth starting, so the commit is run again past its
// attempts -- once, since the replacement no longer errors on it -- and a
// capability that is still missing changes nothing.
func TestASpentErroredGrainCIRunIsRetriedOnceTheMissingCapabilityIsGranted(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	errorJob := ci.JobRun{Workflow: "checks", JobID: "smoke", Name: "smoke", Conclusion: ci.ConclusionErrored,
		Error: `this job requires the capability "gcp-key" and this deployment does not provide it for acme/widgets`}
	now := baseTime
	for attempt := 1; attempt <= 4; attempt++ {
		runner.finish(status.RunID, ci.ConclusionErrored, errorJob)
		if err := runCIReconcilerAt(t, ctx, store, client, runner, now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(5 * time.Minute)
		if err := runCIReconcilerAt(t, ctx, store, client, runner, now); err != nil {
			t.Fatal(err)
		}
		status = ciStatusOf(t, ctx, store, task.ID)
	}
	if got := runner.started(); got != 4 {
		t.Fatalf("runs = %d, want the four bounded attempts", got)
	}

	// Some other capability granted: still not what the run lacked.
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CICapabilities: []string{"gemini-key"}}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if err := runCIReconcilerAt(t, ctx, store, client, runner, now); err != nil {
		t.Fatal(err)
	}
	if got := runner.started(); got != 4 {
		t.Fatalf("runs with gcp-key still missing = %d, want 4", got)
	}

	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CICapabilities: []string{"gemini-key", "gcp-key"}}); err != nil {
		t.Fatal(err)
	}
	if err := runCIReconcilerAt(t, ctx, store, client, runner, now); err != nil {
		t.Fatal(err)
	}
	if got := runner.started(); got != 5 {
		t.Fatalf("runs once gcp-key is granted = %d, want a fifth", got)
	}
	status = ciStatusOf(t, ctx, store, task.ID)
	if status == nil || status.Done() || status.Attempts != 5 {
		t.Fatalf("status = %+v, want a pending attempt 5", status)
	}

	// The replacement errors for some other reason: spent again, and no
	// sixth run.
	runner.finish(status.RunID, ci.ConclusionErrored, ci.JobRun{Workflow: "checks", JobID: "smoke",
		Name: "smoke", Conclusion: ci.ConclusionErrored, Error: "backend unavailable"})
	for i := 0; i < 3; i++ {
		now = now.Add(10 * time.Minute)
		if err := runCIReconcilerAt(t, ctx, store, client, runner, now); err != nil {
			t.Fatal(err)
		}
	}
	if got := runner.started(); got != 5 {
		t.Fatalf("runs after the replacement errored otherwise = %d, want 5", got)
	}
}

// The dead end this was written for. A cancelled run is Done() and is not
// errored, so it used to fall through to "the verdict is already
// recorded" while the merge queue held the pull request pending on a
// verdict that could never arrive -- grain/task-149 for days, and
// grain/task-264 again after grain/task-170 had made the row say so in
// words. A cancellation is grain's own doing rather than the branch's, so
// it earns the same replacement run an errored verdict does, on the same
// delay.
func TestACancelledGrainCIRunIsRetried(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)

	// A job that ran past ci.DefaultJobTimeout: cancelled, with nobody's
	// name on it and nothing written about why, which is exactly what the
	// two jobs grain/task-264 was stuck on looked like.
	runner.cancelBySystem(status.RunID, "", ci.JobRun{
		Workflow: "CI", JobID: "image", Name: "image", Conclusion: ci.ConclusionCancelled,
	})
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	if got := ciStatusOf(t, ctx, store, task.ID); got == nil || got.Conclusion != string(ci.ConclusionCancelled) {
		t.Fatalf("status = %+v, want the cancellation recorded", got)
	}

	// Not immediately, on the same five minutes an errored verdict waits:
	// a deployment restarting under a run is not answered with a run per
	// cycle.
	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := runner.started(); got != 1 {
		t.Fatalf("runs before the retry delay = %d, want 1", got)
	}

	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := runner.started(); got != 2 {
		t.Fatalf("runs after the retry delay = %d, want the replacement", got)
	}
	status = ciStatusOf(t, ctx, store, task.ID)
	if status == nil || status.Attempts != 2 || status.Done() {
		t.Fatalf("retry status = %+v, want a pending attempt 2", status)
	}
	if status.CancelledBy != "" {
		t.Errorf("CancelledBy = %q, want it cleared: this is a new run nobody has cancelled", status.CancelledBy)
	}

	// And the replacement is a verdict like any other, so the wait ends
	// by itself -- which is the whole point.
	runner.finish(status.RunID, ci.ConclusionSuccess)
	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := ciStatusOf(t, ctx, store, task.ID); !got.Green() {
		t.Fatalf("status = %+v, want the replacement's green verdict", got)
	}
}

// A commit whose runs keep being cancelled -- a job that cannot fit
// inside the timeout at all -- is not re-run forever. It stops at the
// same four attempts an errored verdict stops at and becomes a state a
// person owns, which the merge queue's own stall deadline hands over.
func TestACancelledGrainCIRunStopsAfterFourAttempts(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	for attempt := 1; attempt <= 4; attempt++ {
		runner.cancelBySystem(status.RunID, "")
		now := baseTime.Add(time.Duration(attempt-1) * 5 * time.Minute)
		if err := runCIReconcilerAt(t, ctx, store, client, runner, now); err != nil {
			t.Fatal(err)
		}
		if attempt < 4 {
			if err := runCIReconcilerAt(t, ctx, store, client, runner, now.Add(5*time.Minute)); err != nil {
				t.Fatal(err)
			}
			status = ciStatusOf(t, ctx, store, task.ID)
		}
	}
	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := runner.started(); got != 4 {
		t.Fatalf("runs = %d, want four bounded attempts", got)
	}
	// And the row says which of the three cancelled states this is, since
	// it is the one that ends with a person rather than with a run.
	verdict := grainCIVerdictFor(t, ctx, store, client, task, repo, branch)
	if verdict.Replacement != forge.GrainCIReplacementSpent {
		t.Fatalf("replacement = %q, want the attempts spent", verdict.Replacement)
	}
}

// A person pressing cancel is stopping that run, not the task: a run
// cancelled because it hung, or because another run was overlapping it
// ("new run running"), is replaced like any other cancellation -- after
// ciRetryDelay, up to ciMaximumAttempts -- rather than leaving the task
// in waiting_ci with nothing that will ever deliver a verdict (task 285).
// The hand on it is still recorded (ci.CancelSource).
func TestAGrainCIRunCancelledByAPersonIsRetried(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)

	at := baseTime
	for attempt := 1; attempt <= orchestrator.CIMaximumAttempts; attempt++ {
		runner.cancelByHand(status.RunID, "new run running")
		if err := runCIReconcilerAt(t, ctx, store, client, runner, at); err != nil {
			t.Fatal(err)
		}
		got := ciStatusOf(t, ctx, store, task.ID)
		if got == nil || got.CancelledBy != string(ci.CancelByOperator) {
			t.Fatalf("attempt %d: status = %+v, want the hand on the cancellation recorded with it", attempt, got)
		}
		want := forge.GrainCIReplacementDue
		if attempt == orchestrator.CIMaximumAttempts {
			want = forge.GrainCIReplacementSpent
		}
		if verdict := grainCIVerdictFor(t, ctx, store, client, task, repo, branch); verdict.Replacement != want {
			t.Fatalf("attempt %d: replacement = %q, want %q", attempt, verdict.Replacement, want)
		}
		if attempt == orchestrator.CIMaximumAttempts {
			break
		}

		// Not in the same second: the same delay any cancellation waits.
		if err := runCIReconcilerAt(t, ctx, store, client, runner, at.Add(4*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if started := runner.started(); started != attempt {
			t.Fatalf("attempt %d: runs before the retry delay = %d, want %d", attempt, started, attempt)
		}
		at = at.Add(5 * time.Minute)
		if err := runCIReconcilerAt(t, ctx, store, client, runner, at); err != nil {
			t.Fatal(err)
		}
		if started := runner.started(); started != attempt+1 {
			t.Fatalf("attempt %d: runs after the retry delay = %d, want the replacement", attempt, started)
		}
		status = ciStatusOf(t, ctx, store, task.ID)
		if status == nil || status.Done() || status.Attempts != attempt+1 || status.CancelledBy != "" {
			t.Fatalf("attempt %d: status = %+v, want a pending replacement nobody has cancelled", attempt, status)
		}
	}

	// Spent: no further run however long it waits.
	if err := runCIReconcilerAt(t, ctx, store, client, runner, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if started := runner.started(); started != orchestrator.CIMaximumAttempts {
		t.Fatalf("runs = %d, want no more than %d", started, orchestrator.CIMaximumAttempts)
	}
}

// The head moving is still the head moving. A cancelled verdict about a
// commit the branch has left takes the path it always took -- a fresh run
// for the new commit -- rather than being counted as one of that commit's
// retries, which is what would let a branch that is pushed to often
// exhaust a budget it never spent.
func TestACancelledGrainCIVerdictAboutAnOlderCommitStartsAFreshRun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	runner := newFakeCI()
	recordCIStatus(t, ctx, store, task.ID, &model.CIStatus{
		RunID:       "run-on-an-older-commit",
		Commit:      "0000000000000000000000000000000000000000",
		Conclusion:  string(ci.ConclusionCancelled),
		Detail:      "cancelled: CI/image",
		Attempts:    orchestrator.CIMaximumAttempts,
		CancelledBy: string(ci.CancelByOperator),
	})
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	status := ciStatusOf(t, ctx, store, task.ID)
	if status == nil || status.Commit != headSHA(t, client, repo, branch) {
		t.Fatalf("status = %+v, want a run against the branch's own head", status)
	}
	if status.Attempts != 1 {
		t.Errorf("attempts = %d, want a new commit to start its own budget at 1", status.Attempts)
	}
	if status.CancelledBy != "" {
		t.Errorf("CancelledBy = %q, want nothing: nobody has cancelled this run", status.CancelledBy)
	}
}

// grainCIVerdictFor is what the merge queue reads about this task's
// branch -- the same value its rows and its give-up comment are written
// from (orchestrator.GrainCIVerdictFor).
func grainCIVerdictFor(t *testing.T, ctx context.Context, store *model.Store,
	client github.Client, task model.Task, repo model.RepoRef, branch string) forge.GrainCI {

	t.Helper()
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.GetTask(ctx, task.ID)
	if err != nil || current == nil {
		t.Fatalf("reading task %s: %v", task.ID, err)
	}
	return orchestrator.GrainCIVerdictFor(*current, obs, headSHA(t, client, repo, branch))
}

// The repository that declares no workflows. ci.PlanRun makes an empty
// plan a success on purpose, so "nothing to run" and "everything passed"
// arrive here as the same word -- and merging on it is the whole reason
// adding `.grain/workflows` to one repository is not a change to every
// other one.
func TestAGrainCIRunWithNothingToRunMerges(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)

	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	// A run with no jobs at all: the empty plan, concluded success.
	runner.finish(status.RunID, ci.ConclusionSuccess)
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	greenOnGitHub(sim, branch)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatal("a repository whose workflows answer nothing had its merge held")
	}
}

// Enabling grain CI selects it as the repository's CI provider. GitHub
// checks may still be present while a repository is migrating, or may all
// report startup failures while Actions is unavailable; neither is a second
// gate after the exact-head grain CI suite passes.
func TestAGreenGrainCIRunMergesDespiteRedGitHubChecks(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	recordCIStatus(t, ctx, store, task.ID, &model.CIStatus{
		RunID:      "run1",
		Commit:     headSHA(t, client, repo, branch),
		Conclusion: string(ci.ConclusionSuccess),
		Detail:     "success: 4 job(s)",
	})
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{{
		Name: "GitHub Actions", Status: "completed", Conclusion: strPtr("failure"),
	}}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatal("red GitHub checks held a pull request whose required grain CI suite passed")
	}
}

// A grant is a required-success gate: without a full-suite verdict for the
// exact head, the task stays pending rather than merging untested.
func TestATaskWithNoGrainCIVerdictStaysPending(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	greenOnGitHub(sim, branch)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("a task merged before grain recorded a passing required full suite")
	}
}

// A verdict about a commit the branch has moved past decides nothing. It
// is not a stale answer to this question so much as an answer to another
// one, so the reconciler must arrange a full-suite run for this commit.
func TestAGrainCIVerdictAboutAnotherCommitStaysPending(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	recordCIStatus(t, ctx, store, task.ID, &model.CIStatus{
		RunID:      "run1",
		Commit:     "0000000000000000000000000000000000000000",
		Conclusion: string(ci.ConclusionFailure),
		Detail:     "failure: checks/test",
	})
	greenOnGitHub(sim, branch)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("a stale grain CI verdict allowed the new commit to merge untested")
	}
}

// A cancelled run is not a failing tree, but it also is not the required
// successful full-suite verdict, so the task remains pending.
func TestACancelledGrainCIRunStaysPending(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	recordCIStatus(t, ctx, store, task.ID, &model.CIStatus{
		RunID:      "run1",
		Commit:     headSHA(t, client, repo, branch),
		Conclusion: string(ci.ConclusionCancelled),
		Detail:     "cancelled: checks/test",
	})
	greenOnGitHub(sim, branch)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("a cancelled grain CI run satisfied the required successful verdict")
	}
}

// The capability is the switch, here as everywhere else: a task that does
// not hold grain-ci is not a task whose branch grain CI is running for,
// so a verdict left on its record -- by a grant since withdrawn, or by a
// repository that stopped being checked this way -- holds nothing.
func TestARedGrainCIRunOnATaskWithoutTheCapabilityHoldsNothing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	recordCIStatus(t, ctx, store, task.ID, &model.CIStatus{
		RunID:      "run1",
		Commit:     headSHA(t, client, repo, branch),
		Conclusion: string(ci.ConclusionFailure),
		Detail:     "failure: checks/test",
	})
	greenOnGitHub(sim, branch)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatal("a task that does not hold the grain-ci capability was held by a grain CI verdict")
	}
}

func TestGrainCICancelsInFlightRunsWhenAnUnmergedTaskIsClosed(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// Task with an open pull request link.
	withPR, branchWithPR := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, withPR)
	runner := newFakeCI()
	prStatus := startRequiredCI(t, ctx, store, client, runner, withPR, repo, branchWithPR)

	// Task without a pull request link (e.g. closed while in wait_for_checks before opening a PR).
	withoutPR := model.Task{
		ID:       "t2",
		Intent:   model.IntentImplement,
		Title:    "without PR",
		Target:   &repo,
		Base:     "main",
		Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		Grants:   []model.Grant{{Capability: grainci.CapabilityName}},
	}
	if err := store.PutTask(ctx, withoutPR); err != nil {
		t.Fatal(err)
	}
	noPRRun, err := runner.Start(ctx, ci.Request{
		Repo: repo.String(), Ref: withoutPR.BranchName(), Commit: "deadbeef1234",
		Event: ci.EventPush, RequestedBy: withoutPR.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := baseTime
	recordCIStatus(t, ctx, store, withoutPR.ID, &model.CIStatus{
		RunID: noPRRun.ID, Commit: noPRRun.Commit, StartedAt: &started,
	})

	// Close both tasks without merging.
	closedAt := baseTime.Add(time.Minute)
	for _, id := range []string{withPR.ID, withoutPR.ID} {
		if err := store.ObserveField(ctx, id, closedAt, func(o *model.Observation) {
			o.ClosedAt = &closedAt
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := runCIReconcilerAt(t, ctx, store, client, runner, closedAt); err != nil {
		t.Fatalf("runCIReconciler: %v", err)
	}

	for _, tc := range []struct {
		taskID string
		runID  string
	}{
		{withPR.ID, prStatus.RunID},
		{withoutPR.ID, noPRRun.ID},
	} {
		if reason := runner.reasons[tc.runID]; reason != "task "+tc.taskID+" was closed" {
			t.Errorf("task %s run %s cancel reason = %q, want %q", tc.taskID, tc.runID, reason, "task "+tc.taskID+" was closed")
		}
		status := ciStatusOf(t, ctx, store, tc.taskID)
		if status == nil || status.Conclusion != string(ci.ConclusionCancelled) {
			t.Fatalf("task %s status = %+v, want cancelled", tc.taskID, status)
		}
	}
}

func TestGrainCIRetargetsStackedTaskBaseWhenUpstreamBranchMergesAndIsDeleted(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	pushBranch(t, sim.BareRepo, "candidate")

	upstream := model.Task{
		ID:       "732",
		Branch:   "grain/049f1e09/task-732",
		Intent:   model.IntentImplement,
		Title:    "upstream feature",
		Target:   &repo,
		Base:     "candidate",
		Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
	}
	if err := store.PutTask(ctx, upstream); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, upstream.ID, baseTime, func(o *model.Observation) {
		o.PrMergedAt = &baseTime
		o.ClosedAt = &baseTime
	}); err != nil {
		t.Fatal(err)
	}

	stackedBranch := "grain/049f1e09/task-755"
	pushBranch(t, sim.BareRepo, stackedBranch)
	sim.PullRequests = append(sim.PullRequests, githubsim.PullRequest{
		Number: 4706,
		Title:  "stacked review",
		Head:   stackedBranch,
		Base:   "candidate",
		State:  "open",
	})
	stacked := model.Task{
		ID:       "755",
		Branch:   stackedBranch,
		Intent:   model.IntentImplement,
		Title:    "stacked review",
		Target:   &repo,
		Base:     upstream.Branch,
		Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		Grants:   []model.Grant{{Capability: grainci.CapabilityName}},
		Links:    []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#4706"}},
	}
	if err := store.PutTask(ctx, stacked); err != nil {
		t.Fatal(err)
	}
	head := headSHA(t, client, repo, stackedBranch)
	// Record an errored run from when wait_for_checks ran against the deleted upstream branch.
	finished := baseTime
	recordCIStatus(t, ctx, store, stacked.ID, &model.CIStatus{
		RunID:      "run-errored",
		Commit:     head,
		Conclusion: string(ci.ConclusionErrored),
		Detail:     "checking base branch workflow gate: Remote branch grain/049f1e09/task-732 not found in upstream origin",
		FinishedAt: &finished,
		Attempts:   1,
	})
	if err := store.ObserveField(ctx, stacked.ID, baseTime, func(o *model.Observation) {
		o.CompletedAt = &baseTime
	}); err != nil {
		t.Fatal(err)
	}

	runner := newFakeCI()
	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(10*time.Second)); err != nil {
		t.Fatalf("runCIReconciler: %v", err)
	}

	updated, err := store.GetTask(ctx, stacked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Base != "candidate" {
		t.Fatalf("stacked task Base = %q, want candidate", updated.Base)
	}
	req := runner.lastRequest(t)
	if !req.GateOnBase || req.BaseRef != "candidate" {
		t.Fatalf("CI request = %+v, want GateOnBase=true and BaseRef=candidate", req)
	}
}

// A review merges back into the branch it is stacked on, so it is never
// classified on its own account. Retargeted onto that branch's own base
// after the reviewed change merged, it must stop merging itself, or its
// fixes would land on candidate unclassified and unattended.
func TestGrainCIRetargetedReviewLosesAutoMerge(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	pushBranch(t, sim.BareRepo, "candidate")

	upstream := model.Task{
		ID:       "732",
		Branch:   "grain/049f1e09/task-732",
		Intent:   model.IntentImplement,
		Title:    "upstream feature",
		Target:   &repo,
		Base:     "candidate",
		Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
	}
	if err := store.PutTask(ctx, upstream); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, upstream.ID, baseTime, func(o *model.Observation) {
		o.PrMergedAt = &baseTime
		o.ClosedAt = &baseTime
	}); err != nil {
		t.Fatal(err)
	}

	reviewBranch := "grain/049f1e09/task-755"
	pushBranch(t, sim.BareRepo, reviewBranch)
	sim.PullRequests = append(sim.PullRequests, githubsim.PullRequest{
		Number: 4706,
		Title:  "security review fixes",
		Head:   reviewBranch,
		Base:   "candidate",
		State:  "open",
	})
	review := model.Task{
		ID:        "755",
		Branch:    reviewBranch,
		Intent:    model.IntentImplement,
		Title:     "security review",
		Target:    &repo,
		Base:      upstream.Branch,
		AutoMerge: true,
		Origin:    model.Origin{Reason: model.ReasonReview},
		Approval:  &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		Grants:    []model.Grant{{Capability: grainci.CapabilityName}},
		Links:     []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#4706"}},
	}
	if err := store.PutTask(ctx, review); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, review.ID, baseTime, func(o *model.Observation) {
		o.CompletedAt = &baseTime
	}); err != nil {
		t.Fatal(err)
	}

	runner := newFakeCI()
	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(10*time.Second)); err != nil {
		t.Fatalf("runCIReconciler: %v", err)
	}

	updated, err := store.GetTask(ctx, review.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Base != "candidate" {
		t.Fatalf("review Base = %q, want candidate", updated.Base)
	}
	if updated.AutoMerge {
		t.Fatal("review retargeted onto candidate kept AutoMerge; want it cleared so a person submits it")
	}
}

func TestSwitchingRepoToGitHubCIModeCancelsInFlightRunsAndStopsStartingNewOnes(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:   repo,
		CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}

	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Grants = []model.Grant{{Capability: grainci.CapabilityName, Via: model.GrantByDefault}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)

	// Also start an unrecorded/ad-hoc run on the same repo to verify reconcileCI sweeps it.
	extraRun, err := runner.Start(ctx, ci.Request{
		Repo: repo.String(), Ref: branch, Commit: status.Commit,
		Event: ci.EventPush, RequestedBy: task.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	startedBefore := runner.started()

	// Switch the repository from CIModeGrain to CIModeGitHub.
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:   repo,
		CIMode: model.CIModeGitHub,
	}); err != nil {
		t.Fatal(err)
	}

	at := baseTime.Add(time.Minute)
	if err := runCIReconcilerAt(t, ctx, store, client, runner, at); err != nil {
		t.Fatalf("runCIReconciler: %v", err)
	}

	for _, runID := range []string{status.RunID, extraRun.ID} {
		if got := runner.reasons[runID]; got != "repository switched to GitHub CI" {
			t.Errorf("run %s cancel reason = %q, want %q", runID, got, "repository switched to GitHub CI")
		}
	}
	gotStatus := ciStatusOf(t, ctx, store, task.ID)
	if gotStatus == nil || gotStatus.Conclusion != string(ci.ConclusionCancelled) {
		t.Fatalf("status after switching to GitHub CI = %+v, want cancelled", gotStatus)
	}

	// Advancing past ciRetryDelay must NOT start a replacement Grain CI run.
	if err := runCIReconcilerAt(t, ctx, store, client, runner, at.Add(10*time.Minute)); err != nil {
		t.Fatalf("runCIReconciler after retry delay: %v", err)
	}
	if got := runner.started(); got != startedBefore {
		t.Fatalf("runs started after switching to GitHub CI = %d, want %d (no new runs)", got, startedBefore)
	}

	// With green GitHub checks, the PR merges without waiting on the cancelled Grain CI run.
	greenOnGitHub(sim, branch)
	if err := orchestrator.SyncPullRequests(ctx, store, client, at.Add(11*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatal("PR on a CIModeGitHub repo with green GitHub checks was held by a cancelled Grain CI run")
	}
}
