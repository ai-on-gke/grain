// This file walks docs/test-plans/2026-10-11.md §6 end to end: the shared
// fault circuit breaker (3ea13cc05) -- on main, now only that a red
// branch is confirmed and holds no work back (coordinating it is
// system:pipeline-steward's) --
// base-transition invalidation of cached CI failures (same commit), and
// the merge-queue contention telemetry and tuning allowlist (faf82f23e).
//
// What is real: orchestrator.RunCycle over a real SQLite store -- the
// base-branch CI reconciler, dispatch with its holds, the CI reconciler,
// the merge queue and its batches, and the finish path that relays an
// agent's propose_task calls -- the CI service the daemon builds
// (daemon.BuildCIService, with its job cache under a data directory), and
// the API (ui.NewServer) the task pane and the metrics page read.
//
// What is stood in for, and only at the edges (each is listed in
// docs/test-runs/2026-10-12-circuit-breaker-cache-telemetry.md):
//
//   - GitHub: githubsim behind the e2e git host, as every walkthrough here;
//   - the agents: scripted antigravity runs (holdWorld);
//   - a CI job's machine: localCISandbox runs each step with sh on this
//     host, and the one job here reads the base branch straight out of the
//     bare repository, which is what "fails when a marker file exists on
//     the base" needs and all it needs -- and, where a test arms it, fails
//     the first merge batch it is asked about;
//   - where workflows come from: bareRepoCISource;
//   - the forge's clock: githubsim stamps pull requests with the cycle's
//     own moment, and a GitHub check is seeded where a test needs one.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// baseMarkerWorkflow is the repository's one job: red while `marker`
// exists on main, green otherwise, whatever branch or merge batch it is
// run for -- so a
// task branch's verdict depends on its base and not on its own tree, which
// is the case a cached failure must not outlive. And red, once, for the
// first merge batch it is asked about where a test has armed that
// (baseMarkerSandboxes.failFirstBatch).
const baseMarkerWorkflow = `name: checks
on: [push, pull_request]
jobs:
  base-health:
    steps:
      - name: check the base
        run: |
          if git --git-dir "$E2E_BARE" cat-file -e refs/heads/main:marker 2>/dev/null; then
            echo "marker is on main: failing"
            exit 1
          fi
          if [ -n "$E2E_RED" ] && [ -e "$E2E_RED" ]; then
            echo "armed red"
            exit 1
          fi
          if [ -n "$E2E_FAIL_ONCE" ] && mv "$E2E_FAIL_ONCE" "$E2E_FAIL_ONCE.spent" 2>/dev/null; then
            echo "the first merge batch fails"
            exit 1
          fi
          echo "main is clean"
`

// baseMarkerSandboxes is localCISandboxes with the repository's bare
// directory handed to every step.
type baseMarkerSandboxes struct {
	dir, bare string
	// failOnce, when set, is a file whose presence fails the next merge
	// batch's run: the step that sees it moves it aside and goes red.
	failOnce string
	// failBranch is the base branch whose merge batches failOnce arms;
	// empty is main.
	failBranch string
	// red, when set, is a file whose presence fails every run while it
	// is there, whatever its ref or base.
	red string

	mu  sync.Mutex
	got []ci.SandboxRequest
}

// failFirstBatch makes the next merge batch run red, and only that one.
func (s *baseMarkerSandboxes) failFirstBatch(t *testing.T) {
	t.Helper()
	flag := filepath.Join(s.dir, "fail-first-batch")
	mustWriteFile(t, flag, "armed\n")
	s.mu.Lock()
	s.failOnce = flag
	s.mu.Unlock()
}

func (s *baseMarkerSandboxes) Acquire(_ context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	s.mu.Lock()
	s.got = append(s.got, req)
	n := len(s.got)
	env := []string{"E2E_BARE=" + s.bare, "E2E_COMMIT=" + req.Commit}
	if s.red != "" {
		env = append(env, "E2E_RED="+s.red)
	}
	failBranch := s.failBranch
	if failBranch == "" {
		failBranch = "main"
	}
	if s.failOnce != "" && ((failBranch == "candidate" && strings.HasPrefix(req.Ref, "grain/task-t-b")) ||
		(failBranch != "candidate" && strings.HasPrefix(req.Ref, "grain-merge-queue/") && strings.HasSuffix(req.Ref, "/"+failBranch))) {
		env = append(env, "E2E_FAIL_ONCE="+s.failOnce)
	}
	s.mu.Unlock()
	work, err := os.MkdirTemp(s.dir, "job-*")
	if err != nil {
		return nil, err
	}
	return &localCISandbox{name: fmt.Sprintf("local-%d", n), work: work, env: env}, nil
}

