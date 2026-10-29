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

// This file walks grain CI's agent-facing tools end to end, as
// docs/test-plans/2026-10-06.md §10 asks: retrigger_ci re-running only the
// failed jobs and the gate combining per-job verdicts (RecheckOf),
// get_ci_artifact, and the Jobs gate's Stuck hook breaking the
// sandbox-blocks-its-own-CI-job deadlock.
//
// What is real: the CI service the daemon builds (daemon.BuildCIService --
// its stored run history, its job cache and artifact store under a data
// directory, its Jobs gate with the deployment's budget, ledger and Stuck
// hook), a ui.Server over a real SQLite store, the orchestrator's own "ci"
// reconciler reading a simulated GitHub (githubsim) over a real
// github.RESTClient, and the agent's side as a real `grain mcpserver`
// subprocess spoken to over stdio MCP.
//
// What is stood in for, and only at the edges (each is listed in
// docs/test-runs/2026-10-07-main-ci-tools.md):
//
//   - the job's machine: localCISandboxes runs each step with sh in a
//     temporary directory on this host -- a local runner -- instead of the
//     git-proxy checkout daemon.CiSandboxes prepares, since a workflow whose
//     steps need nothing from the checkout needs no checkout;
//   - where workflows come from: bareRepoCISource reads them out of the
//     bare repository githubsim serves, with git, instead of
//     daemon.CiWorkflowSource cloning them from GitHub;
//   - the agent sandbox an orchestrator dispatch would charge to the
//     ledger: the deadlock walkthrough charges it directly, under the id of
//     a real live model.Run, which is what the dispatcher charges it under.
package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// recheckWorkflow is the plan's three jobs. flaky fails the first time it
// runs at a commit and passes after that, keyed by a marker file the local
// runner points it at outside the workspace; on any later run it first
// waits while a hold file exists, which is how the walkthrough keeps a
// recheck in flight long enough to start a wait_for_checks beside it. It
// keeps an artifact when it fails.
const recheckWorkflow = `name: checks
on: push
jobs:
  a:
    steps:
      - run: echo a passed
  b:
    steps:
      - run: echo b passed
  flaky:
    name: flaky e2e
    artifacts:
      - name: trace
        path: out
    steps:
      - name: run the flaky suite
        run: |
          mkdir -p out
          echo "trace for $E2E_COMMIT" > out/trace.txt
          if [ ! -f "$E2E_MARKER" ]; then
            touch "$E2E_MARKER"
            echo "first run at this commit: failing"
            exit 1
          fi
          while [ -f "$E2E_HOLD" ]; do sleep 0.05; done
          echo "passed on a later run"
`

// bigWorkflow is the deadlock walkthrough's one job: a 2 GiB sandbox.
const bigWorkflow = `name: big
on: push
jobs:
  big:
    sandbox:
      cpus: 1
      memory-mb: 2048
    steps:
      - run: echo big job ran
`

// bareRepoCISource reads a repository's workflows out of the bare
// repository githubsim serves for it, at the ref asked for.
type bareRepoCISource struct {
	bare map[string]string // owner/name -> bare repository
}

func (s bareRepoCISource) Workflows(ctx context.Context, repo, ref string) ([]ci.Workflow, string, error) {
	bare, ok := s.bare[repo]
	if !ok {
		return nil, "", fmt.Errorf("no repository %s", repo)
	}
	git := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"--git-dir", bare}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if ref == "" {
		ref = "HEAD" // the default branch, which is what an empty ref asks for
	}
	commit, err := git("rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return nil, "", fmt.Errorf("resolving %s in %s: %v: %s", ref, repo, err, commit)
	}
	names, err := git("ls-tree", "--name-only", commit, ".grain/workflows/")
	if err != nil {
		return nil, "", fmt.Errorf("listing workflows of %s at %s: %v", repo, commit, err)
	}
	var workflows []ci.Workflow
	for _, path := range strings.Fields(names) {
		body, err := git("show", commit+":"+path)
		if err != nil {
			return nil, "", fmt.Errorf("reading %s: %v", path, err)
		}
		wf, err := ci.Parse(path, []byte(body+"\n"))
		if err != nil {
			return nil, "", err
		}
		workflows = append(workflows, wf)
	}
	return workflows, commit, nil
}

