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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
)

// RunObservationFamilies qualifies the merged extension seams with real PR and
// goal rows. Error backoff must not replay a different family's old scope.
func RunObservationFamilies(t *testing.T, s *model.Store, db *sql.DB) {
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "mixed"}
	id := "mixed-observations"
	actor := model.Principal{Kind: model.PrincipalHuman, ID: "alice", Source: model.SourceIAP}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.PutTask(ctx, model.Task{ID: "mixed-review", Intent: model.IntentAnalyze, Title: "review", Target: &repo}))
	must(s.PutTask(ctx, model.Task{ID: id, Intent: model.IntentImplement, Title: "repair PR", Target: &repo, Base: "main", Filing: model.Filing{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect}, Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/mixed#7"}, {Kind: model.LinkReviewTask, Target: "mixed-review"}}}))
	yes := func(string) bool { return true }
	access := model.GateHealthAccess{Repository: yes, PublicationRepository: yes, PipelineRepository: yes, DeploymentWide: true}
	read := func(at time.Time) *gates.ObservationView {
		t.Helper()
		changed := s.Changed()
		before, err := s.TaskGoal(ctx, id)
		must(err)
		v, _, err := s.ObserveTaskGates(ctx, id, at, access)
		must(err)
		after, err := s.TaskGoal(ctx, id)
		must(err)
		if gates.Digest(before) != gates.Digest(after) {
			t.Fatal("observation changed the legacy goal")
		}
		select {
		case <-changed:
			t.Fatal("observation signalled legacy Changed")
		default:
		}
		for _, r := range v.Results {
			if len(r.Result.Actions) > 0 || len(r.Result.Work) > 0 {
				t.Fatal("operational prediction")
			}
		}
		return v
	}
	review := read(now)
	if len(review.Bindings) <= 2 || review.Bindings[2].Type != "review" {
		t.Fatal("upstream PR projection was lost")
	}
	failure := gates.UnavailableObservation(id, now.Add(time.Second))
	must(s.RecordGateObservationFailure(ctx, id, now.Add(time.Second), &failure, access))
	if len(failure.Bindings) != len(review.Bindings) {
		t.Fatal("review failure lost its validated scope")
	}
	must(s.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main", RunID: "base", Commit: "head", Conclusion: "success", StartedAt: now, FinishedAt: &now}))
	must(s.SetTaskGoal(ctx, id, model.GoalDecl{Kind: model.GoalBaseGreen, Spec: model.GoalSpec{Repo: repo.String(), Branch: "main"}}, "automation:test", now.Add(2*time.Second)))
	health := read(now.Add(2 * time.Second))
	if len(health.Bindings) != 2 || health.Bindings[0].Type != "base_health" || health.Results[0].Result.Status != "met" {
		t.Fatal("review error backoff replaced the new health scope")
	}
	failure = gates.UnavailableObservation(id, now.Add(3*time.Second))
	must(s.RecordGateObservationFailure(ctx, id, now.Add(3*time.Second), &failure, access))
	if len(failure.Bindings) != 2 || failure.Bindings[0].Type != "base_health" {
		t.Fatal("health failure reverted to report/review bindings")
	}
	must(s.ClearTaskGoal(ctx, id))
	restored := read(now.Add(4 * time.Second))
	if len(restored.Bindings) != len(review.Bindings) || restored.Bindings[2].Type != "review" {
		t.Fatal("health backoff hid the restored upstream PR projection")
	}
	for _, r := range restored.Results {
		if r.Result.Error != nil || strings.Contains(r.Result.Summary, "unsupported") {
			t.Fatal("normal family transition became a configuration error")
		}
	}
	// Scoped task reads retain the upstream security boundary, including the
	// failure path; a denied task cannot consult or replace a shared cache.
	denied := model.WithExternalRefReadable(ctx, func(string) bool { return false })
	_, _, err := s.ObserveTaskGates(denied, id, now.Add(5*time.Second), access)
	if !errors.Is(err, model.ErrGateObservationScopeDenied) {
		t.Fatalf("collection scope denial: %v", err)
	}
	failure = gates.UnavailableObservation(id, now.Add(5*time.Second))
	if err := s.RecordGateObservationFailure(denied, id, now.Add(5*time.Second), &failure, access); !errors.Is(err, model.ErrGateObservationScopeDenied) {
		t.Fatalf("failure scope denial: %v", err)
	}
	// The current-only path cannot depend on a history SELECT. Preserve cached
	// metadata, make one stored history row undecodable, and replay current state.
	_, err = db.ExecContext(ctx, "UPDATE `gate_observation_shadow_history` SET `view_json` = ? WHERE `task_id` = ?", "not-json", id)
	must(err)
	current, err := s.ObserveTaskGatePredictions(ctx, id, now.Add(4*time.Second), access)
	must(err)
	if len(current.Bindings) != len(review.Bindings) {
		t.Fatal("current-only projection changed")
	}
}
