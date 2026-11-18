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

package ui_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ai-on-gke/grain/pkg/repodecl"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type shadowForge struct {
	github.Client // Every unexpected legacy/action method panics.
	detail        github.PullRequestDetail
	base          string
	checks        []github.CheckRun
	workflows     []github.WorkflowRun
	rules         []byte
	fail          error
	complete      bool
	readHead      string
	policyRef     string
	pushes        bool
	reads         int
	onChecks      func()
}

func (f *shadowForge) GetPullRequest(string, string, int) (github.PullRequestDetail, error) {
	f.reads++
	d := f.detail
	if f.pushes && f.reads%2 == 0 {
		d.HeadSHA = "H2"
	}
	return d, nil
}
func (f *shadowForge) GetBranchHead(string, string, string) (*github.BranchHead, error) {
	return &github.BranchHead{SHA: f.base}, nil
}
func (f *shadowForge) ListPullRequestFiles(string, string, int) ([]string, error) {
	return []string{"main.go"}, nil
}
func (f *shadowForge) ReadFile(_, _, ref, path string) ([]byte, bool, error) {
	f.policyRef = ref
	if path == model.MergeRulesPath && len(f.rules) > 0 {
		return f.rules, true, nil
	}
	return nil, false, nil
}
func (f *shadowForge) ReadCheckCollection(_ context.Context, _, _, head string, limit int) (github.CheckCollection, error) {
	f.readHead = head
	if f.onChecks != nil {
		f.onChecks()
	}
	return github.CheckCollection{Checks: f.checks, Workflows: f.workflows, ChecksComplete: f.complete, Complete: f.complete}, f.fail
}
func newShadowForge() *shadowForge {
	yes := true
	success := "success"
	return &shadowForge{detail: github.PullRequestDetail{Number: 7, HeadSHA: "H1", HeadRef: "feature", BaseRef: "main", State: "open", Mergeable: &yes}, base: "B1", complete: true, checks: []github.CheckRun{{ID: 1, CheckSuiteID: 1, Name: "go", App: "trusted", HeadSHA: "H1", Status: "completed", Conclusion: &success}}}
}