// localCISandboxes is a local runner: a job's sandbox is a temporary
// directory on this host and a step is `sh -c` in it.
type localCISandboxes struct {
	dir string // where markers and the hold file live, outside every workspace

	mu       sync.Mutex
	acquired []ci.SandboxRequest
}

func (l *localCISandboxes) Acquire(_ context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	l.mu.Lock()
	l.acquired = append(l.acquired, req)
	n := len(l.acquired)
	l.mu.Unlock()
	work, err := os.MkdirTemp(l.dir, "job-*")
	if err != nil {
		return nil, err
	}
	marker := filepath.Join(l.dir, "flaky-"+strings.ReplaceAll(req.Repo, "/", "-")+"-"+req.Commit)
	return &localCISandbox{name: fmt.Sprintf("local-%d", n), work: work, env: []string{
		"E2E_MARKER=" + marker, "E2E_HOLD=" + filepath.Join(l.dir, "hold"), "E2E_COMMIT=" + req.Commit,
	}}, nil
}

func (l *localCISandboxes) requests() []ci.SandboxRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]ci.SandboxRequest(nil), l.acquired...)
}

type localCISandbox struct {
	name string
	work string
	env  []string
}

func (s *localCISandbox) Name() string { return s.name }

func (s *localCISandbox) Exec(ctx context.Context, cmd ci.Command) (ci.CommandResult, error) {
	if cmd.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cmd.Timeout)
		defer cancel()
	}
	c := exec.CommandContext(ctx, "sh", "-c", cmd.Script)
	c.Dir = s.work
	if cmd.Dir != "" {
		c.Dir = filepath.Join(s.work, cmd.Dir)
	}
	c.Env = append(os.Environ(), s.env...)
	for k, v := range cmd.Env {
		c.Env = append(c.Env, k+"="+v)
	}
	out, err := c.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok {
		return ci.CommandResult{Output: string(out), ExitCode: exit.ExitCode()}, nil
	}
	if err != nil {
		return ci.CommandResult{Output: string(out)}, err
	}
	return ci.CommandResult{Output: string(out)}, nil
}

func (s *localCISandbox) Release(context.Context) error { return os.RemoveAll(s.work) }

// ciWorld is one deployment: a store, a simulated GitHub, the daemon's own
// CI service, the API in front of them, and a built grain binary to run
// the agent's MCP server from.
type ciWorld struct {
	t         *testing.T
	ctx       context.Context
	store     *model.Store
	sim       *githubsim.Sim
	client    *github.RESTClient
	service   *ci.Service
	sandboxes *localCISandboxes
	daemonURL string
	bin       string
	bare      string
	seed      string

	mu     sync.Mutex
	limits model.Limits
}

func newCIWorld(t *testing.T, workflow string, limits model.Limits) *ciWorld {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	w := &ciWorld{t: t, ctx: ctx, limits: limits}

	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	w.store = model.New(db)
	if err := w.store.Init(ctx); err != nil {
		t.Fatalf("init store: %v", err)
	}

	// acme/widgets, with the workflow on main.
	dir := t.TempDir()
	w.bare = filepath.Join(dir, "widgets.git")
	run(t, dir, "git", "init", "-q", "--bare", "-b", "main", w.bare)
	w.seed = filepath.Join(dir, "seed")
	run(t, dir, "git", "clone", "-q", w.bare, w.seed)
	run(t, w.seed, "git", "config", "user.email", "seed@example.com")
	run(t, w.seed, "git", "config", "user.name", "seed")
	run(t, w.seed, "git", "checkout", "-q", "-b", "main")
	mustWriteFile(t, filepath.Join(w.seed, ".grain", "workflows", "checks.yml"), workflow)
	run(t, w.seed, "git", "add", "-A")
	run(t, w.seed, "git", "commit", "-q", "-m", "workflow")
	run(t, w.seed, "git", "push", "-q", "origin", "main")
	w.sim = githubsim.New("acme", "widgets", w.bare, "main")
	w.client = github.NewClient(w.sim, nil)
	if err := w.store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	tokens := gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "sandbox-tokens.json"))
	w.service = daemon.BuildCIService(ctx, daemon.Config{DataDir: dataDir}, w.currentLimits,
		func() ci.StoredRunRetention { return ci.StoredRunRetention{} },
		func() ci.StorageBounds { return ci.StorageBounds{} }, w.store,
		nil, nil, "", tokens, func() string { return "" }, nil,
		func() *model.CapabilityRegistry { return nil }, nil, nil, nil, nil, nil)
	// The two edges this walkthrough stands in for (see the file comment).
	w.sandboxes = &localCISandboxes{dir: t.TempDir()}
	w.service.Engine.Sandboxes = w.sandboxes
	w.service.Source = bareRepoCISource{bare: map[string]string{"acme/widgets": w.bare, "acme/other": w.bare}}

	w.daemonURL = newTestServer(t, ui.NewServer(withAccessCode(ui.Config{
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
		CI:           w.service,
	}), w.store))
	w.bin = buildGrainBinary(t)
	return w
}

