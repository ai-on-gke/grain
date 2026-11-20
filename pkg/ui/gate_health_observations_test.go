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
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestHealthGateAPIPermissionsAndProjection(t *testing.T) {
	srv, c := testServer(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return now }
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	seedAPITask(t, c, "health-api", repo.String(), "")
	seedAPITask(t, c, "task-only-health-api", repo.String(), "api:tasks:read@acme/widgets")
	seedAPITask(t, c, "ci-health-api", repo.String(), "api:tasks:read@acme/widgets", "api:ci:read@acme/widgets", "api:repos:read@acme/widgets", "api:releases:read@acme/widgets")
	must(c.Store.SetTaskGoal(ctx, "health-api", model.GoalDecl{Kind: model.GoalBaseGreen, Spec: model.GoalSpec{Repo: repo.String(), Branch: "main", Publishing: true}}, "automation:test", now.Add(-time.Hour)))
	cfg := model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain, WatchDefaultBranch: true}
	must(c.Store.PutRepoConfig(ctx, cfg))
	row := model.BaseBranchCI{Repo: repo, Branch: "main", Commit: "exact-base-head", RunID: "base-run", Conclusion: "success", StartedAt: now.Add(-time.Minute), FinishedAt: &now}
	must(c.Store.PutBaseBranchCI(ctx, row))
	must(c.Store.RecordGateBaseSource(ctx, repo, "main", model.GateBaseSource{PlanRevision: "test-plan", ConfirmationRevision: "test-recheck", RowRevision: model.GateBaseRowRevision(row), PolicyRevision: model.GateHealthPolicyRevision(&cfg), Tip: row.Commit, Complete: true, ManagedRefs: []string{"main"}, At: now}))
	c.Config.GateObservationTaskIDs = []string{"health-api"}
	// A repair task can also own a PR. Its health projection must not invoke
	// the merged change-CI collector or acquire unrelated change obligations.
	must(c.Store.UpdateTask(ctx, "health-api", func(task *model.Task) error {
		task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: repo.String() + "#7"})
		return nil
	}))
	forge := newShadowForge()
	forge.onChecks = func() { t.Error("health goal triggered an unrelated change-CI read") }
	c.Config.GitHubPullRequests = forge
	decode := func(code int, body string) ui.TaskDetail {
		t.Helper()
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		var d ui.TaskDetail
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			t.Fatal(err)
		}
		if d.GateObservations == nil || len(d.GateObservations.Results) != 4 {
			t.Fatalf("missing health projection: %s", body)
		}
		if forge.readHead != "" {
			t.Fatal("health projection collected change-CI evidence")
		}
		return d
	}
	code, body := serve(t, srv, http.MethodGet, "/api/tasks/health-api", "")
	d := decode(code, body)
	if d.GateObservations.Results[0].Result.Status != "met" || d.GateObservations.Results[1].Result.Status != "met" || d.GateObservations.Results[3].Result.Status != "unknown" {
		t.Fatalf("%+v", d.GateObservations.Results)
	}
	if path := os.Getenv("GRAIN_TEST_HEALTH_GATE_FIXTURE"); path != "" {
		raw, _ := json.Marshal(d.GateObservations)
		must(os.WriteFile(path, raw, 0600))
	}
	code, body = asAgent(t, srv, "task-only-health-api", http.MethodGet, "/api/tasks/health-api", "", "")
	denied := decode(code, body)
	if denied.GateObservationDiagnostics != nil || strings.Contains(body, "exact-base-head") {
		t.Fatal("task visibility leaked cached health evidence/history")
	}
	for _, r := range denied.GateObservations.Results {
		if r.Result.Status != "unknown" || len(r.Result.Evidence) > 0 {
			t.Fatal("denied source passed")
		}
	}
	code, body = asAgent(t, srv, "ci-health-api", http.MethodGet, "/api/tasks/health-api", "", "")
	allowed := decode(code, body)
	if allowed.GateObservations.Results[1].Result.Status != "met" {
		t.Fatal("scoped health reader denied")
	}
	if allowed.GateObservationDiagnostics != nil {
		t.Fatal("scoped reader received unproven aggregate history metadata")
	}

	// A changed goal scope cannot reveal history from the previous repository.
	hidden := model.RepoRef{Owner: "acme", Name: "private"}
	must(c.Store.SetTaskGoal(ctx, "health-api", model.GoalDecl{Kind: model.GoalBaseGreen, Spec: model.GoalSpec{Repo: hidden.String(), Branch: "main"}}, "automation:test", now))
	code, body = asAgent(t, srv, "ci-health-api", http.MethodGet, "/api/tasks/health-api", "", "")
	if code != 200 || strings.Contains(body, "exact-base-head") {
		t.Fatal("edited subject leaked previous cache")
	}
	var edited ui.TaskDetail
	must(json.Unmarshal([]byte(body), &edited))
	if edited.GateObservationDiagnostics != nil {
		t.Fatal("foreign subject returned history")
	}
}

