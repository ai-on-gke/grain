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

package demo

// Against a real embedded SQLite store, same discipline as pkg/ui's own
// tests (pkg/ui/client_test.go: "Nothing here is a fake standing in for
// the store").

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/systemlog"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

func TestSeedDemoCoversEveryState(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := ui.Config{
		Actor:         ui.DefaultActor("demo-operator"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
	}
	if err := seedDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedDemo: %v", err)
	}

	client := ui.NewClient(cfg, store)
	tasks, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatalf("listing seeded tasks: %v", err)
	}

	want := []model.State{
		model.StateProposed, model.StateQueued, model.StateRunning,
		model.StateAwaitingReply, model.StateAwaitingSubmit,
		model.StateCompleted, model.StateDeferred, model.StateClosed,
	}
	got := map[model.State]int{}
	presubmits := map[string]bool{}
	reviews := map[string]bool{}
	submits, behind := map[bool]bool{}, false
	for _, task := range tasks {
		got[task.State]++
		if task.Presubmit != nil {
			presubmits[task.Presubmit.State] = true
		}
		if task.CLReview != nil {
			reviews[task.CLReview.State] = true
		}
		if s := task.CLSubmit; s != nil {
			submits[s.CanSubmit] = true
			behind = behind || (s.Behind && s.CanSync)
		}
		// The changelist tasks (a CitC workspace, no repo) aside.
		if task.Repo != repo.String() && len(task.Changelists) == 0 {
			t.Errorf("task %s repo = %q, want %q", task.ID, task.Repo, repo.String())
		}
		// The demo's pull requests are spelled the way a real run records
		// one (model.PullRequestRef.String) -- every link the UI builds
		// reads them that way, and a URL here rendered as
		// github.com/https://github.com/.../pull/undefined
		// (grain/task-1355).
		if task.PullRequest != "" {
			if _, err := model.ParsePullRequestRef(task.PullRequest); err != nil {
				t.Errorf("task %s pull request %q: %v", task.ID, task.PullRequest, err)
			}
		}
	}
	// Every presubmit badge state has a CL task to show it on.
	for _, state := range []string{"passed", "failed", "running", "not_run", "unknown"} {
		if !presubmits[state] {
			t.Errorf("no seeded changelist task shows presubmit %q", state)
		}
	}
	// A CL's Submit both live and disabled, and one behind head to Sync
	// (changelists.md §6-§7).
	if !submits[true] || !submits[false] || !behind {
		t.Errorf("seeded CL Submit states %v, behind %v: want a submittable CL, an unsubmittable one and one behind head", submits, behind)
	}
	// And every review badge state (changelists.md §5).
	for _, state := range []string{"draft", "in_review", "changes_requested", "needs_approval", "approved"} {
		if !reviews[state] {
			t.Errorf("no seeded changelist task shows review state %q", state)
		}
	}
	for _, state := range want {
		if got[state] == 0 {
			t.Errorf("no seeded task reads state %q; every state should have one so the UI can show its card", state)
		}
	}

	var awaiting ui.Task
	for _, task := range tasks {
		if task.State == model.StateAwaitingReply {
			awaiting = task
		}
	}
	detail, err := client.GetTask(ctx, awaiting.ID)
	if err != nil {
		t.Fatalf("getting the awaiting-reply task: %v", err)
	}
	if len(detail.Comments) == 0 {
		t.Error("awaiting-reply task has no comments; the pending question should be visible in the conversation")
	}

	// The running task carries a real prompt, so the demo's own prompt
	// pane (grain/task-91) shows what a dispatch actually hands an agent
	// rather than its "nothing has run yet" placeholder -- which is also
	// what ui/e2e's own prompt test reads.
	//
	// Not the *repairing* one, which is the demo's other running card: a
	// task the merge queue has sent back to work on its own branch, seeded
	// to show the green mark that tells the two apart. That one stands for
	// a second attempt and has no prompt of its own here.
	var running, repairing ui.Task
	for _, task := range tasks {
		if task.State != model.StateRunning {
			continue
		}
		if task.Repairing {
			repairing = task
			continue
		}
		running = task
	}
	if repairing.ID == "" {
		t.Error("no seeded task is being repaired by the merge queue; the UI's green running mark has no card to show")
	}
	prompt, err := client.TaskPrompt(ctx, running.ID)
	if err != nil {
		t.Fatalf("reading the running task's prompt: %v", err)
	}
	if prompt.Prompt == "" || prompt.Attempt != 1 {
		t.Errorf("running task's prompt = %+v, want attempt 1's own prompt recorded", prompt)
	}
	if !strings.Contains(prompt.Prompt, model.BranchName(running.ID)) {
		t.Errorf("prompt = %q, want the branch a real dispatch would name", prompt.Prompt)
	}
}

// The other half of what the demo has to cover, beside the states: the
// fields a deployment declares for itself. Every input on the new-task
// form, every chip on a row, every field row on a task's pane and every
// `custom.<name>` entry in the narrowing bar's menus exists only for a
// deployment that has declared something -- so a demo declaring nothing
// leaves all of it invisible to ui/e2e, which drives `grain demo` and
// nothing else (grain/task-274). This is that set asserted where it is
// produced: one declaration per kind of value, one holding a set, one on
// the rows, and tasks actually carrying values for them.
func TestSeedDemoDeclaresAFieldOfEveryKind(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := ui.Config{
		Actor:         ui.DefaultActor("demo-operator"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
	}
	if err := seedDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedDemo: %v", err)
	}

	fields, err := store.ListCustomFields(ctx)
	if err != nil {
		t.Fatalf("listing the declared fields: %v", err)
	}
	declared := map[string]model.CustomField{}
	kinds := map[model.CustomFieldType]int{}
	var multiple, onRow, withDefault int
	for _, f := range fields {
		declared[f.Name] = f
		kinds[f.FieldType()]++
		if f.Multiple {
			multiple++
		}
		if f.ShowOnRow {
			onRow++
		}
		if f.Default != "" {
			withDefault++
		}
	}
	for _, kind := range model.CustomFieldTypes() {
		if kinds[kind] == 0 {
			t.Errorf("no seeded field is a %s; the control the form renders for one has nothing to show it against", kind)
		}
	}
	if multiple == 0 {
		t.Error("no seeded field holds a set; the multi-value input and the tags comparisons have nothing to show")
	}
	if onRow == 0 {
		t.Error("no seeded field is flagged showOnRow; no list row or board card carries a field chip")
	}
	if withDefault == 0 {
		t.Error("no seeded field declares a default; the new-task form opens on nothing for any of them")
	}
	// Small on purpose -- see demoCustomFields on why the demo's forms
	// are a demonstration rather than a wall of inputs. This is the
	// ceiling that says so out loud.
	if len(fields) > 8 {
		t.Errorf("the demo declares %d fields; the new-task form is also what somebody opens to see what grain is", len(fields))
	}

	client := ui.NewClient(cfg, store)
	tasks, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatalf("listing seeded tasks: %v", err)
	}
	// A declaration with no task carrying a value for it shows an input
	// and nothing else: no chip, no field row, and a filter menu whose
	// choices come from the tasks in view (taskFilters.js's optionsFor)
	// with nothing in it.
	carried := map[string]int{}
	for _, task := range tasks {
		for name, values := range task.CustomFields {
			if len(values) > 0 {
				carried[name]++
			}
		}
	}
	for name := range declared {
		if carried[name] == 0 {
			t.Errorf("no seeded task carries a value for %q; the field is declared and invisible everywhere but the form", name)
		}
	}
	// ...and one value whose declaration is gone, which is the row the
	// task pane keeps for a field the deployment has retired.
	var retired []string
	for name := range carried {
		if _, ok := declared[name]; !ok {
			retired = append(retired, name)
		}
	}
	if len(retired) == 0 {
		t.Error("every seeded value has a declaration behind it; nothing shows the row a retired field leaves on a task")
	}
}