func (w *ciWorld) currentLimits() model.Limits {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.limits
}

// task files a grain-ci task on acme/widgets with a pushed branch and an
// open pull request, the shape the CI reconciler collects verdicts for.
// Its merge rules say `flake_recheck: false`, so a red is concluded as red
// rather than rechecked by the orchestrator first: the recheck under test
// is the agent's own.
func (w *ciWorld) task(id string) model.Task {
	t := w.t
	t.Helper()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	actor := human("operator")
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "fold the widget", Body: "please",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: actor},
		Target:   &repo,
		Binding:  model.BindingDirective,
		Grants:   []model.Grant{{Capability: "grain-ci"}},
		Links:    []model.Link{{Kind: model.LinkMergePolicy, Target: `{"NoFlakeRecheck":true}`}},
	}
	if err := w.store.PutTask(w.ctx, task); err != nil {
		t.Fatalf("filing %s: %v", id, err)
	}
	w.push(task.BranchName(), "first change")
	pr, err := orchestrator.EnsurePullRequest(w.ctx, w.store, w.client, task, time.Now())
	if err != nil {
		t.Fatalf("opening %s's pull request: %v", id, err)
	}
	task.Links = append(task.Links, model.Link{
		Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: repo, Number: pr.Number}.String(),
	})
	if err := w.store.PutTask(w.ctx, task); err != nil {
		t.Fatal(err)
	}
	// The run that pushed it has ended: the CI reconciler collects a
	// verdict only for a task that is completed (or awaiting submit) with
	// a pull request open, which is when the merge queue reads it.
	completed := time.Now().UTC()
	if err := w.store.Observe(w.ctx, model.Observation{TaskID: id, CompletedAt: &completed}); err != nil {
		t.Fatal(err)
	}
	return task
}

// push commits a real file change on branch, so every job's fingerprint
// moves, and returns the new head.
func (w *ciWorld) push(branch, content string) string {
	t := w.t
	t.Helper()
	if out, err := exec.Command("git", "-C", w.seed, "checkout", "-q", "-B", branch, "origin/"+branch).CombinedOutput(); err != nil {
		run(t, w.seed, "git", "checkout", "-q", "-B", branch, "origin/main")
		_ = out
	}
	mustWriteFile(t, filepath.Join(w.seed, "change.txt"), content+"\n")
	run(t, w.seed, "git", "add", "-A")
	run(t, w.seed, "git", "commit", "-q", "-m", content)
	run(t, w.seed, "git", "push", "-q", "origin", branch)
	return runOutput(t, w.seed, "git", "rev-parse", "HEAD")
}

// agent is the task's own `grain mcpserver`, the process a dispatched run
// calls its tools through.
func (w *ciWorld) agent(taskID string, workspace string) *mcpProcess {
	return startMCPServer(w.t, w.bin, "-sandbox-root", workspace, "-server", w.daemonURL,
		"-task", taskID, "-grant", "grain-ci", "-pr-repo", "acme/widgets")
}