func TestHealthGateActingTaskNeverWidensLimitedRole(t *testing.T) {
	h := newRoleHarness(t)
	installScopeSets(t,
		model.APIScopeSet{Name: "tasks-only", Scopes: []string{"tasks:read"}},
		model.APIScopeSet{Name: "health-reader", Scopes: []string{"tasks:read", "ci:read", "repos:read"}},
	)
	h.allow(t, "user:limited@example.com", "tasks-only")
	h.allow(t, "user:health@example.com", "health-reader")
	h.allow(t, "user:op@example.com", "grain/operator")

	c := h.client
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return now }
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	seedAPITask(t, c, "health-api", repo.String(), "")
	seedAPITask(t, c, "pipeline-health-api", repo.String(), "")
	seedAPITask(t, c, "privileged-health-agent", repo.String(),
		"api:tasks:read", "api:ci:read", "api:repos:read", "api:releases:read", "api:deployment:read")
	seedAPITask(t, c, "narrow-health-agent", repo.String(), "api:tasks:read@acme/widgets")
	seedAPITask(t, c, "widgets-health-agent", repo.String(),
		"api:tasks:read@acme/widgets", "api:ci:read@acme/widgets", "api:repos:read@acme/widgets")

	must(c.Store.SetTaskGoal(ctx, "health-api", model.GoalDecl{
		Kind: model.GoalBaseGreen,
		Spec: model.GoalSpec{Repo: repo.String(), Branch: "main", Publishing: true},
	}, "automation:test", now.Add(-time.Hour)))
	must(c.Store.SetTaskGoal(ctx, "pipeline-health-api", model.GoalDecl{
		Kind: model.GoalPipelineHealthy,
	}, "automation:test", now.Add(-time.Hour)))
	cfg := model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain, WatchDefaultBranch: true}
	must(c.Store.PutRepoConfig(ctx, cfg))
	row := model.BaseBranchCI{Repo: repo, Branch: "main", Commit: "exact-base-head", RunID: "base-run", Conclusion: "success", StartedAt: now.Add(-time.Minute), FinishedAt: &now}
	must(c.Store.PutBaseBranchCI(ctx, row))
	must(c.Store.RecordGateBaseSource(ctx, repo, "main", model.GateBaseSource{
		PlanRevision: "test-plan", ConfirmationRevision: "test-recheck",
		RowRevision: model.GateBaseRowRevision(row), PolicyRevision: model.GateHealthPolicyRevision(&cfg),
		Tip: row.Commit, Complete: true, ManagedRefs: []string{"main"}, At: now,
	}))
	must(c.Store.PutBaseBranchRelease(ctx, model.BaseBranchRelease{
		Repo: repo, Branch: "main", Commit: "exact-base-head", RunID: "release-run", Conclusion: "success",
		StartedAt: now.Add(-time.Minute), FinishedAt: &now,
	}))
	c.Config.GateObservationTaskIDs = []string{"health-api", "pipeline-health-api"}

	callAs := func(email, actingTask, path string) (int, string, ui.TaskDetail) {
		t.Helper()
		hdr := h.as(email)
		if actingTask != "" {
			hdr[ui.ActingTaskHeader] = actingTask
		}
		rec := serveFrom(h.srv, "203.0.113.10:443", http.MethodGet, path, "", hdr)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s as %s (acting=%q): %d %s", path, email, actingTask, rec.Code, rec.Body.String())
		}
		var d ui.TaskDetail
		must(json.Unmarshal(rec.Body.Bytes(), &d))
		if d.GateObservations == nil {
			t.Fatalf("GET %s as %s (acting=%q): missing gate observations: %s", path, email, actingTask, rec.Body.String())
		}
		return rec.Code, rec.Body.String(), d
	}

	// Warm caches as an unrestricted operator and capture the operator's fence.
	_, opBody, opDetail := callAs("op@example.com", "", "/api/tasks/health-api")
	if !strings.Contains(opBody, "exact-base-head") || opDetail.GateObservations.Results[0].Result.Status != "met" || opDetail.GateObservations.Results[2].Result.Status != "met" {
		t.Fatalf("operator health projection did not populate: %s", opBody)
	}
	opRev := opDetail.GateObservations.Results[0].Result.Dependencies[0].AuthorizationRevision
	_, _, opPipeline := callAs("op@example.com", "", "/api/tasks/pipeline-health-api")
	if opPipeline.GateObservations.Results[0].Result.Status != "met" {
		t.Fatalf("operator pipeline health projection did not populate: %+v", opPipeline.GateObservations.Results)
	}

	// 1. A verified user held to tasks:read cannot use a privileged acting task
	// to read base_health, publishing_health, or pipeline_health observations.
	for _, path := range []string{"/api/tasks/health-api", "/api/tasks/pipeline-health-api"} {
		_, body, denied := callAs("limited@example.com", "privileged-health-agent", path)
		if denied.GateObservationDiagnostics != nil || strings.Contains(body, "exact-base-head") {
			t.Fatalf("limited role with privileged acting task leaked health evidence on %s: %s", path, body)
		}
		for _, r := range denied.GateObservations.Results {
			if r.Result.Status != "unknown" || len(r.Result.Evidence) > 0 {
				t.Fatalf("limited role with privileged acting task escalated %s on %s: %+v", r.Result.Type, path, r.Result)
			}
		}
	}

	// 2. A role with base health scopes (but without releases:read or
	// deployment:read) cannot escalate to publishing_health or legacy
	// deployment-wide pipeline_health via a privileged acting task.
	_, _, roleOnly := callAs("health@example.com", "", "/api/tasks/health-api")
	if roleOnly.GateObservations.Results[0].Result.Status != "met" || roleOnly.GateObservations.Results[2].Result.Status != "unknown" {
		t.Fatalf("unexpected role-only health projection: %+v", roleOnly.GateObservations.Results)
	}
	roleRev := roleOnly.GateObservations.Results[0].Result.Dependencies[0].AuthorizationRevision

	_, _, healthWithPriv := callAs("health@example.com", "privileged-health-agent", "/api/tasks/health-api")
	if healthWithPriv.GateObservations.Results[0].Result.Status != "met" {
		t.Fatalf("expected base_health to stay met under intersection: %+v", healthWithPriv.GateObservations.Results)
	}
	if healthWithPriv.GateObservations.Results[2].Result.Status != "unknown" || len(healthWithPriv.GateObservations.Results[2].Result.Evidence) > 0 {
		t.Fatalf("privileged acting task widened health-reader into publishing_health: %+v", healthWithPriv.GateObservations.Results[2].Result)
	}
	_, _, pipeWithPriv := callAs("health@example.com", "privileged-health-agent", "/api/tasks/pipeline-health-api")
	if pipeWithPriv.GateObservations.Results[0].Result.Status != "unknown" || len(pipeWithPriv.GateObservations.Results[0].Result.Evidence) > 0 {
		t.Fatalf("privileged acting task widened health-reader into deployment-wide pipeline_health: %+v", pipeWithPriv.GateObservations.Results[0].Result)
	}

	// 3. Passing a narrower acting task still narrows a broader role and a full
	// operator, and never collides with the role-only or operator cache fence.
	_, narrowBody, narrowRole := callAs("health@example.com", "narrow-health-agent", "/api/tasks/health-api")
	if narrowRole.GateObservationDiagnostics != nil || strings.Contains(narrowBody, "exact-base-head") {
		t.Fatalf("narrow acting task did not restrict health-reader: %s", narrowBody)
	}
	for _, r := range narrowRole.GateObservations.Results {
		if r.Result.Status != "unknown" || len(r.Result.Evidence) > 0 {
			t.Fatalf("narrow acting task leaked %s to health-reader: %+v", r.Result.Type, r.Result)
		}
	}

	_, _, mixedRole := callAs("health@example.com", "widgets-health-agent", "/api/tasks/health-api")
	if mixedRole.GateObservations.Results[0].Result.Status != "met" {
		t.Fatalf("repo-narrowed mixed principal lost allowed base_health: %+v", mixedRole.GateObservations.Results)
	}
	mixedRev := mixedRole.GateObservations.Results[0].Result.Dependencies[0].AuthorizationRevision
	if mixedRev == roleRev {
		t.Fatalf("mixed-principal authorization revision %d collided with role-only revision %d", mixedRev, roleRev)
	}

	_, narrowOpBody, narrowOp := callAs("op@example.com", "narrow-health-agent", "/api/tasks/health-api")
	if narrowOp.GateObservationDiagnostics != nil || strings.Contains(narrowOpBody, "exact-base-head") {
		t.Fatalf("narrow acting task did not restrict full operator: %s", narrowOpBody)
	}
	for _, r := range narrowOp.GateObservations.Results {
		if r.Result.Status != "unknown" || len(r.Result.Evidence) > 0 {
			t.Fatalf("narrow acting task leaked %s to operator: %+v", r.Result.Type, r.Result)
		}
	}

	_, _, widgetsOp := callAs("op@example.com", "widgets-health-agent", "/api/tasks/health-api")
	if widgetsOp.GateObservations.Results[0].Result.Status != "met" {
		t.Fatalf("operator with repo-scoped acting task lost allowed base_health: %+v", widgetsOp.GateObservations.Results)
	}
	widgetsOpRev := widgetsOp.GateObservations.Results[0].Result.Dependencies[0].AuthorizationRevision
	if widgetsOpRev == opRev {
		t.Fatalf("operator+acting-task authorization revision %d collided with unrestricted operator revision %d", widgetsOpRev, opRev)
	}
}
