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

package gateobservationtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
)

// RunHealth exercises real domain rows and the diagnostic tables on both SQL
// engines. It snapshots every legacy health/lifecycle/action surface around
// observation, including errors, permission denial and source-only writes.
func RunHealth(t *testing.T, s *model.Store, db *sql.DB) {
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice", Source: model.SourceIAP}
	yes := func(string) bool { return true }
	access := model.GateHealthAccess{Repository: yes, PublicationRepository: yes, PipelineRepository: yes, DeploymentWide: true}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	seed := func(id string, g model.GoalDecl) {
		t.Helper()
		must(s.PutTask(ctx, model.Task{ID: id, Intent: model.IntentImplement, Title: "Health repair", Target: &repo, Base: "main", Filing: model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect}, Approval: &model.Attribution{Actor: human}, ApprovedAt: &now, Binding: model.BindingDirective, CreatedAt: &now}))
		must(s.SetTaskGoal(ctx, id, g, "automation:health-test", now.Add(-48*time.Hour)))
	}
	snapshot := func() string {
		t.Helper()
		var out []any
		for _, table := range []string{"task", "task_observation", "task_run", "task_run_tool", "task_comment", "task_attachment", "task_goal", "task_state", "task_ready", "task_pause", "task_link", "task_grant", "task_grant_wait", "task_event", "integration_event", "base_branch_ci", "base_branch_release", "notification", "merge_batch", "repo_config"} {
			rows, err := db.QueryContext(ctx, "SELECT * FROM "+table)
			must(err)
			cols, err := rows.Columns()
			must(err)
			records := []string{}
			for rows.Next() {
				values := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range values {
					ptrs[i] = &values[i]
				}
				must(rows.Scan(ptrs...))
				records = append(records, fmt.Sprint(values))
			}
			must(rows.Err())
			rows.Close()
			out = append(out, records)
		}
		raw, _ := json.Marshal(out)
		return string(raw)
	}
	sampleNumber := 0
	read := func(store *model.Store, id string, at time.Time, a model.GateHealthAccess) (*gates.ObservationView, *model.GateObservationDiagnostics) {
		t.Helper()
		before := snapshot()
		changed := store.Changed()
		v, d, err := store.ObserveTaskGates(model.WithGateHealthAccess(ctx, a), id, at)
		must(err)
		assertShadowView(t, v)
		if before != snapshot() {
			t.Fatal("shadow changed legacy health/lifecycle state")
		}
		select {
		case <-changed:
			t.Fatal("shadow signalled Changed")
		default:
		}
		if v.Authority != "legacy" || v.LifecycleEffects || v.ActionEffects || v.DispatchEffects || v.AutomaticCutover {
			t.Fatal("authority escaped")
		}
		for _, r := range v.Results {
			if len(r.Result.Work) > 0 || len(r.Result.Actions) > 0 {
				t.Fatal("operational prediction")
			}
		}
		if dir := os.Getenv("GRAIN_TEST_HEALTH_VIEWS_DIR"); dir != "" {
			sampleNumber++
			raw, e := json.Marshal(v)
			must(e)
			must(os.MkdirAll(dir, 0700))
			must(os.WriteFile(filepath.Join(dir, fmt.Sprintf("%03d.json", sampleNumber)), raw, 0600))
		}
		return v, d
	}
	statuses := func(v *gates.ObservationView, want ...string) {
		t.Helper()
		if len(v.Results) != len(want) {
			t.Fatalf("results %d want %d", len(v.Results), len(want))
		}
		for i, r := range v.Results {
			if r.Result.Status != want[i] {
				t.Fatalf("%s got %s want %s: %s", v.Bindings[i].ID, r.Result.Status, want[i], r.Result.Summary)
			}
		}
	}
	cfg := model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain, WatchDefaultBranch: true}
	must(s.PutRepoConfig(ctx, cfg))
	row := model.BaseBranchCI{Repo: repo, Branch: "main", Commit: "head", RunID: "base-run", Conclusion: "success", StartedAt: now.Add(-time.Hour), FinishedAt: &now}
	proof := func(at time.Time, nonrepro bool) {
		t.Helper()
		before := snapshot()
		changed := s.Changed()
		must(s.RecordGateBaseSource(ctx, repo, "main", model.GateBaseSource{PlanRevision: "test-plan", ConfirmationRevision: "test-recheck", RowRevision: model.GateBaseRowRevision(row), PolicyRevision: model.GateHealthPolicyRevision(&cfg), Tip: "head", ManagedRefs: []string{"main"}, Complete: true, Nonreproduction: nonrepro, At: at}))
		if before != snapshot() {
			t.Fatal("collector source changed legacy rows")
		}
		select {
		case <-changed:
			t.Fatal("collector source signalled Changed")
		default:
		}
	}
	seed("health-base", model.GoalDecl{Kind: model.GoalBaseGreen, Spec: model.GoalSpec{Repo: repo.String(), Branch: "main", Publishing: true}})
	v, _ := read(s, "health-base", now, access)
	statuses(v, "unknown", "unknown", "met", "unknown")
	must(s.PutBaseBranchCI(ctx, row))
	v, _ = read(s, "health-base", now, access)
	statuses(v, "met", "unknown", "met", "unknown")
	proof(now, false)
	v, d := read(s, "health-base", now.Add(time.Second), access)
	statuses(v, "met", "met", "met", "unknown")
	if d == nil || d.Counts["health_unknown_or_incomplete"] == 0 {
		t.Fatal("no health comparison history")
	}
	restarted := model.New(db)
	replay, _ := read(restarted, "health-base", now.Add(2*time.Second), access)
	if replay.Results[0].Revision != v.Results[0].Revision {
		t.Fatal("restart replay did not reuse diagnostic generation")
	}
	// Permission revocation cannot reveal a prior authorized cache or history.
	denied, diag := read(s, "health-base", now.Add(2*time.Second), model.GateHealthAccess{})
	statuses(denied, "unknown", "unknown", "unknown", "unknown")
	if diag != nil {
		t.Fatal("denied caller saw history")
	}
	for _, r := range denied.Results {
		if len(r.Result.Evidence) > 0 {
			t.Fatal("denied evidence leaked")
		}
	}
	// Exact source-row and policy pins invalidate even when the old verdict was green.
	row.Conclusion = "failure"
	row.Confirmation = model.BaseCICleared
	must(s.PutBaseBranchCI(ctx, row))
	v, _ = read(s, "health-base", now.Add(3*time.Second), access)
	statuses(v, "met", "unknown", "met", "unknown")
	proof(now.Add(3*time.Second), false)
	v, _ = read(s, "health-base", now.Add(4*time.Second), access)
	statuses(v, "met", "unknown", "met", "unknown")
	proof(now.Add(4*time.Second), true)
	v, _ = read(s, "health-base", now.Add(5*time.Second), access)
	statuses(v, "met", "met", "met", "unknown")
	cfg.BaseBranchCIJobs = []string{"ci:test"}
	must(s.PutRepoConfig(ctx, cfg))
	v, _ = read(s, "health-base", now.Add(6*time.Second), access)
	statuses(v, "met", "unknown", "met", "unknown")
	proof(now.Add(6*time.Second), true)
	// Superseded releases do not establish positive publication. Preserve the
	// actual configured legacy threshold (which can differ from the spec default).
	since := now.Add(-model.BaseBranchNotPublishingFor + 8*time.Second)
	release := model.BaseBranchRelease{Repo: repo, Branch: "main", Commit: "head", RunID: "release", StartedAt: now.Add(-time.Hour), FinishedAt: &now, Conclusion: "cancelled", Detail: model.ReleaseSupersededDetailPrefix + "new", FailingSince: &since}
	must(s.PutBaseBranchRelease(ctx, release))
	v, _ = read(s, "health-base", now.Add(7*time.Second), access)
	statuses(v, "met", "met", "met", "unknown")
	if !v.Results[2].Result.ValidUntil.Equal(now.Add(8 * time.Second)) {
		t.Fatal("legacy threshold expiry not leased")
	}
	v, _ = read(s, "health-base", now.Add(9*time.Second), access)
	statuses(v, "met", "met", "unmet", "unknown")
	release.Failures = 3
	release.FailingSince = &now
	must(s.PutBaseBranchRelease(ctx, release))
	v, _ = read(s, "health-base", now.Add(10*time.Second), access)
	statuses(v, "met", "met", "unmet", "unknown")
	v, _ = read(s, "health-base", now.Add(3*time.Minute), access)
	statuses(v, "met", "unknown", "unmet", "unknown")
	// Failure reconstruction retains the current four-profile shape, with no
	// accepted evidence or caller-supplied predictions. Recovery survives restart.
	failure := gates.UnavailableObservation("health-base", now.Add(4*time.Minute))
	before := snapshot()
	changed := s.Changed()
	must(s.RecordGateObservationFailure(ctx, "health-base", now.Add(4*time.Minute), &failure, access))
	if before != snapshot() {
		t.Fatal("failure changed legacy state")
	}
	select {
	case <-changed:
		t.Fatal("failure wake")
	default:
	}
	if len(failure.Results) != 4 || !failure.Dirty {
		t.Fatal("failure lost health binding shape")
	}
	for _, r := range failure.Results {
		if r.Disposition != "rejected" || r.Result.Status != "error" || len(r.Result.Evidence) > 0 {
			t.Fatal("unsafe failure reuse")
		}
	}
	v, _ = read(restarted, "health-base", now.Add(4*time.Minute+time.Millisecond), access)
	if v.Results[0].EvaluationID != failure.Results[0].EvaluationID {
		t.Fatal("failure replay changed generation")
	}
	// Unrelated tasks retain the ordinary outcome projection and do not acquire
	// health requirements just because another task is a repair.
	must(s.PutTask(ctx, model.Task{ID: "unrelated", Intent: model.IntentImplement, Target: &repo, Title: "ordinary work"}))
	unrelated, _ := read(s, "unrelated", now, access)
	if len(unrelated.Bindings) < 2 || unrelated.Bindings[0].Type != "delivery" || unrelated.Bindings[1].Type != "acknowledgment" {
		t.Fatal("ordinary outcome projection was lost")
	}
	for _, binding := range unrelated.Bindings {
		if strings.HasSuffix(binding.Type, "_health") || binding.Required || binding.Purpose != "monitor" {
			t.Fatal("health or required obligations attached to unrelated task")
		}
	}

	t.Run("nightly-real-rows-and-window", func(t *testing.T) {
		seed("health-nightly", model.GoalDecl{Kind: model.GoalNightlyGreen, Spec: model.GoalSpec{Repo: repo.String(), Workflow: "nightly"}})
		at := now.Add(-30 * time.Hour)
		must(s.PutGitHubCIJob(ctx, model.GitHubCIJob{Repo: repo.String(), CheckRunID: 1, RunID: 1, RunAttempt: 1, Workflow: "nightly", Ref: "main", Commit: "old", JobName: "test", Conclusion: "success", CompletedAt: &at, RecordedAt: at, UpdatedAt: at}))
		v, _ := read(s, "health-nightly", now, access)
		statuses(v, "met", "unknown")
		run := gates.WorkflowHealthRun{ID: 2, Attempt: 1, Repository: repo.String(), HeadRepository: repo.String(), Workflow: "nightly", Path: ".github/workflows/nightly.yml", Ref: "main", Commit: "head", Event: "schedule", Source: "github", Conclusion: "success", Complete: true, StartedAt: now.Add(-25 * time.Hour), CompletedAt: now.Add(-24*time.Hour + time.Second), Jobs: []gates.HealthJob{{ID: "20", Name: "test", Workflow: "nightly", Ref: "main", Commit: "head", RunID: 2, Attempt: 1, Conclusion: "success", Source: "github", App: "github-actions"}}}
		before := snapshot()
		changed := s.Changed()
		must(s.RecordGateWorkflowSource(ctx, run, now))
		if before != snapshot() {
			t.Fatal("workflow proof wrote legacy state")
		}
		select {
		case <-changed:
			t.Fatal("workflow proof wake")
		default:
		}
		v, d = read(s, "health-nightly", now, access)
		statuses(v, "met", "met")
		beforeExpirySamples := len(d.History)
		if !v.Results[1].Result.ValidUntil.Equal(now.Add(time.Second)) {
			t.Fatal("window timer missing")
		}
		v, d = read(model.New(db), "health-nightly", now.Add(2*time.Second), access)
		statuses(v, "met", "unknown")
		if len(d.History) <= beforeExpirySamples {
			t.Fatal("window expiry prediction was not retained in history")
		}
		run.ID = 3
		run.Jobs[0].RunID = 3
		run.CompletedAt = now
		run.Event = "pull_request"
		run.HeadRepository = "fork/widgets"
		must(s.RecordGateWorkflowSource(ctx, run, now))
		v, _ = read(s, "health-nightly", now.Add(3*time.Second), access)
		statuses(v, "met", "unknown")
		// Preserve the newest-500 legacy selection, with no fallback to an older
		// green row outside the selected collection.
		for i := 2; i <= 501; i++ {
			must(s.PutGitHubCIJob(ctx, model.GitHubCIJob{Repo: repo.String(), CheckRunID: int64(i), RunID: int64(1000 + i), RunAttempt: 1, Workflow: "other", Ref: "main", Commit: "head", JobName: "test", Conclusion: "success", CompletedAt: &now, UpdatedAt: now, RecordedAt: now}))
		}
		v, _ = read(s, "health-nightly", now.Add(4*time.Second), access)
		statuses(v, "pending", "unknown")
	})

	t.Run("pipeline-scope-paused-self-and-incomplete", func(t *testing.T) {
		now = now.Add(10 * time.Minute)
		// Restore a current, configured base verdict.
		row.Conclusion = "success"
		row.Confirmation = ""
		must(s.PutBaseBranchCI(ctx, row))
		proof(now, false)
		seed("health-pipeline", model.GoalDecl{Kind: model.GoalPipelineHealthy, Spec: model.GoalSpec{Repos: []string{repo.String()}}})
		v, _ := read(s, "health-pipeline", now, access)
		statuses(v, "met", "met")
		steward, e := s.GetTask(ctx, "health-pipeline")
		must(e)
		steward.Target = &model.RepoRef{Owner: "grain", Name: "config"}
		must(s.PutTask(ctx, *steward))
		v, _ = read(s, "health-pipeline", now, access)
		statuses(v, "met", "met")
		if v.Bindings[0].Subject.Repository != repo.String() {
			t.Fatal("steward subject came from its settings repository instead of configured pipeline scope")
		}

		seed("health-global-steward", model.GoalDecl{Kind: model.GoalPipelineHealthy})
		global, _ := read(s, "health-global-steward", now, access)
		statuses(global, "met", "unknown")
		seed("health-stalled-self", model.GoalDecl{Kind: model.GoalPipelineHealthy, Spec: model.GoalSpec{Repos: []string{repo.String()}}})
		self, e := s.GetTask(ctx, "health-stalled-self")
		must(e)
		oldSelf := now.Add(-100 * time.Hour)
		self.ApprovedAt = &oldSelf
		must(s.PutTask(ctx, *self))
		selfView, _ := read(s, "health-stalled-self", now, access)
		statuses(selfView, "unmet", "met")
		_, e = s.Pause(ctx, model.TaskPause{TaskID: self.ID, PausedAt: now, PausedBy: human})
		must(e)
		foreign := model.RepoRef{Owner: "acme", Name: "private"}
		bad := model.BaseBranchCI{Repo: foreign, Branch: "main", Commit: "bad", Conclusion: "failure"}
		must(s.PutBaseBranchCI(ctx, bad))
		v, _ = read(s, "health-pipeline", now.Add(time.Second), access)
		statuses(v, "unmet", "met")
		limited := access
		limited.DeploymentWide = false
		v, diag := read(s, "health-pipeline", now.Add(time.Second), limited)
		statuses(v, "unknown", "met")
		if diag != nil {
			t.Fatal("partial caller saw broad history")
		}
		old := now.Add(-100 * time.Hour)
		must(s.PutTask(ctx, model.Task{ID: "stalled-health", Intent: model.IntentImplement, Title: "stalled", Target: &repo, Approval: &model.Attribution{Actor: human}, ApprovedAt: &old, CreatedAt: &old}))
		v, _ = read(s, "health-pipeline", now.Add(2*time.Second), access)
		statuses(v, "unmet", "unmet")
		_, err := s.Pause(ctx, model.TaskPause{TaskID: "stalled-health", PausedAt: now, PausedBy: human})
		must(err)
		v, _ = read(s, "health-pipeline", now.Add(3*time.Second), access)
		statuses(v, "unmet", "met")
		// A known queue block is a problem regardless of notification state.
		must(s.ObserveField(ctx, "unrelated", now, func(o *model.Observation) { o.CompletedAt = &now; o.MergeQueueBlockedAt = &now }))
		// unrelated lacks an open change and may not be completed in task_state;
		// exercise an actual scoped open notification instead.
		_, err = s.RaiseNotification(ctx, model.Notification{Subject: "pipeline.test:widgets", Family: "pipeline.test", Severity: model.NotificationWarning, Title: "pipeline problem", Body: "test", Repo: repo.String(), RaisedBy: "automation:test", RaisedAt: now, LastRaisedAt: now})
		must(err)
		v, _ = read(s, "health-pipeline", now.Add(4*time.Second), access)
		statuses(v, "unmet", "unmet")
		_, err = s.ClearNotification(ctx, "pipeline.test:widgets", "human:alice")
		must(err)
		// A configured expected ref with no verdict must remain unknown.
		cfg.PortBranches = []string{"release"}
		must(s.PutRepoConfig(ctx, cfg))
		must(s.RecordGateBaseSource(ctx, repo, "main", model.GateBaseSource{PlanRevision: "test-plan", ConfirmationRevision: "test-recheck", RowRevision: model.GateBaseRowRevision(row), PolicyRevision: model.GateHealthPolicyRevision(&cfg), Tip: "head", ManagedRefs: []string{"main", "release"}, Complete: true, At: now}))
		v, _ = read(s, "health-pipeline", now.Add(5*time.Second), limited)
		if v.Results[1].Result.Status == "met" {
			t.Fatal("absent expected base passed")
		}
		// Bound the legacy notification scan. Non-pipeline rows still occupy its
		// historical newest-200 selection and cannot turn truncation into quiet.
		for i := 0; i < 201; i++ {
			_, err = s.RaiseNotification(ctx, model.Notification{Subject: fmt.Sprintf("other:%d", i), Family: "other", Severity: model.NotificationWarning, Title: "other", Repo: repo.String(), RaisedBy: "automation:test", RaisedAt: now, LastRaisedAt: now})
			must(err)
		}
		// Remove the observed foreign failure: the remaining broad scan is incomplete.
		_, err = db.ExecContext(ctx, "DELETE FROM base_branch_ci WHERE owner = ? AND name = ?", foreign.StoredOwner(), foreign.Name)
		must(err)
		v, _ = read(s, "health-pipeline", now.Add(6*time.Second), access)
		if v.Results[0].Result.Status == "met" || !strings.Contains(v.Results[0].Result.Summary, "incomplete") {
			t.Fatalf("truncated legacy scan: %s", v.Results[0].Result.Summary)
		}
	})
	t.Run("uppercase-gating-workflow-never-trusts-fork-main-jobs", func(t *testing.T) {
		seed("health-verified-case", model.GoalDecl{Kind: model.GoalNightlyGreen, Spec: model.GoalSpec{Repo: repo.String(), Workflow: "TESTS.YML"}})
		must(s.PutGitHubCIJob(ctx, model.GitHubCIJob{Repo: repo.String(), CheckRunID: 12000, RunID: 12000, RunAttempt: 1, Workflow: "TESTS.YML", Ref: "main", Commit: "fork-head", JobName: "main", Conclusion: "success", CompletedAt: &now, RecordedAt: now, UpdatedAt: now}))
		v, _ := read(s, "health-verified-case", now, access)
		statuses(v, "pending", "unknown")
	})
	t.Run("bounded-legacy-goal-shapes", func(t *testing.T) {
		seed("health-duplicate-scope", model.GoalDecl{Kind: model.GoalPipelineHealthy, Spec: model.GoalSpec{Repos: []string{repo.String(), repo.String()}}})
		v, _ := read(s, "health-duplicate-scope", now, access)
		for _, b := range v.Bindings {
			_, err := gates.Validate(b.Type, b.Version, b.Config)
			must(err)
		}
		var c gates.PipelineHealthConfig
		must(json.Unmarshal(v.Bindings[0].Config, &c))
		if len(c.Repos) != 1 {
			t.Fatal("duplicate legacy scope broke canonical binding")
		}
		repos := []string{}
		for i := 0; i < 65; i++ {
			repos = append(repos, fmt.Sprintf("acme/scope-%d", i))
		}
		seed("health-large-scope", model.GoalDecl{Kind: model.GoalPipelineHealthy, Spec: model.GoalSpec{Repos: repos}})
		v, _ = read(s, "health-large-scope", now, access)
		statuses(v, "unknown", "unknown")
		for _, b := range v.Bindings {
			_, err := gates.Validate(b.Type, b.Version, b.Config)
			must(err)
		}
		seed("health-large-workflow", model.GoalDecl{Kind: model.GoalNightlyGreen, Spec: model.GoalSpec{Repo: repo.String(), Workflow: strings.Repeat("w", 257)}})
		v, _ = read(s, "health-large-workflow", now, access)
		statuses(v, "unknown", "unknown")
		for _, b := range v.Bindings {
			_, err := gates.Validate(b.Type, b.Version, b.Config)
			must(err)
		}
	})
	t.Run("pipeline-health-scans-finished-and-live-run-timestamps", func(t *testing.T) {
		runNow := now.Add(20 * time.Minute)
		runRepo := model.RepoRef{Owner: "acme", Name: "runscan"}
		runCfg := model.RepoConfig{Repo: runRepo, CIMode: model.CIModeGrain, WatchDefaultBranch: true}
		must(s.PutRepoConfig(ctx, runCfg))
		runBase := model.BaseBranchCI{Repo: runRepo, Branch: "main", Commit: "head", RunID: "runscan-base", Conclusion: "success", StartedAt: runNow.Add(-time.Hour), FinishedAt: &runNow}
		must(s.PutBaseBranchCI(ctx, runBase))
		must(s.RecordGateBaseSource(ctx, runRepo, "main", model.GateBaseSource{
			PlanRevision:         "runscan-plan",
			ConfirmationRevision: "runscan-recheck",
			RowRevision:          model.GateBaseRowRevision(runBase),
			PolicyRevision:       model.GateHealthPolicyRevision(&runCfg),
			Tip:                  "head",
			ManagedRefs:          []string{"main"},
			Complete:             true,
			At:                   runNow,
		}))
		must(s.PutTask(ctx, model.Task{ID: "health-pipeline-runs", Intent: model.IntentImplement, Title: "Pipeline runs scan", Target: &runRepo, Base: "main", Filing: model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect}, Approval: &model.Attribution{Actor: human}, ApprovedAt: &runNow, Binding: model.BindingDirective, CreatedAt: &runNow}))
		must(s.SetTaskGoal(ctx, "health-pipeline-runs", model.GoalDecl{Kind: model.GoalPipelineHealthy, Spec: model.GoalSpec{Repos: []string{runRepo.String()}}}, "automation:health-test", runNow.Add(-time.Hour)))

		// Task approved long ago whose last finished run is recent: scanning MAX(finished_at)
		// must succeed on SQLite and PostgreSQL and keep queued wait below stall threshold.
		oldApproved := runNow.Add(-100 * time.Hour)
		must(s.PutTask(ctx, model.Task{ID: "runscan-finished", Intent: model.IntentImplement, Title: "Finished run task", Target: &runRepo, Base: "main", Approval: &model.Attribution{Actor: human}, ApprovedAt: &oldApproved, CreatedAt: &oldApproved}))
		must(s.StartRun(ctx, model.Run{ID: "run-fin-1", TaskID: "runscan-finished", Attempt: 1, StartedAt: runNow.Add(-10 * time.Minute)}, model.Limits{}))
		must(s.FinishRun(ctx, "run-fin-1", runNow.Add(-2*time.Minute), "failed", "retryable"))

		// Task with a fresh live run: scanning MIN(started_at) WHERE finished_at IS NULL must
		// succeed and stay quiet.
		must(s.PutTask(ctx, model.Task{ID: "runscan-live", Intent: model.IntentImplement, Title: "Live run task", Target: &runRepo, Base: "main", Approval: &model.Attribution{Actor: human}, ApprovedAt: &runNow, CreatedAt: &runNow}))
		must(s.StartRun(ctx, model.Run{ID: "run-live-1", TaskID: "runscan-live", Attempt: 1, StartedAt: runNow.Add(-5 * time.Minute)}, model.Limits{}))

		v, _ := read(s, "health-pipeline-runs", runNow, access)
		if v.Results[0].Result.Status == "error" || v.Results[1].Result.Status != "met" {
			t.Fatalf("expected non-error legacy and met current with finished+live runs, got legacy=%s (%s) current=%s (%s)",
				v.Results[0].Result.Status, v.Results[0].Result.Summary, v.Results[1].Result.Status, v.Results[1].Result.Summary)
		}

		// Task with a stalled live run: parsed LiveRunStartedAt must drive stall detection.
		must(s.PutTask(ctx, model.Task{ID: "runscan-stalled-live", Intent: model.IntentImplement, Title: "Stalled live run", Target: &runRepo, Base: "main", Approval: &model.Attribution{Actor: human}, ApprovedAt: &runNow, CreatedAt: &runNow}))
		must(s.StartRun(ctx, model.Run{ID: "run-live-stalled", TaskID: "runscan-stalled-live", Attempt: 1, StartedAt: runNow.Add(-25 * time.Hour)}, model.Limits{}))

		v, _ = read(s, "health-pipeline-runs", runNow.Add(time.Second), access)
		if v.Results[0].Result.Status == "error" || v.Results[1].Result.Status != "unmet" {
			t.Fatalf("expected unmet current for stalled live run, got legacy=%s current=%s (%s)",
				v.Results[0].Result.Status, v.Results[1].Result.Status, v.Results[1].Result.Summary)
		}
	})

}