func callTool(t *testing.T, p *mcpProcess, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := p.CallTool(context.Background(), name, args)
	if err != nil {
		t.Fatalf("tools/call %s: %v\nstderr:\n%s", name, err, p.stderr.String())
	}
	return res.Text(), res.IsError
}

// settle waits for run id to conclude.
func (w *ciWorld) settle(id string) ci.Run {
	t := w.t
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		run, ok := w.service.Get(id)
		if ok && run.Conclusion.Done() {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s did not conclude: %+v", id, run)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// reconcile is one tick of the orchestrator's own CI reconciler.
func (w *ciWorld) reconcile() {
	t := w.t
	t.Helper()
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == "ci" {
			if err := r.Reconcile(w.ctx, orchestrator.Deps{Store: w.store, Client: w.client, CI: w.service}, time.Now()); err != nil {
				t.Fatalf("ci reconciler: %v", err)
			}
			return
		}
	}
	t.Fatal(`no "ci" reconciler in the cycle`)
}

func (w *ciWorld) verdict(taskID string) model.CIStatus {
	t := w.t
	t.Helper()
	obs, err := w.store.GetObservation(w.ctx, taskID)
	if err != nil || obs == nil || obs.CI == nil {
		t.Fatalf("task %s has no CI status: %+v %v", taskID, obs, err)
	}
	return *obs.CI
}

// runsOn is every run on branch, newest first.
func (w *ciWorld) runsOn(branch string) []ci.Run {
	return w.service.Find(ci.RunFilter{Repo: "acme/widgets", Ref: branch, Descending: true}, 100).Runs
}

func jobIDs(run ci.Run) string {
	var ids []string
	for _, j := range run.Jobs {
		ids = append(ids, j.JobID)
	}
	return strings.Join(ids, ",")
}

func jobOf(t *testing.T, run ci.Run, id string) ci.JobRun {
	t.Helper()
	for _, j := range run.Jobs {
		if j.JobID == id {
			return j
		}
	}
	t.Fatalf("run %s has no job %s: %s", run.ID, id, jobIDs(run))
	return ci.JobRun{}
}

// retriggered is the run id retrigger_ci's answer names.
func retriggered(t *testing.T, text string) string {
	t.Helper()
	const lead = "Triggered fresh uncached Grain CI run "
	i := strings.Index(text, lead)
	if i < 0 {
		t.Fatalf("retrigger_ci named no run:\n%s", text)
	}
	return strings.Fields(text[i+len(lead):])[0]
}

func TestCIToolsWalkthroughRecheckOnlyTheFailedJob(t *testing.T) {
	t.Parallel()
	w := newCIWorld(t, recheckWorkflow, model.Limits{})
	task := w.task("7")
	branch := task.BranchName()
	workspace := t.TempDir()
	agent := w.agent("7", workspace)

	// Push, then wait_for_checks: red on flaky.
	text, isErr := callTool(t, agent, "wait_for_checks", map[string]any{"timeout_seconds": 60})
	if isErr || !strings.Contains(text, "flaky") {
		t.Fatalf("wait_for_checks after the push = %v:\n%s", isErr, text)
	}
	runs := w.runsOn(branch)
	if len(runs) != 1 {
		t.Fatalf("%d runs on %s after the first wait, want 1", len(runs), branch)
	}
	first := w.settle(runs[0].ID)
	if first.Conclusion != ci.ConclusionFailure || jobOf(t, first, "flaky").Conclusion != ci.ConclusionFailure ||
		jobOf(t, first, "a").Conclusion != ci.ConclusionSuccess || jobOf(t, first, "b").Conclusion != ci.ConclusionSuccess {
		t.Fatalf("first run = %s with jobs %+v, want only flaky red", first.Conclusion, first.Jobs)
	}
	w.reconcile()
	red := w.verdict("7")
	if red.Conclusion != string(ci.ConclusionFailure) || red.RunID != first.ID || !strings.Contains(red.Detail, "flaky") {
		t.Fatalf("task verdict after the red run = %+v, want the red run concluded", red)
	}

	t.Run("get_ci_artifact fetches the kept trace into the workspace", func(t *testing.T) {
		kept, _, err := w.service.RunArtifacts(w.ctx, first.ID)
		if err != nil || len(kept) != 1 || !kept[0].Stored {
			t.Fatalf("the red run kept %+v (%v), want flaky's trace", kept, err)
		}
		rc, _, err := w.service.OpenArtifact(w.ctx, kept[0].Ref())
		if err != nil {
			t.Fatal(err)
		}
		want, _ := io.ReadAll(rc)
		rc.Close()

		// Defaults: the newest run, its failed job, its one artifact.
		text, isErr := callTool(t, agent, "get_ci_artifact", map[string]any{})
		if isErr {
			t.Fatalf("get_ci_artifact with defaults refused: %s", text)
		}
		got, err := os.ReadFile(filepath.Join(workspace, kept[0].Ref().Filename()))
		if err != nil {
			t.Fatalf("the artifact is not in the workspace: %v\n%s", err, text)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("workspace copy is %d bytes, the store holds %d", len(got), len(want))
		}
		if strings.Contains(text, "trace for") || len(text) > 1000 {
			t.Fatalf("the tool inlined the artifact rather than writing it:\n%s", text)
		}
		if trace := untar(t, got, "out/trace.txt"); !strings.Contains(trace, "trace for "+first.Commit) {
			t.Fatalf("out/trace.txt = %q", trace)
		}

		// Explicit names, to a path of the agent's choosing.
		text, isErr = callTool(t, agent, "get_ci_artifact", map[string]any{
			"run_id": first.ID, "job": "flaky e2e", "name": "trace", "path": "evidence/trace.tgz"})
		if isErr {
			t.Fatalf("get_ci_artifact with explicit names refused: %s", text)
		}
		if got, err := os.ReadFile(filepath.Join(workspace, "evidence", "trace.tgz")); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("explicit download = %d bytes (%v), want the stored %d", len(got), err, len(want))
		}

		// Another repository's run is not this run's to read.
		other, err := w.service.Start(w.ctx, ci.Request{Repo: "acme/other", Ref: "main", Event: ci.EventPush})
		if err != nil {
			t.Fatal(err)
		}
		other = w.settle(other.ID)
		text, isErr = callTool(t, agent, "get_ci_artifact", map[string]any{"run_id": other.ID, "job": "flaky"})
		if !isErr || !strings.Contains(text, "acme/other") {
			t.Fatalf("get_ci_artifact on acme/other's run = %v:\n%s", isErr, text)
		}
		t.Logf("another repo's run refused: %s", text)
	})

	// retrigger_ci {} while a full wait_for_checks joins it.
	hold := filepath.Join(w.sandboxes.dir, "hold")
	mustWriteFile(t, hold, "")
	text, isErr = callTool(t, agent, "retrigger_ci", map[string]any{})
	if isErr || !strings.Contains(text, "Jobs queued: flaky e2e") {
		t.Fatalf("retrigger_ci {} = %v:\n%s", isErr, text)
	}
	recheckID := retriggered(t, text)
	recheck, ok := w.service.Get(recheckID)
	if !ok || !recheck.Recheck || jobIDs(recheck) != "flaky" {
		t.Fatalf("recheck %q = %+v, want an uncached rerun of flaky alone", recheckID, recheck)
	}
	if v := w.verdict("7"); v.RunID != recheckID || v.RecheckOf != first.ID || v.Done() {
		t.Fatalf("task verdict once retrigger_ci started = %+v, want the recheck as the red verdict's second sample", v)
	}

	before := len(w.runsOn(branch))
	waiter := w.agent("7", t.TempDir())
	answer := make(chan string, 1)
	go func() {
		res, err := waiter.CallTool(context.Background(), "wait_for_checks", map[string]any{"timeout_seconds": 60})
		if err != nil {
			answer <- "error: " + err.Error()
			return
		}
		answer <- res.Text()
	}()
	time.Sleep(time.Second)
	if n := len(w.runsOn(branch)); n != before {
		t.Fatalf("a full wait_for_checks during the recheck started %d runs, want none", n-before)
	}
	if r, _ := w.service.Get(recheckID); r.Conclusion.Done() {
		t.Fatalf("the recheck concluded (%s) while held: the wait cancelled it?", r.Conclusion)
	}
	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	joined := <-answer
	if !strings.Contains(joined, recheckID) || strings.Contains(joined, "cancelled") {
		t.Fatalf("the concurrent full wait answered:\n%s\nwant the recheck %s's verdict", joined, recheckID)
	}
	if r, err := ui.NewHTTPClient(w.daemonURL).CIRun(w.ctx, recheckID); err != nil || !r.Recheck {
		sr, _ := w.service.Get(recheckID)
		t.Fatalf("GET /api/ci/runs/%s: Recheck %v (%v); the service says %v", recheckID, r.Recheck, err, sr.Recheck)
	}
	// And says it was handed the recheck, which lists only the job it
	// re-ran -- the note reads ci.Run.Recheck, which the stored run store
	// used to drop.
	if !strings.Contains(joined, "This is the uncached recheck of this commit's failed jobs") {
		t.Fatalf("the full wait handed the recheck did not say so:\n%s", joined)
	}
	t.Logf("full wait_for_checks during the recheck answered:\n%s", joined)
	recheck = w.settle(recheckID)
	if recheck.Conclusion != ci.ConclusionSuccess || recheck.CancelReason != "" {
		t.Fatalf("recheck = %s (%q), want a pass nobody cancelled", recheck.Conclusion, recheck.CancelReason)
	}
	// Only flaky reran: a and b ran once, in the first run.
	var ran []string
	for _, req := range w.sandboxes.requests() {
		if req.Repo == "acme/widgets" {
			ran = append(ran, req.Name)
		}
	}
	if len(ran) != 4 {
		t.Fatalf("sandboxes acquired for acme/widgets: %v, want a, b, flaky and the one recheck of flaky", ran)
	}

	w.reconcile()
	green := w.verdict("7")
	if !green.Green() || green.RecheckOf != first.ID || green.RunID != recheckID {
		t.Fatalf("task verdict after the recheck passed = %+v, want green with RecheckOf %s", green, first.ID)
	}

	// A later full wait reads every job's newest cached verdict: green
	// without running anything.
	text, isErr = callTool(t, agent, "wait_for_checks", map[string]any{"timeout_seconds": 60})
	if isErr {
		t.Fatalf("a later full wait_for_checks: %s", text)
	}
	latest := w.settle(w.verdict("7").RunID)
	if latest.Conclusion != ci.ConclusionSuccess || latest.Recheck {
		t.Fatalf("later full run = %+v, want a green complete run", latest)
	}
	for _, j := range latest.Jobs {
		if !j.Cached {
			t.Errorf("job %s of the later full run ran again rather than reading its cached verdict", j.JobID)
		}
	}
	w.reconcile()
	if v := w.verdict("7"); !v.Green() || v.RunID != latest.ID {
		t.Fatalf("merge-gate read after the later full run = %+v, want green", v)
	}

	t.Run("a selective run does not hide the failed job", func(t *testing.T) {
		w.push(branch, "second change")
		text, _ := callTool(t, agent, "wait_for_checks", map[string]any{"timeout_seconds": 60})
		full := w.settle(w.verdict("7").RunID)
		if full.Conclusion != ci.ConclusionFailure || jobOf(t, full, "flaky").Conclusion != ci.ConclusionFailure {
			t.Fatalf("fresh full run = %s, want flaky red again:\n%s", full.Conclusion, text)
		}
		w.reconcile()
		text, isErr := callTool(t, agent, "wait_for_checks", map[string]any{"jobs": []any{"a"}, "timeout_seconds": 60})
		selective := w.settle(waitedOn(t, text))
		if isErr || jobIDs(selective) != "a" || selective.Conclusion != ci.ConclusionSuccess {
			t.Fatalf("selective run = %+v (%v):\n%s", selective, isErr, text)
		}
		text, isErr = callTool(t, agent, "retrigger_ci", map[string]any{})
		if isErr || !strings.Contains(text, "Jobs queued: flaky e2e") {
			t.Fatalf("retrigger_ci {} after a selective run = %v:\n%s", isErr, text)
		}
		r := w.settle(retriggered(t, text))
		if jobIDs(r) != "flaky" || r.Commit != full.Commit {
			t.Fatalf("recheck after the selective run reran %s at %s, want flaky at %s", jobIDs(r), r.Commit, full.Commit)
		}
	})

	t.Run("job names are checked against the workflows", func(t *testing.T) {
		text, isErr := callTool(t, agent, "retrigger_ci", map[string]any{"jobs": []any{"nope"}})
		if !isErr || !strings.Contains(text, `"nope"`) || !strings.Contains(text, "a, b, flaky") {
			t.Fatalf("retrigger_ci {jobs: [nope]} = %v:\n%s", isErr, text)
		}
		t.Logf("unknown job refused: %s", text)
		text, isErr = callTool(t, agent, "retrigger_ci", map[string]any{"jobs": []any{"flaky e2e"}})
		if isErr || !strings.Contains(text, "Jobs queued: flaky e2e") {
			t.Fatalf("retrigger_ci {jobs: [\"flaky e2e\"]} = %v:\n%s", isErr, text)
		}
		w.settle(retriggered(t, text))
	})

	t.Run("get_ci_log and get_ci_artifact default to newest run on branch", func(t *testing.T) {
		// Start a run on main for acme/widgets, which is newer than task 7's runs.
		mainRun, err := w.service.Start(w.ctx, ci.Request{Repo: "acme/widgets", Ref: "main", Event: ci.EventPush})
		if err != nil {
			t.Fatal(err)
		}
		mainRun = w.settle(mainRun.ID)

		// Default get_ci_log picks the newest run on task 7's branch, not mainRun.
		text, isErr := callTool(t, agent, "get_ci_log", map[string]any{})
		if isErr {
			t.Fatalf("get_ci_log with defaults: %s", text)
		}
		if strings.Contains(text, mainRun.ID) {
			t.Fatalf("get_ci_log with defaults read main's run %s instead of task branch", mainRun.ID)
		}

		// When explicit run_id is given for a run on the task's branch, get_ci_artifact fetches it.
		kept, _, err := w.service.RunArtifacts(w.ctx, first.ID)
		if err != nil || len(kept) == 0 {
			t.Fatalf("reading artifacts for %s: %v", first.ID, err)
		}
		text, isErr = callTool(t, agent, "get_ci_artifact", map[string]any{"run_id": first.ID})
		if isErr {
			t.Fatalf("get_ci_artifact on first run: %s", text)
		}
		if !strings.Contains(text, kept[0].Ref().Filename()) {
			t.Fatalf("get_ci_artifact on first run text = %q, want filename %s", text, kept[0].Ref().Filename())
		}
	})
}

