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
	if allowed.GateObservationDiagnostics == nil || allowed.GateObservationDiagnostics.Evaluations != 0 || !allowed.GateObservationDiagnostics.StartedAt.IsZero() {
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