func TestGateChecksOrdinaryAPIEventsAndNoninterference(t *testing.T) {
	srv, c := testServer(t)
	ctx := context.Background()
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "Repair checks"})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Store.UpdateTask(ctx, task.ID, func(task *model.Task) error {
		task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#7"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.Config.GateObservationTaskIDs = []string{task.ID}
	forge := newShadowForge()
	acceptShadowRules(t, c, forge)
	c.Config.GitHubPullRequests = forge
	now := baseTime.Add(time.Minute)
	c.Now = func() time.Time { return now }
	read := func(want string) ui.TaskDetail {
		t.Helper()
		before, _ := c.Store.GetObservation(ctx, task.ID)
		stored, _ := c.Store.GetTask(ctx, task.ID)
		wake, commits := c.Store.Changed(), c.Store.CommitCount()
		code, body := serve(t, srv, http.MethodGet, "/api/tasks/"+task.ID, "")
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		var d ui.TaskDetail
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			t.Fatal(err)
		}
		if d.GateObservations == nil || len(d.GateObservations.Results) != 4 || d.GateObservations.Results[2].Result.Status != want {
			t.Fatalf("want %s: %+v", want, d.GateObservations)
		}
		after, _ := c.Store.GetObservation(ctx, task.ID)
		afterTask, _ := c.Store.GetTask(ctx, task.ID)
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(stored, afterTask) || commits != c.Store.CommitCount() {
			t.Fatal("ordinary GET observation mutated legacy state")
		}
		select {
		case <-wake:
			t.Fatal("shadow read signalled legacy wake")
		default:
		}
		if forge.readHead != "H1" || forge.policyRef != forge.base {
			t.Fatal("head or base policy read was not exact", forge.readHead, forge.policyRef)
		}
		for _, r := range d.GateObservations.Results {
			if len(r.Result.Work) > 0 || len(r.Result.Actions) > 0 {
				t.Fatal("actionful output")
			}
		}
		return d
	}
	d := read("met")
	if path := os.Getenv("GRAIN_CHECKS_API"); path != "" {
		raw, _ := json.Marshal(d.GateObservations)
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A delayed inherited check is a wait, never empty success.
	forge.rules = []byte("defaults:\n  ci:\n    mode: github\n    github: [go, lint]\n")
	now = now.Add(time.Second)
	read("unknown")
	acceptShadowRules(t, c, forge)
	now = now.Add(time.Second)
	read("pending")
	// Latest attempts replace successes, including cancelled attempts.
	forge.rules = nil
	acceptShadowRules(t, c, forge)
	cancelled := "cancelled"
	retry := forge.checks[0]
	retry.ID = 2
	retry.Conclusion = &cancelled
	forge.checks = append(forge.checks, retry)
	now = now.Add(time.Second)
	read("pending")
	forge.checks[1].Conclusion = forge.checks[0].Conclusion
	now = now.Add(time.Second)
	read("met")
	// Complete collection includes workflow startup failures with no check jobs.
	forge.workflows = []github.WorkflowRun{{ID: 3, Path: ".github/workflows/broken.yml", Event: "push", HeadBranch: "feature", HeadSHA: "H1", RunAttempt: 1, Status: "completed", Conclusion: "startup_failure"}}
	now = now.Add(time.Second)
	read("unmet")
	forge.workflows = nil
	// Conflicts and behind do not masquerade as CI failures.
	no := false
	forge.detail.Mergeable = &no
	forge.detail.MergeableState = "behind"
	now = now.Add(time.Second)
	d = read("met")
	if !strings.Contains(*d.GateObservations.Results[2].Result.HumanText, "merge conflicts") {
		t.Fatal("merge precondition missing")
	}
	// A stale overlap result cannot be described as current readiness.
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err = c.Store.PutMergeBatch(ctx, model.MergeBatch{Repo: repo, Branch: "main", TaskID: task.ID, BaseCommit: "B0", Commit: "combined", Members: []model.MergeBatchMember{{TaskID: task.ID, Number: 7, HeadSHA: "H1"}}, Conclusion: "success", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	d = read("met")
	if !strings.Contains(*d.GateObservations.Results[2].Result.HumanText, "recheck is stale") {
		t.Fatal("stale recheck was trusted")
	}
	forge.fail = &github.Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by integration"}`)}
	forge.complete = false
	now = now.Add(time.Second)
	read("unknown")
	forge.fail = errors.New("sensitive credential material")
	now = now.Add(time.Second)
	d = read("error")
	raw, _ := json.Marshal(d.GateObservations)
	if strings.Contains(string(raw), "sensitive credential") {
		t.Fatal("raw source error leaked")
	}
	forge.fail = nil
	now = now.Add(2 * time.Second)
	forge.complete = true
	forge.pushes = true
	now = now.Add(time.Second)
	read("unknown")
}

func TestGateChecksGrainBaseDefinitionsRechecksAndBothSources(t *testing.T) {
	srv, c := testServer(t)
	ctx := context.Background()
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "Fix the required suite"})
	if err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err = c.Store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#7"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = c.Store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	forge := newShadowForge()
	c.Config.GitHubPullRequests = forge
	c.Config.GateObservationTaskIDs = []string{task.ID}
	now := baseTime.Add(time.Minute)
	c.Now = func() time.Time { return now }
	body := "on: push\njobs:\n  go:\n    steps:\n      - run: go test ./...\n"
	definitions, err := ci.ParseDeclared([]ci.DeclaredWorkflow{{Path: ".grain/workflows/tests.yml", Text: body}})
	if err != nil {
		t.Fatal(err)
	}
	definitionReads := 0
	c.Config.GateCheckSource = ci.SourceFunc(func(_ context.Context, _, ref string) ([]ci.Workflow, string, error) {
		definitionReads++
		if ref != "main" {
			t.Errorf("read ref %s", ref)
		}
		return definitions, forge.base, nil
	})
	runner := &fakeCIRunner{runs: []ci.Run{{ID: "first", Repo: repo.String(), Commit: "H1", Conclusion: ci.ConclusionSuccess, Jobs: []ci.JobRun{{WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Name: "go", Conclusion: ci.ConclusionSuccess, FinishedAt: &now}}}}}
	c.Config.CI = runner
	status := model.CIStatus{RunID: "first", Commit: "H1", Conclusion: "success"}
	update := func() {
		t.Helper()
		if err := c.Store.ObserveField(ctx, task.ID, now, func(o *model.Observation) { st := status; o.CI = &st }); err != nil {
			t.Fatal(err)
		}
	}
	read := func(want string) *gates.ObservationView {
		t.Helper()
		code, body := serve(t, srv, http.MethodGet, "/api/tasks/"+task.ID, "")
		if code != 200 {
			t.Fatal(code, body)
		}
		var d ui.TaskDetail
		_ = json.Unmarshal([]byte(body), &d)
		if d.GateObservations == nil || d.GateObservations.Results[2].Result.Status != want {
			t.Fatalf("want %s: %+v", want, d.GateObservations)
		}
		if len(runner.started) > 0 || len(runner.cancelled) > 0 {
			t.Fatal("shadow dispatched or cancelled CI")
		}
		return d.GateObservations
	}
	update()
	read("met")
	read("met")
	if definitionReads != 1 {
		t.Fatal("definition read was not coalesced")
	}
	// Base-owned definitions changing invalidate the required set even with a
	// successful status from this same head.
	next, err := ci.ParseDeclared([]ci.DeclaredWorkflow{{Path: ".grain/workflows/tests.yml", Text: body + "  lint:\n    steps:\n      - run: true\n"}})
	if err != nil {
		t.Fatal(err)
	}
	definitions = next
	forge.base = "B2"
	now = now.Add(time.Second)
	read("pending")
	// Same-head recheck overlays the original success; replacement/cancellation
	// must never erase the full-suite requirement.
	definitions = definitions[:1]
	forge.base = "B3"
	runner.runs = append(runner.runs, ci.Run{ID: "retry", Repo: repo.String(), Commit: "H1", Conclusion: ci.ConclusionCancelled, Jobs: []ci.JobRun{{WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionCancelled, FinishedAt: &now}}})
	status.RunID = "retry"
	status.RecheckOf = "first"
	status.Conclusion = "cancelled"
	now = now.Add(time.Second)
	update()
	read("pending")
	// A stale original run cannot fill the recheck's missing evidence.
	runner.runs[0].Commit = "H0"
	now = now.Add(time.Second)
	read("unknown")
	runner.runs[0].Commit = "H1"
	runner.runs[1].Conclusion = ci.ConclusionSuccess
	runner.runs[1].Jobs[0].Conclusion = ci.ConclusionSuccess
	status.Conclusion = "success"
	now = now.Add(time.Second)
	update()
	read("pending") // base still requires lint
	// Both sources are unioned, never selected by whichever one passed.
	definitions, _ = ci.ParseDeclared([]ci.DeclaredWorkflow{{Path: ".grain/workflows/tests.yml", Text: body}})
	forge.base = "B4"
	forge.rules = []byte("defaults:\n  ci:\n    mode: both\n")
	acceptShadowRules(t, c, forge)
	now = now.Add(time.Second)
	read("met")
	failure := "failure"
	forge.checks[0].Conclusion = &failure
	now = now.Add(time.Second)
	read("unmet")
	forge.checks[0].FailureReason = "Actions budget spending limit"
	now = now.Add(time.Second)
	imported := read("unmet")
	if imported.Results[3].Result.Status != "met" {
		t.Fatal("legacy budget exception lost", imported.Results[3])
	}
}

type shadowCLProvider struct {
	fakeSubmitter
	observation change.Observation
	observeErr  error
}

func (p *shadowCLProvider) Observe(context.Context, model.Task, string) (change.Observation, error) {
	return p.observation, p.observeErr
}
func TestGateChecksCLSnapshotsErrorsAndInertProviderCalls(t *testing.T) {
	srv, c := testServer(t)
	ctx := context.Background()
	now := baseTime.Add(time.Minute)
	c.Now = func() time.Time { return now }
	p := &shadowCLProvider{observation: submittable}
	change.Register(p)
	t.Cleanup(func() {
		change.Unregister(model.LinkChangelist)
		change.Forget("cl/42")
		change.ClearBlocked("fakesubmit")
	})
	seedAwaitingSubmit(t, c.Store, ctx, "shadow-cl", "cl/42")
	c.Config.GateObservationTaskIDs = []string{"shadow-cl"}
	read := func(want string) *gates.ObservationView {
		t.Helper()
		before, _ := c.Store.GetTask(ctx, "shadow-cl")
		obsBefore, _ := c.Store.GetObservation(ctx, "shadow-cl")
		sourceBefore, seenBefore := change.LastObserved("cl/42")
		code, body := serve(t, srv, http.MethodGet, "/api/tasks/shadow-cl", "")
		if code != 200 {
			t.Fatal(code, body)
		}
		var d ui.TaskDetail
		_ = json.Unmarshal([]byte(body), &d)
		if d.GateObservations == nil || d.GateObservations.Results[2].Result.Status != want {
			t.Fatalf("want %s: %+v", want, d.GateObservations)
		}
		after, _ := c.Store.GetTask(ctx, "shadow-cl")
		obsAfter, _ := c.Store.GetObservation(ctx, "shadow-cl")
		sourceAfter, seenAfter := change.LastObserved("cl/42")
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(obsBefore, obsAfter) || !reflect.DeepEqual(sourceBefore, sourceAfter) || seenBefore != seenAfter {
			t.Fatal("shadow provider read changed authoritative state")
		}
		if len(p.submitted) > 0 || p.syncs > 0 || len(p.opens) > 0 || change.HealthOf("fakesubmit").Blocked != "" {
			t.Fatal("shadow invoked a provider action or changed provider health")
		}
		return d.GateObservations
	}
	first := read("met")
	firstVersion := first.Results[2].Result.ContextDigest
	p.observation.Snapshot = 4
	p.observation.Presubmit = change.PresubmitRunning
	now = now.Add(time.Second)
	v := read("pending")
	if v.Results[2].Result.ContextDigest == firstVersion {
		t.Fatal("snapshot change did not invalidate presubmit")
	}
	p.observation.Presubmit = change.PresubmitPassed
	p.observation.Health = change.HealthConflicted
	now = now.Add(time.Second)
	v = read("met")
	if !strings.Contains(*v.Results[2].Result.HumanText, "conflicts with head") {
		t.Fatal("submit precondition lost")
	}
	p.observation.Snapshot = 0
	now = now.Add(time.Second)
	read("unknown")
	p.observeErr = errors.New("private CL provider failure")
	now = now.Add(time.Second)
	read("error")
	p.observeErr = nil
	p.observation = submittable
	now = now.Add(2 * time.Second)
	read("met")
}

func acceptShadowRules(t *testing.T, c *ui.Client, f *shadowForge) {
	t.Helper()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	ctx := context.Background()
	cfg, err := c.Store.GetRepoConfig(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		cfg = &model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}
	}
	cfg.Declarations = true
	if err = c.Store.PutRepoConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	tree := fstest.MapFS{}
	if len(f.rules) > 0 {
		tree[model.MergeRulesPath] = &fstest.MapFile{Data: f.rules}
	}
	rules, err := repodecl.LoadMergeRules(tree)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Store.SyncRepoDeclarations(ctx, repo, f.base, model.RepoDeclarations{MergeRules: rules}, c.Now(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
}

func TestGateChecksWorkflowSuitesDoNotMaskFailures(t *testing.T) {
	srv, c := testServer(t)
	ctx := context.Background()
	now := baseTime.Add(time.Minute)
	c.Now = func() time.Time { return now }
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "Check suite identities"})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#7"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.Config.GateObservationTaskIDs = []string{task.ID}
	forge := newShadowForge()
	c.Config.GitHubPullRequests = forge
	success, failure := "success", "failure"
	forge.checks = []github.CheckRun{
		{ID: 1, CheckSuiteID: 11, Name: "go", App: "github-actions", HeadSHA: "H1", Status: "completed", Conclusion: &failure},
		{ID: 2, CheckSuiteID: 12, Name: "go", App: "github-actions", HeadSHA: "H1", Status: "completed", Conclusion: &success},
	}
	forge.workflows = []github.WorkflowRun{
		{ID: 101, CheckSuiteID: 11, Path: "unit.yml", Event: "push", HeadBranch: "feature", HeadSHA: "H1", Status: "completed", Conclusion: "failure", RunAttempt: 4096},
		{ID: 102, CheckSuiteID: 12, Path: "lint.yml", Event: "push", HeadBranch: "feature", HeadSHA: "H1", Status: "completed", Conclusion: "success", RunAttempt: 1},
	}
	read := func(want string) {
		t.Helper()
		code, body := serve(t, srv, http.MethodGet, "/api/tasks/"+task.ID, "")
		if code != 200 {
			t.Fatal(code, body)
		}
		var d ui.TaskDetail
		_ = json.Unmarshal([]byte(body), &d)
		if d.GateObservations == nil || d.GateObservations.Results[2].Result.Status != want {
			t.Fatalf("want %s: %+v", want, d.GateObservations)
		}
	}
	read("unmet") // A successful same-named job in another workflow cannot hide red.
	forge.workflows[1].Path = "unit.yml"
	now = now.Add(time.Second)
	read("met") // A newer run in the same lineage replaces older attempts, even with a larger old attempt number.
	forge.checks[0].ID = 5000
	now = now.Add(time.Second)
	read("unknown") // Conflicting job/run chronology must not invent a positive generation.
	forge.checks[0].ID = 1
	forge.workflows[1].HeadBranch = "other-branch"
	now = now.Add(time.Second)
	read("unmet") // A run for a different ref cannot replace the failed lineage.
	forge.workflows[1].HeadBranch = "feature"
	forge.workflows[0].CheckSuiteID = 0
	forge.workflows[1].CheckSuiteID = 0
	now = now.Add(time.Second)
	read("unknown") // The source cannot prove which suite replaced which.
	forge.checks = nil
	forge.workflows = forge.workflows[1:]
	now = now.Add(time.Second)
	read("unknown") // A positive workflow with an empty job-check set is not no-CI authorization.
}