// waitedOn is the run id wait_for_checks' answer names.
func waitedOn(t *testing.T, text string) string {
	t.Helper()
	const lead = "Grain CI run "
	i := strings.Index(text, lead)
	if i < 0 {
		t.Fatalf("wait_for_checks named no run:\n%s", text)
	}
	return strings.Fields(text[i+len(lead):])[0]
}

func untar(t *testing.T, tgz []byte, name string) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		t.Fatalf("not a gzip: %v", err)
	}
	tr := tar.NewReader(zr)
	var seen []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, h.Name)
		if strings.TrimPrefix(h.Name, "./") == name {
			body, _ := io.ReadAll(tr)
			return string(body)
		}
	}
	t.Fatalf("no %s in the artifact; it holds %v", name, seen)
	return ""
}

// logTee captures the standard logger, which is where the gate's Stuck
// admission is logged (BuildCIService wires the gate's Log to log.Printf).
type logTee struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logTee) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	return os.Stderr.Write(p)
}

func (l *logTee) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// The deadlock: a 3 GiB budget, a task's own 2 GiB agent sandbox live, and
// that task waiting on a CI job that wants 2 GiB. Not parallel: it captures
// the process's standard logger.
func TestCIToolsWalkthroughStuckGateAdmitsTheJobItsOwnSandboxBlocks(t *testing.T) {
	tee := &logTee{}
	prevLog := log.Writer()
	log.SetOutput(tee)
	t.Cleanup(func() { log.SetOutput(prevLog) })

	ledger := &model.Ledger{}
	w := newCIWorld(t, bigWorkflow, model.Limits{
		Budget: model.Budget{Host: model.Commitment{CPUs: 8, MemoryMB: 3072}, CPUOvercommit: 1, MemoryOvercommit: 1},
		Ledger: ledger,
	})
	task := w.task("9")
	branch := task.BranchName()
	if err := w.store.StartRun(w.ctx, model.Run{ID: "9-1", TaskID: "9", Sandbox: "9-1", Attempt: 1,
		StartedAt: time.Now().UTC()}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	releaseAgent := ledger.Charge(model.PoolAgent, "9-1", model.Commitment{CPUs: 2, MemoryMB: 2048})
	defer releaseAgent()
	agent := w.agent("9", t.TempDir())

	text, isErr := callTool(t, agent, "wait_for_checks", map[string]any{"timeout_seconds": 30})
	if isErr || !strings.Contains(text, "success") {
		t.Fatalf("wait_for_checks with the task's own sandbox holding the room = %v:\n%s", isErr, text)
	}
	admitted := w.settle(w.verdict("9").RunID)
	big := jobOf(t, admitted, "big")
	if big.Conclusion != ci.ConclusionSuccess || big.Queued > 5*time.Second {
		t.Fatalf("big = %s after queueing %s, want admitted within a gate cycle", big.Conclusion, big.Queued)
	}
	if !strings.Contains(tee.String(), "running it anyway rather than waiting for a release that is not coming") ||
		!strings.Contains(tee.String(), "CI run "+admitted.ID) {
		t.Fatalf("no Stuck admission logged for run %s", admitted.ID)
	}
	t.Logf("big queued %s (%s): %s", big.Queued, big.QueuedCause, big.QueuedReason)

	// An unrelated live sandbox: the room might be released by something
	// other than this job, so the job waits, and says who holds the room.
	releaseOther := ledger.Charge(model.PoolAgent, "other-1", model.Commitment{CPUs: 1, MemoryMB: 512})
	w.push(branch, "second change")
	answer := make(chan string, 1)
	go func() {
		res, err := agent.CallTool(context.Background(), "wait_for_checks", map[string]any{"timeout_seconds": 120})
		if err != nil {
			answer <- "error: " + err.Error()
			return
		}
		answer <- res.Text()
	}()
	var job ci.JobRun
	var waitingID string
	for deadline := time.Now().Add(30 * time.Second); ; {
		if obs, err := w.store.GetObservation(w.ctx, "9"); err == nil && obs != nil && obs.CI != nil && obs.CI.RunID != admitted.ID {
			waitingID = obs.CI.RunID
			if r, ok := w.service.Get(waitingID); ok && len(r.Jobs) == 1 {
				job = r.Jobs[0]
				if strings.Contains(job.Activity, "held by") {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the second run's job never reported a budget refusal; last activity %q", job.Activity)
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(2 * time.Second) // several gate cycles, none of which may admit it
	if r, _ := w.service.Get(waitingID); r.Jobs[0].StartedAt != nil || r.Conclusion.Done() {
		t.Fatalf("the job was admitted with an unrelated sandbox live: %+v", r.Jobs[0])
	}
	if !strings.Contains(job.Activity, "2 agent sandboxes") || !strings.Contains(job.Activity, "agent run 9-1") || !strings.Contains(job.Activity, "agent run other-1") {
		t.Fatalf("waiting job's activity = %q, want a refusal naming both holders", job.Activity)
	}
	t.Logf("refusal with an unrelated sandbox live: %s", job.Activity)

	// The unrelated sandbox goes; the cycle is all that is left, and the
	// job is admitted through Stuck again.
	releaseOther()
	text = <-answer
	if done := w.settle(waitingID); done.Conclusion != ci.ConclusionSuccess {
		t.Fatalf("second run = %s once the unrelated sandbox went, want it admitted:\n%s", done.Conclusion, text)
	}
	if n := strings.Count(tee.String(), "running it anyway rather than waiting for a release that is not coming"); n < 2 {
		t.Fatalf("%d Stuck admissions logged, want one per run", n)
	}
}