// The two panes that open scrolled to their end -- an attempt's
// transcript and the System pane's log tail -- can only be seen to do so
// against a document longer than the pane (demo.go's own comment on
// demoLogLines/demoTranscriptTurns, and ui/e2e/scroll.spec.js, which is
// what asks a real browser). This is that length asserted where it is
// produced, so a demo trimmed back to a handful of lines fails here
// rather than silently turning that browser test into one that would
// pass against a pane that cannot scroll at all.
const demoScrollableLines = 100

func TestSeedDemoWritesADocumentLongEnoughToScroll(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := ui.Config{
		Actor:         ui.DefaultActor("demo-operator"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
	}
	if err := seedDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedDemo: %v", err)
	}

	client := ui.NewClient(cfg, store)
	tasks, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatalf("listing seeded tasks: %v", err)
	}
	// The same running task TestSeedDemoCoversEveryState reads a prompt
	// off -- not the repairing one, which stands for a second attempt and
	// carries neither.
	var running ui.Task
	for _, task := range tasks {
		if task.State == model.StateRunning && !task.Repairing {
			running = task
		}
	}
	if running.ID == "" {
		t.Fatal("no seeded task is running; the transcript pane has no attempt to open")
	}
	transcript, err := client.AttemptTranscript(ctx, running.ID, 1)
	if err != nil {
		t.Fatalf("reading the running attempt's transcript: %v", err)
	}
	if n := len(strings.Split(transcript, "\n")); n < demoScrollableLines {
		t.Errorf("seeded transcript is %d lines, want at least %d -- a pane that fits its whole document cannot be seen to open at the end", n, demoScrollableLines)
	}

	// The log the System pane tails is not a row in the store: demo()
	// writes it and hands ui.Config.Logs a systemlog.File over it, so it
	// is checked through that same source rather than by reading the file
	// back here.
	path := filepath.Join(t.TempDir(), "demo-daemon.log")
	if err := writeDemoLog(path); err != nil {
		t.Fatalf("writeDemoLog: %v", err)
	}
	lines, err := systemlog.File{Path: path}.Tail(ctx, 500)
	if err != nil {
		t.Fatalf("tailing the seeded log: %v", err)
	}
	if len(lines) < demoScrollableLines {
		t.Errorf("seeded log is %d lines, want at least %d, for the same reason", len(lines), demoScrollableLines)
	}
}

// The Settings pane's Memory tab reads two layers and the documents
// under them (grain/task-333), and a demo has no memory agent to write
// any of it -- so the seed does, and this is what keeps it seeded.
//
// The stale document is the part worth asserting rather than eyeballing:
// the pane marks a note once it is old enough that model.MemoryAge has
// stopped counting it in days, and a demo whose notes were all written
// this week would show that mark to nobody.
// TestSeedDemoMarksEveryMergeBatchMemberBatching: the queue writes the
// overlap re-check state on the change it re-checks, and a task page
// draws its Merge batch panel only for a task in that state, so a member
// the seed left without it opened from the repo page's batch list with
// no batch on it (the 2026-10-23 repository walkthrough).
func TestSeedDemoMarksEveryMergeBatchMemberBatching(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := ui.Config{
		Actor:         ui.DefaultActor("demo-operator"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
	}
	if err := seedDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedDemo: %v", err)
	}
	batches, err := store.ListMergeBatches(ctx)
	if err != nil {
		t.Fatalf("listing merge batches: %v", err)
	}
	if len(batches) == 0 {
		t.Fatal("the demo seeds no merge batch")
	}
	for _, batch := range batches {
		if len(batch.Members) != 1 {
			t.Errorf("batch on %s %s has %d members, want 1 (single-change overlap re-check)",
				batch.Repo, batch.Branch, len(batch.Members))
		}
		for i, member := range batch.Members {
			obs, err := store.GetObservation(ctx, member.TaskID)
			if err != nil {
				t.Fatalf("reading task %s: %v", member.TaskID, err)
			}
			if obs == nil || obs.MergeQueueState != "waiting_ci" || obs.MergeQueueReason != "waiting_ci/grain_running" {
				t.Errorf("batch member %d (task %s) is not in waiting_ci/grain_running: %+v", i, member.TaskID, obs)
				continue
			}
			wantPrefix := "grain CI is re-checking this change on top of " + batch.Branch + " before it merges"
			if !strings.HasPrefix(obs.MergeQueueNote, wantPrefix) {
				t.Errorf("task %s note = %q, want prefix %q", member.TaskID, obs.MergeQueueNote, wantPrefix)
			}
		}
	}
}