func TestGateChecksDefaultGrantsDoNotSwitchAnExistingTasksProvider(t *testing.T) {
	srv, c := testServer(t)
	ctx := context.Background()
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "Existing GitHub task"})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#7"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = c.Store.UpdateConfig(ctx, func(cfg *model.Config) error { cfg.DefaultCapabilities = []string{grainci.Grant}; return nil }); err != nil {
		t.Fatal(err)
	}
	c.Config.GateObservationTaskIDs = []string{task.ID}
	c.Config.GitHubPullRequests = newShadowForge()
	code, body := serve(t, srv, http.MethodGet, "/api/tasks/"+task.ID, "")
	if code != 200 {
		t.Fatal(code, body)
	}
	var d ui.TaskDetail
	_ = json.Unmarshal([]byte(body), &d)
	if d.GateObservations == nil || d.GateObservations.Results[2].Result.Status != "met" {
		t.Fatal("defaults retroactively selected a different CI source", d.GateObservations)
	}
}

func TestGateChecksRecordedEventsAndLaggingReplica(t *testing.T) {
	for _, scenario := range []string{"missing", "contradictory", "during-read", "replaced-attempt"} {
		t.Run(scenario, func(t *testing.T) {
			srv, c := testServer(t)
			ctx := context.Background()
			task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "Observe recorded CI"})
			if err != nil {
				t.Fatal(err)
			}
			err = c.Store.UpdateTask(ctx, task.ID, func(task *model.Task) error {
				task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#7"})
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			c.Config.GateObservationTaskIDs = []string{task.ID}
			f := newShadowForge()
			acceptShadowRules(t, c, f)
			c.Config.GitHubPullRequests = f
			now := baseTime.Add(time.Minute)
			c.Now = func() time.Time { return now }
			job := model.GitHubCIJob{Repo: "acme/widgets", CheckRunID: 1, Commit: "H1", App: "trusted", JobName: "go", Conclusion: "cancelled"}
			if scenario == "missing" {
				job.CheckRunID = 2
			}
			if scenario == "replaced-attempt" {
				job.App = "github-actions"
				job.Conclusion = "success"
				job.RunID = 100
				job.RunAttempt = 2
				f.checks[0].App = job.App
				f.workflows = []github.WorkflowRun{{ID: 100, RunAttempt: 1, CheckSuiteID: 1, Path: ".github/workflows/test.yml", Event: "pull_request", HeadBranch: "feature", HeadSHA: "H1", Status: "completed", Conclusion: "success"}}
			}
			put := func() {
				if e := c.Store.PutGitHubCIJob(ctx, job); e != nil {
					t.Fatal(e)
				}
			}
			if scenario == "during-read" {
				f.onChecks = put
			} else {
				put()
			}
			code, body := serve(t, srv, http.MethodGet, "/api/tasks/"+task.ID, "")
			if code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			var d ui.TaskDetail
			if err = json.Unmarshal([]byte(body), &d); err != nil {
				t.Fatal(err)
			}
			if d.GateObservations == nil || len(d.GateObservations.Results) != 4 || d.GateObservations.Results[2].Result.Status != "unknown" {
				t.Fatalf("lagging or concurrent CI evidence passed: %+v", d.GateObservations)
			}
			// Catch up the remote complete set; a later ordinary read recovers without
			// calling any action method (the embedded nil client panics on those).
			f.onChecks = nil
			f.checks[0].ID = job.CheckRunID
			f.checks[0].Conclusion = &job.Conclusion
			if scenario == "replaced-attempt" {
				f.workflows[0].RunAttempt = 2
			}
			now = now.Add(time.Second)
			wake, commits := c.Store.Changed(), c.Store.CommitCount()
			code, body = serve(t, srv, http.MethodGet, "/api/tasks/"+task.ID, "")
			if code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			if err = json.Unmarshal([]byte(body), &d); err != nil {
				t.Fatal(err)
			}
			want := "pending"
			if scenario == "replaced-attempt" {
				want = "met"
			}
			if d.GateObservations.Results[2].Result.Status != want {
				t.Fatalf("did not recover: %+v", d.GateObservations.Results[2])
			}
			if commits != c.Store.CommitCount() {
				t.Fatal("diagnostic changed legacy accounting")
			}
			select {
			case <-wake:
				t.Fatal("diagnostic woke legacy")
			default:
			}
		})
	}
}
