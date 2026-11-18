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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
)

// RunHealthSourceOrdering exercises the ordering used by the actual scoped
// reader, including a replay that corrects an initially missing completion time.
func RunHealthSourceOrdering(t *testing.T, s *model.Store) {
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.PutTask(ctx, model.Task{ID: "health-source-order", Intent: model.IntentImplement, Title: "Repair nightly", Target: &repo, Base: "main"}))
	must(s.SetTaskGoal(ctx, "health-source-order", model.GoalDecl{Kind: model.GoalNightlyGreen, Spec: model.GoalSpec{Repo: repo.String(), Workflow: "nightly"}}, "automation:test", now.Add(-24*time.Hour)))
	run := func(id int64, conclusion string, completed time.Time) gates.WorkflowHealthRun {
		return gates.WorkflowHealthRun{
			ID: id, Attempt: 1, Repository: repo.String(), HeadRepository: repo.String(),
			Ref: "main", Commit: "head", Workflow: "nightly", Path: ".github/workflows/nightly.yml",
			Event: "schedule", Source: "github", Conclusion: conclusion, Complete: !completed.IsZero(),
			StartedAt: now.Add(-3 * time.Hour), CompletedAt: completed,
			Jobs: []gates.HealthJob{{ID: "job", Name: "test", Workflow: "nightly", Ref: "main", Commit: "head", Conclusion: conclusion, Source: "github", App: "github-actions", RunID: id, Attempt: 1}},
		}
	}
	older := run(1, "success", time.Time{})
	newer := run(2, "failure", now.Add(-time.Hour))
	must(s.RecordGateWorkflowSource(ctx, older, now))
	must(s.RecordGateWorkflowSource(ctx, newer, now))
	access := model.GateHealthAccess{Repository: func(string) bool { return true }}
	read := func(at time.Time, want string) {
		t.Helper()
		view, _, err := s.ObserveTaskGates(ctx, "health-source-order", at, access)
		must(err)
		if got := view.Results[1].Result.Status; got != want {
			t.Fatalf("current nightly status = %s, want %s: %s", got, want, view.Results[1].Result.Summary)
		}
	}
	read(now, "unknown")

	// Resolving the older marker must move its ordering key with its proof.
	// Otherwise it retains the collection-time key and hides the newer failure.
	older.CompletedAt = now.Add(-2 * time.Hour)
	older.Complete = true
	must(s.RecordGateWorkflowSource(ctx, older, now.Add(time.Second)))
	read(now.Add(time.Second), "unmet")

	// A later loss of time provenance must likewise install a newest unknown
	// marker, rather than allowing an older run to establish health.
	older.CompletedAt = time.Time{}
	older.Complete = false
	must(s.RecordGateWorkflowSource(ctx, older, now.Add(2*time.Second)))
	read(now.Add(2*time.Second), "unknown")

	// An out-of-order replay cannot replace that newer incomplete marker.
	older.CompletedAt = now.Add(-2 * time.Hour)
	older.Complete = true
	must(s.RecordGateWorkflowSource(ctx, older, now.Add(time.Second)))
	read(now.Add(3*time.Second), "unknown")
}