// acquiredFor counts the job sandboxes built for ref -- a cache hit builds
// none.
func (s *baseMarkerSandboxes) acquiredFor(commit string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.got {
		if r.Commit == commit {
			n++
		}
	}
	return n
}

type breakerWorld struct {
	*triggerWorld
	service   *ci.Service
	sandboxes *baseMarkerSandboxes
	api       string
	dataDir   string

	dispatchedMu sync.Mutex
	dispatched   map[string]int

	// simNow is the moment the forge stamps a pull request's opening and
	// merge with: the cycle's own, read and written under sim.mu.
	simNow time.Time
}

func newBreakerWorld(t *testing.T) *breakerWorld {
	t.Helper()
	w := &breakerWorld{triggerWorld: newTriggerWorld(t, nil), dispatched: map[string]int{}}
	// The cycle clock starts at the wall clock rather than baseTime: the CI
	// service stamps runs with the wall clock, and a merge batch's start
	// is its run's start while its end is the cycle's own moment, so two
	// clocks a month apart would make every gate latency negative.
	w.now = time.Now().UTC()
	// And the forge keeps the cycle's time too, so a merge is never
	// stamped before the cycle that took the change on.
	w.sim.mu.Lock()
	w.simNow = w.now
	w.sim.sim.Now = func() time.Time { return w.simNow }
	w.sim.mu.Unlock()
	if err := w.store.PutRepoConfig(w.ctx, model.RepoConfig{Repo: widgets, Declarations: true,
		CIMode: model.CIModeGrain, WatchDefaultBranch: true}); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	w.dataDir = dataDir
	tokens := gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "sandbox-tokens.json"))
	w.service = daemon.BuildCIService(w.ctx, daemon.Config{DataDir: dataDir},
		func() model.Limits { return model.Limits{} },
		func() ci.StoredRunRetention { return ci.StoredRunRetention{} },
		func() ci.StorageBounds { return ci.StorageBounds{} }, w.store,
		nil, nil, "", tokens, func() string { return "" }, nil,
		func() *model.CapabilityRegistry { return nil }, nil, nil, nil, nil, nil)
	w.sandboxes = &baseMarkerSandboxes{dir: t.TempDir(), bare: w.bare}
	w.service.Engine.Sandboxes = w.sandboxes
	w.service.Source = bareRepoCISource{bare: map[string]string{"acme/widgets": w.bare}}
	w.deps.CI = w.service
	w.api = newTestServer(t, ui.NewServer(withAccessCode(ui.Config{
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
		CI:           w.service,
	}), w.store))
	w.pushMain("grain CI", map[string]string{".grain/workflows/checks.yml": baseMarkerWorkflow})
	return w
}

// scriptChanges makes every dispatch of a task in ids push a one-file
// change, records every dispatch, and has any other task answer and stop.
func (w *breakerWorld) scriptChanges(ids ...string) {
	w.setScript(func(tk model.Task) []antigravity.Step {
		w.dispatchedMu.Lock()
		w.dispatched[tk.ID]++
		w.dispatchedMu.Unlock()
		if slices.Contains(ids, tk.ID) {
			return w.changeScript(tk, "mkdir -p changes && echo '"+tk.ID+"' > changes/"+tk.ID+".txt")
		}
		return []antigravity.Step{finalText("looked; nothing to push")}
	})
}

func (w *breakerWorld) dispatchCount(id string) int {
	w.dispatchedMu.Lock()
	defer w.dispatchedMu.Unlock()
	return w.dispatched[id]
}

// baseRow is grain's record of main.
func (w *breakerWorld) baseRow() *model.BaseBranchCI {
	w.t.Helper()
	row, err := w.store.GetBaseBranchCI(w.ctx, widgets, "main")
	if err != nil {
		w.t.Fatal(err)
	}
	return row
}