func TestSeedDemoWritesMemoryForBothScopes(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := ui.Config{
		Actor:         ui.DefaultActor("demo-operator"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
	}
	if err := seedDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedDemo: %v", err)
	}

	client := ui.NewClient(cfg, store)
	memories, err := client.ListMemories(ctx)
	if err != nil {
		t.Fatalf("listing seeded memories: %v", err)
	}
	scopes := map[string]ui.Memory{}
	for _, m := range memories {
		scopes[m.Repo] = m
	}
	for _, scope := range []string{"", repo.String()} {
		m, ok := scopes[scope]
		if !ok {
			t.Fatalf("no seeded memory for %q; the Memory tab has one of its two layers empty", scope)
		}
		if m.Text == "" || m.UpdatedAt == nil {
			t.Errorf("seeded memory for %q = %+v, want text and a written-at time", scope, m)
		}
	}
	// And the two authors, one of each kind. The pane's whole point since
	// grain/task-394 is that an operator's own correction is legible
	// beside what the memory agent wrote, and a seed where every row had
	// the same author would show one chip and never the other.
	if kind := scopes[""].UpdatedByKind; kind != string(model.PrincipalAgent) {
		t.Errorf("the seeded deployment memory was written by a %q, want the memory agent", kind)
	}
	if kind := scopes[repo.String()].UpdatedByKind; kind != string(model.PrincipalHuman) {
		t.Errorf("the seeded repo memory was written by a %q, want a person editing the pane", kind)
	}

	documents, err := client.ListMemoryDocuments(ctx)
	if err != nil {
		t.Fatalf("listing seeded memory documents: %v", err)
	}
	var stale bool
	for _, d := range documents {
		if d.Summary == "" || d.Length == 0 || d.UpdatedAt == nil {
			t.Errorf("seeded document %q = %+v, want a summary, a length and a written-at time", d.Name, d)
		}
		if d.UpdatedAt != nil && time.Since(*d.UpdatedAt) > 60*24*time.Hour {
			stale = true
		}
	}
	if len(documents) < 2 {
		t.Errorf("seeded %d memory documents, want one under each scope", len(documents))
	}
	if !stale {
		t.Error("no seeded document is old enough for the pane to mark stale, which is the one mark it makes on its own")
	}
}