// tick runs one cycle and gives the CI service's runs a moment to finish,
// since they run beside the cycle rather than inside it.
func (w *breakerWorld) tick() {
	w.t.Helper()
	w.now = w.now.Add(time.Minute)
	w.sim.mu.Lock()
	w.simNow = w.now
	w.sim.mu.Unlock()
	if err := orchestrator.RunCycle(w.ctx, w.deps, w.now); err != nil {
		w.t.Logf("RunCycle: %v", err)
	}
	w.waitForIdleCI()
}

func (w *breakerWorld) waitForIdleCI() {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		busy := false
		for _, r := range w.service.Find(ci.RunFilter{Repo: "acme/widgets", Descending: true}, 50).Runs {
			if !r.Conclusion.Done() {
				busy = true
			}
		}
		if !busy {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (w *breakerWorld) ticks(n int, done func() bool) bool {
	w.t.Helper()
	for i := 0; i < n; i++ {
		w.markAllClean()
		w.tick()
		if done() {
			return true
		}
	}
	return false
}

// repairs is every task filed to repair main.
func (w *breakerWorld) repairs() []model.Task {
	w.t.Helper()
	all, err := w.store.ListTasks(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	tag := orchestrator.BaseCIRepairBranchTag(widgets, "main")
	var out []model.Task
	for _, tk := range all {
		if slices.Contains(tk.Tags, tag) {
			out = append(out, tk)
		}
	}
	return out
}

// get is one GET against the API, decoded into out.
func (w *breakerWorld) get(path string, out any) string {
	w.t.Helper()
	resp, err := accessGet(w.api + path)
	if err != nil {
		w.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		w.t.Fatalf("GET %s: %s: %s", path, resp.Status, body)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			w.t.Fatalf("GET %s: decoding %s: %v", path, body, err)
		}
	}
	return string(body)
}

// links is id's links as the store holds them.
func (w *breakerWorld) links(id string) []model.Link {
	w.t.Helper()
	task, err := w.store.GetTask(w.ctx, id)
	if err != nil || task == nil {
		w.t.Fatalf("GetTask %s: %v", id, err)
	}
	return task.Links
}

// dependsOn reports whether id carries a depends-on link to target, which
// is how a red base branch holds work on main: the task leaves the ready
// list until the branch's repair task closes.
func (w *breakerWorld) dependsOn(id, target string) bool {
	w.t.Helper()
	for _, l := range w.links(id) {
		if l.Kind == model.LinkDependsOn && l.Target == target {
			return true
		}
	}
	return false
}

// blockedBy is what the task pane shows a queued task waiting on: the
// open tasks its links name, as the API derives them.
func (w *breakerWorld) blockedBy(id string) []string {
	w.t.Helper()
	var detail struct {
		Blocked   bool     `json:"blocked"`
		BlockedBy []string `json:"blockedBy"`
	}
	w.get("/api/tasks/"+id, &detail)
	return detail.BlockedBy
}

// completedWithPullRequest files a task whose run has already pushed a
// branch and opened a pull request -- the shape the CI reconciler judges.
// It is pushed by hand rather than dispatched because dispatch is the
// thing a broken base holds.
func (w *breakerWorld) completedWithPullRequest(id string) model.Task {
	w.t.Helper()
	actor := human("operator")
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "victim " + id, Body: "please",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: actor},
		Target:   &widgets, Base: "main",
		Binding: model.BindingDirective,
		Grants:  []model.Grant{{Capability: "grain-ci"}},
		Links:   []model.Link{{Kind: model.LinkMergePolicy, Target: `{"NoFlakeRecheck":true}`}},
	}
	if err := w.store.PutTask(w.ctx, task); err != nil {
		w.t.Fatal(err)
	}
	dir := filepath.Join(w.t.TempDir(), "v")
	run(w.t, filepath.Dir(dir), "git", "clone", "-q", "-b", "main", w.remote, dir)
	run(w.t, dir, "git", "checkout", "-q", "-b", task.BranchName())
	mustWriteFile(w.t, filepath.Join(dir, "victim.txt"), id+"\n")
	run(w.t, dir, "git", "add", "-A")
	run(w.t, dir, "git", "-c", "user.email=v@example.com", "-c", "user.name=v", "commit", "-q", "-m", "victim change")
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
	return task
}

// cacheEntry is what the daemon's job cache holds under key, read off
// disk the way an operator inspecting it would: whether there is an entry,
// its conclusion, and the base commit it was recorded against.
func (w *breakerWorld) cacheEntry(key ci.Key) (found bool, conclusion ci.Conclusion, base string) {
	w.t.Helper()
	files, _ := filepath.Glob(filepath.Join(w.dataDir, "state", "ci-cache", "*.json"))
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var entry struct {
			Key        ci.Key `json:"key"`
			BaseCommit string `json:"baseCommit"`
			Result     struct {
				Conclusion ci.Conclusion `json:"conclusion"`
			} `json:"result"`
		}
		if json.Unmarshal(body, &entry) == nil && entry.Key == key {
			return true, entry.Result.Conclusion, entry.BaseCommit
		}
	}
	return false, "", ""
}

// jobOf is the one job of run.
func onlyJob(t *testing.T, run ci.Run) ci.JobRun {
	t.Helper()
	if len(run.Jobs) != 1 {
		t.Fatalf("run %s has %d jobs, want 1: %+v", run.ID, len(run.Jobs), run.Jobs)
	}
	return run.Jobs[0]
}

func TestARedBaseHoldsNoDispatchAndCacheForgetsTheOldBase(t *testing.T) {
	t.Parallel()
	w := newBreakerWorld(t)
	w.scriptChanges()

	// main is watched and judged green first.
	if !w.ticks(10, func() bool { r := w.baseRow(); return r != nil && r.Conclusion == string(ci.ConclusionSuccess) }) {
		t.Fatalf("main was never judged green: %+v", w.baseRow())
	}

	// Step 1: the marker lands on main.
	red := w.pushMain("add the marker", map[string]string{"marker": "broken\n"})
	if !w.ticks(20, func() bool { r := w.baseRow(); return r != nil && r.Commit == red && r.Confirmed() }) {
		t.Fatalf("main was never confirmed red: %+v", w.baseRow())
	}
	row := w.baseRow()
	if !row.Broken() || row.Commit != red {
		t.Fatalf("main's row after the marker: %+v, want broken at %s", row, red)
	}

	// Two pieces of ordinary work arrive while main is red.
	a := w.create("Queued A", false, nil)
	b := w.create("Queued B", false, nil)
	w.scriptChanges(a.ID, b.ID)
	// A task whose run already ended: its CI runs now, against a red base.
	victim := w.completedWithPullRequest("t-victim")
	victimHead := w.head(victim.BranchName())
	for i := 0; i < 4; i++ {
		w.tick()
	}

	// The daemon files no repair of a red main and holds nothing behind
	// one: coordinating a red base is system:pipeline-steward's job.
	if n := len(w.repairs()); n != 0 {
		t.Fatalf("%d repair tasks filed by the daemon for a red main, want none: %v", n, w.repairs())
	}
	for _, id := range []string{a.ID, b.ID} {
		if blockers := w.blockedBy(id); len(blockers) != 0 {
			t.Fatalf("the API shows %s blocked by %v while main is red, want nothing holding it", id, blockers)
		}
	}

	// The victim's own run failed against the red base, and that failure
	// is in the cache under the base it ran against.
	runs := w.service.Find(ci.RunFilter{Repo: "acme/widgets", Ref: victim.BranchName(), Descending: true}, 10).Runs
	if len(runs) == 0 {
		t.Fatalf("grain CI never ran %s", victim.BranchName())
	}
	first := runs[0]
	key := onlyJob(t, first).Key
	found, concl, base := w.cacheEntry(key)
	if first.Conclusion != ci.ConclusionFailure || !found || concl != ci.ConclusionFailure || base != red {
		t.Fatalf("victim's run while main is red: %s; cache entry %v %s against base %q; want a cached failure against %s",
			first.Conclusion, found, concl, base, red)
	}
	t.Logf("victim %s failed against red main %s and is cached as such (key %s)", first.ID, shortID(red), shortID(string(key)))
	builtBefore := w.sandboxes.acquiredFor(victimHead)

	// Step 2: the marker goes.
	fixed := w.pushMain("remove the marker", map[string]string{"marker": ""})
	// The cycle that notices main moved: the base-branch reconciler starts
	// a run at the new tip and evicts failures cached against the old one.
	// Dispatch runs earlier in the same cycle, so it still saw the old row.
	w.tick()
	if r := w.baseRow(); r.Commit != fixed || r.Broken() {
		t.Fatalf("the cycle after the fix did not move main's row to %s: %+v", fixed, r)
	}
	if found, concl, base := w.cacheEntry(key); found {
		t.Errorf("one cycle after main moved off %s, the victim's cached %s against base %s is still there", shortID(red), concl, shortID(base))
	}
	if !w.ticks(10, func() bool {
		r := w.baseRow()
		return r.Commit == fixed && r.Conclusion == string(ci.ConclusionSuccess)
	}) {
		t.Fatalf("main was never judged green at %s: %+v", shortID(fixed), w.baseRow())
	}
	if r := w.baseRow(); r.Commit != fixed || r.Conclusion != string(ci.ConclusionSuccess) {
		t.Fatalf("main after the fix: %+v, want green at %s", r, fixed)
	}

	// The victim's cached failure was about the old base: asking again at
	// the same commit runs the job rather than replaying it.
	ciClient := ui.NewClient(ui.Config{Actor: ui.DefaultActor("operator"), CI: w.service}, w.store)
	again, err := ciClient.StartTaskCIRunOpt(w.ctx, victim.ID, ui.TaskCIRunOptions{})
	if err != nil {
		t.Fatalf("re-running the victim's CI: %v", err)
	}
	w.waitForIdleCI()
	got, _ := w.service.Get(again.ID)
	job := onlyJob(t, got)
	if got.Commit != victimHead {
		t.Fatalf("the re-run is at %s, want the same commit %s", got.Commit, victimHead)
	}
	if job.Cached || got.Conclusion != ci.ConclusionSuccess || w.sandboxes.acquiredFor(victimHead) == builtBefore {
		t.Fatalf("the victim's re-run: %s, cached %v, sandboxes %d -> %d; want a fresh pass",
			got.Conclusion, job.Cached, builtBefore, w.sandboxes.acquiredFor(victimHead))
	}
	if found, concl, base := w.cacheEntry(job.Key); !found || concl != ci.ConclusionSuccess || base != fixed {
		t.Errorf("after the re-run the cache holds %v %s against base %q, want a pass against %s", found, concl, base, fixed)
	}
	t.Logf("victim re-run %s: %s, cached=%v, sandboxes built for its head %d -> %d",
		got.ID, got.Conclusion, job.Cached, builtBefore, w.sandboxes.acquiredFor(victimHead))
}