// The alert flags /api/config raises for a fresh, paused install are
// what -unconfigured and -agent-paused exist for
// (ui/e2e/alerts-panel-layout.spec.js), so they are asserted on the same
// response a browser reads.
func TestDemoScenariosRaiseTheSetupAndPauseBanners(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	stored := model.DefaultConfig()
	stored.AgentBackends = []model.AgentBackend{{ID: "gemini-pro", Framework: model.AgentFrameworkAntigravity}}
	stored.GitConnections = []model.TeamGitConnection{{Credential: "default", Host: "github.com"}}
	if err := store.PutConfig(ctx, stored); err != nil {
		t.Fatalf("storing config: %v", err)
	}

	cfg, err := unconfigureDemo(ctx, store, ui.Config{Actor: ui.DefaultActor("demo-operator")})
	if err != nil {
		t.Fatalf("unconfigureDemo: %v", err)
	}
	if err := forgetDemoGitConnections(ctx, store); err != nil {
		t.Fatalf("forgetDemoGitConnections: %v", err)
	}
	cfg.AgentPause = demoAgentPause(time.Now().UTC())

	rec := httptest.NewRecorder()
	ui.NewServer(withAccessCode(cfg), store).ServeHTTP(rec, presentAccessCode(httptest.NewRequest(http.MethodGet, "/api/config", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config: %d %s", rec.Code, rec.Body)
	}
	var got struct {
		ModelsConfigured         bool `json:"modelsConfigured"`
		GitCredentialsConfigured bool `json:"gitCredentialsConfigured"`
		AgentPause               *struct {
			Paused bool `json:"paused"`
		} `json:"agentPause"`
		Deployment struct {
			HasGitCredential bool              `json:"hasGitCredential"`
			GitConnections   []json.RawMessage `json:"gitConnections"`
		} `json:"deployment"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding /api/config: %v", err)
	}
	if got.ModelsConfigured || got.GitCredentialsConfigured {
		t.Errorf("modelsConfigured=%v gitCredentialsConfigured=%v, want both false on an unconfigured demo",
			got.ModelsConfigured, got.GitCredentialsConfigured)
	}
	if got.AgentPause == nil || !got.AgentPause.Paused {
		t.Errorf("agentPause = %+v, want a pause in force", got.AgentPause)
	}
	// What New task's and Repos' no-git-connection notice reads.
	if got.Deployment.HasGitCredential || len(got.Deployment.GitConnections) != 0 {
		t.Errorf("deployment hasGitCredential=%v gitConnections=%d, want none on an unconfigured demo",
			got.Deployment.HasGitCredential, len(got.Deployment.GitConnections))
	}
}

// -fake-ci is what lets ui/e2e/release-run-ref-checks-walkthrough.spec.js
// reach a release run's ref checks at all: a grain CI behind the real
// server that records a run and never executes it, and acme/widgets main
// recorded as the watched base branch those checks read as reviewed.
func TestFakeCIDemoAcceptsAReleaseRunAndNeverRunsIt(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatalf("storing repo config: %v", err)
	}
	cfg, err := configureFakeCIDemo(ctx, store, ui.Config{Actor: ui.DefaultActor("demo-operator")}, repo)
	if err != nil {
		t.Fatalf("configureFakeCIDemo: %v", err)
	}
	if row, err := store.GetBaseBranchCI(ctx, repo, "main"); err != nil || row == nil {
		t.Fatalf("GetBaseBranchCI(acme/widgets, main) = %v, %v; want the watched row", row, err)
	}
	srv := ui.NewServer(withAccessCode(cfg), store)

	// Over loopback with the access code, as the deployment's own actor
	// -- the way `grain demo`'s setup steps in the e2e specs are taken.
	req := httptest.NewRequest(http.MethodPost, "/api/repos/acme/widgets/release-runs",
		strings.NewReader(`{"branch":"main"}`))
	req.RemoteAddr = "127.0.0.1:40000"
	presentAccessCode(req)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST release-runs: %d %s", rec.Code, rec.Body)
	}
	var run struct {
		ID         string `json:"id"`
		Ref        string `json:"ref"`
		Event      string `json:"event"`
		Conclusion string `json:"conclusion"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatalf("decoding the run: %v", err)
	}
	if run.ID == "" || run.Ref != "main" || run.Event != "release" || run.Conclusion != "" {
		t.Errorf("run = %+v, want a pending release run of main", run)
	}

	got, ok := cfg.CI.Get(run.ID)
	if !ok || got.Conclusion.Done() {
		t.Errorf("Get(%s) = %+v, %v; want it recorded and still pending -- the stub executes nothing", run.ID, got, ok)
	}
	if !cfg.CI.Cancel(run.ID, "walked", "") {
		t.Fatalf("Cancel(%s) = false", run.ID)
	}
	if got, _ := cfg.CI.Get(run.ID); got.Conclusion != "cancelled" {
		t.Errorf("after Cancel, conclusion = %q, want cancelled", got.Conclusion)
	}
}

// -github-sim is what lets ui/e2e/base-branch-existence.spec.js walk the
// base-branch checks against the real server: GitHub's branch listing
// for acme/widgets reads main and release/1.0 out of a real bare repo,
// so a one-shot task on a base the repo does not have is refused, and
// one on a branch it has is filed.
func TestGitHubSimDemoListsBranchesAndRefusesAMissingBase(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg, _, err := configureGitHubSimDemo(t.TempDir(), ui.Config{Actor: ui.DefaultActor("demo-operator"), DefaultTarget: &repo}, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}
	srv := ui.NewServer(withAccessCode(cfg), store)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:40000"
		presentAccessCode(req)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	rec := do(http.MethodGet, "/api/repos/acme/widgets/github-branches", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET github-branches: %d %s", rec.Code, rec.Body)
	}
	var names []string
	if err := json.Unmarshal(rec.Body.Bytes(), &names); err != nil {
		t.Fatalf("decoding the listing: %v", err)
	}
	if got := strings.Join(names, ","); got != "main,release/1.0" {
		t.Errorf("github-branches = %q, want main,release/1.0", got)
	}
	// A repo the sim does not stand for is as unreadable as it is on a
	// demo without the flag.
	if rec := do(http.MethodGet, "/api/repos/acme/gadgets/github-branches", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET acme/gadgets github-branches: %d, want 404", rec.Code)
	}

	rec = do(http.MethodPost, "/api/tasks", `{"title":"t","description":"d","repo":"acme/widgets","base":"no-such-branch"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "does not exist on acme/widgets") {
		t.Errorf("POST a task on no-such-branch: %d %s, want 400 saying it does not exist", rec.Code, rec.Body)
	}
	rec = do(http.MethodPost, "/api/tasks", `{"title":"t","description":"d","repo":"acme/widgets","base":"release/1.0"}`)
	if rec.Code != http.StatusCreated {
		t.Errorf("POST a task on release/1.0: %d %s, want 201", rec.Code, rec.Body)
	}
}

// -github-sim also backs the pull request page a task's "Internal PR"
// link opens (docs/test-runs/2026-10-15-task-intake-inbox-lifecycle.md,
// §4): a branch pushed into the simulated repo compares against its base
// through the daemon's own comparison, merge base first, so the diff is
// the commit the branch carries and nothing the base has.
func TestGitHubSimDemoComparesAPushedBranch(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg, bare, err := configureGitHubSimDemo(t.TempDir(), ui.Config{Actor: ui.DefaultActor("demo-operator"), DefaultTarget: &repo}, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}

	// An agent's push: one commit adding a file on top of release/1.0,
	// and main moving on with a change of its own the branch never saw.
	commitOn := func(parent, path, content, message string) string {
		t.Helper()
		blob, err := demoGitStdin(bare, content, "hash-object", "-w", "--stdin")
		if err != nil {
			t.Fatal(err)
		}
		tree, err := demoGitStdin(bare, "100644 blob "+blob+"\t"+path+"\n", "mktree")
		if err != nil {
			t.Fatal(err)
		}
		commit, err := demoGit(bare, "commit-tree", "-p", parent, "-m", message, "--end-of-options", tree)
		if err != nil {
			t.Fatal(err)
		}
		return commit
	}
	pushed := commitOn("refs/heads/release/1.0", "widgets.go", "package widgets\n\nconst Pushed = true\n", "Add widgets.go")
	if _, err := demoGit(bare, "update-ref", "--", "refs/heads/grain/task-15", pushed); err != nil {
		t.Fatal(err)
	}
	moved := commitOn("refs/heads/main", "main-only.txt", "not on the branch\n", "Main moves on")
	if _, err := demoGit(bare, "update-ref", "--", "refs/heads/main", moved); err != nil {
		t.Fatal(err)
	}

	srv := ui.NewServer(withAccessCode(cfg), store)
	compare := func(repoPath, query string) (*httptest.ResponseRecorder, ui.BranchComparison) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/repos/"+repoPath+"/compare?"+query, nil)
		req.RemoteAddr = "127.0.0.1:40000"
		presentAccessCode(req)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		var got ui.BranchComparison
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding the comparison: %v", err)
			}
		}
		return rec, got
	}

	rec, got := compare("acme/widgets", "base=release%2F1.0&head=grain%2Ftask-15")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET compare release/1.0...grain/task-15: %d %s", rec.Code, rec.Body)
	}
	if got.Base != "release/1.0" || got.Head != "grain/task-15" || got.Merged || got.MergeBase == "" {
		t.Errorf("comparison = %+v, want an open branch off release/1.0 with a merge base", got)
	}
	if !strings.Contains(got.Diff, "diff --git a/widgets.go b/widgets.go") || !strings.Contains(got.Diff, "+const Pushed = true") {
		t.Errorf("diff = %q, want widgets.go added", got.Diff)
	}

	// No base is the repo's default branch; the diff is still only what
	// the branch carries, not main's own later commit.
	rec, got = compare("acme/widgets", "head=grain%2Ftask-15")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET compare ...grain/task-15: %d %s", rec.Code, rec.Body)
	}
	if got.Base != "main" || !strings.Contains(got.Diff, "+const Pushed = true") || strings.Contains(got.Diff, "main-only.txt") {
		t.Errorf("comparison against the default branch = %+v", got)
	}

	// A repo the sim does not stand for is refused rather than asked.
	if rec, _ := compare("acme/gadgets", "head=grain%2Ftask-15"); rec.Code == http.StatusOK {
		t.Errorf("GET acme/gadgets compare: %d %s, want an error", rec.Code, rec.Body)
	}
}

// -github-api sends a team's token to the stub in the clear, so the demo
// takes it only on loopback (grain/task-1995).
func TestDemoGitHubAPIIsLoopbackOnly(t *testing.T) {
	for hostPort, want := range map[string]bool{
		"127.0.0.1:8080":     true,
		"[::1]:8080":         true,
		"localhost:8080":     true,
		"api.github.com:443": false,
		"10.0.0.5:80":        false,
		"127.0.0.1":          false,
		"":                   false,
	} {
		if got := IsLoopbackHostPort(hostPort); got != want {
			t.Errorf("IsLoopbackHostPort(%q) = %v, want %v", hostPort, got, want)
		}
	}
}

// -upgrade-image wires the daemon's own image-mode upgrader, so the
// Upgrade tab's picker lists what the registry really holds and the
// allowlist really refuses (grain/task-2074).
func TestUpgradeDemoListsTheRegistrysVersions(t *testing.T) {
	t.Parallel()
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/x/grain/tags/list" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"name":"x/grain","tags":["v1.4.0","v1.6.0","v1.7.0-rc.1","latest","candidate","main","sha-1234567","v1.5.0"]}`)
	}))
	t.Cleanup(registry.Close)

	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	stored := model.DefaultConfig()
	if err := store.PutConfig(ctx, stored); err != nil {
		t.Fatalf("storing config: %v", err)
	}
	image := strings.TrimPrefix(registry.URL, "http://") + "/x/grain"
	cfg, err := configureUpgradeDemo(t.TempDir(), store, ui.Config{Actor: ui.DefaultActor("demo-operator")}, image, []string{"main", "release/1.6"}, nil)
	if err != nil {
		t.Fatalf("configureUpgradeDemo: %v", err)
	}
	srv := ui.NewServer(withAccessCode(cfg), store)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:40000"
		presentAccessCode(req)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	rec := do(http.MethodGet, "/api/upgrade/targets", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/upgrade/targets: %d %s", rec.Code, rec.Body)
	}
	var targets upgrade.Targets
	if err := json.Unmarshal(rec.Body.Bytes(), &targets); err != nil {
		t.Fatalf("decoding the targets: %v", err)
	}
	if targets.VersionsError != "" {
		t.Errorf("versionsError = %q, want none", targets.VersionsError)
	}
	var got []string
	for _, tg := range targets.Targets {
		got = append(got, fmt.Sprintf("%s:%s:%v", tg.Name, tg.Kind, tg.Allowed))
	}
	want := "latest:channel:false candidate:channel:false main:channel:true " +
		"v1.6.0:version:false v1.5.0:version:false v1.4.0:version:false release/1.6:allowed:true"
	if strings.Join(got, " ") != want {
		t.Errorf("targets = %s\nwant      %s", strings.Join(got, " "), want)
	}

	// The allowlist is the real gate, not just the list's marks.
	rec = do(http.MethodPost, "/api/upgrade", `{"branch":"v1.6.0"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "-upgrade-allowed-branches") {
		t.Errorf("POST /api/upgrade v1.6.0: %d %s, want 400 naming -upgrade-allowed-branches", rec.Code, rec.Body)
	}
}

// -sensitive-hold files its review on the held change's branch, which
// -github-sim's base check would refuse unless that branch is pushed to
// the sim first (grain/task-3343).
func TestSensitiveHoldDemoSeedsWithGitHubSim(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg, bare, err := configureGitHubSimDemo(t.TempDir(), ui.Config{Actor: ui.DefaultActor("demo-operator"), DefaultTarget: &repo}, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}
	if err := seedSensitiveHoldDemo(ctx, store, cfg, repo, bare); err != nil {
		t.Fatalf("seedSensitiveHoldDemo with -github-sim: %v", err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	var held, review *model.Task
	for i := range tasks {
		switch tasks[i].Title {
		case "Charge in cents":
			held = &tasks[i]
		case "Security review: Charge in cents":
			review = &tasks[i]
		}
	}
	if held == nil || review == nil {
		t.Fatalf("seeded tasks = %v, want the held change and its review", tasks)
	}
	if want := model.BranchName(held.ID); review.Base != want {
		t.Errorf("review base = %q, want %q", review.Base, want)
	}
	names, err := ui.NewClient(cfg, store).ListGitHubBranches(ctx, repo)
	if err != nil {
		t.Fatalf("listing the sim's branches: %v", err)
	}
	if !slices.Contains(names, model.BranchName(held.ID)) {
		t.Errorf("sim branches = %v, want the held change's %s pushed", names, model.BranchName(held.ID))
	}
}

// -review-questions files its review on the held change's branch, which
// -github-sim's base check would refuse unless that branch is pushed to
// the sim first.
func TestReviewQuestionsDemoSeedsWithGitHubSim(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg, bare, err := configureGitHubSimDemo(t.TempDir(), ui.Config{Actor: ui.DefaultActor("demo-operator"), DefaultTarget: &repo}, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}
	if err := seedReviewQuestionsDemo(ctx, store, cfg, repo, bare); err != nil {
		t.Fatalf("seedReviewQuestionsDemo with -github-sim: %v", err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	var held, review *model.Task
	for i := range tasks {
		switch tasks[i].Title {
		case "Publish both daemon images":
			held = &tasks[i]
		case "Review: Publish both daemon images":
			review = &tasks[i]
		}
	}
	if held == nil || review == nil {
		t.Fatalf("seeded tasks = %v, want the held change and its review", tasks)
	}
	if want := model.BranchName(held.ID); review.Base != want {
		t.Errorf("review base = %q, want %q", review.Base, want)
	}
	if q, ok := model.ReviewQuestionsOf(*held); !ok || q.ReviewTaskID != review.ID {
		t.Errorf("ReviewQuestionsOf(held) = %+v, %v; want review %s", q, ok, review.ID)
	}
	names, err := ui.NewClient(cfg, store).ListGitHubBranches(ctx, repo)
	if err != nil {
		t.Fatalf("listing the sim's branches: %v", err)
	}
	if !slices.Contains(names, model.BranchName(held.ID)) {
		t.Errorf("sim branches = %v, want the held change's %s pushed", names, model.BranchName(held.ID))
	}
}

// TestSeedDemoTimesItsSchedulesFromNow pins the seeded schedules to the
// real clock rather than the backdated one the seeded tasks are filed
// with. Timed from the last task's createdAt (40 minutes ago), "daily at
// 00:00" seeded in the first 40 minutes of a UTC day came out already
// due, so a -schedules demo fired it at start and the standing work
// switch walkthrough found its open task holding the schedule instead of
// the switch -- every night, for any run that straddled midnight.
func TestSeedDemoTimesItsSchedulesFromNow(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := ui.Config{
		Actor:         ui.DefaultActor("demo-operator"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
	}
	start := time.Now().UTC().Truncate(time.Second)
	if err := seedDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedDemo: %v", err)
	}

	schedules, err := store.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("listing seeded schedules: %v", err)
	}
	if len(schedules) == 0 {
		t.Fatal("seedDemo seeded no schedules")
	}
	for _, s := range schedules {
		if s.CreatedAt.Before(start) {
			t.Errorf("schedule %s created at %s, before seeding began at %s: it was timed from a seeded task's backdated clock",
				s.ID, s.CreatedAt.UTC().Format(time.RFC3339), start.Format(time.RFC3339))
		}
		if !s.NextRunAt.After(start) {
			t.Errorf("schedule %s is due at %s, already gone by when seeding began at %s",
				s.ID, s.NextRunAt.UTC().Format(time.RFC3339), start.Format(time.RFC3339))
		}
	}
}

// -merge-rules, -sensitive-hold and -mechanical-port each seed part of
// acme/widgets' .grain/ declarations on top of seedDemo's declared template,
// suite and schedule. Because Store.SyncRepoDeclarations replaces a
// repository's declarations wholesale, each scenario has to merge into what is
// already there rather than wiping the earlier scenarios' rows and error.
func TestDemoScenarioDeclarationsCompose(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := ui.Config{
		Actor:         ui.DefaultActor("demo-operator"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
	}
	if err := seedDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedDemo: %v", err)
	}
	if err := seedMergeRulesDemo(ctx, store, cfg, repo); err != nil {
		t.Fatalf("seedMergeRulesDemo: %v", err)
	}
	if err := seedSensitiveHoldDemo(ctx, store, cfg, repo, ""); err != nil {
		t.Fatalf("seedSensitiveHoldDemo: %v", err)
	}
	if err := seedMechanicalPortDemo(ctx, store, cfg, repo); err != nil {
		t.Fatalf("seedMechanicalPortDemo: %v", err)
	}

	srv := ui.NewServer(withAccessCode(cfg), store)
	get := func(path string, dst any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "127.0.0.1:40000"
		presentAccessCode(req)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
			t.Fatalf("decoding %s: %v", path, err)
		}
	}

	var templates []ui.Template
	get("/api/templates", &templates)
	wantTmplID := model.RepoDeclaredID(model.DeclaredTemplate, repo, "nightly")
	if !slices.ContainsFunc(templates, func(tmpl ui.Template) bool {
		return tmpl.ID == wantTmplID && tmpl.Name == "nightly-maintenance" && tmpl.DeclaredIn != nil
	}) {
		t.Errorf("GET /api/templates = %+v, missing declared template %s", templates, wantTmplID)
	}

	var suites []ui.Suite
	get("/api/suites", &suites)
	wantSuiteID := model.RepoDeclaredID(model.DeclaredSuite, repo, "smoke")
	if !slices.ContainsFunc(suites, func(s ui.Suite) bool {
		return s.ID == wantSuiteID && s.Name == "Smoke Verification Suite" && s.DeclaredIn != nil
	}) {
		t.Errorf("GET /api/suites = %+v, missing declared suite %s", suites, wantSuiteID)
	}

	var schedules []ui.Schedule
	get("/api/schedules", &schedules)
	wantSchedID := model.RepoDeclaredID(model.DeclaredSchedule, repo, "nightly")
	if !slices.ContainsFunc(schedules, func(s ui.Schedule) bool {
		return s.ID == wantSchedID && s.Title == "Nightly Repo Maintenance Sweep" && s.DeclaredIn != nil
	}) {
		t.Errorf("GET /api/schedules = %+v, missing declared schedule %s", schedules, wantSchedID)
	}

	var repoCfg ui.RepoDefaults
	get("/api/repos/acme/widgets/settings", &repoCfg)
	if !repoCfg.Declarations {
		t.Error("declarations = false, want true")
	}
	status := repoCfg.DeclarationsStatus
	if status == nil {
		t.Fatal("declarationsStatus is nil")
	}
	if !slices.Equal(status.Templates, []string{".grain/templates/nightly.yml"}) {
		t.Errorf("declarationsStatus.templates = %v, want [.grain/templates/nightly.yml]", status.Templates)
	}
	if !slices.Equal(status.Suites, []string{".grain/suites/smoke.yml"}) {
		t.Errorf("declarationsStatus.suites = %v, want [.grain/suites/smoke.yml]", status.Suites)
	}
	if !slices.Equal(status.Schedules, []string{".grain/schedules/nightly.yml"}) {
		t.Errorf("declarationsStatus.schedules = %v, want [.grain/schedules/nightly.yml]", status.Schedules)
	}
	if !slices.Equal(status.Triggers, []string{".grain/triggers/payments.yml"}) {
		t.Errorf("declarationsStatus.triggers = %v, want [.grain/triggers/payments.yml]", status.Triggers)
	}
	if status.MergeRules != ".grain/merge-rules.yml" {
		t.Errorf("declarationsStatus.mergeRules = %q, want .grain/merge-rules.yml", status.MergeRules)
	}
	if !slices.ContainsFunc(status.Warnings, func(w string) bool { return strings.Contains(w, ".grain/sensitive.yml") }) {
		t.Errorf("declarationsStatus.warnings = %v, missing .grain/sensitive.yml warning", status.Warnings)
	}
	if !strings.Contains(status.Error, ".grain/triggers/broken.yml") {
		t.Errorf("declarationsStatus.error = %q, want .grain/triggers/broken.yml failure", status.Error)
	}
	if !strings.HasPrefix(status.Commit, "0f1e2d3c4b5a") {
		t.Errorf("declarationsStatus.commit = %q, want prefix 0f1e2d3c4b5a", status.Commit)
	}
	if !slices.ContainsFunc(repoCfg.PortDirections, func(d ui.RepoPortDirection) bool {
		return d.From == "main" && d.To == "candidate" && slices.Equal(d.Types, []string{"fix", "revert"})
	}) {
		t.Errorf("portDirections = %+v, want main->candidate widened to [fix revert]", repoCfg.PortDirections)
	}
}

// -observability runs after demo.Run has stored the deployment's git
// connection, capability servers and sandbox backend in grain_config, so
// configureObservabilityDemo must mutate that stored row rather than
// replacing it with a fresh DefaultConfig() (grain/task-1216).
func TestObservabilityDemoPreservesStoredConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	store.SingleTenant = true
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := ui.Config{
		Actor:         ui.DefaultActor("demo-operator"),
		DefaultTarget: &repo,
		Capabilities:  append(ui.OfferedCapabilities(), ui.APICapabilities()...),
	}
	storedCfg := model.DefaultConfig()
	storedCfg.GitConnections = []model.TeamGitConnection{{Credential: "default"}}
	storedCfg.SandboxBackend = "container"
	storedCfg.GitHubHost = "127.0.0.1:8080"
	storedCfg.GitHubInsecureHTTP = true
	capabilityServers, err := configureCapabilityServerDemo(ctx, &storedCfg, func(string) (string, bool) {
		return "", false
	}, store)
	if err != nil {
		t.Fatalf("configureCapabilityServerDemo: %v", err)
	}
	cfg.CapabilityServers = capabilityServers
	cfg.Wake = func() {
		if stored, err := store.GetConfig(ctx); err == nil && stored != nil {
			capabilityServers.Update(stored.MCPServers)
		}
	}
	if err := store.PutConfig(ctx, storedCfg); err != nil {
		t.Fatalf("storing initial config: %v", err)
	}

	cfg, _, err = configureObservabilityDemo(ctx, dir, store, cfg)
	if err != nil {
		t.Fatalf("configureObservabilityDemo: %v", err)
	}

	got, err := store.GetConfig(ctx)
	if err != nil || got == nil {
		t.Fatalf("GetConfig: %v, %v", got, err)
	}
	if got.SandboxOvercommit.Mode != model.OvercommitAuto {
		t.Errorf("SandboxOvercommit.Mode = %q, want %q", got.SandboxOvercommit.Mode, model.OvercommitAuto)
	}
	if !slices.Equal(got.GitConnections, storedCfg.GitConnections) {
		t.Errorf("GitConnections = %+v, want %+v", got.GitConnections, storedCfg.GitConnections)
	}
	if got.SandboxBackend != storedCfg.SandboxBackend {
		t.Errorf("SandboxBackend = %q, want %q", got.SandboxBackend, storedCfg.SandboxBackend)
	}
	if got.GitHubHost != storedCfg.GitHubHost || !got.GitHubInsecureHTTP {
		t.Errorf("GitHubHost=%q GitHubInsecureHTTP=%v, want %q true", got.GitHubHost, got.GitHubInsecureHTTP, storedCfg.GitHubHost)
	}
	for _, id := range []string{DemoCapabilityID, DemoGCPCapabilityID} {
		if _, ok := got.MCPServers[id]; !ok {
			t.Errorf("MCPServers = %v, missing %s", got.MCPServers, id)
		}
	}

	srv := ui.NewServer(withAccessCode(cfg), store)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:40000"
		presentAccessCode(req)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	rec := do(http.MethodPut, "/api/settings", `{"maxConcurrentTasks":4}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings: %d %s", rec.Code, rec.Body)
	}
	rec = do(http.MethodGet, "/api/capability-servers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/capability-servers: %d %s", rec.Code, rec.Body)
	}
	var servers []ui.CapabilityServer
	if err := json.Unmarshal(rec.Body.Bytes(), &servers); err != nil {
		t.Fatalf("decoding capability servers: %v", err)
	}
	for _, id := range []string{DemoCapabilityID, DemoGCPCapabilityID} {
		if !slices.ContainsFunc(servers, func(s ui.CapabilityServer) bool { return s.ID == id }) {
			t.Errorf("GET /api/capability-servers = %+v, missing %s", servers, id)
		}
	}
	rec = do(http.MethodGet, "/api/capability-methods", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/capability-methods: %d %s", rec.Code, rec.Body)
	}
	var methods []ui.CapabilityMethod
	if err := json.Unmarshal(rec.Body.Bytes(), &methods); err != nil {
		t.Fatalf("decoding capability methods: %v", err)
	}
	for _, want := range []string{"cloud-key", "cloud-viewer", "gcp-key"} {
		if !slices.ContainsFunc(methods, func(m ui.CapabilityMethod) bool { return m.Method == want }) {
			t.Errorf("after PUT /api/settings, methods = %+v, missing %s", methods, want)
		}
	}
}