func shortID(sha string) string {
	if len(sha) > 9 {
		return sha[:9]
	}
	return sha
}

// metricsMerges is the part of /api/metrics' merges section this
// walkthrough checks.
type metricsMerges struct {
	Merges struct {
		Landed     int `json:"landed"`
		Contention struct {
			BatchSizes struct {
				N     int   `json:"n"`
				Min   int64 `json:"min"`
				Max   int64 `json:"max"`
				Total int64 `json:"total"`
			} `json:"batchSizes"`
			GateLatency struct {
				N          int     `json:"n"`
				MaxSeconds float64 `json:"maxSeconds"`
			} `json:"gateLatency"`
			QueueWaitTime struct {
				N           int     `json:"n"`
				MinSeconds  float64 `json:"minSeconds"`
				MaxSeconds  float64 `json:"maxSeconds"`
				MeanSeconds float64 `json:"meanSeconds"`
			} `json:"queueWaitTime"`
			RebaseRetest struct {
				Rebases         int `json:"rebases"`
				Retests         int `json:"retests"`
				TotalContention int `json:"totalContention"`
			} `json:"rebaseRetest"`
		} `json:"contention"`
	} `json:"merges"`
}

// contendedRules are the walkthrough repository's merge rules: a red
// batch is taken at its word (bisected, not run again), and a change
// under legacy/ is gated on GitHub's checks as well as grain CI's.
const contendedRules = `defaults:
  flake_recheck: false
rules:
  - id: legacy
    paths: ["legacy/**"]
    ci:
      mode: both
`

func TestMergeQueueContentionTelemetryMatchesWhatMerged(t *testing.T) {
	t.Parallel()
	w := newBreakerWorld(t)
	w.scriptChanges()
	w.pushMain("merge rules", map[string]string{model.MergeRulesPath: contendedRules})
	if got := w.sync(); got == nil || got.LastError != "" {
		t.Fatalf("the merge rules did not sync: %+v", got)
	}
	if !w.ticks(10, func() bool { r := w.baseRow(); return r != nil && r.Conclusion == string(ci.ConclusionSuccess) }) {
		t.Fatalf("main was never judged green: %+v", w.baseRow())
	}
	// Step 3: five auto-merge changes merged by the real queue.
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, w.create(fmt.Sprintf("Change %d", i), true, nil).ID)
	}
	w.scriptChanges(ids...)
	allMerged := func() bool {
		for _, id := range ids {
			if !w.merged(model.BranchName(id)) {
				return false
			}
		}
		return true
	}
	notMerged := func() {
		t.Helper()
		var states []string
		for _, id := range ids {
			states = append(states, fmt.Sprintf("%s=%s merged=%v", id, w.state(id), w.merged(model.BranchName(id))))
		}
		t.Fatalf("not everything merged: %s", strings.Join(states, ", "))
	}
	if !w.ticks(40, allMerged) {
		notMerged()
	}

	// Then one change under legacy/, whose GitHub check -- which that path
	// is gated on as well as grain CI -- is still running when its pull
	// request opens. Main moves under it, and the check then reports red
	// against the main it ran on. At the head of the queue it is merged up
	// to date (refreshStaleHead) rather than handed to a repair, and the
	// check that runs again on the merged head passes.
	late := w.create("Late change", true, nil)
	lateBranch := model.BranchName(late.ID)
	ids = append(ids, late.ID)
	setCheck := func(status string, conclusion *string) {
		w.sim.mu.Lock()
		defer w.sim.mu.Unlock()
		w.sim.sim.CheckRuns[lateBranch] = []github.CheckRun{{Name: "build", Status: status, Conclusion: conclusion}}
	}
	setCheck("in_progress", nil)
	w.setScript(func(tk model.Task) []antigravity.Step {
		if tk.ID != late.ID {
			return []antigravity.Step{finalText("looked; nothing to push")}
		}
		return w.changeScript(tk, "mkdir -p legacy && echo '"+tk.ID+"' > legacy/"+tk.ID+".txt")
	})
	if !w.ticks(10, func() bool {
		o, _ := w.store.GetObservation(w.ctx, late.ID)
		return o != nil && o.PrOpenedAt != nil && o.CI != nil && o.CI.Done()
	}) {
		t.Fatalf("%s's pull request never opened with grain CI's verdict on it: %s", late.ID, w.state(late.ID))
	}
	if w.merged(lateBranch) {
		t.Fatalf("%s merged while its GitHub check was still running", late.ID)
	}
	moved := w.pushMain("an unrelated change on main", map[string]string{"MAIN.md": "unrelated work\n"})
	failure, success := "failure", "success"
	setCheck("completed", &failure)
	rebased := false
	if !w.ticks(30, func() bool {
		if !rebased && exec.Command("git", "--git-dir", w.bare, "merge-base", "--is-ancestor",
			moved, "refs/heads/"+lateBranch).Run() == nil {
			rebased = true
			setCheck("completed", &success)
		}
		return allMerged()
	}) {
		notMerged()
	}
	if !rebased {
		t.Fatalf("%s merged without main ever being merged into it", late.ID)
	}
	w.ticks(3, func() bool { return false }) // let the sync record each landing

	// Queue wait is measured from the moment the queue first took a change
	// on (Observation.MergeQueueEnteredAt) to its merge, and a zero is a
	// sample: one per change the queue merged.
	var waits []time.Duration
	for _, id := range ids {
		o, _ := w.store.GetObservation(w.ctx, id)
		if o == nil || o.PrOpenedAt == nil || o.PrMergedAt == nil {
			t.Fatalf("%s merged with no pull request moments recorded: %+v", id, o)
		}
		if o.MergeQueueEnteredAt == nil {
			t.Fatalf("%s merged through the queue with no queue entry recorded: %+v", id, o)
		}
		wait := o.PrMergedAt.Sub(*o.MergeQueueEnteredAt)
		if wait < 0 {
			t.Fatalf("%s merged at %s, before it entered the queue at %s", id, o.PrMergedAt, o.MergeQueueEnteredAt)
		}
		waits = append(waits, wait)
		t.Logf("%s: PR opened %s, entered the queue %s, merged %s (waited %s)", id, o.PrOpenedAt.Format(time.TimeOnly),
			o.MergeQueueEnteredAt.Format(time.TimeOnly), o.PrMergedAt.Format(time.TimeOnly), wait)
	}
	records, err := w.store.MergeBatchRecords(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("expected no merge batch records after batching was removed, got %+v", records)
	}

	q := url.Values{
		"section": {"merges"},
		"since":   {baseTime.Add(-time.Hour).Format(time.RFC3339)},
		"until":   {w.now.Add(24 * time.Hour).Format(time.RFC3339)},
	}
	var got metricsMerges
	w.get("/api/metrics?"+q.Encode(), &got)
	c := got.Merges.Contention
	t.Logf("/api/metrics merges: landed %d; batch sizes n=%d min=%d max=%d total=%d; gate latency n=%d max %.1fs; "+
		"queue wait n=%d min %.1fs max %.1fs mean %.1fs; rebases %d, retests %d",
		got.Merges.Landed, c.BatchSizes.N, c.BatchSizes.Min, c.BatchSizes.Max, c.BatchSizes.Total,
		c.GateLatency.N, c.GateLatency.MaxSeconds,
		c.QueueWaitTime.N, c.QueueWaitTime.MinSeconds, c.QueueWaitTime.MaxSeconds, c.QueueWaitTime.MeanSeconds,
		c.RebaseRetest.Rebases, c.RebaseRetest.Retests)

	if got.Merges.Landed != len(ids) {
		t.Errorf("landed = %d, want the %d changes merged", got.Merges.Landed, len(ids))
	}
	if c.BatchSizes.N != 0 || c.BatchSizes.Total != 0 || c.GateLatency.N != 0 {
		t.Errorf("batch sizes = %+v, gate latency = %+v, want 0 batches", c.BatchSizes, c.GateLatency)
	}
	mean := sumDurations(waits) / time.Duration(len(waits))
	if c.QueueWaitTime.N != len(ids) || !sameSeconds(c.QueueWaitTime.MinSeconds, slices.Min(waits)) ||
		!sameSeconds(c.QueueWaitTime.MaxSeconds, slices.Max(waits)) || !sameSeconds(c.QueueWaitTime.MeanSeconds, mean) {
		t.Errorf("queue wait = %+v, want one sample per merged change (%d) measured from MergeQueueEnteredAt: "+
			"min %s, max %s, mean %s", c.QueueWaitTime, len(ids), slices.Min(waits), slices.Max(waits), mean)
	}
	// Rebases and retests are the queue's own record of doing things over
	// (model.MergeQueueContentionEvent): here the one base merge into the
	// late change's stale head and the suite it sent that change back
	// through.
	events, err := w.store.MergeQueueContentionEvents(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	byReason := map[string]int{}
	rebases, retests := 0, 0
	for _, e := range events {
		t.Logf("contention: %s %s %s (%s) at %s", e.Repo, e.TaskID, e.Kind, e.Reason, e.At.Format(time.TimeOnly))
		byReason[e.Kind+"/"+e.Reason]++
		switch e.Kind {
		case model.MergeQueueRebase:
			rebases++
			if e.TaskID != late.ID {
				t.Errorf("rebase recorded for %s, want only the late change %s", e.TaskID, late.ID)
			}
		case model.MergeQueueRetest:
			retests++
		}
	}
	if want := map[string]int{
		model.MergeQueueRebase + "/" + model.MergeQueueStaleHead:   1,
		model.MergeQueueRetest + "/" + model.MergeQueueAfterRebase: 1,
	}; !maps.Equal(byReason, want) {
		t.Errorf("contention recorded = %v, want %v", byReason, want)
	}
	if c.RebaseRetest.Rebases != 1 || c.RebaseRetest.Rebases != rebases ||
		c.RebaseRetest.Retests != 1 || c.RebaseRetest.Retests != retests ||
		c.RebaseRetest.TotalContention != rebases+retests {
		t.Errorf("rebase/retest = %+v, want rebases 1 and retests 1 "+
			"matching the queue's own records (%d, %d)", c.RebaseRetest, rebases, retests)
	}
}