func TestDemoForceMergeOnRunningTaskAndStackedBaseRetargeting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := ui.Config{
		Actor:              ui.DefaultActor("demo-operator"),
		DefaultTarget:      &repo,
		Capabilities:       append(ui.OfferedCapabilities(), ui.APICapabilities()...),
		GitHubPullRequests: demoPullRequests{repo: repo, store: store},
	}
	cfg, err = configureFakeCIDemo(ctx, store, cfg, repo)
	if err != nil {
		t.Fatalf("configureFakeCIDemo: %v", err)
	}
	cfg, bare, err := configureGitHubSimDemo(dir, cfg, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}
	if err := seedDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedDemo: %v", err)
	}
	if err := seedMergeRulesDemo(ctx, store, cfg, repo); err != nil {
		t.Fatalf("seedMergeRulesDemo: %v", err)
	}

	srv := ui.NewServer(withAccessCode(cfg), store)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:40000"
		presentAccessCode(req)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	// Start a pending fake-ci run on seeded running task 8 (acme/widgets#104).
	initialHead := fmt.Sprintf("%040x", 104)
	rec := do(http.MethodPost, "/api/ci/runs", fmt.Sprintf(`{"taskId":"8","commit":%q}`, initialHead))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/ci/runs on task 8: %d %s", rec.Code, rec.Body)
	}
	var startedRun struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &startedRun); err != nil {
		t.Fatalf("decoding started CI run: %v", err)
	}

	// Advance task 8's branch in githubsim so the reviewed initialHead is stale.
	tree, err := demoGit(bare, "mktree")
	if err != nil {
		t.Fatal(err)
	}
	movedHead, err := demoGit(bare, "commit-tree", "-m", "Advance task-8", "--end-of-options", tree)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := demoGit(bare, "update-ref", "--", "refs/heads/grain/task-8", movedHead); err != nil {
		t.Fatal(err)
	}

	// Force-merging the stale head is refused because the branch head moved.
	rec = do(http.MethodPost, "/api/tasks/8/force-merge", fmt.Sprintf(`{"head":%q,"reason":"stale check"}`, initialHead))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "has moved since you looked") {
		t.Fatalf("POST /api/tasks/8/force-merge stale head: %d %s, want 400 naming moved head", rec.Code, rec.Body)
	}

	// Force-merging the current head succeeds, stops the live run, and cancels in-flight CI.
	rec = do(http.MethodPost, "/api/tasks/8/force-merge", fmt.Sprintf(`{"head":%q,"reason":"operator override"}`, movedHead))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/tasks/8/force-merge current head: %d %s", rec.Code, rec.Body)
	}
	var forced ui.TaskDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &forced); err != nil {
		t.Fatalf("decoding force-merged task 8: %v", err)
	}
	obs8, err := store.GetObservation(ctx, "8")
	if err != nil {
		t.Fatal(err)
	}
	if forced.State != model.StateCompleted || obs8.ForceMergeHeadSHA != movedHead {
		t.Errorf("task 8 after force-merge: state=%q forceMergeHeadSHA=%q, want completed and %q",
			forced.State, obs8.ForceMergeHeadSHA, movedHead)
	}
	if run, ok := cfg.CI.Get(startedRun.ID); !ok || run.Conclusion != "cancelled" || run.CancelReason != "the merge of acme/widgets#104 was forced" || run.CancelledBy != "" {
		t.Errorf("in-flight CI run after force-merge = %+v, %v; want cancelled by system", run, ok)
	}

	// Stacked review task retargets Base to main and clears AutoMerge when upstream merges.
	rec = do(http.MethodPost, "/api/tasks", `{"title":"Upstream feature","description":"d","repo":"acme/widgets","base":"main","approved":true,"autoMerge":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating upstream task: %d %s", rec.Code, rec.Body)
	}
	var upstream ui.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &upstream); err != nil {
		t.Fatal(err)
	}
	upstreamBranch := model.BranchName(upstream.ID)
	if err := pushDemoGitHubSimBranch(bare, upstreamBranch); err != nil {
		t.Fatal(err)
	}
	rec = do(http.MethodPost, "/api/tasks", fmt.Sprintf(`{"title":"Stacked review","description":"d","repo":"acme/widgets","base":%q,"dependsOn":[%q],"approved":true,"autoMerge":true}`, upstreamBranch, upstream.ID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating stacked task: %d %s", rec.Code, rec.Body)
	}
	var stacked ui.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &stacked); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTask(ctx, stacked.ID, func(tk *model.Task) error {
		tk.Origin.Reason = model.ReasonReview
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.ObserveField(ctx, upstream.ID, now, func(o *model.Observation) {
		o.CompletedAt = &now
		o.PrMergedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := demoGit(bare, "update-ref", "-d", "--", "refs/heads/"+upstreamBranch); err != nil {
		t.Fatal(err)
	}
	rec = do(http.MethodPost, "/api/ci/runs", fmt.Sprintf(`{"taskId":%q,"commit":%q}`, stacked.ID, movedHead))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/ci/runs on stacked task: %d %s", rec.Code, rec.Body)
	}
	updatedStacked, err := store.GetTask(ctx, stacked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedStacked.Base != "main" || updatedStacked.AutoMerge {
		t.Errorf("stacked task after upstream merge: base=%q autoMerge=%v, want base=main autoMerge=false",
			updatedStacked.Base, updatedStacked.AutoMerge)
	}
}

// seedDemoNotifications raises release.pending:v2.4.0 on model.GrainSourceRepo(),
// which is not in TargetRepos; configureFakeCIDemo enables grain CI for that
// repository so approving or rejecting the seeded pending release works out of
// the box while leaving acme/widgets' CI mode unset for its own refusal checks.
func TestFakeCIDemoPendingReleaseApproval(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := ui.Config{
		Actor:         ui.DefaultActor("demo-operator"),
		DefaultTarget: &repo,
		Capabilities:  append(ui.OfferedCapabilities(), ui.APICapabilities()...),
	}
	cfg, err = configureFakeCIDemo(ctx, store, cfg, repo)
	if err != nil {
		t.Fatalf("configureFakeCIDemo: %v", err)
	}
	if err := seedDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedDemo: %v", err)
	}

	srv := ui.NewServer(withAccessCode(cfg), store)
	do := func(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:40000"
		presentAccessCode(req)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	// acme/widgets still has CI mode unset until walked.
	rec := do(http.MethodPost, "/api/repos/acme/widgets/release-runs", `{"branch":"main"}`, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "grain CI is not enabled for acme/widgets") {
		t.Fatalf("POST /api/repos/acme/widgets/release-runs: %d %s, want 403", rec.Code, rec.Body)
	}

	grain := model.GrainSourceRepo()
	approvePath := fmt.Sprintf("/api/repos/%s/%s/pending-releases/v2.4.0/approve", grain.Owner, grain.Name)
	rejectPath := fmt.Sprintf("/api/repos/%s/%s/pending-releases/v2.4.0/reject", grain.Owner, grain.Name)

	// Agent callers (even with api:releases:write attached) are refused.
	rec = do(http.MethodPost, "/api/tasks/1/capabilities", `{"id":"api:releases:write","attach":true}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("attaching api:releases:write to task 1: %d %s", rec.Code, rec.Body)
	}
	rec = do(http.MethodPost, approvePath, "", map[string]string{"X-Grain-Acting-Task": "1"})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "only a person may approve a release") {
		t.Fatalf("agent approve: %d %s, want 403 only a person may approve a release", rec.Code, rec.Body)
	}

	// Human approval starts promote-release.yml:promote and clears the notification.
	rec = do(http.MethodPost, approvePath, "", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("human approve: %d %s, want 202", rec.Code, rec.Body)
	}
	var decision ui.ReleaseDecision
	if err := json.Unmarshal(rec.Body.Bytes(), &decision); err != nil {
		t.Fatalf("decoding approved decision: %v", err)
	}
	if !decision.Approve || decision.Version != "v2.4.0" {
		t.Errorf("decision = %+v, want approve v2.4.0", decision)
	}
	if decision.Run.Repo != grain.String() || decision.Run.Ref != "main" || decision.Run.Event != "workflow_dispatch" || decision.Run.RequestedBy != "demo-operator" {
		t.Errorf("approved run = %+v, want %s main workflow_dispatch by demo-operator", decision.Run, grain)
	}
	if len(decision.Run.Jobs) != 1 || decision.Run.Jobs[0].JobID != ".grain/workflows/promote-release.yml:promote" || decision.Run.Jobs[0].WorkflowPath != ".grain/workflows/promote-release.yml" {
		t.Errorf("approved run jobs = %+v, want [.grain/workflows/promote-release.yml:promote]", decision.Run.Jobs)
	}

	ns, err := store.ListNotifications(ctx, model.NotificationFilter{})
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	var pending *model.Notification
	for i := range ns {
		if ns[i].Subject == "release.pending:v2.4.0" {
			pending = &ns[i]
			break
		}
	}
	if pending == nil || pending.ClearedAt == nil || pending.ClearedBy != "human:demo-operator" {
		t.Errorf("release.pending:v2.4.0 after approve = %+v, want cleared by human:demo-operator", pending)
	}

	// Human rejection starts promote-release.yml:reject.
	rec = do(http.MethodPost, rejectPath, "", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("human reject: %d %s, want 202", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &decision); err != nil {
		t.Fatalf("decoding rejected decision: %v", err)
	}
	if decision.Approve || len(decision.Run.Jobs) != 1 || decision.Run.Jobs[0].JobID != ".grain/workflows/promote-release.yml:reject" || decision.Run.Jobs[0].WorkflowPath != ".grain/workflows/promote-release.yml" {
		t.Errorf("rejected run jobs = %+v, want [.grain/workflows/promote-release.yml:reject]", decision.Run.Jobs)
	}
}