// sameSeconds reports whether a duration the API stated in seconds is d.
func sameSeconds(seconds float64, d time.Duration) bool {
	return math.Abs(seconds-d.Seconds()) < 0.001
}

func sumDurations(ds []time.Duration) time.Duration {
	var total time.Duration
	for _, d := range ds {
		total += d
	}
	return total
}

func TestTuningRunnerRefusesAParameterOutsideTheAllowlist(t *testing.T) {
	t.Parallel()
	w := newBreakerWorld(t)

	// Step 4: the tuning runner (an infra-review task) proposes one change
	// to an allowlisted grain_config parameter and one to a parameter
	// grain_config does not have.
	const allowed, refused = "Tune max_merges_per_repo to 4", "Tune merge_window_secs to 9"
	runner := w.create("Infra review", false, nil)
	runner.TemplateID = model.InfraReviewTemplateID
	if err := w.store.PutTask(w.ctx, runner); err != nil {
		t.Fatal(err)
	}
	// And an ordinary task whose split-out work merely has "tune" in its
	// title: it is not the tuning runner, and its proposal is not a tuning
	// recommendation.
	const ordinary = "Tune the e2e retry loop"
	other := w.create("Flaky e2e", false, nil)
	w.setScript(func(tk model.Task) []antigravity.Step {
		switch tk.ID {
		case runner.ID:
			return []antigravity.Step{
				toolCall("propose_task", map[string]any{"title": allowed, "body": "merges keep landing cleanly",
					"template_fields": map[string]any{"target_parameter": "max_merges_per_repo", "proposed_value": "4"}}),
				toolCall("propose_task", map[string]any{"title": refused, "body": "wait longer to gather",
					"template_fields": map[string]any{"target_parameter": "merge_window_secs", "proposed_value": "9"}}),
				finalText("two recommendations filed"),
			}
		case other.ID:
			return []antigravity.Step{
				toolCall("propose_task", map[string]any{"title": ordinary, "body": "the retry loop needs a longer backoff"}),
				finalText("split out the retry work"),
			}
		}
		return []antigravity.Step{finalText("nothing to do")}
	})
	if !w.cycles(6, func() bool { return w.state(runner.ID) != model.StateQueued && w.state(other.ID) != model.StateQueued }) {
		t.Fatalf("the runs never finished: runner %s, other %s", w.state(runner.ID), w.state(other.ID))
	}

	titles := func(parent string) []string {
		var out []string
		for _, p := range w.proposalsOf(parent) {
			out = append(out, p.Title)
		}
		return out
	}
	got := titles(runner.ID)
	t.Logf("filed from the tuning runner: %q", got)
	if !slices.Contains(got, allowed) {
		t.Errorf("the allowlisted recommendation %q was not filed: %q", allowed, got)
	}
	if slices.Contains(got, refused) {
		t.Errorf("the recommendation for a parameter outside the allowlist was filed: %q", got)
	}
	recs, err := w.store.ListTuningRecommendations(w.ctx, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var params []string
	for _, r := range recs {
		params = append(params, r.TargetParameter+"="+r.ProposedValue)
	}
	t.Logf("tuning_recommendation rows: %q", params)
	if !slices.Equal(params, []string{"max_merges_per_repo=4"}) {
		t.Errorf("tuning_recommendation rows = %q, want only max_merges_per_repo=4", params)
	}

	// The runner is told what was refused: one comment on its task naming
	// the refused title, the parameter and the allowlist's error.
	comments, err := w.store.Comments(w.ctx, runner.ID)
	if err != nil {
		t.Fatal(err)
	}
	var refusals []string
	for _, c := range comments {
		if strings.Contains(c.Body, refused) {
			refusals = append(refusals, c.Body)
		}
	}
	if len(refusals) != 1 {
		t.Fatalf("the tuning runner has %d comments naming %q, want exactly 1: %q", len(refusals), refused, refusals)
	}
	t.Logf("refusal comment on the tuning runner: %s", refusals[0])
	for _, want := range []string{"`merge_window_secs`", "unrecognized grain_config parameter"} {
		if !strings.Contains(refusals[0], want) {
			t.Errorf("the refusal comment does not name %s: %q", want, refusals[0])
		}
	}

	if got := titles(other.ID); !slices.Contains(got, ordinary) {
		t.Errorf("an ordinary task's proposal %q was dropped as a tuning recommendation: filed %q", ordinary, got)
	}
}
